package sync_test

// 统一快速更新用例测试（契约 07 §3.1；票 #86，缝②既有风格：真实 store +
// 真实 adapters + 真实用例，只断言外部行为）：
//   - 无差异短路 no_diff（不建计划）；
//   - 停靠 awaiting_confirmation（draft 含冲突 / requirements 空 ∧ 授权关闭）
//     与 apply_started（requirements 空 ∧ 授权开启）双链端到端；
//   - relation_invalidated 新发射点（停于 awaiting_confirmation 之后，收口时序）；
//   - 并发 join（同 relation 并发调用等待并返回同一结果，链只跑一条）；
//   - 任务互斥守卫（其他来源活跃任务 err.scan.already_running 透传）；
//   - pending_plan_id 投影（draft/resolved 最新入选，stale/expired/applied/祖先排除）。
//
// 停靠判定纯函数全格表见 quickupdate_dock_test.go（内部包）。复用 T37 的 config
// 文件夹具与链路助手（makeApplyFixtures/mustRelationForApply/waitApplyTask/
// tableRowCount，headless_t37_test.go）。

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// mustInitializeCommitted 走手动确认链完成 initialize（QuickUpdate 的前置基线：
// 初始化决议属人工面，链内只承接 sync 语境）。
func mustInitializeCommitted(t *testing.T, app syncapp.Application, rel view.RelationView) {
	t.Helper()
	ctx := context.Background()
	resolved := mustResolveApplyPlan(t, app, rel, round1Choices)
	task, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan(initialize): %v", err)
	}
	if got := waitApplyTask(t, app, task.TaskID); got.Status != model.TaskStatusSucceeded {
		t.Fatalf("initialize 未成功: %s %+v", got.Status, got.Problem)
	}
}

