package sync_test

// P2-T03 headless（票 #36）：ConfirmPlan 用例与 apply_sync 点亮
// （契约 05 §1/§3.1/§6，ADR-0004 §1 落列）。
// Apply 任务此刻无 runner（T04 引擎票）：断言止于任务 queued、运行 prepared。
//
// 覆盖：
//  1. confirm 单事务：token/任务/run 三者同生共死（含注入 apply_runs 写入失败
//     的零残留断言）；run 落列（digest/revision/preconditions/recovery_refs/
//     operation_count）；提交后 task_updated 事件持久化；
//  2. features 三值变更（sync_apply/history_view/materialization_modes=["copy"]）
//     且 restore 项不变；
//  3. apply_sync availability 推导（available / 活跃任务 already_running）；
//  4. 幂等重入（D4）：活跃运行重入同 TaskDTO、确认记录追加、任务/运行不新建；
//  5. committed 重入 → err.plan.apply_not_reentrant；恢复未收口（运行态或
//     关系健康态）→ err.recovery.in_progress；其他计划运行活跃 → already_running；
//  6. 调用级拆码：not_found / draft→stale / expired / 修订前进→stale。

import (
	"context"
	"testing"
	"time"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
	"packgradle/internal/store/sqlite"
)

// mustRelationWithScan 建关系并完成首次扫描（每用例独立 fixture，每关系只调一次）。
func mustRelationWithScan(t *testing.T, app syncapp.Application, projectRoot, instanceDir string) view.RelationView {
	t.Helper()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)
	return rel
}

