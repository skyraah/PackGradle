package sync

// P2-T05 恢复裁决单测族（票 #38，P2 验收规格 §2.2/§2.3）：
// 伪造 journal + 文件系统夹具驱动 probe（不经引擎），逐路断言 ADR-0004 §4 矩阵：
//
//   1. 目标已达 after digest + 所有权证明匹配 → mark-applied（进入可验证路径，
//      与未写入操作的幂等 redo 一并走完 verifying→committed 收口）；
//   2. 目标未写入 + staging 完整 + 前置成立 → 幂等 redo；
//   3. 受阻运行中「本运行已落地且可证归属」的写入 → compensate（create 删除
//      新建文件 / modify 以 CAS before 保全恢复旧内容）；
//   4. 含糊（外部篡改/伪造证明/digest 与证明双不过）→ 保持 recovery_required，
//      且目标绝不被触碰（不凭外观猜测、无法证明归属不得删除）。
//
// 另覆盖：重复恢复幂等、redo 阶段 io 注入（不推 Baseline/不建 Commit/证据保留/
// recovery_required）、committed 崩溃窗口簿记重建、AcknowledgeRecovery 幂等/
// 非法前置/基线不动、scan 类任务 P1 中断语义不回退。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
	"packgradle/internal/syncstage"
)

// recoveryFixture 搭建双操作 initialize 夹具（a→runtime create、b→project create）
// 的完整探测输入：真实关系/计划/快照 + deriveApplyFilePlans 推导的文件执行计划。
type recoveryFixture struct {
	app          *App
	db           *sql.DB
	dataRoot     string
	projectRoot  string
	instanceDir  string
	runtimeRoot  string
	rel          view.RelationView
	plan         model.SyncPlan
	snapP, snapR model.ObservedSnapshot
	fps          []applyFilePlan
}

func newRecoveryFixture(t *testing.T) recoveryFixture {
	t.Helper()
	projectRoot, instanceDir, runtimeRoot := applyTestFixture(t)
	app, db, dataRoot := newApplyEngineStack(t)
	rel := applyTestRelation(t, app, projectRoot, instanceDir)
	resolved := applyTestResolvedPlan(t, app, rel.RelationID)
	ctx := context.Background()
	plan, err := app.deps.Plans.Get(ctx, resolved.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	snapP, err := app.deps.Snapshots.Get(ctx, ws.LatestProjectSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	snapR, err := app.deps.Snapshots.Get(ctx, ws.LatestRuntimeSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	fps := deriveApplyFilePlans(plan, snapP, snapR, nil, projectRoot, runtimeRoot)
	if len(fps) != 2 {
		t.Fatalf("文件执行计划 %d 项，期望 2", len(fps))
	}
	return recoveryFixture{app, db, dataRoot, projectRoot, instanceDir, runtimeRoot,
		rel, plan, snapP, snapR, fps}
}

// recoveryWriteSource 返回操作 after 内容（源侧文件内容，计划即契约）。
func recoveryWriteSource(t *testing.T, fx recoveryFixture, fp applyFilePlan) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fp.sourceRoot, filepath.FromSlash(fp.sourceRel)))
	if err != nil {
		t.Fatalf("读取源内容 %s: %v", fp.sourceRel, err)
	}
	return string(data)
}

// opRefs 组装运行级恢复引用条目。
func opRefs(opID, digest string) map[string]string {
	return map[string]string{"operation_id": opID, "kind": "cas", "algorithm": "sha256",
		"digest": digest, "purpose": "before_preservation"}
}

