package sync_test

// P2-T04 headless（票 #37）：Apply 引擎六阶段端到端与失败面。
// 本文件覆盖外部可注入的失败路径（SQLite 触发器/文件系统篡改）；需要引擎
// 内部 seam（文件动作包装）的注入在 apply_inject_test.go（package sync）。
//
// 覆盖：
//  1. confirm → 六阶段 → committed 端到端两轮（initialize 建链 + sync 修改/删除），
//     内容逐字节断言；GetApplyRun/ListApplyOperations/ListCommits/GetCommit/
//     GetPlan(applied) 投影；journal 事件序单调且意图先行；staging 仅提交后清理；
//     object_refs/恢复引用/确认令牌消费/relation_invalidated/分相计时；
//  2. running 意图被触发器拒绝（op_0002）→ recovery_required：动作未执行
//     （意图先行的直接证据）、Baseline/Commit 零推进、staging 证据保留；
//  3. committed 单事务原子：sync_commits INSERT 注入 ABORT → 全量回滚零残留；
//  4. staging 前置条件复核：目标被外部篡改 → 操作 failed(precondition_violated)
//     → recovery_required，投影 ResultCode 透出。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/store/sqlite"
)

const (
	fxApplyPackToml = `name = "ApplyEngine"
author = "tester"
version = "1.0.0"
`
	// 空条目 index：受管面只含 config/ 文件规则（建议规则并入），无 mod。
	fxApplyIndex = `hash-format = "sha256"
`
	fxApplyA  = "a = \"project v1\"\n"
	fxApplyB  = "b = \"runtime v1\"\n"
	fxApplyC  = "c = \"project\"\n"
	fxApplyC2 = "c = \"project v2\"\n"
)

// makeApplyFixtures 搭建纯文本文件夹具（config/ 双向受管）：a 仅项目侧
// （write_runtime create）、b 仅运行时侧（write_project create）、c 双侧不同
// （init 后可再 modify）。
func makeApplyFixtures(t *testing.T) (projectRoot, instanceDir, dataRoot string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instance")

	writeFile(t, filepath.Join(projectRoot, "pack.toml"), fxApplyPackToml)
	writeFile(t, filepath.Join(projectRoot, "index.toml"), fxApplyIndex)
	writeFile(t, filepath.Join(projectRoot, "config", "a.toml"), fxApplyA)
	writeFile(t, filepath.Join(projectRoot, "config", "c.toml"), fxApplyC)
	writeFile(t, filepath.Join(instanceDir, "instance.cfg"), "[General]\nname=\"ApplyEngine\"\niconKey=default\n")
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "b.toml"), fxApplyB)
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "c.toml"), "c = \"runtime\"\n")

	dataRoot = filepath.Join(base, "userdata")
	return
}

