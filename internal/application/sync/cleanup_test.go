package sync

// cleanup_test.go 覆盖惰性清理通道的应用编排（票 #89，ADR-0011 §2/§3，缝②
// 横切 fake clock 单测族）：10k 条数窗口截断与 stream_sequence 从 MAX+1 续行、
// 旧数据行删留边界（过期/修订过时删除、applied 随运行与提交存亡、tasks 200
// 保底、apply_runs 永不删）、任务终态钩子触发清理通道。时间一律注入 fake
// clock，判定语义（SQL 守卫）的细粒度边界在 sqlite 包 cleanup_repo_test.go。

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/packwiz"
	"packgradle/internal/adapters/prism"
	"packgradle/internal/core/ids"
	"packgradle/internal/core/model"
	"packgradle/internal/store"
	"packgradle/internal/store/objectstore"
	"packgradle/internal/store/sqlite"
)

// fakeClock 是测试注入的固定时钟（cleanup 时间锚点）。
var fakeClock = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

// newCleanupStack 装配含惰性清理面的真实栈（newGCEngineStack 同构 + Cleanup
// 依赖 + fake clock）。
func newCleanupStack(t *testing.T) (*App, *sql.DB) {
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
		GC:            sqlite.NewGCRepository(db),
		GCTrash:       cas,
		Cleanup:       sqlite.NewCleanupRepository(db),
		Tx:            sqlite.NewUnitOfWork(db),
		ProjectScan:   packwiz.New(),
		RuntimeScan:   prism.New(),
		Hasher:        filesystem.NewHasher(),
		Fingerprinter: filesystem.NewFingerprinter(),
		EndpointPaths: filesystem.PathNormalizer{},
		IDs:           ids.New,
		Now:           func() time.Time { return fakeClock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, db
}

// bulkInsertTaskEvents 单事务批量直插事件（stream_sequence from..to 连号），
// 绕开逐条 Append 的逐事务 fsync（万行级播种）。
func bulkInsertTaskEvents(t *testing.T, db *sql.DB, from, to int64) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO task_events(event_id, stream_sequence, event_type,
		emitted_at, relation_id, task_id, payload_json) VALUES(?,?, 'task_updated',
		'2026-09-03T12:00:00Z', NULL, NULL, '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := from; i <= to; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("evt_bulk_%06d", i), i); err != nil {
			t.Fatalf("插入事件 %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// countEvents 统计 task_events 行数与最小 stream_sequence。
func countEvents(t *testing.T, db *sql.DB) (int, int64) {
	t.Helper()
	var n int
	var min sql.NullInt64
	if err := db.QueryRow("SELECT COUNT(*), MIN(stream_sequence) FROM task_events").Scan(&n, &min); err != nil {
		t.Fatal(err)
	}
	return n, min.Int64
}

// TestLazyCleanupTaskEventsWindowAndSequence fake clock 单测：10,000 条窗口
// 截断（按 stream_sequence 留尾）+ 截断后新事件序号从 MAX+1 续（前端重启以
// 首个事件建基线，不误判漏包；ADR-0011 §2）。
func TestLazyCleanupTaskEventsWindowAndSequence(t *testing.T) {
	app, db := newCleanupStack(t)
	ctx := context.Background()

	bulkInsertTaskEvents(t, db, 1, TaskEventsKeep+5) // 10,005 条
	n, minSeq := countEvents(t, db)
	if n != TaskEventsKeep+5 || minSeq != 1 {
		t.Fatalf("播种 %d 条（min=%d），期望 %d 条", n, minSeq, TaskEventsKeep+5)
	}

	if err := app.RunLazyCleanup(ctx); err != nil {
		t.Fatalf("RunLazyCleanup: %v", err)
	}
	n, minSeq = countEvents(t, db)
	if n != TaskEventsKeep {
		t.Fatalf("截断后 %d 条，期望窗口 %d 条", n, TaskEventsKeep)
	}
	if minSeq != 6 {
		t.Fatalf("留尾最小序号 %d，期望 6（老 5 条已删）", minSeq)
	}

	// 截断后序号从 MAX+1 续（既有硬约束）。
	seq, err := sqlite.NewEventRepository(db).Append(ctx, model.EventEnvelope{
		SchemaVersion: model.CurrentSchemaVersion, EventID: "evt_after_truncate",
		EventType: model.EventTaskUpdated, EmittedAt: fakeClock.UTC().Format(time.RFC3339),
		Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if seq != int64(TaskEventsKeep+6) {
		t.Fatalf("截断后新序号 %d，期望 %d（MAX+1 续行）", seq, TaskEventsKeep+6)
	}
}

// cleanupSeedPlan 直插最小计划行（终态/过期时点按参；输入快照需已存在）。
func cleanupSeedPlan(t *testing.T, db *sql.DB, id, relationID, status string, revision int, expiresAt time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sync_plans(id, relation_id, kind, input_project_snapshot_id,
		input_runtime_snapshot_id, relation_revision, plan_digest, status, expires_at,
		normalization_version, plan_json) VALUES(?,?,'sync','cln_snap_pro','cln_snap_run',?,'sha256:p',?,?,1,'{}')`,
		id, relationID, revision, status, expiresAt.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("插入计划 %s: %v", id, err)
	}
}

// TestLazyCleanupOldRowsBoundaries fake clock 单测：旧数据行删留边界
//（ADR-0011 §3）——过期/修订过时计划删除、applied 计划随运行与提交存亡、
// 过期预检/已消费预检删除、终态任务 200 保底、apply_runs 永不删。
func TestLazyCleanupOldRowsBoundaries(t *testing.T) {
	app, db := newCleanupStack(t)
	ctx := context.Background()
	relationID := gcInsertRelation(t, db, "cln")
	if _, err := db.ExecContext(ctx, "UPDATE relations SET revision=2 WHERE id=?", relationID); err != nil {
		t.Fatal(err)
	}
	// 计划输入快照（全部计划行共用；快照清理归 GC 通道，此处不断言）。
	for _, side := range []string{"project", "runtime"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO observed_snapshots(id, relation_id, side,
			binding_fingerprint, scanner_name, scanner_version, captured_at, snapshot_digest,
			normalization_version, policy_digest, resource_count, diagnostics_json)
			VALUES(?,?,?,'sha256:bf','tester','1',?,'sha256:sd',1,'sha256:pd',0,'[]')`,
			"cln_snap_"+side[:3], relationID, side, fakeClock.UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	expired := fakeClock.Add(-time.Hour)
	fresh := fakeClock.Add(time.Hour)

	// 删：过期 draft；留：未过期 draft；删：修订过时（stale）。
	cleanupSeedPlan(t, db, "plan_expired", relationID, "draft", 2, expired)
	cleanupSeedPlan(t, db, "plan_fresh", relationID, "draft", 2, fresh)
	cleanupSeedPlan(t, db, "plan_stale", relationID, "draft", 1, fresh)
	// 留：applied 计划随其运行与提交存亡（提交行在——提交被修剪后仍由运行行
	// 永久钉住，apply_runs.plan_id NOT NULL + 运行行永不删的保守语义）。
	cleanupSeedPlan(t, db, "plan_applied", relationID, "resolved", 2, expired)
	nowStr := fakeClock.UTC().Format(time.RFC3339)
	if err := sqlite.NewTaskRepository(db).Insert(ctx, model.Task{
		TaskID: "task_applied", RelationID: relationID, Kind: model.TaskKindApply,
		Status: model.TaskStatusSucceeded, Phase: "done", CreatedAt: nowStr, UpdatedAt: nowStr,
		PlanID: "plan_applied",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO apply_runs(task_id, relation_id, plan_id,
		plan_digest, relation_revision, state, preconditions_json, recovery_refs_json,
		operation_count, created_at, updated_at)
		VALUES('task_applied',?,'plan_applied','sha256:p',2,'committed','[]','[]',0,?,?)`,
		relationID, nowStr, nowStr); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_baselines(id, relation_id, created_at,
		baseline_digest, normalization_version) VALUES('cln_base',?,?,'sha256:b',1)`,
		relationID, nowStr); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_commits(id, relation_id, created_at, plan_id,
		verified_project_snapshot_id, verified_runtime_snapshot_id, result_baseline_id, commit_kind,
		completeness, remaining_change_count, summary_json)
		VALUES('cln_commit',?,?,'plan_applied','cln_snap_pro','cln_snap_run','cln_base','sync','exact',0,'{}')`,
		relationID, nowStr); err != nil {
		t.Fatal(err)
	}

	// 删：过期预检（expires_at < now）与已消费预检；留：未过期未消费。
	if _, err := db.ExecContext(ctx, `INSERT INTO preparations(preparation_id, created_at,
		expires_at, consumed_at, input_json, project_json, runtime_json, policy_json, checks_json)
		VALUES
		('prep_expired',?,?,NULL,'{}',NULL,NULL,'{}','[]'),
		('prep_consumed',?,?,'2026-09-03T11:30:00Z','{}',NULL,NULL,'{}','[]'),
		('prep_fresh',?,?,NULL,'{}',NULL,NULL,'{}','[]')`,
		nowStr, expired.UTC().Format(time.RFC3339),
		nowStr, fresh.UTC().Format(time.RFC3339),
		nowStr, fresh.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	// 205 条终态 scan 任务（created_at 向 fake now 收敛递增）：保 200、删最老 5。
	for i := 0; i < 205; i++ {
		ts := fakeClock.Add(-time.Duration(300-i) * time.Hour).UTC().Format(time.RFC3339)
		if err := sqlite.NewTaskRepository(db).Insert(ctx, model.Task{
			TaskID: fmt.Sprintf("task_scan_%03d", i), RelationID: relationID,
			Kind: model.TaskKindScan, Status: model.TaskStatusSucceeded, Phase: "done",
			CreatedAt: ts, UpdatedAt: ts,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := app.RunLazyCleanup(ctx); err != nil {
		t.Fatalf("RunLazyCleanup: %v", err)
	}
	// 计划：expired/stale 删，fresh/applied 留。
	for _, id := range []string{"plan_expired", "plan_stale"} {
		var c int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sync_plans WHERE id=?", id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("计划 %s 应已删除", id)
		}
	}
	for _, id := range []string{"plan_fresh", "plan_applied"} {
		var c int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sync_plans WHERE id=?", id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Fatalf("计划 %s 应保留", id)
		}
	}
	// 预检：过期/已消费删，fresh 留。
	var c int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM preparations").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatalf("预检应剩 1 条（fresh），现存 %d", c)
	}
	// 任务：终态保 200。206 条终态（205 scan + 1 applied，后者 created_at 即
	// fake now、在保尾内）→ 最老 6 条（task_scan_000..005）修剪。
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tasks WHERE id='task_scan_005'").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 0 {
		t.Fatalf("最老终态任务 task_scan_005 应已修剪")
	}
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tasks WHERE id='task_scan_204'").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatalf("最新终态任务 task_scan_204 应保留")
	}
	var totalTasks, runs int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks").Scan(&totalTasks); err != nil {
		t.Fatal(err)
	}
	if totalTasks != TerminalTasksKeep {
		t.Fatalf("任务总数 %d，期望 %d（终态保尾窗口）", totalTasks, TerminalTasksKeep)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM apply_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("apply_runs 应永不删，现存 %d", runs)
	}
}

// TestTaskTerminalHookTriggersCleanup 任务终态钩子触发惰性清理（启动时之外的
// 第二时机，ADR-0011 §2/§3）：终态落库成功后异步清理生效，过期历史行被删、
// 存活行不受波及；无定时器（驱动一次终态只清一轮）。
func TestTaskTerminalHookTriggersCleanup(t *testing.T) {
	app, db := newCleanupStack(t)
	ctx := context.Background()
	relationID := gcInsertRelation(t, db, "hook")
	for _, side := range []string{"project", "runtime"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO observed_snapshots(id, relation_id, side,
			binding_fingerprint, scanner_name, scanner_version, captured_at, snapshot_digest,
			normalization_version, policy_digest, resource_count, diagnostics_json)
			VALUES(?,?,?,'sha256:bf','tester','1',?,'sha256:sd',1,'sha256:pd',0,'[]')`,
			"cln_snap_"+side[:3], relationID, side, fakeClock.UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	expired := fakeClock.Add(-time.Hour)
	cleanupSeedPlan(t, db, "plan_hook_gone", relationID, "draft", 1, expired)
	cleanupSeedPlan(t, db, "plan_hook_keep", relationID, "draft", 1, fakeClock.Add(time.Hour))

	// 经 runner 驱动一次任务终态（scan 任务创建 → 推进到 succeeded）。
	runner := app.taskRunner()
	created, err := runner.Create(ctx, relationID, model.TaskKindScan, false)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := runner.Update(ctx, model.Task{
		TaskID: created.TaskID, RelationID: relationID, Kind: model.TaskKindScan,
		Status: model.TaskStatusSucceeded, Phase: "done", Sequence: 1,
		MessageKey: "msg.task.scan.succeeded", MessageArgs: []string{},
		CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt,
	})
	if err != nil {
		t.Fatalf("终态推进: %v", err)
	}
	if terminal.Status != model.TaskStatusSucceeded {
		t.Fatalf("终态 %s，期望 succeeded", terminal.Status)
	}

	// 钩子异步执行：轮询直至过期计划被清理。
	deadline := time.Now().Add(3 * time.Second)
	for {
		var c int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sync_plans WHERE id='plan_hook_gone'").Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("终态钩子未触发清理（过期计划仍在）")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !clnPlanExists(t, db, "plan_hook_keep") {
		t.Fatalf("未过期计划不应被清理")
	}
}

// clnPlanExists 计划存在性断言辅助。
func clnPlanExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var c int
	if err := db.QueryRow("SELECT COUNT(*) FROM sync_plans WHERE id=?", id).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c == 1
}
