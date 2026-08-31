package sync

// P2-T04 引擎 seam 注入测试（票 #37）：文件动作包装 applyActionRunner 是
// 引擎的测试接缝（本包内可替换），覆盖外部触发器无法触达的三条路径：
//
//  1. 文件动作在 running 意图落库后失败 → failed+result，动作意图序可断言，
//     run→recovery_required，Baseline/Commit 零推进，staging 证据保留；
//  2. 动作成功但结果被外部篡改 → verifying 复扫不一致 → 不 committed；
//  3. 取消在操作边界响应：已 started 的操作做完再停（op_0001 applied、
//     op_0002 滞留 pending）。
//
// 运行头/操作行的 SQLite 级注入（意图被拒、提交事务回滚、前置条件篡改）在
// headless_t37_test.go（package sync_test）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/packwiz"
	"packgradle/internal/adapters/prism"
	"packgradle/internal/application/view"
	"packgradle/internal/core/ids"
	"packgradle/internal/core/model"
	"packgradle/internal/store"
	"packgradle/internal/store/objectstore"
	"packgradle/internal/store/sqlite"
	"packgradle/internal/syncstage"
)

// newApplyEngineStack 在独立数据目录装配真实栈（headless，事件桥 nil）。
// 与 sync_test 的 newStack 同构——两个测试包不能共享 helper，各自维护。
func newApplyEngineStack(t *testing.T) (*App, *sql.DB, string) {
	t.Helper()
	base := t.TempDir()
	dataRoot := filepath.Join(base, "userdata")
	layout, err := store.EnsureLayout(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(layout.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(context.Background(), db, dataRoot); err != nil {
		t.Fatal(err)
	}
	cas, err := objectstore.Open(layout.ObjectsDir, db)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(AppDeps{
		Endpoints:     sqlite.NewEndpointRepository(db),
		Relations:     sqlite.NewRelationRepository(db),
		Snapshots:     sqlite.NewSnapshotRepository(db),
		Baselines:     sqlite.NewBaselineRepository(db),
		Plans:         sqlite.NewPlanRepository(db),
		Tasks:         sqlite.NewTaskRepository(db),
		Mappings:      sqlite.NewMappingRepository(db),
		Preparations:  sqlite.NewPreparationRepository(db),
		HashCache:     sqlite.NewHashCacheRepository(db),
		Events:        sqlite.NewEventRepository(db),
		ApplyRuns:     sqlite.NewApplyRunRepository(db),
		Journal:       sqlite.NewOperationJournalRepository(db),
		Commits:       sqlite.NewCommitRepository(db),
		CAS:           cas,
		StagingRoot:   layout.StagingDir,
		Tx:            sqlite.NewUnitOfWork(db),
		ProjectScan:   packwiz.New(),
		RuntimeScan:   prism.New(),
		Hasher:        filesystem.NewHasher(),
		Fingerprinter: filesystem.NewFingerprinter(),
		EndpointPaths: filesystem.PathNormalizer{},
		IDs:           ids.New,
		Now:           time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, db, dataRoot
}

// applyTestWriteFile 写夹具文件。
func applyTestWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// applyTestFixture 搭建 config/ 双向受管的最小文本夹具；运行时实例目录与其
// minecraft 游戏目录按登记不变量（gameDir=<instance>/minecraft）布局。
func applyTestFixture(t *testing.T) (projectRoot, instanceDir, runtimeRoot string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instance")
	runtimeRoot = filepath.Join(instanceDir, "minecraft")
	applyTestWriteFile(t, filepath.Join(projectRoot, "pack.toml"),
		"name = \"Inject\"\nauthor = \"tester\"\nversion = \"1.0.0\"\n")
	applyTestWriteFile(t, filepath.Join(projectRoot, "index.toml"), "hash-format = \"sha256\"\n")
	applyTestWriteFile(t, filepath.Join(projectRoot, "config", "a.toml"), "a = \"v1\"\n")
	applyTestWriteFile(t, filepath.Join(runtimeRoot, "config", "b.toml"), "b = \"v1\"\n")
	return projectRoot, instanceDir, runtimeRoot
}

// applyTestScanAndWait 扫描至成功。
func applyTestScanAndWait(t *testing.T, app *App, relationID string) {
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
			t.Fatalf("扫描终态 %s", got.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("扫描超时")
}

// applyTestResolvedPlan 建 + 扫 + 出 resolved 计划（a→runtime create、b→project
// create，各一操作；选择固定 initialize_from_*）。
func applyTestResolvedPlan(t *testing.T, app *App, relationID string) view.SyncPlanView {
	t.Helper()
	ctx := context.Background()
	applyTestScanAndWait(t, app, relationID)
	ws, err := app.GetWorkspace(ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             relationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	resolutions := []model.Resolution{
		{ResourceID: "file:config/a.toml", Choice: model.ChoiceInitializeFromProject},
		{ResourceID: "file:config/b.toml", Choice: model.ChoiceInitializeFromRuntime},
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	if len(resolved.Operations) != 2 {
		t.Fatalf("resolved 操作数 = %d，期望 2", len(resolved.Operations))
	}
	return resolved
}

// applyTestRelation 经默认模板 + config 建议规则创建关系。
func applyTestRelation(t *testing.T, app *App, projectRoot, instanceDir string) view.RelationView {
	t.Helper()
	ctx := context.Background()
	applyTestWriteFile(t, filepath.Join(instanceDir, "instance.cfg"), "[General]\nname=\"Inject\"\niconKey=default\n")
	prep, err := app.PrepareRelation(ctx, model.PrepareRelationInput{
		ProjectRoot:        projectRoot,
		RuntimeInstanceDir: instanceDir,
		Suggestions:        []string{"config"},
	})
	if err != nil {
		t.Fatalf("PrepareRelation: %v", err)
	}
	rel, err := app.CreateRelation(ctx, prep.PreparationID)
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	return rel
}

// applyTestWaitTask 轮询任务至任一终态。
func applyTestWaitTask(t *testing.T, app *App, taskID string) view.TaskView {
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

// applyTestOperationStatuses 读单操作的历史状态子序。
func applyTestOperationStatuses(t *testing.T, db *sql.DB, taskID, operationID string) []string {
	t.Helper()
	rows, err := db.Query(
		"SELECT to_status FROM operation_journal_events WHERE task_id=? AND operation_id=? ORDER BY seq ASC",
		taskID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func applyTestRowCount(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestApplyActionFailureAfterIntent 验证逐操作两段式的失败分支：running 意图
// 落库后文件动作失败 → failed+result（历史含 running，动作意图先行的直接证据），
// run→recovery_required、零 Baseline/Commit、staging 证据保留、目标未被触碰。
func TestApplyActionFailureAfterIntent(t *testing.T) {
	projectRoot, instanceDir, runtimeRoot := applyTestFixture(t)
	app, db, dataRoot := newApplyEngineStack(t)
	ctx := context.Background()
	rel := applyTestRelation(t, app, projectRoot, instanceDir)
	plan := applyTestResolvedPlan(t, app, rel.RelationID)

	orig := applyActionRunner
	applyActionRunner = func(act *syncstage.Actions, kind string, p syncstage.OwnershipProof, content io.Reader) (syncstage.ApplyResult, error) {
		if p.OperationID == "op_0002" {
			return syncstage.ApplyResult{}, errors.New("injected action failure")
		}
		return orig(act, kind, p, content)
	}
	defer func() { applyActionRunner = orig }()

	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	final := applyTestWaitTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusRecoveryRequired || final.Problem == nil {
		t.Fatalf("任务终态应 recovery_required 带 Problem: %+v", final)
	}
	run, err := app.deps.ApplyRuns.Get(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunRecoveryRequired {
		t.Fatalf("运行态 = %s", run.State)
	}
	// 意图先行的直接证据：op_0002 历史为 pending→running→failed（running 意图
	// 在动作之前落库；动作失败后收口 failed）
	want := []string{model.OperationStatusPending, model.OperationStatusRunning, model.OperationStatusFailed}
	got := applyTestOperationStatuses(t, db, tv.TaskID, "op_0002")
	if len(got) != len(want) {
		t.Fatalf("op_0002 历史状态序 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("op_0002 历史状态序 = %v，期望 %v", got, want)
		}
	}
	// 失败结果码透出（io 注入 → io_error 族）
	op, err := app.deps.Journal.GetOperation(ctx, tv.TaskID, "op_0002")
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != model.OperationStatusFailed || op.Result == nil {
		t.Fatalf("op_0002 应 failed 且带 result: %+v", op)
	}
	// op_0001 已落地（部分完成诚实可见），op_0002 的目标未被创建（动作从未执行）
	if _, err := os.Stat(filepath.Join(runtimeRoot, "config", "a.toml")); err != nil {
		t.Fatalf("op_0001 应已应用: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "config", "b.toml")); !os.IsNotExist(err) {
		t.Fatalf("op_0002 目标不应被创建: %v", err)
	}
	if n := applyTestRowCount(t, db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("不得建 Commit: %d", n)
	}
	if n := applyTestRowCount(t, db, "SELECT COUNT(*) FROM sync_baselines"); n != 0 {
		t.Fatalf("不得推 Baseline: %d", n)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "staging", tv.TaskID)); err != nil {
		t.Fatalf("staging 证据应保留: %v", err)
	}
}

// TestApplyVerifyMismatchNoCommit 验证 verifying 不一致不得 committed：
// 动作成功后目标被篡改 → 复扫与计划目标不一致 → recovery_required，
// 操作保持 applied（verified 未达成），零提交。
func TestApplyVerifyMismatchNoCommit(t *testing.T) {
	projectRoot, instanceDir, runtimeRoot := applyTestFixture(t)
	app, db, dataRoot := newApplyEngineStack(t)
	ctx := context.Background()
	rel := applyTestRelation(t, app, projectRoot, instanceDir)
	plan := applyTestResolvedPlan(t, app, rel.RelationID)

	orig := applyActionRunner
	applyActionRunner = func(act *syncstage.Actions, kind string, p syncstage.OwnershipProof, content io.Reader) (syncstage.ApplyResult, error) {
		res, err := orig(act, kind, p, content)
		if err == nil && p.OperationID == "op_0001" {
			// 动作完成后外部篡改目标，制造复扫与计划目标不一致
			applyTestWriteFile(t, filepath.Join(runtimeRoot, "config", "a.toml"), "a = \"tampered\"\n")
		}
		return res, err
	}
	defer func() { applyActionRunner = orig }()

	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	final := applyTestWaitTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusRecoveryRequired {
		t.Fatalf("复扫不一致应 recovery_required, got %s (%+v)", final.Status, final.Problem)
	}
	run, err := app.deps.ApplyRuns.Get(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunRecoveryRequired || run.CommitID != "" {
		t.Fatalf("运行头: %+v", run)
	}
	// 操作全部 applied（动作成功），但 verified 未达成
	for _, opID := range []string{"op_0001", "op_0002"} {
		op, err := app.deps.Journal.GetOperation(ctx, tv.TaskID, opID)
		if err != nil {
			t.Fatal(err)
		}
		if op.Status != model.OperationStatusApplied {
			t.Fatalf("%s 状态 = %s，期望 applied", opID, op.Status)
		}
	}
	if n := applyTestRowCount(t, db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("不得建 Commit: %d", n)
	}
	if n := applyTestRowCount(t, db, "SELECT COUNT(*) FROM sync_baselines"); n != 0 {
		t.Fatalf("不得推 Baseline: %d", n)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "staging", tv.TaskID)); err != nil {
		t.Fatalf("staging 证据应保留: %v", err)
	}
}

// TestApplyCancelAtOperationBoundary 验证取消语义（票 #37.6）：取消请求在操作
// 边界响应，已 started 的操作做完再停——op_0001 applied，op_0002 滞留 pending，
// run→recovery_required（半途文件系统必须走恢复），零提交。
func TestApplyCancelAtOperationBoundary(t *testing.T) {
	projectRoot, instanceDir, runtimeRoot := applyTestFixture(t)
	app, db, dataRoot := newApplyEngineStack(t)
	ctx := context.Background()
	rel := applyTestRelation(t, app, projectRoot, instanceDir)
	plan := applyTestResolvedPlan(t, app, rel.RelationID)

	var taskID atomic.Value
	orig := applyActionRunner
	applyActionRunner = func(act *syncstage.Actions, kind string, p syncstage.OwnershipProof, content io.Reader) (syncstage.ApplyResult, error) {
		if p.OperationID == "op_0001" {
			// 第一个操作执行中发起取消（等 confirm 返回写入 taskID）
			for i := 0; i < 500 && taskID.Load() == nil; i++ {
				time.Sleep(10 * time.Millisecond)
			}
			id, _ := taskID.Load().(string)
			if id == "" {
				return syncstage.ApplyResult{}, errors.New("test harness: taskID 未就绪")
			}
			if err := app.CancelTask(context.Background(), id); err != nil {
				return syncstage.ApplyResult{}, fmt.Errorf("CancelTask: %w", err)
			}
		}
		return orig(act, kind, p, content)
	}
	defer func() { applyActionRunner = orig }()

	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	taskID.Store(tv.TaskID)
	final := applyTestWaitTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusRecoveryRequired {
		t.Fatalf("取消后任务终态 = %s，期望 recovery_required", final.Status)
	}
	if final.Problem == nil {
		t.Fatalf("取消收口应带 Problem: %+v", final)
	}
	// 已 started 的操作做完：op_0001 applied（runtime a 已物化）；边界停住：
	// op_0002 仍 pending（project b 未创建——动作从未执行）
	if _, err := os.Stat(filepath.Join(runtimeRoot, "config", "a.toml")); err != nil {
		t.Fatalf("op_0001 应已应用: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "config", "b.toml")); !os.IsNotExist(err) {
		t.Fatalf("op_0002 目标不应被创建: %v", err)
	}
	op1, err := app.deps.Journal.GetOperation(ctx, tv.TaskID, "op_0001")
	if err != nil {
		t.Fatal(err)
	}
	if op1.Status != model.OperationStatusApplied {
		t.Fatalf("op_0001 应已做完（applied）, got %s", op1.Status)
	}
	op2, err := app.deps.Journal.GetOperation(ctx, tv.TaskID, "op_0002")
	if err != nil {
		t.Fatal(err)
	}
	if op2.Status != model.OperationStatusPending {
		t.Fatalf("op_0002 应停在 pending, got %s", op2.Status)
	}
	if n := applyTestRowCount(t, db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("取消不得建 Commit: %d", n)
	}
	if n := applyTestRowCount(t, db, "SELECT COUNT(*) FROM sync_baselines"); n != 0 {
		t.Fatalf("取消不得推 Baseline: %d", n)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "staging", tv.TaskID)); err != nil {
		t.Fatalf("staging 证据应保留: %v", err)
	}
}