// mustWorkspace 严格读取工作区投影。
func mustWorkspace(t *testing.T, app syncapp.Application, relationID string) view.WorkspaceView {
	t.Helper()
	w, err := app.GetWorkspace(context.Background(), relationID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	return w
}

// waitEventsStable 等待关系失效事件计数静止（连续三次读数一致）后返回绝对总数。
// 扫描/apply 的既有发射在任务终态落库之后异步补发，与测试读数存在毫秒级竞态；
// 发射者是同一连接池上的排队 SQLite 写，静止窗口（≥400ms）内必然落地。
func waitEventsStable(t *testing.T, db *sql.DB, relationID string) int {
	t.Helper()
	count := func() int {
		return tableRowCount(t, db,
			"SELECT COUNT(*) FROM task_events WHERE event_type=? AND relation_id=?",
			model.EventRelationInvalidated, relationID)
	}
	prev, streak := count(), 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && streak < 2 {
		time.Sleep(200 * time.Millisecond)
		if cur := count(); cur == prev {
			streak++ // 连续三次读数一致即静止
		} else {
			prev, streak = cur, 0
		}
	}
	return prev
}

// TestQuickUpdateNoDiffShortCircuit 验证无差异短路（契约 07 §3.1.2）：基线已建、
// 双端 noop → no_diff 且不建计划（空计划在今天会走完全链，本用例补口）。
func TestQuickUpdateNoDiffShortCircuit(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	mustInitializeCommitted(t, app, rel)

	plansBefore := tableRowCount(t, db, "SELECT COUNT(*) FROM sync_plans WHERE relation_id=?", rel.RelationID)
	res, err := app.QuickUpdate(context.Background(), view.QuickUpdateInput{RelationID: rel.RelationID})
	if err != nil {
		t.Fatalf("QuickUpdate: %v", err)
	}
	if res.Outcome != syncapp.QuickUpdateNoDiff {
		t.Fatalf("outcome = %s, 期望 no_diff", res.Outcome)
	}
	if res.PlanID != "" || res.ApplyTaskID != "" {
		t.Fatalf("no_diff 不应携带 plan/apply 标识: %+v", res)
	}
	plansAfter := tableRowCount(t, db, "SELECT COUNT(*) FROM sync_plans WHERE relation_id=?", rel.RelationID)
	if plansAfter != plansBefore {
		t.Fatalf("no_diff 不建计划: 计划数 %d → %d", plansBefore, plansAfter)
	}
	// 已应用计划不再待人工：投影为空
	if w := mustWorkspace(t, app, rel.RelationID); w.State.PendingPlanID != "" {
		t.Fatalf("no_diff 后 pending_plan_id = %q, 期望空", w.State.PendingPlanID)
	}
}

// TestQuickUpdateAwaitingDraftConflicts 验证停靠判定分支一：初始化语境 draft 含
// 冲突（无决议输入）→ awaiting_confirmation，计划停留 draft 既有流，requested_
// exactness 恒 exact，并补发 relation_invalidated（扫描中段之外的收口发射）。
func TestQuickUpdateAwaitingDraftConflicts(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	res, err := app.QuickUpdate(context.Background(), view.QuickUpdateInput{RelationID: rel.RelationID})
	if err != nil {
		t.Fatalf("QuickUpdate: %v", err)
	}
	if res.Outcome != syncapp.QuickUpdateAwaitingConfirmation {
		t.Fatalf("outcome = %s, 期望 awaiting_confirmation", res.Outcome)
	}
	if res.ApplyTaskID != "" {
		t.Fatal("awaiting_confirmation 不应携带 apply_task_id")
	}
	// 计划停留 draft（决议面交还用户）；requested_exactness 恒 exact
	plan, err := app.GetPlan(context.Background(), res.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if plan.Status != "draft" {
		t.Fatalf("停靠计划 status = %s, 期望 draft", plan.Status)
	}
	if plan.RequestedExactness != string(model.ExactnessExact) {
		t.Fatalf("requested_exactness = %q, 期望 exact", plan.RequestedExactness)
	}
	// 停靠后补发收口发射（契约 07 §4）。发射者全枚举：首扫 1 + 链内扫描 1
	// + 停靠收口 1 = 3（后端发射在任务终态之后异步落地，静止后断言绝对总数）
	if events := waitEventsStable(t, db, rel.RelationID); events != 3 {
		t.Fatalf("relation_invalidated 发射数 = %d, 期望 3（首扫 + 链内扫描 + 停靠收口）", events)
	}
	// 角标数据源
	if w := mustWorkspace(t, app, rel.RelationID); w.State.PendingPlanID != res.PlanID {
		t.Fatalf("pending_plan_id = %q, 期望停靠计划 %q", w.State.PendingPlanID, res.PlanID)
	}
}

// TestQuickUpdateResolvedDockAndApplyStarted 验证停靠判定分支二/三与 apply_started：
// 新增项目侧文件（write_runtime create，requirements 空）在授权关闭时停靠
// resolved；开启授权后同链直达 apply_started；应用收口后 applied 排除、角标清空；
// apply_started 轮不再补停靠发射。
func TestQuickUpdateResolvedDockAndApplyStarted(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	mustInitializeCommitted(t, app, rel)

	// 项目侧新增文件：create（目标侧 absent）→ write_runtime 无 overwrite/delete
	writeFile(t, filepath.Join(projectRoot, "config", "d.toml"), "d = \"project v1\"\n")

	// 授权关闭：requirements 空 ∧ 授权关闭 → 停靠 resolved
	res, err := app.QuickUpdate(context.Background(), view.QuickUpdateInput{RelationID: rel.RelationID})
	if err != nil {
		t.Fatalf("QuickUpdate（未授权）: %v", err)
	}
	if res.Outcome != syncapp.QuickUpdateAwaitingConfirmation {
		t.Fatalf("未授权 outcome = %s, 期望 awaiting_confirmation", res.Outcome)
	}
	plan, err := app.GetPlan(context.Background(), res.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if plan.Status != "resolved" {
		t.Fatalf("停靠计划 status = %s, 期望 resolved", plan.Status)
	}
	if len(plan.ConfirmationRequirements) != 0 {
		t.Fatalf("create 链不应有确认要求: %+v", plan.ConfirmationRequirements)
	}

	if w := mustWorkspace(t, app, rel.RelationID); w.State.PendingPlanID != res.PlanID {
		t.Fatalf("pending_plan_id = %q, 期望 %q", w.State.PendingPlanID, res.PlanID)
	}

	// 既有计划全部置为已过期（SQL 手术沿 t36 先例：plan_json 是读取权威，
	// 列与 JSON 同改）：为授权轮的「applied 后角标清空」断言清场，避免旧计划
	// 残留混入最新投影
	if _, err := db.Exec(`UPDATE sync_plans SET expires_at='2000-01-01T00:00:00Z',
		plan_json=json_set(plan_json, '$.expires_at', '2000-01-01T00:00:00Z') WHERE relation_id=?`,
		rel.RelationID); err != nil {
		t.Fatal(err)
	}

	// 开启授权：requirements 空 ∧ 授权开启 → apply_started
	if _, err := app.SetWorkspaceAuthorized(context.Background(), rel.RelationID, true); err != nil {
		t.Fatalf("SetWorkspaceAuthorized: %v", err)
	}
	res2, err := app.QuickUpdate(context.Background(), view.QuickUpdateInput{RelationID: rel.RelationID})
	if err != nil {
		t.Fatalf("QuickUpdate（已授权）: %v", err)
	}
	if res2.Outcome != syncapp.QuickUpdateApplyStarted {
		t.Fatalf("已授权 outcome = %s, 期望 apply_started", res2.Outcome)
	}
	if res2.PlanID == "" || res2.ApplyTaskID == "" {
		t.Fatalf("apply_started 应回填 plan_id/apply_task_id: %+v", res2)
	}
	if got := waitApplyTask(t, app, res2.ApplyTaskID); got.Status != model.TaskStatusSucceeded {
		t.Fatalf("apply 未成功: %s %+v", got.Status, got.Problem)
	}
	// applied 读取时投影排除（契约 05 §5）：待人工计划清空
	if w := mustWorkspace(t, app, rel.RelationID); w.State.PendingPlanID != "" {
		t.Fatalf("应用收口后 pending_plan_id = %q, 期望空", w.State.PendingPlanID)
	}
	// 本轮直达 apply_started 不停靠，不补发射。发射者全枚举（静止后断言绝对
	// 总数）：首扫 1 + initialize apply 提交 1（既有引擎发射）+ 第一轮扫描 1
	// + 停靠收口 1 + 第二轮扫描 1 + 第二轮 apply 提交 1 = 6
	eventsFinal := waitEventsStable(t, db, rel.RelationID)
	if eventsFinal != 6 {
		t.Fatalf("relation_invalidated 总数 = %d, 期望 6（两首扫+两 apply 提交+一轮内扫描+一次停靠）", eventsFinal)
	}
}

// TestQuickUpdateConcurrentJoin 验证并发 join（契约 07 §3.1.5）：同 relation 链
// 进行中时并发调用等待并返回同一结果（双击/双窗口安全），链只跑一条——扫描任务
// 与计划都只有一份。慢扫描注入保证后到调用落在首链进行中窗口。
func TestQuickUpdateConcurrentJoin(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot, func(d *syncapp.AppDeps) {
		d.ProjectScan = slowScanner{inner: d.ProjectScan, delay: 400 * time.Millisecond}
		d.RuntimeScan = slowScanner{inner: d.RuntimeScan, delay: 400 * time.Millisecond}
	})
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	const callers = 3
	results := make([]view.QuickUpdateResultView, callers)
	errsOut := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errsOut[i] = app.QuickUpdate(context.Background(), view.QuickUpdateInput{RelationID: rel.RelationID})
		}(i)
		time.Sleep(120 * time.Millisecond) // 后到调用逐个落入首链进行中窗口
	}
	wg.Wait()

	if errsOut[0] != nil {
		t.Fatalf("QuickUpdate: %v", errsOut[0])
	}
	for i := 1; i < callers; i++ {
		if errsOut[i] != nil {
			t.Fatalf("并发调用 #%d 报错: %v", i, errsOut[i])
		}
		if results[i] != results[0] {
			t.Fatalf("并发调用结果不一致:\n#0 %+v\n#%d %+v", results[0], i, results[i])
		}
	}
	// 初始化语境 draft 含冲突 → 停靠；链只跑一条（join 不新建扫描/计划）
	if results[0].Outcome != syncapp.QuickUpdateAwaitingConfirmation {
		t.Fatalf("outcome = %s, 期望 awaiting_confirmation", results[0].Outcome)
	}
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM tasks WHERE relation_id=? AND kind='scan'", rel.RelationID); n != 2 {
		t.Fatalf("扫描任务数 = %d, 期望 2（首扫 + 链内一次）", n)
	}
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM sync_plans WHERE relation_id=?", rel.RelationID); n != 1 {
		t.Fatalf("计划数 = %d, 期望 1（join 不重复建计划）", n)
	}
}

