package sync

// P2-T14 批量化探针测试（票 #48）：applying 相改为「批前单事务整批 running
// 意图 + 批内有界并行动作 + 批后单事务整批结果」后，两条不变量的直接证据——
//
//  1. 意图先行的批形态（ADR-0004 §2 铁律）：批内任一文件动作执行前，其
//     running 意图已持久化；且同批其他成员的意图同时已在案（批前单事务）。
//     探针 = applyActionRunner seam 在每个真实动作前直查 journal。
//  2. 「批内前缀动作执行、批后崩溃」形态可裁决（ADR-0004 §4）：阻塞批内
//     首个动作之后的 worker，冻结出崩溃窗口的真实 journal/文件形态——已执行
//     操作 running + 目标已达 after digest + 证明匹配（矩阵第一行 mark-applied
//     输入），同批未执行操作 running + 目标未写 + staging/证明完整（矩阵第二行
//     幂等 redo 输入），后续批保持 pending（意图未落）。
//
// 自适应批（1→2→4→…→32）在 6/8 操作夹具下的批划分：{1},{2,3},{4..6}/{4..7},{8}。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/syncstage"
)

// applyTestInitFixture 搭建 n 个项目侧 config 文件的 initialize 夹具（运行时
// 侧 config 为空 → 全部 write_runtime create，操作 ID 按文件名字节序编号）。
func applyTestInitFixture(t *testing.T, n int) (projectRoot, instanceDir, runtimeRoot string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instance")
	runtimeRoot = filepath.Join(instanceDir, "minecraft")
	applyTestWriteFile(t, filepath.Join(projectRoot, "pack.toml"),
		"name = \"Batch\"\nauthor = \"tester\"\nversion = \"1.0.0\"\n")
	applyTestWriteFile(t, filepath.Join(projectRoot, "index.toml"), "hash-format = \"sha256\"\n")
	// 运行时侧受管目录存在但为空（端点预检要求可读；不放文件以免进入计划）
	if err := os.MkdirAll(filepath.Join(runtimeRoot, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		applyTestWriteFile(t, filepath.Join(projectRoot, "config", fmt.Sprintf("f%d.toml", i)),
			fmt.Sprintf("f = \"%d\"\n", i))
	}
	return projectRoot, instanceDir, runtimeRoot
}

// applyTestInitResolvedPlan 扫描并产出全量 initialize_from_project 的 resolved
// 计划（n 个 create 操作）。
func applyTestInitResolvedPlan(t *testing.T, app *App, relationID string) view.SyncPlanView {
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
	resolutions := make([]model.Resolution, 0, len(draft.Conflicts))
	for _, c := range draft.Conflicts {
		resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: model.ChoiceInitializeFromProject})
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	return resolved
}

// journalStatus 直查操作行当前状态（seam 探针用；引擎在动作执行窗口不持有
// 数据库事务，单连接池上安全）。
func journalStatus(t *testing.T, db *sql.DB, taskID, operationID string) string {
	t.Helper()
	var status string
	if err := db.QueryRowContext(context.Background(),
		"SELECT status FROM operation_journal WHERE task_id=? AND operation_id=?",
		taskID, operationID).Scan(&status); err != nil {
		t.Fatalf("读取 %s 状态: %v", operationID, err)
	}
	return status
}

