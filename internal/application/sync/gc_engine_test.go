package sync

// gc_engine_test.go 覆盖 GC 引擎的任务化行为（票 #64，ADR-0007 §3–§6）：
// 全局单飞幂等、安全窗口关闭→pending 排队文案→开窗自动续排、空库快跑收口、
// 孤儿三向清扫（file-without-row 入回收站 / .tmp-* 直删 / 零引用孤行删行）、
// 回收站全周期（隔离→搬运→复活）。真实栈装配（sqlite GCRepository +
// objectstore.CAS），窗口构成项以 SQL 夹具注入。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

// newGCEngineStack 装配含 GC 面的真实栈（newApplyEngineStack 同构 + GC 依赖）。
func newGCEngineStack(t *testing.T) (*App, *sql.DB, *objectstore.CAS, string) {
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
	return app, db, cas, dataRoot
}

// gcInsertRelation 直插最小 relation（project/runtime/relation 三行）。
func gcInsertRelation(t *testing.T, db *sql.DB, suffix string) string {
	t.Helper()
	ctx := context.Background()
	endpoints := sqlite.NewEndpointRepository(db)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := endpoints.CreateProject(ctx, model.Project{
		SchemaVersion: model.CurrentSchemaVersion, ProjectID: "prj_gc_" + suffix,
		Adapter: "packwiz", DisplayName: "P", RootPath: "D:/packs/gc_" + suffix,
		BindingFingerprint: "sha256:prj" + suffix, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := endpoints.CreateRuntime(ctx, model.Runtime{
		SchemaVersion: model.CurrentSchemaVersion, RuntimeID: "run_gc_" + suffix,
		Adapter: "prism", DisplayName: "R", RootPath: "D:/inst/gc_" + suffix + "/minecraft",
		AdapterIdentity: "inst-gc-" + suffix, BindingFingerprint: "sha256:run" + suffix, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewRelationRepository(db).Create(ctx, model.Relation{
		SchemaVersion: model.CurrentSchemaVersion, RelationID: "rel_gc_" + suffix,
		ProjectID: "prj_gc_" + suffix, RuntimeID: "run_gc_" + suffix,
		PolicySet: "default-v1", Revision: 1, Health: model.HealthHealthy, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return "rel_gc_" + suffix
}

// gcInsertUnresolvedRun 注入未收口运行（安全窗口构成项）。
func gcInsertUnresolvedRun(t *testing.T, db *sql.DB, relationID, taskID, state string, acknowledged bool) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := sqlite.NewTaskRepository(db).Insert(ctx, model.Task{
		TaskID: taskID, RelationID: relationID, Kind: model.TaskKindApply,
		Status: model.TaskStatusRunning, Phase: "probe", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	ack := "NULL"
	if acknowledged {
		ack = "'" + now + "'"
	}
	// 最小计划行（apply_runs.plan_id 外键要求）：两个输入快照 + 一行 plan。
	for _, side := range []string{"project", "runtime"} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO observed_snapshots(id, relation_id, side, binding_fingerprint, scanner_name,"+
				" scanner_version, captured_at, snapshot_digest, normalization_version, policy_digest,"+
				" resource_count, diagnostics_json) VALUES(?,?,?,'sha256:bf','tester','1',?,'sha256:sd',1,'sha256:pd',0,'[]')",
			"snap_gc_"+taskID+"_"+side, relationID, side, now); err != nil {
			t.Fatal(err)
		}
	}
	planID := "plan_" + taskID
	if _, err := db.ExecContext(ctx,
		"INSERT INTO sync_plans(id, relation_id, kind, input_project_snapshot_id, input_runtime_snapshot_id,"+
			" relation_revision, plan_digest, status, expires_at, normalization_version, plan_json)"+
			" VALUES(?,?,?,'snap_gc_"+taskID+"_project','snap_gc_"+taskID+"_runtime',1,'sha256:p','draft','2999-01-01',1,'{}')",
		planID, relationID, "sync"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest, relation_revision, state,"+
			" preconditions_json, recovery_refs_json, operation_count, created_at, updated_at, acknowledged_at)"+
			" VALUES(?,?,?,'sha256:p',1,?,'[]','[]',0,?,?,"+ack+")",
		taskID, relationID, planID, state, now, now); err != nil {
		t.Fatal(err)
	}
}

// gcWaitTask 轮询任务至期望状态（引擎异步，查询 API 为准）。
func gcWaitTask(t *testing.T, app *App, taskID, want string, timeout time.Duration) model.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		tv, err := app.GetTask(context.Background(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		if tv.Status == want {
			return model.Task{TaskID: tv.TaskID, Status: tv.Status, Phase: tv.Phase,
				MessageKey: tv.MessageKey, Sequence: tv.Sequence}
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务 %s 超时未达 %s（当前 %s/%s msg=%s）", taskID, want, tv.Status, tv.Phase, tv.MessageKey)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestGCRequestGCSingleFlight：全局单飞——并发/连续触发复用同一活跃任务，
// relation_id 为空（全局任务）。
func TestGCRequestGCSingleFlight(t *testing.T) {
	app, _, _, _ := newGCEngineStack(t)
	ctx := context.Background()

	first, err := app.RequestGC(ctx)
	if err != nil {
		t.Fatalf("RequestGC: %v", err)
	}
	second, err := app.RequestGC(ctx)
	if err != nil {
		t.Fatalf("RequestGC 二次: %v", err)
	}
	if first.TaskID != second.TaskID {
		t.Fatalf("单飞破坏：首次 %s 二次 %s", first.TaskID, second.TaskID)
	}
	if first.RelationID != "" {
		t.Fatalf("GC 任务应为全局（relation_id 空），got %q", first.RelationID)
	}
}

// TestGCEngineRunsInOpenWindow：窗口开（无运行/无恢复）时任务直接跑完收口。
func TestGCEngineRunsInOpenWindow(t *testing.T) {
	app, _, _, _ := newGCEngineStack(t)
	tv, err := app.RequestGC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	final := gcWaitTask(t, app, tv.TaskID, model.TaskStatusSucceeded, 5*time.Second)
	if final.MessageKey != "msg.task.gc.succeeded" {
		t.Fatalf("终态文案 %s，期望 msg.task.gc.succeeded", final.MessageKey)
	}
}

// TestGCEngineWindowWaitAndResume：安全窗口关闭（未确认 recovery_required）
// → 任务停 queued 且文案为排队态 → 确认收口（恢复出口 kickGC）→ 开窗自动续排
// 至 succeeded（ADR-0007 §3；契约 06 排队文案）。
func TestGCEngineWindowWaitAndResume(t *testing.T) {
	app, db, _, _ := newGCEngineStack(t)
	ctx := context.Background()
	relationID := gcInsertRelation(t, db, "wait")
	// 未确认 recovery_required：窗口关闭。
	gcInsertUnresolvedRun(t, db, relationID, "gc_engine_rec", model.ApplyRunRecoveryRequired, false)
	if _, err := db.ExecContext(ctx,
		"UPDATE relations SET health='recovery_required' WHERE id=?", relationID); err != nil {
		t.Fatal(err)
	}

	tv, err := app.RequestGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 等待排队文案出现（引擎首个窗口检查落 waiting）。
	waitDeadline := time.Now().Add(3 * time.Second)
	for {
		got, err := app.GetTask(ctx, tv.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if got.MessageKey == "msg.task.gc.waiting" {
			if got.Status != model.TaskStatusQueued {
				t.Fatalf("排队态 status=%s，期望 queued", got.Status)
			}
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("排队文案未出现（当前 msg=%s）", got.MessageKey)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 真实恢复出口收口：acknowledged_at 落库 + 关系复位 healthy + kickGC。
	if _, err := app.AcknowledgeRecovery(ctx, "gc_engine_rec"); err != nil {
		t.Fatalf("AcknowledgeRecovery: %v", err)
	}
	// 开窗自动续排：同一任务跑到 succeeded（新引擎轮询/kick 驱动）。
	final := gcWaitTask(t, app, tv.TaskID, model.TaskStatusSucceeded, 10*time.Second)
	if final.TaskID != tv.TaskID {
		t.Fatalf("续排换了任务 %s ≠ %s", final.TaskID, tv.TaskID)
	}
}

// TestGCEngineOrphanSweepThreeWay：孤儿三向清扫（ADR-0007 §6，GC 末位）——
// file-without-row 入回收站走时钟；.tmp-* 直删；零引用 row-without-file 删行
// 对账；被引用的 row-without-file 保留（Has() 不可见、restore 走降级）。
func TestGCEngineOrphanSweepThreeWay(t *testing.T) {
	app, db, cas, _ := newGCEngineStack(t)
	ctx := context.Background()

	// ①file-without-row：Put 成功后删账目行（模拟 Put 后事务失败）。
	orphanRef, err := cas.Put(ctx, strings.NewReader("孤儿对象内容"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM object_refs WHERE digest=?; DELETE FROM objects WHERE digest=?",
		orphanRef.Digest, orphanRef.Digest); err != nil {
		t.Fatal(err)
	}
	// ②.tmp-* 写中断残渣（objectsRoot 根下，Put 的临时文件前缀）。
	if err := os.WriteFile(filepath.Join(cas.Root(), ".tmp-orph"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ③row-without-file（零引用）：账上有行、盘上无文件。
	lonely := strings.Repeat("ef", 32)
	if _, err := db.ExecContext(ctx,
		"INSERT INTO objects(algorithm, digest, size, state, created_at) VALUES('sha256',?,1,'quarantined',?)",
		lonely, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	tv, err := app.RequestGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gcWaitTask(t, app, tv.TaskID, model.TaskStatusSucceeded, 10*time.Second)

	// ① 孤儿文件应已在回收站（digest 从文件名复原）。
	entries, err := cas.ListTrash()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Digest == orphanRef.Digest {
			found = true
		}
	}
	if !found {
		t.Fatalf("孤儿对象未入回收站（trash=%v）", entries)
	}
	// ② .tmp 残渣已直删。
	if _, err := os.Stat(filepath.Join(cas.Root(), ".tmp-orph")); !os.IsNotExist(err) {
		t.Fatalf(".tmp 残渣未删除: %v", err)
	}
	// ③ 零引用孤行已删行对账。
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM objects WHERE digest=?", lonely).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("零引用孤行未删除")
	}
}

// TestGCEngineReviveObject：回收站人工复活（解压回 objects + 隔离行置回
// ready）——GC 误收的最后一道保险（ADR-0007 §5）。
func TestGCEngineReviveObject(t *testing.T) {
	app, db, cas, _ := newGCEngineStack(t)
	ctx := context.Background()

	digest := putGCObject(t, cas)
	// 隔离 + 搬运（引擎删除协议的手工前两步）。
	if _, err := db.ExecContext(ctx,
		"UPDATE objects SET state='quarantined' WHERE digest=?", digest); err != nil {
		t.Fatal(err)
	}
	if err := cas.MoveToTrash(digest); err != nil {
		t.Fatal(err)
	}
	if ok, _ := cas.Has(ctx, digest); ok {
		t.Fatalf("隔离+搬运后 Has 应不可见")
	}
	if err := app.ReviveObject(ctx, digest); err != nil {
		t.Fatalf("ReviveObject: %v", err)
	}
	if ok, _ := cas.Has(ctx, digest); !ok {
		t.Fatalf("复活后 Has 应可见")
	}
	var state string
	if err := db.QueryRowContext(ctx,
		"SELECT state FROM objects WHERE digest=?", digest).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "ready" {
		t.Fatalf("复活后行状态 %s，期望 ready", state)
	}
	// 幂等：重复复活不报错。
	if err := app.ReviveObject(ctx, digest); err != nil {
		t.Fatalf("重复复活: %v", err)
	}
}

// putGCObject 放入测试对象并返回 digest。
func putGCObject(t *testing.T, cas *objectstore.CAS) string {
	t.Helper()
	ref, err := cas.Put(context.Background(), strings.NewReader("GC 复活测试对象"))
	if err != nil {
		t.Fatal(err)
	}
	return ref.Digest
}