// TestQuickUpdateActiveTaskMutex 验证任务互斥守卫：其他来源活跃任务照常互斥，
// err.scan.already_running 透传（不绕守卫，ADR-0010 §5）。
func TestQuickUpdateActiveTaskMutex(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	// 注入一条其他来源的活跃任务（沿僵尸任务注入先例）
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO tasks(id, relation_id, kind, status, phase, sequence, can_cancel,
		completed, total, message_key, message_args_json, created_at, updated_at)
		VALUES('task_other', ?, 'apply', 'running', 'staging', 1, 0, 0, 6, 'msg.task.apply.queued', '[]', ?, ?)`,
		rel.RelationID, now, now); err != nil {
		t.Fatal(err)
	}

	_, err := app.QuickUpdate(context.Background(), view.QuickUpdateInput{RelationID: rel.RelationID})
	if code := errCode(t, err); code != "err.scan.already_running" {
		t.Fatalf("错误码 = %s, 期望 err.scan.already_running", code)
	}
}

// TestQuickUpdatePendingPlanIDProjection 验证 pending_plan_id 投影（契约 07 §3.2）：
// draft/resolved 最新入选；applied / 祖先 draft / stale / expired 排除。
func TestQuickUpdatePendingPlanIDProjection(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	ctx := context.Background()
	pendingOf := func() string {
		return mustWorkspace(t, app, rel.RelationID).State.PendingPlanID
	}
	prepareDraft := func() view.SyncPlanView {
		t.Helper()
		ws := mustWorkspace(t, app, rel.RelationID)
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
		return draft
	}

	// 初始：无计划 → 空
	if got := pendingOf(); got != "" {
		t.Fatalf("初始 pending_plan_id = %q, 期望空", got)
	}

	// draft 入选
	draft := prepareDraft()
	if got := pendingOf(); got != draft.PlanID {
		t.Fatalf("draft 入选: pending = %q, 期望 %q", got, draft.PlanID)
	}

	// resolved 推进后：祖先 draft 排除（resolved_from 指向），resolved 入选
	resolutions := make([]model.Resolution, 0, len(draft.Conflicts))
	for _, c := range draft.Conflicts {
		choice, ok := round1Choices[c.ResourceID]
		if !ok {
			t.Fatalf("冲突 %s 缺少测试选择", c.ResourceID)
		}
		resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: choice})
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	if got := pendingOf(); got != resolved.PlanID {
		t.Fatalf("resolved 入选: pending = %q, 期望 %q（祖先 draft 应排除）", got, resolved.PlanID)
	}

	// applied 排除：确认并收口（committed → 读取时投影 applied）→ 空
	task, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	if got := waitApplyTask(t, app, task.TaskID); got.Status != model.TaskStatusSucceeded {
		t.Fatalf("apply 未成功: %s %+v", got.Status, got.Problem)
	}
	if got := pendingOf(); got != "" {
		t.Fatalf("applied 后 pending = %q, 期望空", got)
	}

	// stale 排除：draft 入选后修订前进（policy 原样写回递增修订，ADR-0002）→ 空
	if prepareDraft().PlanID == "" {
		t.Fatal("draft 未生成")
	}
	pol, err := app.GetMappingPolicy(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: rel.RelationID, ExpectedRevision: pol.RelationRevision, Rules: pol.Rules,
	}); err != nil {
		t.Fatalf("UpdateMappingPolicy: %v", err)
	}
	if got := pendingOf(); got != "" {
		t.Fatalf("修订前进后 pending = %q, 期望空（stale 排除）", got)
	}

	// expired 排除：新修订下 draft 再入选，SQL 手术置过期（prep_expired 先例）→ 空
	expired := prepareDraft()
	if got := pendingOf(); got != expired.PlanID {
		t.Fatalf("新修订 draft 入选: pending = %q, 期望 %q", got, expired.PlanID)
	}
	if _, err := db.Exec(`UPDATE sync_plans SET expires_at='2000-01-01T00:00:00Z',
		plan_json=json_set(plan_json, '$.expires_at', '2000-01-01T00:00:00Z') WHERE id=?`, expired.PlanID); err != nil {
		t.Fatal(err)
	}
	if got := pendingOf(); got != "" {
		t.Fatalf("过期后 pending = %q, 期望空（expired 排除）", got)
	}
}

// slowScanner 是扫描器装饰器：固定延迟后委托（并发 join 的窗口确定性注入）。
type slowScanner struct {
	inner ports.ProjectScanner
	delay time.Duration
}

func (s slowScanner) Scan(ctx context.Context, root string, opts ports.ScanOptions) (model.ScanReport, error) {
	select {
	case <-ctx.Done():
		return model.ScanReport{}, ctx.Err()
	case <-time.After(s.delay):
	}
	return s.inner.Scan(ctx, root, opts)
}

func (s slowScanner) Name() string    { return s.inner.Name() }
func (s slowScanner) Version() string { return s.inner.Version() }