// mustResolvePlan 在关系上产出新的 resolved 计划（快照输入复用两侧最新，三冲突
// 全决产出 2 操作；计划行不可变，可多次调用产出多个 resolved 计划）。
func mustResolvePlan(t *testing.T, app syncapp.Application, rel view.RelationView) view.SyncPlanView {
	t.Helper()
	ctx := context.Background()
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	resolutions := []model.Resolution{
		{ResourceID: "mod:curseforge:228525", Choice: model.ChoiceInitializeFromProject},
		{ResourceID: "mod:path:mods/local.pw.toml", Choice: model.ChoiceSkip},
		{ResourceID: "mod:jar:runtimeonly-1.0.jar", Choice: model.ChoiceInitializeFromRuntime},
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	if len(resolved.Operations) != 2 {
		t.Fatalf("resolved 操作数: %d", len(resolved.Operations))
	}
	return resolved
}

// expectedPreconditionCount 由计划操作推导运行级前置条件期望数（同资源同侧去重，
// 与 aggregatePreconditions 同规则）。
func expectedPreconditionCount(plan view.SyncPlanView) int {
	seen := map[string]bool{}
	for _, op := range plan.Operations {
		for _, pre := range op.Preconditions {
			seen[string(pre.ResourceID)+"|"+pre.Side] = true
		}
	}
	return len(seen)
}

// mustConfirm 严格确认计划。
func mustConfirm(t *testing.T, app syncapp.Application, planID string) view.TaskView {
	t.Helper()
	tv, err := app.ConfirmPlan(context.Background(), view.ConfirmPlanInput{PlanID: planID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	return tv
}

// TestHeadlessConfirmPlanPreparedRun 验证首次确认：任务 queued + 运行 prepared
// 按 ADR-0004 §1 落列；确认令牌落 plan_confirmations；提交后 task_updated 持久化；
// features 三值变更生效且 restore 项不变；apply_sync availability 可用。
func TestHeadlessConfirmPlanPreparedRun(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationWithScan(t, app, projectRoot, instanceDir)
	plan := mustResolvePlan(t, app, rel)

	// features 三值变更（契约 05 §1）+ P3 restore 点亮（契约 06 §1，票 #59）
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if !ws.Features.SyncApply || !ws.Features.HistoryView {
		t.Fatalf("P2 应点亮 sync_apply/history_view: %+v", ws.Features)
	}
	// P3（票 #63）：download 物化点亮
	if len(ws.Features.MaterializationModes) != 2 || ws.Features.MaterializationModes[0] != "copy" || ws.Features.MaterializationModes[1] != "download" {
		t.Fatalf("materialization_modes 应为 [\"copy\",\"download\"]: %v", ws.Features.MaterializationModes)
	}
	if !ws.Features.RestorePreview || !ws.Features.RestoreApply {
		t.Fatalf("P3 应点亮 restore_preview/restore_apply: %+v", ws.Features)
	}
	// availability：resolved 可应用计划在位 → apply_sync 可用
	var applySync *view.ActionAvailabilityView
	for i := range ws.Availability {
		if ws.Availability[i].Action == "apply_sync" {
			applySync = &ws.Availability[i]
		}
	}
	if applySync == nil {
		t.Fatal("availability 应注册 apply_sync 动作")
	}
	if !applySync.Available || applySync.ReasonCode != "" {
		t.Fatalf("apply_sync 应可用: %+v", applySync)
	}

	// 确认：任务 queued（无 runner，止步于此）
	tv := mustConfirm(t, app, plan.PlanID)
	if tv.Kind != model.TaskKindApply || tv.Status != model.TaskStatusQueued {
		t.Fatalf("任务 kind/status = %s/%s，期望 apply/queued", tv.Kind, tv.Status)
	}
	if tv.PlanID != plan.PlanID {
		t.Fatalf("TaskDTO PlanID 未回填: %q", tv.PlanID)
	}

	// 运行 prepared 落列（ADR-0004 §1）
	runRepo := sqlite.NewApplyRunRepository(db)
	run, err := runRepo.Get(ctx, tv.TaskID)
	if err != nil {
		t.Fatalf("apply_runs 应有运行行: %v", err)
	}
	if run.State != model.ApplyRunPrepared {
		t.Fatalf("运行 state = %s，期望 prepared", run.State)
	}
	if run.RelationID != rel.RelationID || run.PlanID != plan.PlanID {
		t.Fatalf("运行归属错: rel=%s plan=%s", run.RelationID, run.PlanID)
	}
	if run.PlanDigest != plan.PlanDigest {
		t.Fatalf("plan_digest = %s，期望 %s", run.PlanDigest, plan.PlanDigest)
	}
	if run.RelationRevision != plan.RelationRevision {
		t.Fatalf("relation_revision = %d，期望 %d", run.RelationRevision, plan.RelationRevision)
	}
	if run.OperationCount != len(plan.Operations) {
		t.Fatalf("operation_count = %d，期望 %d", run.OperationCount, len(plan.Operations))
	}
	if len(run.Preconditions) != expectedPreconditionCount(plan) {
		t.Fatalf("运行级前置条件数 = %d，期望 %d（操作前置条件同资源同侧去重）",
			len(run.Preconditions), expectedPreconditionCount(plan))
	}
	if run.RecoveryRefs != nil && string(run.RecoveryRefs) != "[]" {
		t.Fatalf("prepared 意图恢复引用应为空集: %s", string(run.RecoveryRefs))
	}
	if run.StagingCleared || run.AcknowledgedAt != "" || run.CommitID != "" {
		t.Fatalf("prepared 运行不应带收口事实: %+v", run)
	}

	// 确认令牌落 plan_confirmations（表收口，未消费）
	confirmRepo := sqlite.NewPlanConfirmationRepository(db)
	confs, err := confirmRepo.ListByPlan(ctx, plan.PlanID)
	if err != nil || len(confs) != 1 {
		t.Fatalf("plan_confirmations 应有 1 行: n=%d err=%v", len(confs), err)
	}
	if confs[0].ConfirmationToken == "" || confs[0].PlanDigest != plan.PlanDigest ||
		confs[0].ConsumedAt != "" || confs[0].ExpiresAt != plan.ExpiresAt {
		t.Fatalf("确认记录落列不符: %+v", confs[0])
	}

	// 提交后发布 task_updated（事件持久化；headless 桥为 nil）
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM task_events WHERE task_id=? AND event_type='task_updated'",
		tv.TaskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("task_updated 事件应为 1 条, got %d", n)
	}
}

// TestHeadlessConfirmPlanAvailabilityBlockedByActiveTask 验证确认后的活跃任务
// 使 apply_sync 不可用（already_running，推导表列序第一）。
func TestHeadlessConfirmPlanAvailabilityBlockedByActiveTask(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationWithScan(t, app, projectRoot, instanceDir)
	plan := mustResolvePlan(t, app, rel)

	confirmed := mustConfirm(t, app, plan.PlanID)

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	blockedSeen := false
	for _, a := range ws.Availability {
		if a.Action != "apply_sync" {
			continue
		}
		if a.Available || a.ReasonCode != "err.scan.already_running" {
			t.Fatalf("活跃任务应阻塞 apply_sync: %+v", a)
		}
		blockedSeen = true
		break
	}
	if !blockedSeen {
		t.Fatal("缺少 apply_sync 条目")
	}
	// 断言依赖活跃任务在场；返回前等 apply 离开执行态——引擎协程在确认后
	// 仍会写 userdata/staging/task（P4 尾部更长），测试先行返回会与
	// t.TempDir 清理竞态炸 RemoveAll（Windows: directory is not empty）。
	// 本 fixture 的 apply 本就以 recovery_required 收场（staging 证据保留、
	// 零后续写盘），只等终态不苛求成功（waitTask 对 recovery 会 Fatal）。
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		task, err := app.GetTask(ctx, confirmed.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		switch task.Status {
		case model.TaskStatusSucceeded, model.TaskStatusFailed,
			model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("apply 超时未到终态")
}

// waitApplyQuiescent 等确认产生的 apply 任务离开执行态（任一终态即可）：
// 引擎协程在断言完成后仍会写 userdata/staging/task，测试先行返回会与
// t.TempDir 清理竞态（Windows: directory is not empty）。本文件 fixture 的
// apply 允许以 recovery_required 收场（staging 证据保留、零后续写盘），故
// 只等终态不苛求成功——waitTask 对 recovery 会 Fatal，不适用。
func waitApplyQuiescent(t *testing.T, app syncapp.Application, taskID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		task, err := app.GetTask(context.Background(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		switch task.Status {
		case model.TaskStatusSucceeded, model.TaskStatusFailed,
			model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("apply 超时未到终态")
}

// TestHeadlessConfirmPlanIdempotentReentry 验证 D4 幂等重入：活跃运行重入返回
// 同一 TaskDTO；追加确认记录但任务/运行不新建（双击/双窗口安全）。
func TestHeadlessConfirmPlanIdempotentReentry(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustRelationWithScan(t, app, projectRoot, instanceDir)
	plan := mustResolvePlan(t, app, rel)

	first := mustConfirm(t, app, plan.PlanID)
	second := mustConfirm(t, app, plan.PlanID)
	if first.TaskID != second.TaskID {
		t.Fatalf("重入应返回同一任务: %s vs %s", first.TaskID, second.TaskID)
	}
	if second.Status != model.TaskStatusQueued {
		t.Fatalf("重入任务状态: %s", second.Status)
	}

	var tasks, runs, confs int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM tasks WHERE relation_id=? AND kind='apply'", plan.RelationID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM apply_runs WHERE plan_id=?", plan.PlanID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM plan_confirmations WHERE plan_id=?", plan.PlanID).Scan(&confs); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || runs != 1 {
		t.Fatalf("重入不得新建任务/运行: tasks=%d runs=%d", tasks, runs)
	}
	if confs != 2 {
		t.Fatalf("重入应追加确认记录（2 次确认）: %d", confs)
	}
	waitApplyQuiescent(t, app, first.TaskID)
}

// TestHeadlessConfirmPlanCommittedNotReentrant 验证 committed 拆码：同计划上一
// 运行已 committed 后重入 → err.plan.apply_not_reentrant（预置 apply_runs 行）。
func TestHeadlessConfirmPlanCommittedNotReentrant(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustRelationWithScan(t, app, projectRoot, instanceDir)
	plan := mustResolvePlan(t, app, rel)

	tv := mustConfirm(t, app, plan.PlanID)
	if _, err := db.Exec(`UPDATE apply_runs SET state='committed' WHERE task_id=?`, tv.TaskID); err != nil {
		t.Fatal(err)
	}
	_, err := app.ConfirmPlan(context.Background(), view.ConfirmPlanInput{PlanID: plan.PlanID})
	if code := errCode(t, err); code != "err.plan.apply_not_reentrant" {
		t.Fatalf("committed 重入错误码: %s", code)
	}
	// 此处不做 waitApplyQuiescent：上面的手工 UPDATE 已把 run 状态改写为
	// committed，引擎终态迁移被切断、任务永不到终态（等待只会超时）。
}

// TestHeadlessConfirmPlanRecoveryInProgress 验证恢复未收口拆码：运行态
// recovery_required 或关系健康态 recovery_required → err.recovery.in_progress。
func TestHeadlessConfirmPlanRecoveryInProgress(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationWithScan(t, app, projectRoot, instanceDir)
	plan := mustResolvePlan(t, app, rel)

	// 运行态恢复（预置 apply_runs 行）
	tv := mustConfirm(t, app, plan.PlanID)
	if _, err := db.Exec(`UPDATE apply_runs SET state='recovery_required' WHERE task_id=?`, tv.TaskID); err != nil {
		t.Fatal(err)
	}
	_, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if code := errCode(t, err); code != "err.recovery.in_progress" {
		t.Fatalf("运行恢复态错误码: %s", code)
	}

	// 关系健康态恢复（运行回 prepared，恢复占用同样禁新 Apply，ADR-0004 §4）
	if _, err := db.Exec(`UPDATE apply_runs SET state='prepared' WHERE task_id=?`, tv.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE relations SET health='recovery_required' WHERE id=?`, rel.RelationID); err != nil {
		t.Fatal(err)
	}
	_, err = app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if code := errCode(t, err); code != "err.recovery.in_progress" {
		t.Fatalf("关系恢复健康态错误码: %s", code)
	}
}

// TestHeadlessConfirmPlanSingleApplyPerRelation 验证 ADR-0004 §6 同关系单 Apply：
// 本计划无运行但其他计划的运行仍活跃 → err.scan.already_running。
func TestHeadlessConfirmPlanSingleApplyPerRelation(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	rel := mustRelationWithScan(t, app, projectRoot, instanceDir)
	planA := mustResolvePlan(t, app, rel)
	planB := mustResolvePlan(t, app, rel)

	mustConfirm(t, app, planA.PlanID) // 计划 A 运行 prepared（活跃）
	_, err := app.ConfirmPlan(context.Background(), view.ConfirmPlanInput{PlanID: planB.PlanID})
	if code := errCode(t, err); code != "err.scan.already_running" {
		t.Fatalf("他计划运行活跃错误码: %s", code)
	}
}

// TestHeadlessConfirmPlanSingleTransaction 验证 token/任务/run 三者同生共死：
// 注入 apply_runs 写入失败（测试触发器 ABORT）→ 整体回滚、零残留。
func TestHeadlessConfirmPlanSingleTransaction(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustRelationWithScan(t, app, projectRoot, instanceDir)
	plan := mustResolvePlan(t, app, rel)

	// 失败注入：apply_runs INSERT 一律 ABORT（事务内第三个写入点）
	if _, err := db.Exec(`CREATE TRIGGER t36_block_run_insert BEFORE INSERT ON apply_runs
		BEGIN SELECT RAISE(ABORT, 'injected by t36'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := app.ConfirmPlan(context.Background(), view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err == nil {
		t.Fatal("注入失败未生效")
	}
	if code := errs.CodeOf(err); code != "" {
		t.Fatalf("基础设施错误不应包装为错误码: %v", err)
	}

	// 零残留：任务、运行、确认令牌均不存在
	for _, probe := range []struct{ table, where string }{
		{"tasks", "plan_id='" + plan.PlanID + "'"},
		{"apply_runs", "plan_id='" + plan.PlanID + "'"},
		{"plan_confirmations", "plan_id='" + plan.PlanID + "'"},
	} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + probe.table + " WHERE " + probe.where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("失败后 %s 应零残留, 有 %d 行", probe.table, n)
		}
	}

	// 排除注入后同一计划确认成功（事务回滚不烧计划）
	if _, err := db.Exec(`DROP TRIGGER t36_block_run_insert`); err != nil {
		t.Fatal(err)
	}
	if tv := mustConfirm(t, app, plan.PlanID); tv.Status != model.TaskStatusQueued {
		t.Fatalf("回滚后确认应成功: %s", tv.Status)
	}
}

// TestHeadlessConfirmPlanErrorContracts 验证调用级拆码：not_found、draft→stale、
// expired（本票补键）、修订前进→stale。
func TestHeadlessConfirmPlanErrorContracts(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationWithScan(t, app, projectRoot, instanceDir)
	plan := mustResolvePlan(t, app, rel)

	confirm := func(planID string) string {
		t.Helper()
		_, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: planID})
		return errCode(t, err)
	}

	if code := confirm("plan_missing"); code != "err.plan.not_found" {
		t.Fatalf("not_found 错误码: %s", code)
	}

	// draft → stale（未解决冲突的计划不可确认）
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code := confirm(draft.PlanID); code != "err.plan.stale" {
		t.Fatalf("draft 确认错误码: %s", code)
	}

	// expired（契约 05 §6：缺键随本票补）。plan_json 是读取权威，列与 JSON 同改
	if _, err := db.Exec(`UPDATE sync_plans SET expires_at='2000-01-01T00:00:00Z',
		plan_json=json_set(plan_json, '$.expires_at', '2000-01-01T00:00:00Z') WHERE id=?`, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if code := confirm(plan.PlanID); code != "err.plan.expired" {
		t.Fatalf("expired 错误码: %s", code)
	}
	if _, err := db.Exec(`UPDATE sync_plans SET expires_at=?,
		plan_json=json_set(plan_json, '$.expires_at', ?) WHERE id=?`, plan.ExpiresAt, plan.ExpiresAt, plan.PlanID); err != nil {
		t.Fatal(err)
	}

	// 修订前进（policy 修改同事务递增 relations.revision，ADR-0002）→ stale
	pol, err := app.GetMappingPolicy(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: rel.RelationID, ExpectedRevision: pol.RelationRevision, Rules: pol.Rules,
	}); err != nil {
		t.Fatal(err)
	}
	if code := confirm(plan.PlanID); code != "err.plan.stale" {
		t.Fatalf("修订前进后错误码: %s", code)
	}
}
