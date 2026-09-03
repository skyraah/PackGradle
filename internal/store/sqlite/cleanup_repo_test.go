package sqlite

// cleanup_repo_test.go 覆盖 ports.CleanupRepository 的 SQLite 实现（票 #89，
// ADR-0011 §2/§3）：task_events 留尾截断与序号续行、历史计划行删留边界
//（applied 随提交存亡 / 未收口运行引用保留 / 子计划钉住 / 随行清 conflicts
// 与确认令牌）、预检过期/消费删、终态任务保尾与运行引用守卫、apply_runs 永不删。

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"packgradle/internal/core/model"
)

// cleanupNow 是测试的确定性时钟锚点（RFC3339 字符串直用，免去解析往返）。
const cleanupNow = "2026-09-03T12:00:00Z"

// clExpired 是相对锚点已过期 1 小时的 expires_at。
const clExpired = "2026-09-03T11:00:00Z"

// clFresh 是远未过期的 expires_at。
const clFresh = "2999-01-01T00:00:00Z"

// clInsertSnapshot 直插最小快照行（observed_snapshots；快照外键前态）。
func clInsertSnapshot(t *testing.T, db *sql.DB, id, relationID, side, capturedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO observed_snapshots(id, relation_id, side, binding_fingerprint,
		scanner_name, scanner_version, captured_at, snapshot_digest, normalization_version, policy_digest,
		resource_count, diagnostics_json) VALUES(?,?,?,'sha256:bf','tester','1',?,'sha256:sd',1,'sha256:pd',0,'[]')`,
		id, relationID, side, capturedAt); err != nil {
		t.Fatalf("插入快照 %s: %v", id, err)
	}
}

// clInsertPlan 直插最小计划行（sync_plans；状态/修订/过期时点按参）。
// 前态要求：cl_snap_p / cl_snap_r 两快照已存在。
func clInsertPlan(t *testing.T, db *sql.DB, id, relationID, status string, revision int, expiresAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sync_plans(id, relation_id, kind, input_project_snapshot_id,
		input_runtime_snapshot_id, relation_revision, plan_digest, status, expires_at,
		normalization_version, plan_json) VALUES(?,?,'sync','cl_snap_p','cl_snap_r',?,'sha256:p',?,?,1,'{}')`,
		id, relationID, revision, status, expiresAt); err != nil {
		t.Fatalf("插入计划 %s: %v", id, err)
	}
}

// clInsertRun 直插任务+运行对（apply_runs.task_id 主键外键前态），
// createdAt 由调用方给定（终态保尾测试以创建序记账）。
func clInsertRun(t *testing.T, db *sql.DB, relationID, taskID, planID, state, createdAt string) {
	t.Helper()
	updatedAt := createdAt
	if err := NewTaskRepository(db).Insert(context.Background(), model.Task{
		TaskID: taskID, RelationID: relationID, Kind: model.TaskKindApply,
		Status: model.TaskStatusSucceeded, Phase: "done", CreatedAt: createdAt, UpdatedAt: updatedAt,
		PlanID: planID,
	}); err != nil {
		t.Fatalf("插入任务 %s: %v", taskID, err)
	}
	if _, err := db.Exec(`INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest,
		relation_revision, state, preconditions_json, recovery_refs_json, operation_count, created_at, updated_at)
		VALUES(?,?,?,'sha256:p',1,?,'[]','[]',0,?,?)`, taskID, relationID, planID, state, createdAt, updatedAt); err != nil {
		t.Fatalf("插入运行 %s: %v", taskID, err)
	}
}

// clPlanExists 查计划行存在性。
func clPlanExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var c int
	if err := db.QueryRow("SELECT COUNT(*) FROM sync_plans WHERE id=?", id).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c == 1
}

