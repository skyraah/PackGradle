package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// schema v5 与 journal 仓库层契约测试（ADR-0004 §1/§2，票 #34）：
// apply_runs 六阶段 / operation_journal 六状态 CHECK 与外键、追加历史 append-only
// （可回答最后已持久化意图）、四仓库往返与守卫、v4→v5 迁移幂等与重建不丢数据。

// applyFixture 是 Apply 仓库测试的最小脚手架：relation + 两侧快照 + 计划 + apply 任务。
type applyFixture struct {
	relationID  string
	planID      string
	taskID      string
	projectSnap model.ObservedSnapshot
	runtimeSnap model.ObservedSnapshot
}

// fixtureApplyScaffold 建出 applyFixture（快照含 mod:modrinth:AANobbMI 资源，
// 供 CommitRepository 的资源身份联表断言）。
func fixtureApplyScaffold(t *testing.T, db *sql.DB, suffix string) applyFixture {
	t.Helper()
	ctx := context.Background()
	relationID := fixtureRelation(t, db, suffix)
	snapshots := NewSnapshotRepository(db)
	projectSnap, runtimeSnap := insertSnapPair(t, snapshots, relationID, suffix)
	plan := fixturePlan(t, "plan_"+suffix, relationID, projectSnap, runtimeSnap)
	if err := NewPlanRepository(db).Insert(ctx, plan); err != nil {
		t.Fatalf("插入计划失败: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	task := model.Task{
		TaskID: "task_" + suffix, RelationID: relationID, Kind: model.TaskKindApply,
		Status: model.TaskStatusQueued, Phase: "apply", PlanID: plan.PlanID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := NewTaskRepository(db).Insert(ctx, task); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	return applyFixture{relationID: relationID, planID: plan.PlanID, taskID: task.TaskID,
		projectSnap: projectSnap, runtimeSnap: runtimeSnap}
}

// fixtureApplyRun 构造 prepared 运行头（含前置条件与恢复引用，供往返断言）。
func fixtureApplyRun(fx applyFixture) model.ApplyRun {
	return model.ApplyRun{
		TaskID: fx.taskID, RelationID: fx.relationID, PlanID: fx.planID,
		PlanDigest: "sha256:plan-digest", RelationRevision: 1, State: model.ApplyRunPrepared,
		Preconditions: []model.Precondition{
			{ResourceID: "mod:modrinth:AANobbMI", Side: "runtime", Existence: "present",
				Expected: &model.ContentRef{Algorithm: "sha256", Digest: "aa11", Size: 1234}},
		},
		RecoveryRefs:   json.RawMessage(`[{"kind":"cas","algorithm":"sha256","digest":"deadbeef"}]`),
		OperationCount: 1,
		CreatedAt:      guardTestTime, UpdatedAt: guardTestTime,
	}
}

// fixtureJournalTask 插入无 Relation 的最小任务（operation_journal 只引用 tasks）。
func fixtureJournalTask(t *testing.T, db *sql.DB, taskID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	task := model.Task{TaskID: taskID, Kind: model.TaskKindApply,
		Status: model.TaskStatusQueued, Phase: "apply", CreatedAt: now, UpdatedAt: now}
	if err := NewTaskRepository(db).Insert(context.Background(), task); err != nil {
		t.Fatalf("插入最小任务失败: %v", err)
	}
}

// fixtureJournalOps 构造三行操作（status 显式 pending，供往返 DeepEqual）。
func fixtureJournalOps(taskID string) []model.JournalOperation {
	return []model.JournalOperation{
		{TaskID: taskID, OperationID: "op_0001", Ordinal: 0, Status: model.OperationStatusPending,
			TargetRelativePath: "mods/sodium.jar", BeforeDigest: "sha256:before-1", AfterDigest: "sha256:after-1",
			TempRelativePath: "staging/" + taskID + "/op_0001.tmp",
			RecoveryRef:      json.RawMessage(`{"kind":"cas","digest":"aa11"}`),
			OwnershipProof:   json.RawMessage(`{"proof":"p1"}`),
			Operation:        json.RawMessage(`{"id":"op_0001","kind":"write_runtime"}`)},
		{TaskID: taskID, OperationID: "op_0002", Ordinal: 1, Status: model.OperationStatusPending,
			TargetRelativePath: "config/jei/jei-client.ini",
			OwnershipProof:     json.RawMessage(`{"proof":"p2"}`),
			Operation:          json.RawMessage(`{"id":"op_0002","kind":"write_runtime"}`),
			Result:             json.RawMessage(`{"note":"pending"}`)},
		{TaskID: taskID, OperationID: "op_0003", Ordinal: 2, Status: model.OperationStatusPending,
			TargetRelativePath: "mods/jei.jar",
			OwnershipProof:     json.RawMessage(`{"proof":"p3"}`),
			Operation:          json.RawMessage(`{"id":"op_0003","kind":"remove_runtime"}`)},
	}
}

// ---- v5 列契约：schema 版本、CHECK、外键、新列 ----

func TestSchemaV5ColumnContract(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// 目标版本断言：Migrate 恒迁至最新版。v5 交付时钉 5（票 #34），v6 起随
	// 版本演进更新（票 #57）；本测试的 v5 列契约断言在最新版上仍然成立。
	if SchemaVersion() != 6 {
		t.Fatalf("SchemaVersion() = %d, 期望 6", SchemaVersion())
	}
	if v := userVersion(t, db); v != SchemaVersion() {
		t.Fatalf("user_version = %d, 期望 %d", v, SchemaVersion())
	}

	// plan_confirmations 增列 consumed_at（确认令牌消费标记）
	if !columnExists(t, db, "plan_confirmations", "consumed_at") {
		t.Error("plan_confirmations 缺少 consumed_at 列")
	}

	// operation_journal.status 六状态 CHECK：合法值放行、非法值拒绝
	fixtureJournalTask(t, db, "task_v5chk")
	if err := NewOperationJournalRepository(db).InsertBatch(ctx,
		fixtureJournalOps("task_v5chk"), guardTestTime); err != nil {
		t.Fatalf("写入操作批失败: %v", err)
	}
	for _, status := range []string{
		model.OperationStatusPending, model.OperationStatusRunning, model.OperationStatusApplied,
		model.OperationStatusVerified, model.OperationStatusFailed, model.OperationStatusCompensated,
	} {
		if _, err := db.Exec(`UPDATE operation_journal SET status=? WHERE task_id='task_v5chk'`, status); err != nil {
			t.Errorf("CHECK 应放行合法 status=%s: %v", status, err)
		}
	}
	if _, err := db.Exec(`UPDATE operation_journal SET status='done' WHERE task_id='task_v5chk'`); err == nil {
		t.Error("CHECK 应拒绝非法 operation_journal.status='done'（v1 时代该列无 CHECK）")
	}

	// operation_journal_events.to_status CHECK 拒绝非法取值
	if _, err := db.Exec(`UPDATE operation_journal_events SET to_status='done' WHERE task_id='task_v5chk'`); err == nil {
		t.Error("CHECK 应拒绝非法 operation_journal_events.to_status='done'")
	}

	// apply_runs 六阶段 CHECK
	fx := fixtureApplyScaffold(t, db, "v5")
	runs := NewApplyRunRepository(db)
	run := fixtureApplyRun(fx)
	if err := runs.Insert(ctx, run); err != nil {
		t.Fatalf("写入运行头失败: %v", err)
	}
	if _, err := db.Exec(`UPDATE apply_runs SET state='flying' WHERE task_id=?`, fx.taskID); err == nil {
		t.Error("CHECK 应拒绝非法 apply_runs.state='flying'")
	}
	for _, state := range []string{
		model.ApplyRunStaged, model.ApplyRunApplying, model.ApplyRunVerifying,
		model.ApplyRunCommitted, model.ApplyRunRecoveryRequired,
	} {
		// 只验证 CHECK 放行合法字面量（状态机路径由仓库测试覆盖），直接打状态列。
		if _, err := db.Exec(`UPDATE apply_runs SET state=? WHERE task_id=?`, state, fx.taskID); err != nil {
			t.Errorf("CHECK 应放行合法 state=%s: %v", state, err)
		}
	}

	// 外键：逐行直接插入（每行一个悬挂引用），EXPECT 外键错误
	rawInsert := func(taskID, relationID, planID string) error {
		_, err := db.Exec(`INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest,
			relation_revision, state, preconditions_json, recovery_refs_json, operation_count, created_at, updated_at)
			VALUES(?,?,?,?,1,'prepared','[]','[]',0,?,?)`,
			taskID, relationID, planID, guardTestTime, guardTestTime)
		return err
	}
	if err := rawInsert("task_fk1", "rel_none", fx.planID); err == nil {
		t.Error("悬挂 relation_id 应被外键拒绝")
	}
	if err := rawInsert("task_fk2", fx.relationID, "plan_none"); err == nil {
		t.Error("悬挂 plan_id 应被外键拒绝")
	}
	if err := rawInsert("task_none", fx.relationID, fx.planID); err == nil {
		t.Error("悬挂 task_id 应被外键拒绝")
	}

	// operation_journal_events 复合外键：只引用已存在的操作行
	if _, err := db.Exec(`INSERT INTO operation_journal_events
		(task_id, seq, operation_id, from_status, to_status, occurred_at)
		VALUES('task_ghost',1,'op_0001','','pending',?)`, guardTestTime); err == nil {
		t.Error("悬挂操作引用应被复合外键拒绝")
	}
}

// columnExists 报告表是否存在指定列。
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) 失败: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("读取 table_info 失败: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

// ---- v4 → v5 迁移：journal 表重建不丢数据、CHECK 补齐、迁移幂等 ----

func TestMigrateV4ToV5RebuildsJournal(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "packgradle.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer db.Close()

	// 手工搭出 v4 库（v1 全量 + v2/v3/v4 顺序执行）
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("建 v1 schema 失败: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatalf("置 user_version 失败: %v", err)
	}
	for _, stmt := range []string{schemaV2, schemaV3, schemaV4} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("手工执行 v2/v3/v4 失败: %v", err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatalf("置 user_version=4 失败: %v", err)
	}

	// v4 阶段预置 journal 行；先证明旧列无 CHECK（'done' 可写），再落合法值
	if _, err := db.Exec(`INSERT INTO tasks(id, kind, status, phase, created_at, updated_at)
		VALUES('task_v4','apply','running','apply','2026-08-30T10:00:00Z','2026-08-30T10:00:00Z')`); err != nil {
		t.Fatalf("预置任务失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO operation_journal
		(task_id, operation_id, ordinal, status, target_relative_path, ownership_proof_json, operation_json)
		VALUES('task_v4','op_1',0,'pending','mods/x.jar','{}','{}')`); err != nil {
		t.Fatalf("预置 journal 行失败: %v", err)
	}
	if _, err := db.Exec(`UPDATE operation_journal SET status='done' WHERE task_id='task_v4'`); err != nil {
		t.Fatalf("v4 阶段 status 应无 CHECK（'done' 可写）: %v", err)
	}
	if _, err := db.Exec(`UPDATE operation_journal SET status='pending' WHERE task_id='task_v4'`); err != nil {
		t.Fatalf("恢复合法 status 失败: %v", err)
	}

	ctx := context.Background()
	if err := Migrate(ctx, db, filepath.Join(dir, "backup")); err != nil {
		t.Fatalf("v4→v5 迁移失败: %v", err)
	}
	// Migrate 迁至最新版（票 #57 起 v5→v6 继续前推）；重建与 CHECK 断言不受影响。
	if v := userVersion(t, db); v != SchemaVersion() {
		t.Fatalf("迁移后 user_version = %d, 期望 %d", v, SchemaVersion())
	}

	// 重建不丢数据：既有行原样保留
	got, err := NewOperationJournalRepository(db).GetOperation(ctx, "task_v4", "op_1")
	if err != nil {
		t.Fatalf("迁移后读取 journal 失败: %v", err)
	}
	if got.Status != model.OperationStatusPending || got.TargetRelativePath != "mods/x.jar" {
		t.Errorf("迁移后 journal 行不一致: %+v", got)
	}

	// CHECK 生效：非法状态拒绝
	if _, err := db.Exec(`UPDATE operation_journal SET status='done' WHERE task_id='task_v4'`); err == nil {
		t.Error("v5 后 CHECK 应拒绝 status='done'")
	}

	// 迁移幂等可重入：再次 Migrate 无副作用
	if err := Migrate(ctx, db, filepath.Join(dir, "backup")); err != nil {
		t.Fatalf("重复 Migrate 应幂等: %v", err)
	}
	if v := userVersion(t, db); v != SchemaVersion() {
		t.Errorf("重复迁移后 user_version = %d, 期望 %d", v, SchemaVersion())
	}

	// 重建后 events 表与 append-only 触发器在位
	if _, err := db.Exec(`INSERT INTO operation_journal_events
		(task_id, seq, operation_id, from_status, to_status, occurred_at)
		VALUES('task_v4',1,'op_1','','pending','2026-08-30T10:00:00Z')`); err != nil {
		t.Fatalf("插入事件失败: %v", err)
	}
	if _, err := db.Exec(`UPDATE operation_journal_events SET to_status='applied' WHERE task_id='task_v4'`); err == nil {
		t.Error("append-only 触发器应拒绝 UPDATE")
	}
	if _, err := db.Exec(`DELETE FROM operation_journal_events WHERE task_id='task_v4'`); err == nil {
		t.Error("append-only 触发器应拒绝 DELETE")
	}
}

// ---- ApplyRunRepository ----

func TestApplyRunRepositoryContract(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fx := fixtureApplyScaffold(t, db, "ar")
	runs := NewApplyRunRepository(db)
	run := fixtureApplyRun(fx)

	if err := runs.Insert(ctx, run); err != nil {
		t.Fatalf("Insert 运行头失败: %v", err)
	}
	got, err := runs.Get(ctx, fx.taskID)
	if err != nil {
		t.Fatalf("Get 运行头失败: %v", err)
	}
	if !reflect.DeepEqual(got, run) {
		t.Errorf("运行头往返不一致:\n got  %+v\n want %+v", got, run)
	}
	// 重复 task_id（一任务一运行）被拒
	if err := runs.Insert(ctx, run); err == nil {
		t.Error("重复 task_id 应被主键拒绝")
	}
	// 悬挂引用哨兵拆码
	orphan := run
	orphan.TaskID = "task_orphan"
	orphan.RelationID = "rel_none"
	if err := runs.Insert(ctx, orphan); !errors.Is(err, ErrRelationNotFound) {
		t.Errorf("悬挂 relation 应返回 ErrRelationNotFound, got %v", err)
	}

	// LatestByRelation：命中与未命中
	latest, ok, err := runs.LatestByRelation(ctx, fx.relationID)
	if err != nil || !ok || latest.TaskID != fx.taskID {
		t.Errorf("LatestByRelation 未命中: ok=%v err=%v got=%+v", ok, err, latest)
	}
	if _, ok, _ := runs.LatestByRelation(ctx, "rel_none"); ok {
		t.Error("无运行关系不应命中")
	}

	// 六阶段单调推进
	next := guardTestTime
	for _, state := range []string{model.ApplyRunStaged, model.ApplyRunApplying, model.ApplyRunVerifying} {
		if err := runs.AdvanceState(ctx, fx.taskID, state, next); err != nil {
			t.Fatalf("推进至 %s 失败: %v", state, err)
		}
	}
	if err := runs.AttachCommit(ctx, fx.taskID, "commit_none", next); !errors.Is(err, ErrNotFound) {
		t.Errorf("悬挂 commit 应返回 ErrNotFound, got %v", err)
	}
	// 先落一个合法提交供 AttachCommit（复用 CommitRepository，细节在其专属测试展开）
	if err := insertFixtureCommit(t, db, fx, "commit_ar", ""); err != nil {
		t.Fatalf("插入脚手架提交失败: %v", err)
	}
	if err := runs.AdvanceState(ctx, fx.taskID, model.ApplyRunCommitted, next); err != nil {
		t.Fatalf("推进至 committed 失败: %v", err)
	}
	if err := runs.AttachCommit(ctx, fx.taskID, "commit_ar", next); err != nil {
		t.Fatalf("AttachCommit 失败: %v", err)
	}
	got, _ = runs.Get(ctx, fx.taskID)
	if got.State != model.ApplyRunCommitted || got.CommitID != "commit_ar" {
		t.Errorf("committed 运行头不一致: %+v", got)
	}
	// 终态不可再迁移（含 recovery_required 出口）
	if err := runs.AdvanceState(ctx, fx.taskID, model.ApplyRunRecoveryRequired, next); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("committed 为终态, got %v", err)
	}

	// 非法跳变：prepared → committed
	fx2 := fixtureApplyScaffold(t, db, "as")
	run2 := fixtureApplyRun(fx2)
	if err := runs.Insert(ctx, run2); err != nil {
		t.Fatalf("Insert run2 失败: %v", err)
	}
	if err := runs.AdvanceState(ctx, fx2.taskID, model.ApplyRunCommitted, next); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("prepared→committed 应返回 ErrInvalidTransition, got %v", err)
	}

	// 失败分支与人工确认（幂等，保留首次时间）
	if err := runs.AdvanceState(ctx, fx2.taskID, model.ApplyRunRecoveryRequired, next); err != nil {
		t.Fatalf("prepared→recovery_required 失败: %v", err)
	}
	if err := runs.MarkAcknowledged(ctx, fx2.taskID, "2026-08-30T12:00:00Z", next); err != nil {
		t.Fatalf("MarkAcknowledged 失败: %v", err)
	}
	if err := runs.MarkAcknowledged(ctx, fx2.taskID, "2026-08-30T13:00:00Z", next); err != nil {
		t.Fatalf("重复 MarkAcknowledged 应幂等: %v", err)
	}
	got2, _ := runs.Get(ctx, fx2.taskID)
	if got2.AcknowledgedAt != "2026-08-30T12:00:00Z" {
		t.Errorf("应保留首次确认时间, got %q", got2.AcknowledgedAt)
	}
	if err := runs.MarkStagingCleared(ctx, fx2.taskID, next); err != nil {
		t.Fatalf("MarkStagingCleared 失败: %v", err)
	}
	got2, _ = runs.Get(ctx, fx2.taskID)
	if !got2.StagingCleared {
		t.Error("staging_cleared 应为 true")
	}
	// recovery_required 为终态
	if err := runs.AdvanceState(ctx, fx2.taskID, model.ApplyRunStaged, next); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("recovery_required 为终态, got %v", err)
	}

	// 不存在的运行
	for name, call := range map[string]error{
		"AdvanceState":       runs.AdvanceState(ctx, "task_none", model.ApplyRunStaged, next),
		"MarkStagingCleared": runs.MarkStagingCleared(ctx, "task_none", next),
		"MarkAcknowledged":   runs.MarkAcknowledged(ctx, "task_none", next, next),
		"AttachCommit":       runs.AttachCommit(ctx, "task_none", "commit_ar", next),
	} {
		if !errors.Is(call, ErrNotFound) {
			t.Errorf("%s 不存在运行应返回 ErrNotFound, got %v", name, call)
		}
	}
	if _, err := runs.Get(ctx, "task_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在应返回 ErrNotFound, got %v", err)
	}
}

// ---- OperationJournalRepository ----

func TestOperationJournalRepositoryContract(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewOperationJournalRepository(db)
	fixtureJournalTask(t, db, "task_j")
	ops := fixtureJournalOps("task_j")

	// 批量插入（初始意图 pending）+ 初始事件
	if err := repo.InsertBatch(ctx, ops, guardTestTime); err != nil {
		t.Fatalf("InsertBatch 失败: %v", err)
	}
	got, err := repo.GetOperation(ctx, "task_j", "op_0001")
	if err != nil {
		t.Fatalf("GetOperation 失败: %v", err)
	}
	if !reflect.DeepEqual(got, ops[0]) {
		t.Errorf("操作行往返不一致:\n got  %+v\n want %+v", got, ops[0])
	}

	// 初始历史事件：每行一条 from=''→pending，seq 1..3
	events, err := repo.ListEvents(ctx, "task_j")
	if err != nil {
		t.Fatalf("ListEvents 失败: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("初始事件数 = %d, 期望 3", len(events))
	}
	for i, ev := range events {
		wantSeq := i + 1
		if ev.Seq != wantSeq || ev.FromStatus != "" || ev.ToStatus != model.OperationStatusPending {
			t.Errorf("初始事件 #%d 不一致: %+v", i, ev)
		}
	}
	last, ok, err := repo.LastEvent(ctx, "task_j")
	if err != nil || !ok {
		t.Fatalf("LastEvent 失败: ok=%v err=%v", ok, err)
	}
	if last.Seq != 3 || last.OperationID != "op_0003" {
		t.Errorf("最后持久化意图不一致: %+v", last)
	}

	// 单调推进（先追加事件再更新当前行）：pending→running→applied→verified
	for _, to := range []string{model.OperationStatusRunning, model.OperationStatusApplied, model.OperationStatusVerified} {
		if err := repo.AdvanceStatus(ctx, "task_j", "op_0001", to, guardTestTime,
			json.RawMessage(`{"probe":"unit"}`)); err != nil {
			t.Fatalf("推进 op_0001 至 %s 失败: %v", to, err)
		}
	}
	got, _ = repo.GetOperation(ctx, "task_j", "op_0001")
	if got.Status != model.OperationStatusVerified {
		t.Errorf("op_0001 status = %q, 期望 verified", got.Status)
	}
	last, _, _ = repo.LastEvent(ctx, "task_j")
	if last.OperationID != "op_0001" || last.FromStatus != model.OperationStatusApplied ||
		last.ToStatus != model.OperationStatusVerified || string(last.Detail) != `{"probe":"unit"}` {
		t.Errorf("推进事件不一致: %+v", last)
	}

	// 非法迁移：跳步 / 同态重放 / 终态出口
	illegal := []struct{ op, to string }{
		{"op_0002", model.OperationStatusVerified}, // 跳过 running/applied
		{"op_0001", model.OperationStatusRunning},  // verified 为终态
		{"op_0001", model.OperationStatusCompensated},
	}
	for _, tc := range illegal {
		if err := repo.AdvanceStatus(ctx, "task_j", tc.op, tc.to, guardTestTime, nil); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s→%s 应返回 ErrInvalidTransition, got %v", tc.op, tc.to, err)
		}
	}

	// 失败分支与补偿完成：pending→running→failed→compensated
	for _, to := range []string{model.OperationStatusRunning, model.OperationStatusFailed, model.OperationStatusCompensated} {
		if err := repo.AdvanceStatus(ctx, "task_j", "op_0002", to, guardTestTime, nil); err != nil {
			t.Fatalf("推进 op_0002 至 %s 失败: %v", to, err)
		}
	}

	// 事件总数 = 3 初始 + 3 推进 + 3 失败分支
	events, _ = repo.ListEvents(ctx, "task_j")
	if len(events) != 9 {
		t.Errorf("事件总数 = %d, 期望 9", len(events))
	}

	// 分页：ordinal 升序，cursor 为最后一条 ordinal
	page1, cursor, err := repo.ListByTask(ctx, "task_j", pageOf("", 2))
	if err != nil || len(page1) != 2 || cursor != "1" {
		t.Fatalf("第 1 页: len=%d cursor=%q err=%v, 期望 len=2 cursor=1", len(page1), cursor, err)
	}
	page2, cursor, err := repo.ListByTask(ctx, "task_j", pageOf(cursor, 2))
	if err != nil || len(page2) != 1 || cursor != "" {
		t.Fatalf("第 2 页: len=%d cursor=%q err=%v, 期望 len=1 cursor=\"\"", len(page2), cursor, err)
	}
	if page2[0].OperationID != "op_0003" {
		t.Errorf("第 2 页首条 = %q, 期望 op_0003", page2[0].OperationID)
	}

	// 不存在
	if _, err := repo.GetOperation(ctx, "task_j", "op_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetOperation 不存在应返回 ErrNotFound, got %v", err)
	}
	if err := repo.AdvanceStatus(ctx, "task_j", "op_none", model.OperationStatusRunning, guardTestTime, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("AdvanceStatus 不存在应返回 ErrNotFound, got %v", err)
	}
	if _, ok, _ := repo.LastEvent(ctx, "task_none"); ok {
		t.Error("无历史任务 LastEvent 应返回 ok=false")
	}

	// append-only：库层触发器拒绝改写/删除历史
	if _, err := db.Exec(`UPDATE operation_journal_events SET to_status='applied' WHERE task_id='task_j'`); err == nil {
		t.Error("仓储层与触发器应拒绝改写历史")
	}
	if _, err := db.Exec(`DELETE FROM operation_journal_events WHERE task_id='task_j'`); err == nil {
		t.Error("仓储层与触发器应拒绝删除历史")
	}

	// 批量原子性：任一行失败整批回滚
	fixtureJournalTask(t, db, "task_j2")
	bad := []model.JournalOperation{
		{TaskID: "task_j2", OperationID: "op_0001", Ordinal: 0, Status: model.OperationStatusPending,
			TargetRelativePath: "mods/a.jar", OwnershipProof: json.RawMessage(`{}`), Operation: json.RawMessage(`{}`)},
		{TaskID: "task_ghost", OperationID: "op_0002", Ordinal: 1, Status: model.OperationStatusPending,
			TargetRelativePath: "mods/b.jar", OwnershipProof: json.RawMessage(`{}`), Operation: json.RawMessage(`{}`)},
	}
	if err := repo.InsertBatch(ctx, bad, guardTestTime); err == nil {
		t.Fatal("悬挂任务引用应使批量插入失败")
	}
	if _, err := repo.GetOperation(ctx, "task_j2", "op_0001"); !errors.Is(err, ErrNotFound) {
		t.Errorf("批量失败应整体回滚, got %v", err)
	}
}

// ---- CommitRepository ----

// insertFixtureCommit 经仓库写入一个合法提交（供 AttachCommit 等测试复用）。
// parentCommitID 非空时同时落其 result 基线所需的父链校验由仓库守卫承担。
func insertFixtureCommit(t *testing.T, db *sql.DB, fx applyFixture, commitID, parentCommitID string) error {
	t.Helper()
	ctx := context.Background()
	resultBase := fixtureBaseline(t, "base_"+commitID, fx.relationID, "")
	if err := NewBaselineRepository(db).Insert(ctx, resultBase); err != nil {
		t.Fatalf("插入结果基线失败: %v", err)
	}
	commit := model.SyncCommit{
		CommitID: commitID, RelationID: fx.relationID, ParentCommitID: parentCommitID,
		CreatedAt: guardTestTime, PlanID: fx.planID,
		VerifiedProjectSnapshotID: fx.projectSnap.SnapshotID,
		VerifiedRuntimeSnapshotID: fx.runtimeSnap.SnapshotID,
		ResultBaselineID:          resultBase.BaselineID,
		CommitKind:                "sync", Completeness: "exact",
		RemainingChangeCount: 0,
		Summary:              json.RawMessage(`{"changed":2}`),
		Changes: []model.CommitChange{
			{
				ResourceID: "file:config/jei/jei-client.ini", ChangeKind: "modify",
				ProjectBefore: &model.Representation{RelativePath: "config/jei/jei-client.ini", Format: "ini"},
				ProjectAfter: &model.Representation{RelativePath: "config/jei/jei-client.ini", Format: "ini",
					Content: &model.ContentRef{Algorithm: "sha256", Digest: "cc33", Size: 88}},
			},
			{
				ResourceID: "mod:modrinth:AANobbMI", ChangeKind: "modify",
				ProjectBefore: &model.Representation{RelativePath: "mods/sodium.pw.toml", Format: "packwiz-mod-toml",
					Content: &model.ContentRef{Algorithm: "sha256", Digest: "aa11", Size: 1234}},
				ProjectAfter: &model.Representation{RelativePath: "mods/sodium.pw.toml", Format: "packwiz-mod-toml",
					Content: &model.ContentRef{Algorithm: "sha256", Digest: "bb22", Size: 2048}},
			},
		},
	}
	return NewCommitRepository(db).Insert(ctx, commit)
}

func TestCommitRepositoryContract(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fx := fixtureApplyScaffold(t, db, "cm")
	commits := NewCommitRepository(db)

	if err := insertFixtureCommit(t, db, fx, "commit_cm", ""); err != nil {
		t.Fatalf("Insert 提交失败: %v", err)
	}

	// 单 commit 读：changes 联资源身份（verified 快照的 resource_representations）
	got, err := commits.GetForRelation(ctx, "commit_cm", fx.relationID)
	if err != nil {
		t.Fatalf("GetForRelation 失败: %v", err)
	}
	if got.CommitID != "commit_cm" || got.CommitKind != "sync" || got.Completeness != "exact" ||
		string(got.Summary) != `{"changed":2}` {
		t.Errorf("提交头不一致: %+v", got)
	}
	if len(got.Changes) != 2 {
		t.Fatalf("changes 数 = %d, 期望 2", len(got.Changes))
	}
	// 按 resource_id 升序：file:… 在前
	first := got.Changes[0]
	if first.ResourceID != "file:config/jei/jei-client.ini" ||
		first.Identity.Provider != "path" || first.Identity.Key != "config/jei/jei-client.ini" {
		t.Errorf("首条变化身份不一致: %+v", first)
	}
	second := got.Changes[1]
	wantIdentity := model.Identity{Provider: "modrinth", Key: "AANobbMI", Confidence: model.ConfidenceHigh}
	if second.ResourceID != "mod:modrinth:AANobbMI" || second.Identity != wantIdentity {
		t.Errorf("第二条变化身份不一致: got %+v, want %+v", second.Identity, wantIdentity)
	}
	wantAfter := &model.Representation{RelativePath: "mods/sodium.pw.toml", Format: "packwiz-mod-toml",
		Content: &model.ContentRef{Algorithm: "sha256", Digest: "bb22", Size: 2048}}
	if !reflect.DeepEqual(second.ProjectAfter, wantAfter) {
		t.Errorf("project_after 往返不一致:\n got  %+v\n want %+v", second.ProjectAfter, wantAfter)
	}

	// 跨关系 / 不存在一律 ErrNotFound（契约 05 err.commit.not_found 口径）
	fx2 := fixtureApplyScaffold(t, db, "cn")
	if _, err := commits.GetForRelation(ctx, "commit_cm", fx2.relationID); !errors.Is(err, ErrNotFound) {
		t.Errorf("跨关系读取应返回 ErrNotFound, got %v", err)
	}
	if _, err := commits.GetForRelation(ctx, "commit_none", fx.relationID); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在读取应返回 ErrNotFound, got %v", err)
	}

	// 守卫：计划跨 Relation 拒绝（fx2 的计划属于 rel_cn，却声明 rel_cm）
	crossPlan := model.SyncCommit{
		CommitID: "commit_cross", RelationID: fx.relationID, CreatedAt: guardTestTime,
		PlanID:                    fx2.planID,
		VerifiedProjectSnapshotID: fx.projectSnap.SnapshotID,
		VerifiedRuntimeSnapshotID: fx.runtimeSnap.SnapshotID,
		ResultBaselineID:          "base_none",
		CommitKind:                "sync", Completeness: "exact",
	}
	if err := commits.Insert(ctx, crossPlan); !errors.Is(err, ErrCrossRelation) {
		t.Errorf("跨 Relation 计划应返回 ErrCrossRelation, got %v", err)
	}

	// 守卫：parent 提交跨 Relation 拒绝
	if err := insertFixtureCommit(t, db, fx2, "commit_cn", ""); err != nil {
		t.Fatalf("插入 fx2 提交失败: %v", err)
	}
	if err := insertFixtureCommit(t, db, fx2, "commit_cn2", "commit_cm"); !errors.Is(err, ErrParentMismatch) {
		t.Errorf("跨 Relation parent 提交应返回 ErrParentMismatch, got %v", err)
	}

	// 单事务原子性：变化行主键冲突 → 提交头一并回滚
	dupBase := fixtureBaseline(t, "base_dup", fx.relationID, "")
	if err := NewBaselineRepository(db).Insert(ctx, dupBase); err != nil {
		t.Fatalf("插入 dup 基线失败: %v", err)
	}
	dup := model.SyncCommit{
		CommitID: "commit_dup", RelationID: fx.relationID, CreatedAt: guardTestTime,
		PlanID:                    fx.planID,
		VerifiedProjectSnapshotID: fx.projectSnap.SnapshotID,
		VerifiedRuntimeSnapshotID: fx.runtimeSnap.SnapshotID,
		ResultBaselineID:          dupBase.BaselineID,
		CommitKind:                "sync", Completeness: "exact",
		Changes: []model.CommitChange{
			{ResourceID: "mod:modrinth:AANobbMI", ChangeKind: "modify"},
			{ResourceID: "mod:modrinth:AANobbMI", ChangeKind: "create"},
		},
	}
	if err := commits.Insert(ctx, dup); err == nil {
		t.Fatal("重复变化行应使写入失败")
	}
	if _, err := commits.GetForRelation(ctx, "commit_dup", fx.relationID); !errors.Is(err, ErrNotFound) {
		t.Errorf("变化行失败时提交头应回滚, got %v", err)
	}

	// 按 relation 分页（id 升序）：为 fx 关系再落一个提交
	if err := insertFixtureCommit(t, db, fx, "commit_cm2", ""); err != nil {
		t.Fatalf("插入第二提交失败: %v", err)
	}
	page1, cursor, err := commits.ListByRelation(ctx, fx.relationID, pageOf("", 1))
	if err != nil || len(page1) != 1 || cursor != "commit_cm" {
		t.Fatalf("第 1 页: len=%d cursor=%q err=%v", len(page1), cursor, err)
	}
	if len(page1[0].Changes) != 0 {
		t.Error("分页投影不应装载 changes")
	}
	page2, cursor, err := commits.ListByRelation(ctx, fx.relationID, pageOf(cursor, 1))
	if err != nil || len(page2) != 1 || cursor != "" {
		t.Fatalf("第 2 页: len=%d cursor=%q err=%v", len(page2), cursor, err)
	}
}

// ---- PlanConfirmationRepository ----

func TestPlanConfirmationRepositoryContract(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fx := fixtureApplyScaffold(t, db, "pc")
	repo := NewPlanConfirmationRepository(db)

	confirmation := model.PlanConfirmation{
		PlanID: fx.planID, PlanDigest: "sha256:plan-digest", ConfirmationToken: "tok_1",
		RelationRevision: 1, Acknowledgements: json.RawMessage(`["overwrite","delete"]`),
		ConfirmedAt: guardTestTime, ExpiresAt: "2999-01-01T00:00:00Z",
	}
	if err := repo.Insert(ctx, confirmation); err != nil {
		t.Fatalf("Insert 确认失败: %v", err)
	}
	// 同 (plan, token) 重复被拒
	if err := repo.Insert(ctx, confirmation); !errors.Is(err, ErrDuplicate) {
		t.Errorf("重复确认应返回 ErrDuplicate, got %v", err)
	}
	// 悬挂计划被拒
	orphan := confirmation
	orphan.PlanID = "plan_none"
	if err := repo.Insert(ctx, orphan); !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("悬挂计划应返回 ErrPlanNotFound, got %v", err)
	}

	items, err := repo.ListByPlan(ctx, fx.planID)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListByPlan: len=%d err=%v", len(items), err)
	}
	if !reflect.DeepEqual(items[0], confirmation) {
		t.Errorf("确认往返不一致:\n got  %+v\n want %+v", items[0], confirmation)
	}

	// 消费语义：成功 → 已消费 → 过期/不存在拆码
	if err := repo.MarkConsumed(ctx, fx.planID, "tok_1"); err != nil {
		t.Fatalf("首次 MarkConsumed 失败: %v", err)
	}
	if err := repo.MarkConsumed(ctx, fx.planID, "tok_1"); !errors.Is(err, ErrConfirmationConsumed) {
		t.Errorf("二次消费应返回 ErrConfirmationConsumed, got %v", err)
	}
	expired := confirmation
	expired.ConfirmationToken = "tok_2"
	expired.ExpiresAt = "2000-01-01T00:00:00Z"
	if err := repo.Insert(ctx, expired); err != nil {
		t.Fatalf("Insert 过期确认失败: %v", err)
	}
	if err := repo.MarkConsumed(ctx, fx.planID, "tok_2"); !errors.Is(err, ErrConfirmationExpired) {
		t.Errorf("过期令牌应返回 ErrConfirmationExpired, got %v", err)
	}
	if err := repo.MarkConsumed(ctx, fx.planID, "tok_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("未知令牌应返回 ErrNotFound, got %v", err)
	}

	// 消费标记往返
	items, _ = repo.ListByPlan(ctx, fx.planID)
	if items[0].ConfirmationToken == "tok_1" && items[0].ConsumedAt == "" {
		t.Error("已消费令牌应带 consumed_at")
	}
}

// ---- RunInTx 事务域挂载 ----

// TestApplyReposJoinRunInTx 验证四个新仓库经 txRepos 绑定外层事务：
// 闭包失败时运行头、操作行与历史一并回滚；成功时整体可见。
func TestApplyReposJoinRunInTx(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fx := fixtureApplyScaffold(t, db, "tx")
	uow := NewUnitOfWork(db)

	ops := fixtureJournalOps(fx.taskID)
	run := func() model.ApplyRun {
		r := fixtureApplyRun(fx)
		r.TaskID = fx.taskID
		return r
	}()

	rollbackFn := func(repos ports.Repos) error {
		if err := repos.ApplyRuns.Insert(ctx, run); err != nil {
			return err
		}
		if err := repos.Journal.InsertBatch(ctx, ops, guardTestTime); err != nil {
			return err
		}
		if err := repos.Journal.AdvanceStatus(ctx, fx.taskID, "op_0001", model.OperationStatusRunning, guardTestTime, nil); err != nil {
			return err
		}
		return errors.New("注入失败")
	}
	if err := uow.RunInTx(ctx, rollbackFn); err == nil {
		t.Fatal("期望闭包错误外抛")
	}
	if _, err := NewApplyRunRepository(db).Get(ctx, fx.taskID); !errors.Is(err, ErrNotFound) {
		t.Errorf("运行头应随闭包回滚, got %v", err)
	}
	if _, err := NewOperationJournalRepository(db).GetOperation(ctx, fx.taskID, "op_0001"); !errors.Is(err, ErrNotFound) {
		t.Errorf("操作行应随闭包回滚, got %v", err)
	}
	if events, _ := NewOperationJournalRepository(db).ListEvents(ctx, fx.taskID); len(events) != 0 {
		t.Errorf("历史应随闭包回滚, got %d 条", len(events))
	}

	if err := uow.RunInTx(ctx, func(repos ports.Repos) error {
		if err := repos.ApplyRuns.Insert(ctx, run); err != nil {
			return err
		}
		return repos.Journal.InsertBatch(ctx, ops, guardTestTime)
	}); err != nil {
		t.Fatalf("成功闭包不应报错: %v", err)
	}
	if _, err := NewApplyRunRepository(db).Get(ctx, fx.taskID); err != nil {
		t.Errorf("提交后应可读运行头: %v", err)
	}
}
