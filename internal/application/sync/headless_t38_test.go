package sync_test

// P2-T05 headless（票 #38）：恢复协议经公开栈（transport 同构入口）的端到端落点。
//
//  1. probe 自动裁决收口：预置「进程崩溃遗留」的非终态运行（伪造 journal + 真实
//     staging 证据）→ RecoverInterruptedTasks → mark-applied/redo 自动收口
//     committed 可断言（任务/运行头/文件/健康态/事件）；
//  2. acknowledge 落点：引擎真实失败面产生 recovery_required 运行 →
//     AcknowledgeRecovery → healthy + 头基线不动 + 零 Commit +
//     relation_invalidated 重扫引导 + 幂等重入 + 非法前置拆码。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"packgradle/internal/core/model"
	"packgradle/internal/store/sqlite"
	"packgradle/internal/syncstage"
)

// t38MustJSON 序列化为 RawMessage（测试夹具；失败即致命）。
func t38MustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestHeadlessRecoveryProbeAutoClose 预置崩溃运行（op_0001 已落地 + 其余未写入）
// → 启动恢复 probe 自动裁决收口 committed：mark-applied 与 redo 双路径落点逐字节
// 断言，任务/运行头/关系健康/relation_invalidated 全部落点可查。
func TestHeadlessRecoveryProbeAutoClose(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	plan := mustResolveApplyPlan(t, app, rel, round1Choices)
	if len(plan.Operations) != 3 {
		t.Fatalf("计划操作数 %d，期望 3", len(plan.Operations))
	}
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}

	// 提取逐操作证据要素（源侧 present 期望 = after digest，目标侧 present 期望 =
	// before digest；file: 资源 ID 内嵌目标 root-relative 路径）。
	type opEvidence struct {
		id, targetRel, beforeDigest, afterDigest string
	}
	evByTarget := map[string]opEvidence{}
	opByID := map[string]model.PlannedOperation{}
	var order []string
	for _, op := range plan.Operations {
		opByID[op.ID] = op
		ev := opEvidence{id: op.ID, targetRel: strings.TrimPrefix(string(op.ResourceID), "file:")}
		srcSide := "project"
		if op.Kind == model.OpWriteProject {
			srcSide = "runtime"
		}
		for _, pre := range op.Preconditions {
			if pre.Expected == nil {
				continue
			}
			switch {
			case pre.Side == srcSide && pre.Existence == "present":
				ev.afterDigest = pre.Expected.Digest
			case pre.Side != srcSide && pre.Existence == "present":
				ev.beforeDigest = pre.Expected.Digest
			}
		}
		if ev.afterDigest == "" {
			t.Fatalf("操作 %s 无 after digest 前置", op.ID)
		}
		evByTarget[ev.targetRel] = ev
		order = append(order, ev.targetRel)
	}
	gameDir := filepath.Join(instanceDir, "minecraft")
	rootByTarget := map[string]string{
		"config/a.toml": gameDir,     // write_runtime 目标侧
		"config/b.toml": projectRoot, // write_project 目标侧
		"config/c.toml": gameDir,
	}
	srcByTarget := map[string]string{
		"config/a.toml": filepath.Join(projectRoot, "config", "a.toml"),
		"config/b.toml": filepath.Join(gameDir, "config", "b.toml"),
		"config/c.toml": filepath.Join(projectRoot, "config", "c.toml"),
	}

	// 伪造 journal + 真实 staging 证据（运行头 state=applying，任务 running 僵尸）
	taskID := "task_t38_crash"
	stg, err := syncstage.OpenRun(filepath.Join(dataRoot, "staging"), taskID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rows := make([]model.JournalOperation, 0, len(order))
	for i, targetRel := range order {
		ev := evByTarget[targetRel]
		content, err := os.ReadFile(srcByTarget[targetRel])
		if err != nil {
			t.Fatal(err)
		}
		tempRel, err := stg.StageContent(targetRel, strings.NewReader(string(content)), ev.afterDigest)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := stg.IssueProof(rel.RelationID, ev.id, targetRel, ev.beforeDigest, ev.afterDigest)
		if err != nil {
			t.Fatal(err)
		}
		if err := stg.SaveProof(proof); err != nil {
			t.Fatal(err)
		}
		row := model.JournalOperation{
			TaskID: taskID, OperationID: ev.id, Ordinal: i + 1,
			TargetRelativePath: targetRel, BeforeDigest: ev.beforeDigest, AfterDigest: ev.afterDigest,
			TempRelativePath: tempRel,
			Operation:        t38MustJSON(t, opByID[ev.id]),
		}
		if data, err := json.Marshal(proof); err == nil {
			row.OwnershipProof = data
		}
		rows = append(rows, row)
		// config/a.toml 已落地：崩溃于写后落库前 → mark-applied 象限
		if targetRel == "config/a.toml" {
			if err := os.WriteFile(filepath.Join(rootByTarget[targetRel], filepath.FromSlash(targetRel)), content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 任务行与运行头先行（operation_journal 外键引用 tasks）
	if _, err := db.Exec(`INSERT INTO tasks(id, relation_id, kind, status, phase, sequence,
		can_cancel, completed, total, message_key, message_args_json, plan_id, created_at, updated_at)
		VALUES(?, ?, 'apply', 'running', 'applying', 1, 1, 1, 3,
		'msg.task.apply.applying', '[]', ?, ?, ?)`, taskID, rel.RelationID, plan.PlanID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewApplyRunRepository(db).Insert(ctx, model.ApplyRun{
		TaskID: taskID, RelationID: rel.RelationID, PlanID: plan.PlanID,
		PlanDigest: plan.PlanDigest, RelationRevision: ws.State.RelationRevision,
		State: model.ApplyRunApplying, RecoveryRefs: json.RawMessage("[]"),
		OperationCount: 3, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewOperationJournalRepository(db).InsertBatch(ctx, rows, now); err != nil {
		t.Fatal(err)
	}

	if err := app.RecoverInterruptedTasks(ctx); err != nil {
		t.Fatal(err)
	}

	// 收口落点：probe 自动裁决 → committed
	final, err := app.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != model.TaskStatusSucceeded || final.CommitID == "" {
		t.Fatalf("任务应 succeeded: %+v (%+v)", final, final.Problem)
	}
	ar, err := app.GetApplyRun(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ar.TaskID != taskID || ar.State != "committed" || !ar.StagingCleared {
		t.Fatalf("运行头应 committed: %+v", ar)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "staging", taskID)); !os.IsNotExist(err) {
		t.Fatalf("committed 后 staging 应清理: %v", err)
	}
	ws2, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws2.State.RelationHealth != string(model.HealthHealthy) {
		t.Fatalf("健康态 = %s", ws2.State.RelationHealth)
	}
	// 逐字节落点（mark-applied 的 a 与 redo 的 b）
	mustFileContent(t, filepath.Join(gameDir, "config", "a.toml"), fxApplyA)
	mustFileContent(t, filepath.Join(gameDir, "config", "b.toml"), fxApplyB)
	if n := tableRowCount(t, db,
		"SELECT COUNT(*) FROM task_events WHERE relation_id=? AND event_type='relation_invalidated'",
		rel.RelationID); n < 1 {
		t.Fatalf("relation_invalidated 应至少 1 条, got %d", n)
	}
}

// TestHeadlessAcknowledgeRecoveryPath 引擎真实失败面 → recovery_required →
// AcknowledgeRecovery：healthy + 头基线不动 + 零 Commit + relation_invalidated
// 重扫引导 + 幂等重入 + 非法前置拆码。
func TestHeadlessAcknowledgeRecoveryPath(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	plan := mustResolveApplyPlan(t, app, rel, round1Choices)

	if _, err := db.Exec(`CREATE TRIGGER t38_block_intent BEFORE UPDATE ON operation_journal
		WHEN NEW.status='running' AND OLD.status='pending' AND NEW.operation_id='op_0002'
		BEGIN SELECT RAISE(ABORT, 't38 intent blocked'); END`); err != nil {
		t.Fatal(err)
	}
	tv := mustConfirm(t, app, plan.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusRecoveryRequired {
		t.Fatalf("前置应 recovery_required: %+v", final)
	}

	var headBase, headCommit, headBase2, headCommit2 sql.NullString
	if err := db.QueryRow("SELECT head_baseline_id, head_commit_id FROM relations WHERE id=?",
		rel.RelationID).Scan(&headBase, &headCommit); err != nil {
		t.Fatal(err)
	}
	invalidatedBefore := tableRowCount(t, db,
		"SELECT COUNT(*) FROM task_events WHERE relation_id=? AND event_type='relation_invalidated'",
		rel.RelationID)

	wsAck, err := app.AcknowledgeRecovery(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if wsAck.State.RelationHealth != string(model.HealthHealthy) {
		t.Fatalf("确认后健康态 = %s", wsAck.State.RelationHealth)
	}
	// 头基线不动、不建 Commit（恢复路径不推进 Baseline，ADR-0004 §5）
	if err := db.QueryRow("SELECT head_baseline_id, head_commit_id FROM relations WHERE id=?",
		rel.RelationID).Scan(&headBase2, &headCommit2); err != nil {
		t.Fatal(err)
	}
	if headBase.Valid != headBase2.Valid || headCommit.Valid != headCommit2.Valid {
		t.Fatalf("头引用不得变动: %v/%v -> %v/%v", headBase.Valid, headCommit.Valid, headBase2.Valid, headCommit2.Valid)
	}
	if n := tableRowCount(t, db, "SELECT COUNT(*) FROM sync_commits"); n != 0 {
		t.Fatalf("确认不得建 Commit: %d", n)
	}
	// relation_invalidated 重扫引导 + acknowledged_at 投影
	if n := tableRowCount(t, db,
		"SELECT COUNT(*) FROM task_events WHERE relation_id=? AND event_type='relation_invalidated'",
		rel.RelationID); n != invalidatedBefore+1 {
		t.Fatalf("relation_invalidated %d -> %d", invalidatedBefore, n)
	}
	ar, err := app.GetApplyRun(ctx, rel.RelationID)
	if err != nil || ar.AcknowledgedAt == "" || ar.State != "recovery_required" {
		t.Fatalf("确认投影: %+v err=%v", ar, err)
	}
	// 幂等重入
	if _, err := app.AcknowledgeRecovery(ctx, tv.TaskID); err != nil {
		t.Fatalf("已确认重入应幂等: %v", err)
	}
	// 非法前置：不存在任务
	if _, err := app.AcknowledgeRecovery(ctx, "task_missing"); err == nil || errCode(t, err) != "err.recovery.not_required" {
		t.Fatalf("不存在任务应 err.recovery.not_required: %v", err)
	}
}