// TestApplyBatchIntentPersistsBeforeAction 探针（票 #48 验收 2）：每个真实文件
// 动作执行前，其 running 意图已持久化，且同批其余成员的意图同时已在案
// （批前单事务落整批 running——批首成员动作执行时批尾成员必须已 running）。
func TestApplyBatchIntentPersistsBeforeAction(t *testing.T) {
	const n = 6 // 批划分 {op_0001},{op_0002,op_0003},{op_0004..op_0006}
	projectRoot, instanceDir, _ := applyTestInitFixture(t, n)
	app, db, _ := newApplyEngineStack(t)
	ctx := context.Background()
	rel := applyTestRelation(t, app, projectRoot, instanceDir)
	plan := applyTestInitResolvedPlan(t, app, rel.RelationID)
	if len(plan.Operations) != n {
		t.Fatalf("操作数 = %d，期望 %d", len(plan.Operations), n)
	}

	var taskID atomic.Value
	var mu sync.Mutex
	checked := 0
	orig := applyActionRunner
	applyActionRunner = func(act *syncstage.Actions, kind string, p syncstage.OwnershipProof, content io.Reader) (syncstage.ApplyResult, error) {
		mu.Lock()
		defer mu.Unlock()
		id := taskID.Load().(string)
		// 不变量：本操作的动作执行前，其 running 意图已持久化
		if got := journalStatus(t, db, id, p.OperationID); got != model.OperationStatusRunning {
			t.Errorf("动作 %s 执行前 journal 状态 = %s，期望 running（意图先行）", p.OperationID, got)
		}
		// 批前单事务：同批尾成员的意图必须与批首成员同时已在案
		switch p.OperationID {
		case "op_0001":
			if got := journalStatus(t, db, id, "op_0002"); got != model.OperationStatusPending {
				t.Errorf("批 {op_0001} 动作执行时 op_0002 应仍 pending（批边界未越）, got %s", got)
			}
		case "op_0002":
			if got := journalStatus(t, db, id, "op_0003"); got != model.OperationStatusRunning {
				t.Errorf("op_0002 动作执行时同批 op_0003 意图应已持久化, got %s", got)
			}
		case "op_0004":
			for _, mate := range []string{"op_0005", "op_0006"} {
				if got := journalStatus(t, db, id, mate); got != model.OperationStatusRunning {
					t.Errorf("op_0004 动作执行时同批 %s 意图应已持久化, got %s", mate, got)
				}
			}
		}
		checked++
		return orig(act, kind, p, content)
	}
	defer func() { applyActionRunner = orig }()

	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	taskID.Store(tv.TaskID)
	final := applyTestWaitTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		t.Fatalf("任务终态 = %s (%+v)，期望 succeeded", final.Status, final.Problem)
	}
	mu.Lock()
	defer mu.Unlock()
	if checked != n {
		t.Fatalf("探针覆盖 %d 个动作，期望 %d", checked, n)
	}
	// 全链收口：全部 verified（批事务不改变成功路径终态）
	for i := 1; i <= n; i++ {
		op, err := app.deps.Journal.GetOperation(ctx, tv.TaskID, fmt.Sprintf("op_%04d", i))
		if err != nil {
			t.Fatal(err)
		}
		if op.Status != model.OperationStatusVerified {
			t.Fatalf("op_%04d 状态 = %s，期望 verified", i, op.Status)
		}
	}
}