// TestCleanupTruncateTaskEvents 覆盖条数窗口留尾截断（ADR-0011 §2）：
// 保最近 keep 条、老行删除、现存不足 keep 条空转、重复执行幂等、
// 截断后 stream_sequence 从 MAX+1 续（前端不误判漏包）。
func TestCleanupTruncateTaskEvents(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewEventRepository(db)
	for i := 1; i <= 25; i++ {
		if _, err := repo.Append(ctx, model.EventEnvelope{
			SchemaVersion: model.CurrentSchemaVersion, EventID: fmt.Sprintf("evt_trunc_%02d", i),
			EventType: model.EventTaskUpdated, EmittedAt: cleanupNow, Payload: []byte(`{}`),
		}); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	cleanup := NewCleanupRepository(db)
	n, err := cleanup.TruncateTaskEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 15 {
		t.Fatalf("删除 %d 条，期望 15", n)
	}
	var minSeq int
	if err := db.QueryRowContext(ctx, "SELECT MIN(stream_sequence) FROM task_events").Scan(&minSeq); err != nil {
		t.Fatal(err)
	}
	if minSeq != 16 {
		t.Fatalf("留尾最小序号 %d，期望 16（老 15 条已删）", minSeq)
	}
	// 现存不足 keep 条：空转零删除；幂等重跑同结果。
	if n, err = cleanup.TruncateTaskEvents(ctx, 100); err != nil || n != 0 {
		t.Fatalf("不足窗口空转 = %d/%v，期望 0", n, err)
	}
	if n, err = cleanup.TruncateTaskEvents(ctx, 10); err != nil || n != 0 {
		t.Fatalf("幂等重跑 = %d/%v，期望 0", n, err)
	}
	// 截断后序号从 MAX+1 续（既有硬约束）。
	seq, err := repo.Append(ctx, model.EventEnvelope{
		SchemaVersion: model.CurrentSchemaVersion, EventID: "evt_trunc_after",
		EventType: model.EventTaskUpdated, EmittedAt: cleanupNow, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 26 {
		t.Fatalf("截断后新序号 %d，期望 26（MAX+1 续行）", seq)
	}
}

// TestCleanupTruncateTaskEventsResetFromOne 覆盖清全表分支：表空后序号从 1
// 重来（前端重启以首个事件建基线，不误判漏包，ADR-0011 §2）。
func TestCleanupTruncateTaskEventsResetFromOne(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewEventRepository(db)
	for i := 1; i <= 3; i++ {
		if _, err := repo.Append(ctx, model.EventEnvelope{
			SchemaVersion: model.CurrentSchemaVersion, EventID: fmt.Sprintf("evt_reset_%02d", i),
			EventType: model.EventTaskUpdated, EmittedAt: cleanupNow, Payload: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM task_events"); err != nil {
		t.Fatal(err)
	}
	seq, err := repo.Append(ctx, model.EventEnvelope{
		SchemaVersion: model.CurrentSchemaVersion, EventID: "evt_reset_after_clear",
		EventType: model.EventTaskUpdated, EmittedAt: cleanupNow, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatalf("清全表后新序号 %d，期望 1（从 1 重来）", seq)
	}
}

// TestCleanupDeleteExpiredPlans 覆盖历史计划行删留边界（ADR-0011 §3）。
func TestCleanupDeleteExpiredPlans(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewCleanupRepository(db)
	relationID := fixtureRelation(t, db, "cln")
	// stale 通道（修订过时）需要关系修订号前进。
	if _, err := db.Exec("UPDATE relations SET revision=2 WHERE id=?", relationID); err != nil {
		t.Fatal(err)
	}
	clInsertSnapshot(t, db, "cl_snap_p", relationID, "project", cleanupNow)
	clInsertSnapshot(t, db, "cl_snap_r", relationID, "runtime", cleanupNow)

	// 删：过期 draft（无引用）。
	clInsertPlan(t, db, "plan_expired", relationID, "draft", 2, clExpired)
	// 留：未过期 draft。
	clInsertPlan(t, db, "plan_fresh", relationID, "draft", 2, clFresh)
	// 删：修订过时（stale 投影）——expires 未到但 revision 落后。
	clInsertPlan(t, db, "plan_stale", relationID, "draft", 1, clFresh)
	// 留：applied 行随其运行与提交存亡（apply_runs.plan_id NOT NULL + 运行行
	// 永不删 → confirmed 计划结构上永久保留；schema v6 冻结的保守语义）。
	clInsertPlan(t, db, "plan_applied", relationID, "resolved", 2, clExpired)
	clInsertRun(t, db, relationID, "task_applied", "plan_applied", "committed", cleanupNow)
	if _, err := db.Exec(`INSERT INTO sync_baselines(id, relation_id, created_at, baseline_digest,
		normalization_version) VALUES('cl_base',?,?,'sha256:b',1)`, relationID, cleanupNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sync_commits(id, relation_id, created_at, plan_id,
		verified_project_snapshot_id, verified_runtime_snapshot_id, result_baseline_id, commit_kind,
		completeness, remaining_change_count, summary_json)
		VALUES('cl_commit',?,?,'plan_applied','cl_snap_p','cl_snap_r','cl_base','sync','exact',0,'{}')`,
		relationID, cleanupNow); err != nil {
		t.Fatal(err)
	}
	// 留：提交被 GC 修剪后的 applied 行——运行行仍在（永不删），计划行随之
	// 保留（只可能晚于提交存亡，绝不早删的保守方向）。
	if _, err := db.Exec("DELETE FROM sync_commits WHERE id='cl_commit'"); err != nil {
		t.Fatal(err)
	}
	// 留：过期但被未收口运行（staged）引用。
	clInsertPlan(t, db, "plan_staged", relationID, "resolved", 2, clExpired)
	clInsertRun(t, db, relationID, "task_staged", "plan_staged", "staged", cleanupNow)
	// 留：过期 draft 被存活 resolved 子计划钉住（resolved_from_plan_id 外键）。
	clInsertPlan(t, db, "plan_draft_pinned", relationID, "draft", 2, clExpired)
	if _, err := db.Exec(`INSERT INTO sync_plans(id, relation_id, kind, resolved_from_plan_id,
		input_project_snapshot_id, input_runtime_snapshot_id, relation_revision, plan_digest, status,
		expires_at, normalization_version, plan_json) VALUES('plan_child',?,'sync','plan_draft_pinned',
		'cl_snap_p','cl_snap_r',2,'sha256:child','resolved',?,1,'{}')`, relationID, clFresh); err != nil {
		t.Fatal(err)
	}

	n, err := repo.DeleteExpiredPlans(ctx, cleanupNow)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("删除 %d 条计划，期望 2（expired + stale）", n)
	}
	for _, id := range []string{"plan_expired", "plan_stale"} {
		if clPlanExists(t, db, id) {
			t.Fatalf("计划 %s 应已删除", id)
		}
	}
	for _, id := range []string{"plan_fresh", "plan_applied", "plan_staged", "plan_draft_pinned", "plan_child"} {
		if !clPlanExists(t, db, id) {
			t.Fatalf("计划 %s 应保留", id)
		}
	}
	// 提交已修剪但运行行存续的 applied 行：随运行行保留（apply_runs 永不删）。
	var runs int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM apply_runs WHERE task_id='task_applied'").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("applied 计划的运行行应永不删，现存 %d", runs)
	}
}

// TestCleanupDeleteExpiredPlansChildRelease 覆盖同轮放行：draft 与其 resolved
// 子计划双双过期时，同一轮内先删子（resolved）再删父（draft）。
func TestCleanupDeleteExpiredPlansChildRelease(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewCleanupRepository(db)
	relationID := fixtureRelation(t, db, "clnr")
	clInsertSnapshot(t, db, "cl_snap_p", relationID, "project", cleanupNow)
	clInsertSnapshot(t, db, "cl_snap_r", relationID, "runtime", cleanupNow)
	clInsertPlan(t, db, "plan_parent", relationID, "draft", 1, clExpired)
	if _, err := db.Exec(`INSERT INTO sync_plans(id, relation_id, kind, resolved_from_plan_id,
		input_project_snapshot_id, input_runtime_snapshot_id, relation_revision, plan_digest, status,
		expires_at, normalization_version, plan_json) VALUES('plan_child',?,'sync','plan_parent',
		'cl_snap_p','cl_snap_r',1,'sha256:child','resolved',?,1,'{}')`, relationID, clExpired); err != nil {
		t.Fatal(err)
	}
	n, err := repo.DeleteExpiredPlans(ctx, cleanupNow)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("删除 %d 条，期望 2（子先删、父同轮放行）", n)
	}
	if clPlanExists(t, db, "plan_parent") || clPlanExists(t, db, "plan_child") {
		t.Fatalf("双过期父子计划应同轮全删")
	}
}

// TestCleanupDeleteExpiredPlansCascade 覆盖随行删除：conflicts 与
// plan_confirmations 行随计划行同轮清理（plan_id 外键）。
func TestCleanupDeleteExpiredPlansCascade(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewCleanupRepository(db)
	relationID := fixtureRelation(t, db, "clnc")
	clInsertSnapshot(t, db, "cl_snap_p", relationID, "project", cleanupNow)
	clInsertSnapshot(t, db, "cl_snap_r", relationID, "runtime", cleanupNow)
	clInsertPlan(t, db, "plan_casc", relationID, "draft", 1, clExpired)
	if _, err := db.Exec(`INSERT INTO conflicts(plan_id, resource_id, conflict_kind, detail)
		VALUES('plan_casc','file:a','conflict_modify','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plan_confirmations(plan_id, plan_digest, confirmation_token,
		relation_revision, acknowledgements_json, confirmed_at, expires_at)
		VALUES('plan_casc','sha256:p','tok',1,'[]',?,?)`, cleanupNow, clFresh); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteExpiredPlans(ctx, cleanupNow); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SELECT COUNT(*) FROM sync_plans WHERE id='plan_casc'",
		"SELECT COUNT(*) FROM conflicts WHERE plan_id='plan_casc'",
		"SELECT COUNT(*) FROM plan_confirmations WHERE plan_id='plan_casc'",
	} {
		var c int
		if err := db.QueryRowContext(ctx, q).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("级联残留：%s = %d", q, c)
		}
	}
}

// TestCleanupDeleteExpiredPreparations 覆盖预检删除（创建预检 + 重绑预检）：
// 过期即删、consumed 即删、未过期未消费保留。
func TestCleanupDeleteExpiredPreparations(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewCleanupRepository(db)
	if _, err := db.Exec(`INSERT INTO preparations(preparation_id, created_at, expires_at, consumed_at,
		input_json, project_json, runtime_json, policy_json, checks_json)
		VALUES
		('prep_expired',?,? ,NULL,'{}',NULL,NULL,'{}','[]'),
		('prep_consumed',?,?,'2026-09-03T11:30:00Z','{}',NULL,NULL,'{}','[]'),
		('prep_fresh',?,?,NULL,'{}',NULL,NULL,'{}','[]')`,
		cleanupNow, clExpired, cleanupNow, clFresh, cleanupNow, clFresh); err != nil {
		t.Fatal(err)
	}
	relationID := fixtureRelation(t, db, "clnp")
	if _, err := db.Exec(`INSERT INTO rebind_preparations(preparation_id, relation_id, side, created_at,
		expires_at, consumed_at, input_root_path, fingerprint_changed, baseline_inheritance,
		invalidated_plan_count, checks_json)
		VALUES('rb_consumed',?,'project',?,?,'2026-09-03T11:30:00Z','D:/x',0,'reinitialize',0,'[]')`,
		relationID, cleanupNow, clFresh); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO rebind_preparations(preparation_id, relation_id, side, created_at,
		expires_at, consumed_at, input_root_path, fingerprint_changed, baseline_inheritance,
		invalidated_plan_count, checks_json)
		VALUES('rb_expired',?,'runtime',?,? ,NULL,'D:/y',1,'reinitialize',0,'[]')`,
		relationID, cleanupNow, clExpired); err != nil {
		t.Fatal(err)
	}
	n, err := repo.DeleteExpiredPreparations(ctx, cleanupNow)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("删除 %d 条预检，期望 4（expired + consumed + rebind consumed + rebind expired）", n)
	}
	var c int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM preparations").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatalf("未过期未消费预检应保留，现存 %d", c)
	}
}

// TestCleanupPruneTerminalTasks 覆盖终态任务保尾（ADR-0011 §3）：保最近
// keep 条、运行引用的行守卫跳过（apply_runs.task_id 主键，运行行永不删）、
// 活跃行不触碰、apply_runs 永不删。
func TestCleanupPruneTerminalTasks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewCleanupRepository(db)
	relationID := fixtureRelation(t, db, "clnt")

	// 6 条终态 scan 任务（created_at 递增），keep=3 → 保尾 d/e/f。
	for i := 0; i < 6; i++ {
		ts := time.Date(2026, 9, 1, 0, i, 0, 0, time.UTC).Format(time.RFC3339)
		if err := NewTaskRepository(db).Insert(ctx, model.Task{
			TaskID: fmt.Sprintf("task_scan_%c", 'a'+i), RelationID: relationID,
			Kind: model.TaskKindScan, Status: model.TaskStatusSucceeded, Phase: "done",
			CreatedAt: ts, UpdatedAt: ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 更老的终态任务带运行引用：结构上不可删（运行行永不删 → 任务行被钉住）。
	clInsertSnapshot(t, db, "cl_snap_p", relationID, "project", cleanupNow)
	clInsertSnapshot(t, db, "cl_snap_r", relationID, "runtime", cleanupNow)
	clInsertPlan(t, db, "plan_run", relationID, "resolved", 1, cleanupNow)
	clInsertRun(t, db, relationID, "task_pinned", "plan_run", "committed", "2026-08-31T23:00:00Z")
	// 活跃任务：不在终态集，不触碰。
	if err := NewTaskRepository(db).Insert(ctx, model.Task{
		TaskID: "task_active", RelationID: relationID, Kind: model.TaskKindScan,
		Status: model.TaskStatusRunning, Phase: "scanning",
		CreatedAt: "2026-08-01T00:00:00Z", UpdatedAt: "2026-08-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := repo.PruneTerminalTasks(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("删除 %d 条，期望 3（7 条终态 − 3 保尾 − 1 运行引用守卫）", n)
	}
	for _, id := range []string{"task_pinned", "task_active", "task_scan_d", "task_scan_e", "task_scan_f"} {
		var c int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE id=?", id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Fatalf("任务 %s 应保留", id)
		}
	}
	for _, id := range []string{"task_scan_a", "task_scan_b", "task_scan_c"} {
		var c int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE id=?", id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("任务 %s 应已修剪", id)
		}
	}
	// apply_runs 永不删（墓碑计数分子）。
	var runs int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM apply_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("apply_runs 应永不删，现存 %d", runs)
	}
}