// seedCrashedRun 预置一次「进程崩溃遗留」的 apply 运行：running 僵尸任务 + 非终态
// 运行头 + staging 运行（暂存副本与所有权证明按计划真实签发）+ journal 操作行
// （默认 pending）。decorate 逐操作注入测试状态（目标文件布点/证明伪造/引用），
// 返回的切片为运行级恢复引用（按序合并）。
func (fx recoveryFixture) seedCrashedRun(t *testing.T, state string,
	decorate func(i int, fp applyFilePlan, stg *syncstage.Run, row *model.JournalOperation) []map[string]string) string {

	t.Helper()
	ctx := context.Background()
	taskID := fx.app.deps.IDs("task_")
	stg, err := syncstage.OpenRun(fx.app.deps.StagingRoot, taskID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := fx.app.deps.Tasks.Insert(ctx, model.Task{
		TaskID: taskID, RelationID: fx.rel.RelationID, Kind: model.TaskKindApply,
		Status: model.TaskStatusRunning, Phase: "applying",
		MessageKey: "msg.task.apply.applying", MessageArgs: []string{},
		PlanID: fx.plan.PlanID, Sequence: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	refs := make([]map[string]string, 0, len(fx.fps))
	rows := make([]model.JournalOperation, 0, len(fx.fps))
	for i, fp := range fx.fps {
		tempRel := ""
		if fp.action != applyActionDelete {
			content := recoveryWriteSource(t, fx, fp)
			tempRel, err = stg.StageContent(fp.targetRel, strings.NewReader(content), fp.afterDigest)
			if err != nil {
				t.Fatalf("暂存 %s: %v", fp.targetRel, err)
			}
		}
		proof, err := stg.IssueProof(fx.rel.RelationID, fp.op.ID, fp.targetRel, fp.beforeDigest, fp.afterDigest)
		if err != nil {
			t.Fatal(err)
		}
		if err := stg.SaveProof(proof); err != nil {
			t.Fatal(err)
		}
		row := model.JournalOperation{
			TaskID: taskID, OperationID: fp.op.ID, Ordinal: i + 1,
			TargetRelativePath: fp.targetRel, BeforeDigest: fp.beforeDigest, AfterDigest: fp.afterDigest,
			TempRelativePath: tempRel,
			Operation:        marshalJSONRaw(fp.op),
			OwnershipProof:   marshalJSONRaw(proof),
		}
		if decorate != nil {
			if opRefs := decorate(i, fp, stg, &row); opRefs != nil {
				refs = append(refs, opRefs...)
			}
		}
		rows = append(rows, row)
	}
	refsJSON := json.RawMessage("[]")
	if len(refs) > 0 {
		refsJSON = marshalJSONRaw(refs)
	}
	if err := fx.app.deps.Journal.InsertBatch(ctx, rows, now); err != nil {
		t.Fatal(err)
	}
	if err := fx.app.deps.ApplyRuns.Insert(ctx, model.ApplyRun{
		TaskID: taskID, RelationID: fx.rel.RelationID, PlanID: fx.plan.PlanID,
		PlanDigest: fx.plan.PlanDigest, RelationRevision: fx.plan.RelationRevision,
		State: state, Preconditions: aggregatePreconditions(fx.plan.Operations),
		RecoveryRefs: refsJSON, OperationCount: len(fx.plan.Operations),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return taskID
}

// recoveryRunDir 运行暂存目录。
func (fx recoveryFixture) recoveryRunDir(taskID string) string {
	return filepath.Join(fx.dataRoot, "staging", taskID)
}

// recoveryEvents 读取任务的追加历史（to_status 序与 detail intent）。
func (fx recoveryFixture) recoveryEvents(t *testing.T, taskID, opID string) [][2]string {
	t.Helper()
	events, err := fx.app.deps.Journal.ListEvents(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out [][2]string
	for _, ev := range events {
		if ev.OperationID != opID {
			continue
		}
		intent := ""
		if len(ev.Detail) > 0 {
			var m map[string]string
			if json.Unmarshal(ev.Detail, &m) == nil {
				intent = m["intent"]
			}
		}
		out = append(out, [2]string{ev.ToStatus, intent})
	}
	return out
}

func recoveryCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func recoveryRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	return string(data)
}

func recoveryHealth(t *testing.T, fx recoveryFixture) string {
	t.Helper()
	var health string
	if err := fx.db.QueryRow("SELECT health FROM relations WHERE id=?", fx.rel.RelationID).Scan(&health); err != nil {
		t.Fatal(err)
	}
	return health
}

func recoveryInvalidatedCount(t *testing.T, fx recoveryFixture) int {
	t.Helper()
	return recoveryCount(t, fx.db,
		"SELECT COUNT(*) FROM task_events WHERE relation_id=? AND event_type='relation_invalidated'",
		fx.rel.RelationID)
}

// TestRecoveryProbeMatrixMarkAppliedAndRedo 裁决矩阵第一/二象限：op_0001 目标已
// 达 after digest 且证明匹配 → mark-applied（意图补记 applied，进入可验证路径）；
// op_0002 目标未写入 + staging 完整 + 前置成立 → 幂等 redo。两路收口后运行走完
// verifying→committed（引擎收口复用）：任务 succeeded、staging 提交后清理、关系
// 复位 healthy、relation_invalidated 发布、头基线推进。
func TestRecoveryProbeMatrixMarkAppliedAndRedo(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := context.Background()
	targetA := filepath.Join(fx.runtimeRoot, "config", "a.toml")
	afterA := recoveryRead(t, filepath.Join(fx.projectRoot, "config", "a.toml"))

	taskID := fx.seedCrashedRun(t, model.ApplyRunApplying, func(i int, fp applyFilePlan,
		stg *syncstage.Run, row *model.JournalOperation) []map[string]string {
		if i == 0 { // op_0001：目标已被本运行写入（崩溃于落库前）
			applyTestWriteFile(t, targetA, afterA)
		}
		return nil // op_0002 未写入 → redo
	})

	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}

	task, err := fx.app.deps.Tasks.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskStatusSucceeded || task.CommitID == "" || task.Outcome != model.TaskOutcomeExact {
		t.Fatalf("任务应 succeeded: %+v", task)
	}
	run, err := fx.app.deps.ApplyRuns.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunCommitted || !run.StagingCleared || run.CommitID != task.CommitID {
		t.Fatalf("运行头应 committed: %+v", run)
	}
	// 逐字节断言（copy 物化）：mark-applied 与 redo 落点一致
	if got := recoveryRead(t, targetA); got != afterA {
		t.Fatalf("runtime a 内容不符: %q", got)
	}
	if got := recoveryRead(t, filepath.Join(fx.projectRoot, "config", "b.toml")); got != "b = \"v1\"\n" {
		t.Fatalf("project b 内容不符: %q", got)
	}
	// journal 事实：op_0001 意图=recovery_mark_applied，op_0002 意图=recovery_redo
	// （初始 pending 行之后的第一个恢复意图）；终态均 verified（committed 事务内收口）
	for opID, wantIntent := range map[string]string{"op_0001": "recovery_mark_applied", "op_0002": "recovery_redo"} {
		evs := fx.recoveryEvents(t, taskID, opID)
		intent := ""
		for _, ev := range evs {
			if ev[1] != "" {
				intent = ev[1]
				break
			}
		}
		if intent != wantIntent {
			t.Fatalf("%s 恢复意图应 %s: %v", opID, wantIntent, evs)
		}
		if evs[len(evs)-1][0] != model.OperationStatusVerified {
			t.Fatalf("%s 终态应 verified: %v", opID, evs)
		}
	}
	// 收口断言：staging 清理 + 关系 healthy + relation_invalidated + 头推进
	if _, err := os.Stat(fx.recoveryRunDir(taskID)); !os.IsNotExist(err) {
		t.Fatalf("committed 后 staging 应清理: %v", err)
	}
	if h := recoveryHealth(t, fx); h != string(model.HealthHealthy) {
		t.Fatalf("关系健康态 = %s", h)
	}
	if n := recoveryInvalidatedCount(t, fx); n < 1 {
		t.Fatalf("relation_invalidated 应至少 1 条, got %d", n)
	}
	if n := recoveryCount(t, fx.db, "SELECT COUNT(*) FROM sync_commits WHERE relation_id=?", fx.rel.RelationID); n != 1 {
		t.Fatalf("sync_commits 应 1 行, got %d", n)
	}
	var head sql.NullString
	if err := fx.db.QueryRow("SELECT head_baseline_id FROM relations WHERE id=?", fx.rel.RelationID).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if !head.Valid {
		t.Fatal("头基线应已推进")
	}
}

// TestRecoveryProbeAmbiguousCompensates 裁决矩阵第三/四象限组合：op_0001 已落地
// （可证归属 → compensate：删除新建文件）；op_0002 目标被外部篡改（内容既非
// before 也非 after → 含糊）。运行保持 recovery_required，含糊目标逐字节不动，
// staging 证据保留，零 Baseline/Commit。
func TestRecoveryProbeAmbiguousCompensates(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := context.Background()
	targetA := filepath.Join(fx.runtimeRoot, "config", "a.toml")
	targetB := filepath.Join(fx.projectRoot, "config", "b.toml")
	tampered := "b = \"user data\"\n"

	taskID := fx.seedCrashedRun(t, model.ApplyRunApplying, func(i int, fp applyFilePlan,
		stg *syncstage.Run, row *model.JournalOperation) []map[string]string {
		switch i {
		case 0: // op_0001：已落地（崩溃于落库前）
			applyTestWriteFile(t, targetA, recoveryWriteSource(t, fx, fp))
		case 1: // op_0002：目标被外部写入第三内容 → 含糊
			applyTestWriteFile(t, targetB, tampered)
		}
		return nil
	})

	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}

	// 补偿：create 的新建文件被删除（可证归属）
	if _, err := os.Stat(targetA); !os.IsNotExist(err) {
		t.Fatalf("op_0001 新建文件应已补偿删除: %v", err)
	}
	// 铁律：含糊目标逐字节不动
	if got := recoveryRead(t, targetB); got != tampered {
		t.Fatalf("含糊目标不得触碰: %q", got)
	}
	// 运行/任务/关系落点
	run, err := fx.app.deps.ApplyRuns.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunRecoveryRequired || run.StagingCleared {
		t.Fatalf("运行头应 recovery_required: %+v", run)
	}
	task, err := fx.app.deps.Tasks.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskStatusRecoveryRequired || task.Problem == nil {
		t.Fatalf("任务应 recovery_required 带 Problem: %+v", task)
	}
	if h := recoveryHealth(t, fx); h != string(model.HealthRecoveryRequired) {
		t.Fatalf("关系健康态 = %s", h)
	}
	// journal：op_0001 pending→failed→compensated（补偿结果码），op_0002 保持 pending
	evs := fx.recoveryEvents(t, taskID, "op_0001")
	want := []string{model.OperationStatusPending, model.OperationStatusFailed, model.OperationStatusCompensated}
	if len(evs) != len(want) {
		t.Fatalf("op_0001 历史序 = %v", evs)
	}
	for i := range want {
		if evs[i][0] != want[i] {
			t.Fatalf("op_0001 历史序 = %v", evs)
		}
	}
	op2, err := fx.app.deps.Journal.GetOperation(ctx, taskID, "op_0002")
	if err != nil || op2.Status != model.OperationStatusPending {
		t.Fatalf("op_0002 应保持 pending: %+v err=%v", op2, err)
	}
	// 四条后果：零 Baseline / 零 Commit / staging 证据保留 / recovery_required（上）
	if n := recoveryCount(t, fx.db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("不得建 Commit: %d", n)
	}
	if n := recoveryCount(t, fx.db, "SELECT COUNT(*) FROM sync_baselines"); n != 0 {
		t.Fatalf("不得推 Baseline: %d", n)
	}
	if _, err := os.Stat(fx.recoveryRunDir(taskID)); err != nil {
		t.Fatalf("staging 证据应保留: %v", err)
	}
	return
}

// TestRecoveryProbeForgedProofNeverTouchesTarget 负例（铁律）：跨运行伪造证明的
// 操作即使目标内容恰为 after digest 也判含糊——补偿跳过（无法证明归属不得删除），
// 文件必须幸存；有效证明的已落地操作照常补偿。
func TestRecoveryProbeForgedProofNeverTouchesTarget(t *testing.T) {
	fx := newRecoveryFixture(t)
	targetA := filepath.Join(fx.runtimeRoot, "config", "a.toml")
	targetB := filepath.Join(fx.projectRoot, "config", "b.toml")

	taskID := fx.seedCrashedRun(t, model.ApplyRunApplying, func(i int, fp applyFilePlan,
		stg *syncstage.Run, row *model.JournalOperation) []map[string]string {
		switch i {
		case 0: // op_0001：有效证明 + 已落地 → 补偿删除
			applyTestWriteFile(t, targetA, recoveryWriteSource(t, fx, fp))
		case 1: // op_0002：目标已落地，但证明以「另一运行」的密钥重签 → 伪造
			applyTestWriteFile(t, targetB, recoveryWriteSource(t, fx, fp))
			other, err := syncstage.OpenRun(fx.app.deps.StagingRoot, "task_forged_other_run")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Remove() })
			forged, err := other.IssueProof(fx.rel.RelationID, fp.op.ID, fp.targetRel, fp.beforeDigest, fp.afterDigest)
			if err != nil {
				t.Fatal(err)
			}
			*row = model.JournalOperation{ // 目标路径对齐的跨运行伪证
				TaskID: row.TaskID, OperationID: row.OperationID, Ordinal: row.Ordinal,
				TargetRelativePath: row.TargetRelativePath, BeforeDigest: row.BeforeDigest,
				AfterDigest: row.AfterDigest, TempRelativePath: row.TempRelativePath,
				Operation: row.Operation, OwnershipProof: marshalJSONRaw(forged),
			}
		}
		return nil
	})

	if err := fx.app.RecoverInterruptedTasks(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(targetA); !os.IsNotExist(err) {
		t.Fatalf("op_0001 应已补偿删除: %v", err)
	}
	// 伪造证明的操作：目标（恰为 after 内容）绝不得被删除/覆盖
	if got := recoveryRead(t, targetB); got != "b = \"v1\"\n" {
		t.Fatalf("伪造证明目标不得触碰: %q", got)
	}
	op2, err := fx.app.deps.Journal.GetOperation(context.Background(), taskID, "op_0002")
	if err != nil || op2.Status != model.OperationStatusPending {
		t.Fatalf("伪造证明操作应保持 pending: %+v err=%v", op2, err)
	}
}

// TestRecoveryProbeModifyCompensateRestoresCASBefore 补偿的 CAS 恢复路径：
// modify 操作已落地（目标=after）且运行受阻时，以运行级恢复引用中的 CAS before
// 保全恢复旧内容（copy 场景「before CAS 引用恢复旧内容」）。
func TestRecoveryProbeModifyCompensateRestoresCASBefore(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := context.Background()

	// 第一轮真实提交（建立基线），使第二轮产生 modify 操作
	confirmed, err := fx.app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: applyTestResolvedPlan(t, fx.app, fx.rel.RelationID).PlanID})
	if err != nil {
		t.Fatal(err)
	}
	if final := applyTestWaitTask(t, fx.app, confirmed.TaskID); final.Status != model.TaskStatusSucceeded {
		t.Fatalf("round1 应成功: %+v", final)
	}
	const beforeA, afterA = "a = \"v1\"\n", "a = \"v2\"\n"
	if err := os.WriteFile(filepath.Join(fx.projectRoot, "config", "a.toml"), []byte(afterA), 0o644); err != nil {
		t.Fatal(err)
	}
	applyTestScanAndWait(t, fx.app, fx.rel.RelationID)
	ws, err := fx.app.GetWorkspace(ctx, fx.rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := fx.app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             fx.rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := fx.app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fx.app.deps.Plans.Get(ctx, resolved.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 {
		t.Fatalf("round2 应为单 modify 操作, got %d", len(plan.Operations))
	}
	snapP, err := fx.app.deps.Snapshots.Get(ctx, plan.InputProjectSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	snapR, err := fx.app.deps.Snapshots.Get(ctx, plan.InputRuntimeSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := fx.app.deps.Baselines.Get(ctx, plan.BaseBaselineID)
	if err != nil {
		t.Fatal(err)
	}
	fx.plan = plan
	fx.snapP, fx.snapR = snapP, snapR
	fx.fps = deriveApplyFilePlans(plan, snapP, snapR, &base, fx.projectRoot, fx.runtimeRoot)
	if len(fx.fps) != 1 || fx.fps[0].action != applyActionModify {
		t.Fatalf("round2 计划应为 modify: %+v", fx.fps)
	}

	// before 内容保全进 CAS（模拟引擎 PreserveBeforeContent 产物）+ 恢复引用
	ref, err := fx.app.deps.CAS.Put(ctx, strings.NewReader(beforeA))
	if err != nil {
		t.Fatal(err)
	}
	targetA := filepath.Join(fx.runtimeRoot, "config", "a.toml")
	taskID := fx.seedCrashedRun(t, model.ApplyRunApplying, func(i int, fp applyFilePlan,
		stg *syncstage.Run, row *model.JournalOperation) []map[string]string {
		if fp.action != applyActionModify {
			t.Fatalf("期望 modify, got %s", fp.action)
		}
		// 目标已被本运行覆盖为 after，随后外部场景受阻（制造同运行第二含糊操作）
		applyTestWriteFile(t, targetA, afterA)
		return []map[string]string{opRefs(fp.op.ID, ref.Digest)}
	})
	// 同运行追加一个含糊操作（外部篡改 project b），使运行受阻
	forgeAmbiguousSecondOp(t, fx, taskID)

	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}

	// modify 补偿：以 CAS before 保全恢复旧内容（既非 after 也非篡改内容）
	if got := recoveryRead(t, targetA); got != beforeA {
		t.Fatalf("modify 补偿应恢复 before 内容: %q", got)
	}
	run, err := fx.app.deps.ApplyRuns.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunRecoveryRequired {
		t.Fatalf("运行头 = %s", run.State)
	}
}

// forgeAmbiguousSecondOp 向已 seed 的运行追加一个外部篡改目标的含糊操作行
// （不走 staging——含糊判定的关键正是证据与目标状态冲突）。
func forgeAmbiguousSecondOp(t *testing.T, fx recoveryFixture, taskID string) {
	t.Helper()
	applyTestWriteFile(t, filepath.Join(fx.projectRoot, "config", "b.toml"), "b = \"external\"\n")
	evil, err := fx.app.deps.CAS.Put(context.Background(), strings.NewReader("b = \"evil\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := fx.app.deps.Journal.InsertBatch(context.Background(), []model.JournalOperation{{
		TaskID: taskID, OperationID: "op_forge", Ordinal: 99,
		TargetRelativePath: "config/b.toml", BeforeDigest: evil.Digest, AfterDigest: evil.Digest,
		Status: model.OperationStatusPending,
	}}, now); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryProbeRedoIOFailureBlocked 磁盘写满 io 注入（验收规格 §2.3）：
// redo 阶段写失败 → 不推 Baseline、不建 Commit、staging 证据保留、recovery_required，
// 已落地操作保持诚实部分完成（io 失败非外部篡改，不触发补偿）。
func TestRecoveryProbeRedoIOFailureBlocked(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := context.Background()
	targetA := filepath.Join(fx.runtimeRoot, "config", "a.toml")
	afterA := recoveryRead(t, filepath.Join(fx.projectRoot, "config", "a.toml"))

	taskID := fx.seedCrashedRun(t, model.ApplyRunApplying, func(i int, fp applyFilePlan,
		stg *syncstage.Run, row *model.JournalOperation) []map[string]string {
		if i == 0 {
			applyTestWriteFile(t, targetA, afterA) // op_0001 已落地
		}
		return nil
	})

	orig := applyActionRunner
	applyActionRunner = func(act *syncstage.Actions, kind string, p syncstage.OwnershipProof, content io.Reader) (syncstage.ApplyResult, error) {
		if p.OperationID == "op_0002" {
			return syncstage.ApplyResult{}, errors.New("injected disk full") // 非 ErrTargetModified 族
		}
		return orig(act, kind, p, content)
	}
	defer func() { applyActionRunner = orig }()

	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}

	// 四条后果齐断言
	if n := recoveryCount(t, fx.db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("不得建 Commit: %d", n)
	}
	if n := recoveryCount(t, fx.db, "SELECT COUNT(*) FROM sync_baselines"); n != 0 {
		t.Fatalf("不得推 Baseline: %d", n)
	}
	if _, err := os.Stat(fx.recoveryRunDir(taskID)); err != nil {
		t.Fatalf("staging 证据应保留: %v", err)
	}
	run, err := fx.app.deps.ApplyRuns.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunRecoveryRequired {
		t.Fatalf("运行头 = %s", run.State)
	}
	// io 失败非含糊：已落地写入保留（诚实部分完成），不补偿
	if got := recoveryRead(t, targetA); got != afterA {
		t.Fatalf("已落地写入不得被 io 失败连带补偿: %q", got)
	}
	op1, err := fx.app.deps.Journal.GetOperation(ctx, taskID, "op_0001")
	if err != nil || op1.Status != model.OperationStatusApplied {
		t.Fatalf("op_0001 应已落 mark-applied 事实: %+v err=%v", op1, err)
	}
}

// TestRecoveryRepeatedInvocationIdempotent 重复恢复幂等（验收规格 §2.1 不变式 4）：
// 首轮恢复补偿后，二次恢复不得重复补偿/删除/覆盖——journal 事件与文件状态零变化。
func TestRecoveryRepeatedInvocationIdempotent(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := context.Background()
	targetA := filepath.Join(fx.runtimeRoot, "config", "a.toml")
	targetB := filepath.Join(fx.projectRoot, "config", "b.toml")
	applyTestWriteFile(t, targetB, "b = \"user data\"\n") // op_0002 外部篡改 → 含糊

	taskID := fx.seedCrashedRun(t, model.ApplyRunApplying, func(i int, fp applyFilePlan,
		stg *syncstage.Run, row *model.JournalOperation) []map[string]string {
		if i == 0 {
			applyTestWriteFile(t, targetA, recoveryWriteSource(t, fx, fp))
		}
		return nil
	})

	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}
	eventsAfterFirst := fx.recoveryEvents(t, taskID, "op_0001")
	stagingFirst := recoveryRead(t, filepath.Join(fx.recoveryRunDir(taskID), "run.key"))

	// 二次恢复：运行已终态，不得产生任何新文件动作或 journal 事件
	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(targetA); !os.IsNotExist(err) {
		t.Fatal("二次恢复不得复活或重复处理已补偿目标")
	}
	if got := recoveryRead(t, targetB); got != "b = \"user data\"\n" {
		t.Fatalf("二次恢复不得触碰含糊目标: %q", got)
	}
	if evs := fx.recoveryEvents(t, taskID, "op_0001"); len(evs) != len(eventsAfterFirst) {
		t.Fatalf("二次恢复不得追加事件: %v vs %v", evs, eventsAfterFirst)
	}
	if got := recoveryRead(t, filepath.Join(fx.recoveryRunDir(taskID), "run.key")); got != stagingFirst {
		t.Fatal("二次恢复不得改动 staging 证据")
	}
	// 第三次仍幂等（acknowledge 前后各一）
	if _, err := fx.app.AcknowledgeRecovery(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if evs := fx.recoveryEvents(t, taskID, "op_0001"); len(evs) != len(eventsAfterFirst) {
		t.Fatalf("确认后恢复不得追加事件: %v", evs)
	}
}

// TestRecoveryReconcileCommittedRunTask 崩溃窗口簿记：提交事务成功后、任务终态
// 落库前强杀（run=committed、任务 running）→ 启动恢复重建任务成功投影
// （completeness 读提交事实），不重跑任何文件动作。
func TestRecoveryReconcileCommittedRunTask(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := context.Background()
	taskID := fx.seedCrashedRun(t, model.ApplyRunApplying, nil)
	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := fx.app.deps.ApplyRuns.Get(ctx, taskID)
	if err != nil || run.State != model.ApplyRunCommitted {
		t.Fatalf("首轮应 committed: %+v err=%v", run, err)
	}
	// 制造崩溃窗口：任务回设 running（保留 commit 引用清零模拟未落库）
	if _, err := fx.db.Exec(`UPDATE tasks SET status='running', commit_id=NULL, outcome=NULL WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	commitsBefore := recoveryCount(t, fx.db, "SELECT COUNT(*) FROM sync_commits")
	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := fx.app.deps.Tasks.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskStatusSucceeded || task.CommitID != run.CommitID || task.Outcome != model.TaskOutcomeExact {
		t.Fatalf("任务成功投影应重建: %+v", task)
	}
	if n := recoveryCount(t, fx.db, "SELECT COUNT(*) FROM sync_commits"); n != commitsBefore {
		t.Fatal("簿记重建不得新建提交")
	}
}

// TestRecoveryScanZombieStillInterrupted P1 语义不回退：scan 类僵尸任务仍按
// err.scan.interrupted 中断收口（不进 apply 恢复管线、不改 Relation 健康）。
func TestRecoveryScanZombieStillInterrupted(t *testing.T) {
	fx := newRecoveryFixture(t)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := fx.db.Exec(`INSERT INTO tasks(id, relation_id, kind, status, phase, sequence,
		can_cancel, completed, total, message_key, message_args_json, created_at, updated_at)
		VALUES('task_scan_zombie', ?, 'scan', 'running', 'scan_project', 3, 1, 1, 4,
		'msg.task.scan.scanning_project', '[]', ?, ?)`, fx.rel.RelationID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := fx.app.RecoverInterruptedTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := fx.app.deps.Tasks.Get(context.Background(), "task_scan_zombie")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskStatusFailed || task.Problem == nil || task.Problem.Code != CodeScanInterrupted {
		t.Fatalf("scan 僵尸应按 P1 口径中断: %+v", task)
	}
}

// TestRecoveryAcknowledgeRecovery 人工确认路径（契约 05 §3.4）：recovery_required
// 运行 acknowledge 后关系复位 healthy、头基线不动、不建 Commit、发布
// relation_invalidated；重复确认幂等；非 recovery_required 前置拒绝。
func TestRecoveryAcknowledgeRecovery(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := context.Background()
	applyTestWriteFile(t, filepath.Join(fx.projectRoot, "config", "b.toml"), "b = \"user data\"\n")
	taskID := fx.seedCrashedRun(t, model.ApplyRunApplying, func(i int, fp applyFilePlan,
		stg *syncstage.Run, row *model.JournalOperation) []map[string]string {
		if i == 0 { // 已落地 create 在受阻运行中被补偿
			applyTestWriteFile(t, filepath.Join(fx.runtimeRoot, "config", "a.toml"), recoveryWriteSource(t, fx, fp))
		}
		return nil
	})
	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}

	var headBase, headCommit sql.NullString
	if err := fx.db.QueryRow("SELECT head_baseline_id, head_commit_id FROM relations WHERE id=?",
		fx.rel.RelationID).Scan(&headBase, &headCommit); err != nil {
		t.Fatal(err)
	}
	invalidatedBefore := recoveryInvalidatedCount(t, fx)

	ws, err := fx.app.AcknowledgeRecovery(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.State.RelationHealth != string(model.HealthHealthy) {
		t.Fatalf("确认后健康态 = %s", ws.State.RelationHealth)
	}
	// 头基线不动、不建 Commit（ADR-0004 §5：恢复路径不推进 Baseline）
	var headBase2, headCommit2 sql.NullString
	if err := fx.db.QueryRow("SELECT head_baseline_id, head_commit_id FROM relations WHERE id=?",
		fx.rel.RelationID).Scan(&headBase2, &headCommit2); err != nil {
		t.Fatal(err)
	}
	if headBase.Valid != headBase2.Valid || headCommit.Valid != headCommit2.Valid {
		t.Fatalf("头引用不得变动: %v %v -> %v %v", headBase, headCommit, headBase2, headCommit2)
	}
	if n := recoveryCount(t, fx.db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("确认不得建 Commit: %d", n)
	}
	// relation_invalidated 引导重扫
	if n := recoveryInvalidatedCount(t, fx); n != invalidatedBefore+1 {
		t.Fatalf("确认应发布 relation_invalidated: %d -> %d", invalidatedBefore, n)
	}
	// acknowledged_at 落库（recovery_required 终态语义收口）
	run, err := fx.app.deps.ApplyRuns.Get(ctx, taskID)
	if err != nil || run.AcknowledgedAt == "" {
		t.Fatalf("acknowledged_at 应落库: %+v err=%v", run, err)
	}
	// 幂等重入：返回当前投影，不再次发布事件、不改首次确认时间
	firstAck := run.AcknowledgedAt
	if _, err := fx.app.AcknowledgeRecovery(ctx, taskID); err != nil {
		t.Fatalf("已确认重入应幂等: %v", err)
	}
	run2, _ := fx.app.deps.ApplyRuns.Get(ctx, taskID)
	if run2.AcknowledgedAt != firstAck {
		t.Fatalf("首次确认时间应保留: %s vs %s", run2.AcknowledgedAt, firstAck)
	}
	if n := recoveryInvalidatedCount(t, fx); n != invalidatedBefore+1 {
		t.Fatalf("幂等重入不得再次发布: %d", n)
	}
	// 非法前置：已 committed 运行 / 不存在的任务
	// （先复原含糊目标，使新种子运行可自动收口 committed）
	applyTestWriteFile(t, filepath.Join(fx.projectRoot, "config", "b.toml"), "b = \"v1\"\n")
	committedTask := fx.seedCrashedRun(t, model.ApplyRunApplying, nil)
	if err := fx.app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if run3, err := fx.app.deps.ApplyRuns.Get(ctx, committedTask); err != nil || run3.State != model.ApplyRunCommitted {
		t.Fatalf("前置运行应 committed: %+v err=%v", run3, err)
	}
	if _, err := fx.app.AcknowledgeRecovery(ctx, committedTask); err == nil ||
		errs.CodeOf(err) != CodeRecoveryNotRequired {
		t.Fatalf("committed 运行应拒绝: %v", err)
	}
	if _, err := fx.app.AcknowledgeRecovery(ctx, "task_missing"); err == nil ||
		errs.CodeOf(err) != CodeRecoveryNotRequired {
		t.Fatalf("不存在任务应拒绝: %v", err)
	}
}