// TestApplyBatchCrashShapeMatrixDecidable 探针（票 #48 验收 3）：冻结「批内
// 前缀动作执行、批后崩溃」窗口——批 {op_0004..op_0007} 的首个动作（op_0004）
// 真实执行后阻塞全部批内 worker，journal/文件系统形态与进程强杀后一致：
// 已执行者 running + 目标已达 after + 证明匹配（mark-applied 输入），未执行者
// running + 目标未写 + 暂存/证明完整（redo 输入），后续批 pending。
func TestApplyBatchCrashShapeMatrixDecidable(t *testing.T) {
	const n = 8 // 批划分 {1},{2,3},{4..7},{8}：op_0004 是第 3 批首成员
	projectRoot, instanceDir, runtimeRoot := applyTestInitFixture(t, n)
	app, db, dataRoot := newApplyEngineStack(t)
	ctx := context.Background()
	rel := applyTestRelation(t, app, projectRoot, instanceDir)
	plan := applyTestInitResolvedPlan(t, app, rel.RelationID)
	if len(plan.Operations) != n {
		t.Fatalf("操作数 = %d，期望 %d", len(plan.Operations), n)
	}

	firstDone := make(chan struct{}) // op_0004 真实动作完成
	release := make(chan struct{})   // 测试断言完毕后放行被阻塞的 worker
	var once sync.Once
	var taskID atomic.Value
	orig := applyActionRunner
	applyActionRunner = func(act *syncstage.Actions, kind string, p syncstage.OwnershipProof, content io.Reader) (syncstage.ApplyResult, error) {
		if got := journalStatus(t, db, taskID.Load().(string), p.OperationID); got != model.OperationStatusRunning {
			t.Errorf("动作 %s 执行前 journal 状态 = %s，期望 running", p.OperationID, got)
		}
		if p.OperationID == "op_0004" {
			res, err := orig(act, kind, p, content)
			once.Do(func() { close(firstDone) })
			<-release // 冻结崩溃窗口：批结果事务永不发生
			return res, err
		}
		if p.OperationID == "op_0005" || p.OperationID == "op_0006" || p.OperationID == "op_0007" {
			<-release // 同批未执行成员：动作永不发生
			return syncstage.ApplyResult{}, errors.New("test: released after assertions")
		}
		return orig(act, kind, p, content)
	}
	defer func() { applyActionRunner = orig }()

	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	taskID.Store(tv.TaskID)
	select {
	case <-firstDone:
	case <-time.After(30 * time.Second):
		t.Fatal("op_0004 动作未在时限内执行")
	}

	targetOf := func(i int) string {
		return filepath.Join(runtimeRoot, "config", fmt.Sprintf("f%d.toml", i))
	}

	// 前缀批已收口：op_0001..op_0003 applied（批结果事务已提交），目标已物化
	for i := 1; i <= 3; i++ {
		op, err := app.deps.Journal.GetOperation(ctx, tv.TaskID, fmt.Sprintf("op_%04d", i))
		if err != nil {
			t.Fatal(err)
		}
		if op.Status != model.OperationStatusApplied {
			t.Fatalf("op_%04d 状态 = %s，期望 applied（前缀批已收口）", i, op.Status)
		}
		if _, err := os.Stat(targetOf(i)); err != nil {
			t.Fatalf("op_%04d 目标应已物化: %v", i, err)
		}
	}

	// 崩溃窗口核心形态：op_0004 running + 目标已达 after digest（动作已执行、
	// 批结果事务未发生）——矩阵第一行 mark-applied 的完整输入
	op4, err := app.deps.Journal.GetOperation(ctx, tv.TaskID, "op_0004")
	if err != nil {
		t.Fatal(err)
	}
	if op4.Status != model.OperationStatusRunning {
		t.Fatalf("op_0004 状态 = %s，期望 running（已执行未收口）", op4.Status)
	}
	ref, err := syncstage.HashFile(targetOf(4))
	if err != nil {
		t.Fatal(err)
	}
	if op4.AfterDigest == "" || ref.Digest != op4.AfterDigest {
		t.Fatalf("op_0004 目标 digest %s 与 after %s 不符", ref.Digest, op4.AfterDigest)
	}

	// 同批未执行成员 op_0005..op_0007：running + 目标未写 + 暂存/证明完整
	// ——矩阵第二行幂等 redo 的完整输入
	run, err := syncstage.OpenRun(filepath.Join(dataRoot, "staging"), tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 5; i <= 7; i++ {
		opID := fmt.Sprintf("op_%04d", i)
		op, err := app.deps.Journal.GetOperation(ctx, tv.TaskID, opID)
		if err != nil {
			t.Fatal(err)
		}
		if op.Status != model.OperationStatusRunning {
			t.Fatalf("%s 状态 = %s，期望 running（意图已持久化、动作未执行）", opID, op.Status)
		}
		if _, err := os.Stat(targetOf(i)); !os.IsNotExist(err) {
			t.Fatalf("%s 目标不应被创建: %v", opID, err)
		}
		proof, err := run.LoadProof(opID)
		if err != nil {
			t.Fatalf("%s 所有权证明应可加载: %v", opID, err)
		}
		if err := run.VerifyOwnershipProof(proof); err != nil {
			t.Fatalf("%s 所有权证明校验失败: %v", opID, err)
		}
		if op.TempRelativePath == "" {
			t.Fatalf("%s 应带暂存引用", opID)
		}
		stagedAbs, err := run.StageAbs(op.TempRelativePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(stagedAbs); err != nil {
			t.Fatalf("%s 暂存副本应存在: %v", opID, err)
		}
	}

	// 后续批保持 pending：意图未落、动作未启（批边界完整性）
	if got := journalStatus(t, db, tv.TaskID, "op_0008"); got != model.OperationStatusPending {
		t.Fatalf("op_0008 状态 = %s，期望 pending（下一批意图未持久化）", got)
	}

	// 运行头滞留 applying（崩溃时的事务边界）、零 Baseline/Commit
	head, err := app.deps.ApplyRuns.Get(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if head.State != model.ApplyRunApplying {
		t.Fatalf("运行头状态 = %s，期望 applying（崩溃窗口冻结）", head.State)
	}
	if n := applyTestRowCount(t, db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("崩溃窗口不得有 Commit: %d", n)
	}
	if n := applyTestRowCount(t, db, "SELECT COUNT(*) FROM sync_baselines"); n != 0 {
		t.Fatalf("崩溃窗口不得有 Baseline: %d", n)
	}

	// 放行被阻塞的 worker（在测试返回前等引擎收口到终态）：seam 返回错误，
	// 引擎按失败面收口 recovery_required，随后才做 TempDir/db 清理——无泄漏
	// 协程、无打开句柄竞争。
	close(release)
	final := applyTestWaitTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusRecoveryRequired {
		t.Fatalf("放行后任务终态 = %s，期望 recovery_required", final.Status)
	}
}