// mustScanAndWait 严格扫描至成功。
func mustScanAndWait(t *testing.T, app interface {
	StartScan(ctx context.Context, relationID string) (view.TaskView, error)
	GetTask(ctx context.Context, taskID string) (view.TaskView, error)
}, relationID string) {
	t.Helper()
	ctx := context.Background()
	tv, err := app.StartScan(ctx, relationID)
	if err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := app.GetTask(ctx, tv.TaskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.Status == model.TaskStatusSucceeded {
			return
		}
		if got.Status != model.TaskStatusQueued && got.Status != model.TaskStatusRunning {
			t.Fatalf("扫描终态 %s: %+v", got.Status, got.Problem)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("扫描超时")
}

// mustResolveApplyPlan 产出新的 resolved 计划；choices 为 init_choice 冲突的
// 逐资源选择（sync 计划无冲突时传 nil/空）。
func mustResolveApplyPlan(t *testing.T, app syncapp.Application, rel view.RelationView,
	choices map[model.ResourceID]model.ResolutionChoice) view.SyncPlanView {
	t.Helper()
	ctx := context.Background()
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	resolutions := make([]model.Resolution, 0, len(choices))
	for _, c := range draft.Conflicts {
		choice, ok := choices[c.ResourceID]
		if !ok {
			t.Fatalf("冲突 %s 缺少选择", c.ResourceID)
		}
		resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: choice})
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	return resolved
}

// waitApplyTask 轮询任务至任一终态（recovery_required 是合法终态，交由调用方断言）。
func waitApplyTask(t *testing.T, app syncapp.Application, taskID string) view.TaskView {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		tv, err := app.GetTask(ctx, taskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		switch tv.Status {
		case model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
			return tv
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("apply 任务超时未结束")
	return view.TaskView{}
}

func stagingRunDir(t *testing.T, dataRoot, taskID string) string {
	t.Helper()
	return filepath.Join(dataRoot, "staging", taskID)
}

// mustRelationForApply 建关系（并入 config 文件受管规则）+ 首扫。
func mustRelationForApply(t *testing.T, app syncapp.Application, projectRoot, instanceDir string) view.RelationView {
	t.Helper()
	ctx := context.Background()
	prep, err := app.PrepareRelation(ctx, model.PrepareRelationInput{
		ProjectRoot:        projectRoot,
		RuntimeInstanceDir: instanceDir,
		Suggestions:        []string{"config"},
	})
	if err != nil {
		t.Fatalf("PrepareRelation: %v", err)
	}
	for _, c := range prep.Checks {
		if c.Severity == "blocking" && !c.Passed {
			t.Fatalf("预检 %s 未通过: %s", c.Code, c.Detail)
		}
	}
	rel, err := app.CreateRelation(ctx, prep.PreparationID)
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	mustScanAndWait(t, app, rel.RelationID)
	return rel
}

func tableRowCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// round1Choices 是首轮 initialize 计划的固定选择：a 从项目初始化、b 从运行时
// 初始化、c 取项目侧（init_choice 只接受 initialize_*/skip/manual）。
var round1Choices = map[model.ResourceID]model.ResolutionChoice{
	"file:config/a.toml": model.ChoiceInitializeFromProject,
	"file:config/b.toml": model.ChoiceInitializeFromRuntime,
	"file:config/c.toml": model.ChoiceInitializeFromProject,
}

// TestHeadlessApplyEngineCommittedEndToEnd 验证成功路径两轮：
// initialize（2 create + 1 modify）→ committed；sync（modify + delete）→ committed；
// 内容逐字节一致、投影全列断言、journal 事件序单调且意图先行、staging 仅提交后
// 清理、复扫 diff 归零。
func TestHeadlessApplyEngineCommittedEndToEnd(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	// ---- 第 1 轮：initialize ----
	plan1 := mustResolveApplyPlan(t, app, rel, round1Choices)
	if len(plan1.Operations) != 3 {
		t.Fatalf("round1 操作数 = %d，期望 3", len(plan1.Operations))
	}
	tv := mustConfirm(t, app, plan1.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		t.Fatalf("round1 任务终态 %s: %+v", final.Status, final.Problem)
	}
	if final.Outcome != model.TaskOutcomeExact || final.CommitID == "" || final.Phase != "done" {
		t.Fatalf("round1 任务收口字段: %+v", final)
	}
	if !final.CanCancel {
		t.Fatal("运行中的 apply 任务应可取消（can_cancel 随引擎收口）")
	}
	if final.Completed != final.Total || final.Total != 3 {
		t.Fatalf("round1 进度 %d/%d，期望 3/3", final.Completed, final.Total)
	}

	// 运行头：committed + commit 引用 + staging 已清理 + 恢复引用落列
	runRepo := sqlite.NewApplyRunRepository(db)
	run, err := runRepo.Get(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunCommitted || run.CommitID == "" || !run.StagingCleared {
		t.Fatalf("round1 运行头: state=%s commit=%s cleared=%v", run.State, run.CommitID, run.StagingCleared)
	}
	refs := string(run.RecoveryRefs)
	if !strings.Contains(refs, "files/config/a.toml") || !strings.Contains(refs, "\"cas\"") {
		t.Fatalf("恢复引用应含暂存与 CAS 引用: %s", refs)
	}

	// GetApplyRun 投影
	ar, err := app.GetApplyRun(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ar.TaskID != tv.TaskID || ar.State != "committed" || ar.CommitID != run.CommitID ||
		!ar.StagingCleared || ar.OperationCount != 3 {
		t.Fatalf("GetApplyRun: %+v", ar)
	}

	// 逐操作投影：全部 verified，无临时路径/证明泄漏字段，ResultCode 空
	ops, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: rel.RelationID, TaskID: tv.TaskID, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 3 {
		t.Fatalf("操作清单 %d 项", len(ops.Items))
	}
	for _, op := range ops.Items {
		if op.Status != model.OperationStatusVerified || op.ResultCode != "" || op.RelativePath == "" {
			t.Fatalf("操作投影: %+v", op)
		}
	}

	// 提交投影：单记录 initialize/exact/0 剩余；逐资源变化 3 行
	commits, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(commits.Items) != 1 || commits.Items[0].Kind != string(model.PlanInitialize) ||
		commits.Items[0].Completeness != model.TaskOutcomeExact || commits.Items[0].RemainingChangeCnt != 0 {
		t.Fatalf("ListCommits: %+v", commits.Items)
	}
	detail, err := app.GetCommit(ctx, rel.RelationID, commits.Items[0].CommitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Changes) != 3 {
		t.Fatalf("GetCommit 变化行 %d", len(detail.Changes))
	}

	// 计划投影 applied（契约 05 §5）
	gotPlan, err := app.GetPlan(ctx, plan1.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if gotPlan.Status != string(model.PlanApplied) {
		t.Fatalf("committed 后计划投影 = %s，期望 applied", gotPlan.Status)
	}

	// 内容逐字节断言（copy 物化）
	mustFileContent(t, filepath.Join(instanceDir, "minecraft", "config", "a.toml"), fxApplyA)
	mustFileContent(t, filepath.Join(projectRoot, "config", "b.toml"), fxApplyB)
	mustFileContent(t, filepath.Join(instanceDir, "minecraft", "config", "c.toml"), fxApplyC)

	// journal 追加历史：seq 严格递增；每操作子序 pending→running→applied→verified
	journal := sqlite.NewOperationJournalRepository(db)
	events, err := journal.ListEvents(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	assertApplyEventSequence(t, events, map[string][]string{
		"op_0001": {model.OperationStatusPending, model.OperationStatusRunning, model.OperationStatusApplied, model.OperationStatusVerified},
		"op_0002": {model.OperationStatusPending, model.OperationStatusRunning, model.OperationStatusApplied, model.OperationStatusVerified},
		"op_0003": {model.OperationStatusPending, model.OperationStatusRunning, model.OperationStatusApplied, model.OperationStatusVerified},
	})

	// 关系头推进 + object_refs + 确认令牌消费 + relation_invalidated 持久化
	var headBaseline, headCommit sql.NullString
	if err := db.QueryRow("SELECT head_baseline_id, head_commit_id FROM relations WHERE id=?",
		rel.RelationID).Scan(&headBaseline, &headCommit); err != nil {
		t.Fatal(err)
	}
	if !headBaseline.Valid || !headCommit.Valid || headCommit.String != run.CommitID {
		t.Fatalf("关系头未推进: base=%v commit=%v", headBaseline, headCommit)
	}
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM object_refs WHERE owner_type='commit'"); n < 1 {
		t.Fatalf("object_refs 应有 before 保全引用, got %d", n)
	}
	confs, err := sqlite.NewPlanConfirmationRepository(db).ListByPlan(ctx, plan1.PlanID)
	if err != nil || len(confs) != 1 || confs[0].ConsumedAt == "" {
		t.Fatalf("确认令牌应已消费: %+v err=%v", confs, err)
	}
	// relation_invalidated：committed 后新发射点（契约 05 §4；扫描提交的既有
	// 发射点也在计数内，此处断言 apply 贡献了至少一条）
	if n := tableRowCount(t, db,
		"SELECT COUNT(*) FROM task_events WHERE relation_id=? AND event_type='relation_invalidated'",
		rel.RelationID); n < 1 {
		t.Fatalf("relation_invalidated 应至少 1 条, got %d", n)
	}

	// staging 仅在提交成功后清理：运行目录已删除
	if _, err := os.Stat(stagingRunDir(t, dataRoot, tv.TaskID)); !os.IsNotExist(err) {
		t.Fatalf("committed 后 staging 应已清理: %v", err)
	}

	// 分相计时（T09 消费口）
	timing := app.LastApplyTiming()
	if timing.RelationID != rel.RelationID || timing.StagingMs <= 0 || timing.ApplyingMs <= 0 ||
		timing.VerifyingMs <= 0 || timing.TotalMs <= 0 || timing.OperationCount != 3 {
		t.Fatalf("分相计时: %+v", timing)
	}

	// ---- 第 2 轮：sync（modify c + delete runtime b） ----
	mustScanAndWait(t, app, rel.RelationID)
	if err := os.Remove(filepath.Join(instanceDir, "minecraft", "config", "b.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "config", "c.toml"), []byte(fxApplyC2), 0o644); err != nil {
		t.Fatal(err)
	}
	mustScanAndWait(t, app, rel.RelationID)
	plan2 := mustResolveApplyPlan(t, app, rel, nil)
	if len(plan2.Operations) != 2 || plan2.Kind != string(model.PlanSync) {
		t.Fatalf("round2 计划: kind=%s ops=%d", plan2.Kind, len(plan2.Operations))
	}
	tv2 := mustConfirm(t, app, plan2.PlanID)
	final2 := waitApplyTask(t, app, tv2.TaskID)
	if final2.Status != model.TaskStatusSucceeded {
		t.Fatalf("round2 任务终态 %s: %+v", final2.Status, final2.Problem)
	}

	commits2, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(commits2.Items) != 2 {
		t.Fatalf("round2 提交数 %d", len(commits2.Items))
	}
	second := commits2.Items[0] // created_at DESC，新提交在前
	if second.Kind != string(model.PlanSync) || second.Completeness != model.TaskOutcomeExact ||
		second.RemainingChangeCnt != 0 {
		t.Fatalf("round2 提交头: %+v", second)
	}
	detail2, err := app.GetCommit(ctx, rel.RelationID, second.CommitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail2.Changes) != 2 {
		t.Fatalf("round2 变化行 %d", len(detail2.Changes))
	}
	byKind := map[string]int{}
	for _, ch := range detail2.Changes {
		byKind[ch.ChangeKind]++
		// 删除目标是 project 侧副本（runtime 侧删除是计划的输入变更，删除传播
		// 收敛双侧）：before 取 project 侧表示
		if ch.ChangeKind == string(model.ChangeDelete) && ch.ProjectBefore == nil {
			t.Fatalf("delete 变化缺 before 表示: %+v", ch)
		}
	}
	if byKind[string(model.ChangeModify)] != 1 || byKind[string(model.ChangeDelete)] != 1 {
		t.Fatalf("round2 变化类别: %v", byKind)
	}

	// 内容逐字节 + 删除落地（删除传播：runtime 侧删除由 apply 收敛掉 project 副本）
	mustFileContent(t, filepath.Join(instanceDir, "minecraft", "config", "c.toml"), fxApplyC2)
	if _, err := os.Stat(filepath.Join(projectRoot, "config", "b.toml")); !os.IsNotExist(err) {
		t.Fatalf("project b 副本应已删除: %v", err)
	}
	if _, err := os.Stat(filepath.Join(instanceDir, "minecraft", "config", "b.toml")); !os.IsNotExist(err) {
		t.Fatalf("runtime b 应保持删除: %v", err)
	}

	// committed 后重扫 diff 归零（基线推进 + 双侧一致）
	mustScanAndWait(t, app, rel.RelationID)
	changes, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID})
	if err != nil {
		t.Fatal(err)
	}
	s := changes.Summary
	if s.CreateCount != 0 || s.ModifyCount != 0 || s.DeleteCount != 0 ||
		s.ConflictCount != 0 || s.InitChoiceCount != 0 {
		t.Fatalf("复扫 diff 未归零: %+v", s)
	}
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM sync_commits WHERE relation_id=?", rel.RelationID); n != 2 {
		t.Fatalf("sync_commits 应为 2 行, got %d", n)
	}
}

// TestHeadlessApplyEngineIntentBlockedGoesRecovery 验证意图先行与失败面：
// 触发器拒绝 op_0002 的 pending→running 意图 → 引擎不得执行该文件动作
// （runtime c 保持原内容），操作行收口 failed，run→recovery_required，
// Baseline/Commit 零推进，staging 证据保留，恢复未收口期间新 Apply 被拒。
func TestHeadlessApplyEngineIntentBlockedGoesRecovery(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	plan := mustResolveApplyPlan(t, app, rel, round1Choices)

	if _, err := db.Exec(`CREATE TRIGGER t37_block_intent BEFORE UPDATE ON operation_journal
		WHEN NEW.status='running' AND OLD.status='pending' AND NEW.operation_id='op_0002'
		BEGIN SELECT RAISE(ABORT, 't37 intent blocked'); END`); err != nil {
		t.Fatal(err)
	}

	tv := mustConfirm(t, app, plan.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusRecoveryRequired || final.Problem == nil {
		t.Fatalf("任务终态应 recovery_required 带 Problem: %+v", final)
	}

	run, err := sqlite.NewApplyRunRepository(db).Get(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunRecoveryRequired || run.StagingCleared {
		t.Fatalf("运行头: %+v", run)
	}

	// 意图先行的直接证据：op_0002 历史只有 pending→failed，无 running/applied；
	// runtime c 文件未被触碰（动作从未执行）
	events, err := sqlite.NewOperationJournalRepository(db).ListEvents(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	assertApplyEventSequence(t, events, map[string][]string{
		"op_0001": {model.OperationStatusPending, model.OperationStatusRunning, model.OperationStatusApplied},
		"op_0002": {model.OperationStatusPending, model.OperationStatusFailed},
	})
	mustFileContent(t, filepath.Join(instanceDir, "minecraft", "config", "c.toml"), "c = \"runtime\"\n")
	// op_0001 已 executed（部分完成是诚实事实，恢复裁决的输入）
	mustFileContent(t, filepath.Join(instanceDir, "minecraft", "config", "a.toml"), fxApplyA)

	// Baseline/Commit 零推进
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("失败面不得建 Commit: %d", n)
	}
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM sync_baselines"); n != 0 {
		t.Fatalf("失败面不得推 Baseline: %d", n)
	}
	var health string
	if err := db.QueryRow("SELECT health FROM relations WHERE id=?", rel.RelationID).Scan(&health); err != nil {
		t.Fatal(err)
	}
	if health != string(model.HealthRecoveryRequired) {
		t.Fatalf("关系健康态 = %s", health)
	}

	// staging 证据保留
	if _, err := os.Stat(stagingRunDir(t, dataRoot, tv.TaskID)); err != nil {
		t.Fatalf("失败路径 staging 证据应保留: %v", err)
	}

	// 恢复未收口：新 Apply 被拒（同计划重入 → err.recovery.in_progress）
	if _, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID}); err == nil ||
		errCode(t, err) != "err.recovery.in_progress" {
		t.Fatalf("恢复未收口应拒绝新确认: %v", err)
	}
}

// TestHeadlessApplyEngineCommitTxAtomic 验证 committed 单事务原子：
// sync_commits INSERT 注入 ABORT → 事务整体回滚，验证快照/基线/提交/引用/head/
// run 终态/verified 标记零推进；文件已应用但无「部分完成假象」（无基线推进），
// run→recovery_required，staging 证据保留。
func TestHeadlessApplyEngineCommitTxAtomic(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	plan := mustResolveApplyPlan(t, app, rel, round1Choices)

	if _, err := db.Exec(`CREATE TRIGGER t37_block_commit BEFORE INSERT ON sync_commits
		BEGIN SELECT RAISE(ABORT, 't37 commit blocked'); END`); err != nil {
		t.Fatal(err)
	}

	tv := mustConfirm(t, app, plan.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusRecoveryRequired {
		t.Fatalf("任务终态 = %s，期望 recovery_required", final.Status)
	}

	run, err := sqlite.NewApplyRunRepository(db).Get(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunRecoveryRequired || run.CommitID != "" {
		t.Fatalf("运行头应 recovery_required 且无提交引用: %+v", run)
	}
	for _, probe := range []struct{ name, query string }{
		{"sync_commits", "SELECT COUNT(*) FROM sync_commits"},
		{"sync_baselines", "SELECT COUNT(*) FROM sync_baselines"},
		{"object_refs", "SELECT COUNT(*) FROM object_refs"},
	} {
		if n := tableRowCount(t, db, probe.query); n != 0 {
			t.Fatalf("事务回滚后 %s 应零残留, got %d", probe.name, n)
		}
	}
	// 输入快照仍在（2 行），验证快照随事务回滚未落库
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM observed_snapshots"); n != 2 {
		t.Fatalf("observed_snapshots 应只有输入 2 行, got %d", n)
	}
	var headBaseline, headCommit sql.NullString
	if err := db.QueryRow("SELECT head_baseline_id, head_commit_id FROM relations WHERE id=?",
		rel.RelationID).Scan(&headBaseline, &headCommit); err != nil {
		t.Fatal(err)
	}
	if headBaseline.Valid || headCommit.Valid {
		t.Fatalf("head 引用应保持 NULL: %v %v", headBaseline, headCommit)
	}
	// 操作行保持 applied（verified 标记同事务回滚）
	ops, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: rel.RelationID, TaskID: tv.TaskID, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops.Items {
		if op.Status != model.OperationStatusApplied {
			t.Fatalf("回滚后操作 %s 状态 = %s，期望 applied", op.OperationID, op.Status)
		}
	}
	// 文件已应用 + staging 证据保留（恢复管线可据此裁决/续做）
	mustFileContent(t, filepath.Join(instanceDir, "minecraft", "config", "a.toml"), fxApplyA)
	if _, err := os.Stat(stagingRunDir(t, dataRoot, tv.TaskID)); err != nil {
		t.Fatalf("staging 证据应保留: %v", err)
	}
}

// TestHeadlessApplyEnginePreconditionViolated 验证 staged 前的前置条件复核：
// 确认前目标被外部篡改 → 操作 staging 失败（precondition_violated），不得覆盖
// 用户数据；run→recovery_required，投影 ResultCode 透出说明码。
func TestHeadlessApplyEnginePreconditionViolated(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	plan := mustResolveApplyPlan(t, app, rel, round1Choices)

	// 外部篡改 runtime c（op_0002 的覆盖目标）：计划的 before 期望不再成立
	tampered := "c = \"user edited\"\n"
	if err := os.WriteFile(filepath.Join(instanceDir, "minecraft", "config", "c.toml"),
		[]byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}

	tv := mustConfirm(t, app, plan.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusRecoveryRequired {
		t.Fatalf("任务终态 = %s，期望 recovery_required", final.Status)
	}
	mustFileContent(t, filepath.Join(instanceDir, "minecraft", "config", "c.toml"), tampered)

	ops, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: rel.RelationID, TaskID: tv.TaskID, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	var failed *view.ApplyOperationView
	for i := range ops.Items {
		if ops.Items[i].OperationID == "op_0002" {
			failed = &ops.Items[i]
		}
	}
	if failed == nil || failed.Status != model.OperationStatusFailed || failed.ResultCode != "precondition_violated" {
		t.Fatalf("op_0002 应 failed(precondition_violated): %+v", failed)
	}
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("不得建 Commit: %d", n)
	}
	// op_0001 已 staged（暂存事实在案）但 applying 未开始
	journal := sqlite.NewOperationJournalRepository(db)
	op1, err := journal.GetOperation(ctx, tv.TaskID, "op_0001")
	if err != nil {
		t.Fatal(err)
	}
	if op1.Status != model.OperationStatusPending || op1.TempRelativePath == "" {
		t.Fatalf("op_0001 应停在 pending 且带暂存事实: %+v", op1)
	}
	if _, err := os.Stat(stagingRunDir(t, dataRoot, tv.TaskID)); err != nil {
		t.Fatalf("staging 证据应保留: %v", err)
	}
}

// assertApplyEventSequence 断言追加历史：seq 严格递增（单调）；每个操作的
// 状态子序与期望完全一致（意图先行：running 恒先于 applied/verified；无静默跳变）。
func assertApplyEventSequence(t *testing.T, events []model.JournalEvent, want map[string][]string) {
	t.Helper()
	lastSeq := 0
	got := map[string][]string{}
	for _, ev := range events {
		if ev.Seq <= lastSeq {
			t.Fatalf("历史 seq 非单调: %d after %d", ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
		got[ev.OperationID] = append(got[ev.OperationID], ev.ToStatus)
	}
	for opID, wantSeq := range want {
		gotSeq := got[opID]
		if len(gotSeq) != len(wantSeq) {
			t.Fatalf("操作 %s 历史状态序 = %v，期望 %v", opID, gotSeq, wantSeq)
		}
		for i := range wantSeq {
			if gotSeq[i] != wantSeq[i] {
				t.Fatalf("操作 %s 历史状态序 = %v，期望 %v", opID, gotSeq, wantSeq)
			}
		}
	}
}

func mustFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s 内容不符:\n got %q\nwant %q", path, data, want)
	}
}
