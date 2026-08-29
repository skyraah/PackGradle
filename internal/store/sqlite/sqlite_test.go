package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// openTestDB 打开临时目录中的全新数据库并完成迁移，测试结束自动关闭。
// 不使用 t.Setenv / t.Parallel（数据库与其子进程资源按目录隔离）。
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "packgradle.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db, filepath.Join(t.TempDir(), "backup")); err != nil {
		db.Close()
		t.Fatalf("Migrate 失败: %v", err)
	}
	return db
}

// userVersion 读取 PRAGMA user_version。
func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("读取 user_version 失败: %v", err)
	}
	return v
}

// fixtureRelation 创建 project/runtime/relation 三件套并返回 relation id。
func fixtureRelation(t *testing.T, db *sql.DB, suffix string) string {
	t.Helper()
	ctx := context.Background()
	endpoints := NewEndpointRepository(db)
	now := time.Now().UTC().Format(time.RFC3339)
	proj := model.Project{
		SchemaVersion: model.CurrentSchemaVersion, ProjectID: "prj_" + suffix,
		Adapter: "packwiz", DisplayName: "Project " + suffix,
		RootPath: "D:/packs/" + suffix, BindingFingerprint: "sha256:prj" + suffix, CreatedAt: now,
	}
	rt := model.Runtime{
		SchemaVersion: model.CurrentSchemaVersion, RuntimeID: "run_" + suffix,
		Adapter: "prism", DisplayName: "Runtime " + suffix,
		RootPath: "D:/instances/" + suffix + "/minecraft", AdapterIdentity: "inst-" + suffix,
		BindingFingerprint: "sha256:run" + suffix, CreatedAt: now,
	}
	if err := endpoints.CreateProject(ctx, proj); err != nil {
		t.Fatalf("创建 Project 失败: %v", err)
	}
	if err := endpoints.CreateRuntime(ctx, rt); err != nil {
		t.Fatalf("创建 Runtime 失败: %v", err)
	}
	relations := NewRelationRepository(db)
	rel := model.Relation{
		SchemaVersion: model.CurrentSchemaVersion, RelationID: "rel_" + suffix,
		ProjectID: proj.ProjectID, RuntimeID: rt.RuntimeID,
		PolicySet: "default-v1", Revision: 1, Health: model.HealthHealthy, CreatedAt: now,
	}
	if err := relations.Create(ctx, rel); err != nil {
		t.Fatalf("创建 Relation 失败: %v", err)
	}
	return rel.RelationID
}

// fixtureSnapshot 构造一条含资源与诊断的快照。
func fixtureSnapshot(id, relationID string, side model.Side, capturedAt string) model.ObservedSnapshot {
	return model.ObservedSnapshot{
		SchemaVersion:        model.CurrentSchemaVersion,
		SnapshotID:           id,
		RelationID:           relationID,
		Side:                 side,
		CapturedAt:           capturedAt,
		BindingFingerprint:   "sha256:bind-" + id,
		SnapshotDigest:       "sha256:digest-" + id,
		NormalizationVersion: 1,
		PolicyDigest:         "sha256:policy",
		Scanner:              model.ScannerInfo{Name: "packwiz-probe", Version: "1.2.3"},
		Resources: map[model.ResourceID]model.ResourceObservation{
			"mod:modrinth:AANobbMI": {
				ResourceID: "mod:modrinth:AANobbMI",
				Kind:       model.ResourceMod,
				Identity:   model.Identity{Provider: "modrinth", Key: "AANobbMI", Confidence: model.ConfidenceHigh},
				Representation: model.Representation{
					RelativePath: "mods/sodium.pw.toml",
					Format:       "packwiz-mod-toml",
					Content:      &model.ContentRef{Algorithm: "sha256", Digest: "aa11", Size: 1234},
					Metadata:     map[string]string{model.MetaVersion: "0.6.5", model.MetaDisplayName: "Sodium"},
				},
				PolicyID: "rule-mod",
			},
			"file:config/jei/jei-client.ini": {
				ResourceID: "file:config/jei/jei-client.ini",
				Kind:       model.ResourceTextFile,
				Identity:   model.Identity{Provider: "path", Key: "config/jei/jei-client.ini"},
				Representation: model.Representation{
					RelativePath: "config/jei/jei-client.ini",
					Format:       "ini",
				},
				PolicyID: "rule-file",
			},
		},
		Diagnostics: []model.Diagnostic{
			{Severity: "warning", Code: "diag.scan.meta", Args: []string{"mods/sodium.pw.toml"},
				Detail: "缺少声明 hash", ResourceID: "mod:modrinth:AANobbMI"},
		},
	}
}

// ---- Open + Migrate ----

func TestOpenAndMigrateFreshDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "packgradle.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 全新库失败: %v", err)
	}
	defer db.Close()

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("读取 journal_mode 失败: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, 期望 wal", mode)
	}

	if err := Migrate(context.Background(), db, filepath.Join(dir, "backup")); err != nil {
		t.Fatalf("全新库 Migrate 失败: %v", err)
	}
	if v := userVersion(t, db); v != 2 {
		t.Errorf("user_version = %d, 期望 2", v)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("读取 schema_migrations 失败: %v", err)
	}
	if count != 2 {
		t.Errorf("schema_migrations 行数 = %d, 期望 2", count)
	}
}

func TestMigrateReopenIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "packgradle.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if err := Migrate(context.Background(), db, filepath.Join(dir, "backup")); err != nil {
		db.Close()
		t.Fatalf("首次 Migrate 失败: %v", err)
	}
	db.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer db2.Close()
	if err := Migrate(context.Background(), db2, filepath.Join(dir, "backup")); err != nil {
		t.Fatalf("重开 Migrate 应幂等: %v", err)
	}
	if v := userVersion(t, db2); v != 2 {
		t.Errorf("重开后 user_version = %d, 期望 2", v)
	}
}

// ---- FK 生效 ----

func TestForeignKeyRejectsMissingRelation(t *testing.T) {
	db := openTestDB(t)
	snapshots := NewSnapshotRepository(db)
	snap := fixtureSnapshot("snap_orphan", "rel_missing", model.SideProject, "2026-08-22T10:00:00Z")
	if err := snapshots.Insert(context.Background(), snap); !errors.Is(err, ErrRelationNotFound) {
		t.Fatalf("为不存在 relation 插 snapshot 应返回 ErrRelationNotFound, got %v", err)
	}

	tasks := NewTaskRepository(db)
	task := model.Task{
		TaskID: "task_orphan", RelationID: "rel_missing", Kind: model.TaskKindScan,
		Status: model.TaskStatusQueued, Phase: "scan", Sequence: 0,
		CreatedAt: "2026-08-22T10:00:00Z", UpdatedAt: "2026-08-22T10:00:00Z",
	}
	if err := tasks.Insert(context.Background(), task); !errors.Is(err, ErrRelationNotFound) {
		t.Fatalf("为不存在 relation 插 task 应返回 ErrRelationNotFound, got %v", err)
	}
}

// ---- endpoint / relation ----

func TestEndpointRepositoryRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewEndpointRepository(db)

	now := time.Now().UTC().Format(time.RFC3339)
	proj := model.Project{
		SchemaVersion: model.CurrentSchemaVersion, ProjectID: "prj_a",
		Adapter: "packwiz", DisplayName: "Demo", RootPath: "D:/packs/demo",
		BindingFingerprint: "sha256:fp-a", CreatedAt: now,
	}
	rt := model.Runtime{
		SchemaVersion: model.CurrentSchemaVersion, RuntimeID: "run_a",
		Adapter: "prism", DisplayName: "Inst", RootPath: "D:/inst/minecraft",
		AdapterIdentity: "inst-a", BindingFingerprint: "sha256:fp-run-a", CreatedAt: now,
	}
	if err := repo.CreateProject(ctx, proj); err != nil {
		t.Fatalf("CreateProject 失败: %v", err)
	}
	if err := repo.CreateRuntime(ctx, rt); err != nil {
		t.Fatalf("CreateRuntime 失败: %v", err)
	}

	gotProj, err := repo.GetProject(ctx, proj.ProjectID)
	if err != nil {
		t.Fatalf("GetProject 失败: %v", err)
	}
	if !reflect.DeepEqual(gotProj, proj) {
		t.Errorf("Project 往返不一致: %+v vs %+v", gotProj, proj)
	}
	gotRT, err := repo.GetRuntime(ctx, rt.RuntimeID)
	if err != nil {
		t.Fatalf("GetRuntime 失败: %v", err)
	}
	if !reflect.DeepEqual(gotRT, rt) {
		t.Errorf("Runtime 往返不一致: %+v vs %+v", gotRT, rt)
	}

	// fingerprint / identity 精确查找
	found, ok, err := repo.FindProjectByRoot(ctx, "sha256:fp-a")
	if err != nil || !ok || found.ProjectID != proj.ProjectID {
		t.Errorf("FindProjectByRoot 未命中: ok=%v err=%v got=%+v", ok, err, found)
	}
	if _, ok, _ := repo.FindProjectByRoot(ctx, "sha256:none"); ok {
		t.Error("FindProjectByRoot 不应命中未知 fingerprint")
	}
	foundRT, ok, err := repo.FindRuntimeByIdentity(ctx, "prism", "inst-a")
	if err != nil || !ok || foundRT.RuntimeID != rt.RuntimeID {
		t.Errorf("FindRuntimeByIdentity 未命中: ok=%v err=%v got=%+v", ok, err, foundRT)
	}
	if _, ok, _ := repo.FindRuntimeByIdentity(ctx, "prism", "inst-b"); ok {
		t.Error("FindRuntimeByIdentity 不应命中未知 identity")
	}

	// 重复创建被拒
	if err := repo.CreateProject(ctx, proj); !errors.Is(err, ErrDuplicate) {
		t.Errorf("重复 CreateProject 应返回 ErrDuplicate, got %v", err)
	}
	if err := repo.CreateRuntime(ctx, rt); !errors.Is(err, ErrDuplicate) {
		t.Errorf("重复 CreateRuntime 应返回 ErrDuplicate, got %v", err)
	}

	// 不存在 → ErrNotFound
	if _, err := repo.GetProject(ctx, "prj_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetProject 不存在应返回 ErrNotFound, got %v", err)
	}
	if _, err := repo.GetRuntime(ctx, "run_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRuntime 不存在应返回 ErrNotFound, got %v", err)
	}

	// List：多行按 display_name 升序、空库返回 [] 而非 nil
	mkProj := func(id, name string) model.Project {
		return model.Project{
			SchemaVersion: model.CurrentSchemaVersion, ProjectID: id,
			Adapter: "packwiz", DisplayName: name, RootPath: "D:/packs/" + id,
			BindingFingerprint: "sha256:fp-" + id, CreatedAt: now,
		}
	}
	second := mkProj("prj_b", "aaa-first")
	if err := repo.CreateProject(ctx, second); err != nil {
		t.Fatalf("CreateProject(prj_b) 失败: %v", err)
	}
	projs, err := repo.ListProjects(ctx)
	if err != nil || projs == nil {
		t.Fatalf("ListProjects: err=%v nil=%v", err, projs == nil)
	}
	if len(projs) != 2 || projs[0].ProjectID != "prj_b" || projs[1].ProjectID != "prj_a" {
		t.Errorf("ListProjects 排序/内容: %+v", projs)
	}
	rt2 := model.Runtime{
		SchemaVersion: model.CurrentSchemaVersion, RuntimeID: "run_b",
		Adapter: "prism", DisplayName: "Another", RootPath: "D:/inst/b/minecraft",
		AdapterIdentity: "inst-b", BindingFingerprint: "sha256:fp-run-b", CreatedAt: now,
	}
	if err := repo.CreateRuntime(ctx, rt2); err != nil {
		t.Fatalf("CreateRuntime(run_b) 失败: %v", err)
	}
	rts, err := repo.ListRuntimes(ctx)
	if err != nil || rts == nil {
		t.Fatalf("ListRuntimes: err=%v nil=%v", err, rts == nil)
	}
	if len(rts) != 2 || rts[0].RuntimeID != "run_b" || rts[1].RuntimeID != "run_a" {
		t.Errorf("ListRuntimes 排序/内容: %+v", rts)
	}
}

func TestRelationRepository(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewRelationRepository(db)

	fixtureRelation(t, db, "r1")
	got, err := repo.Get(ctx, "rel_r1")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if got.ProjectID != "prj_r1" || got.RuntimeID != "run_r1" ||
		got.PolicySet != "default-v1" || got.Revision != 1 || got.Health != model.HealthHealthy {
		t.Errorf("Relation 往返字段不一致: %+v", got)
	}

	// pair 重复被拒
	dup := got
	dup.RelationID = "rel_r1_dup"
	if err := repo.Create(ctx, dup); !errors.Is(err, ErrDuplicate) {
		t.Errorf("pair 重复 Create 应返回 ErrDuplicate, got %v", err)
	}

	// Get 不存在
	if _, err := repo.Get(ctx, "rel_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在应返回 ErrNotFound, got %v", err)
	}

	// PairExists
	exists, err := repo.PairExists(ctx, "prj_r1", "run_r1")
	if err != nil || !exists {
		t.Errorf("PairExists 应为 true: %v %v", exists, err)
	}
	exists, err = repo.PairExists(ctx, "prj_r1", "run_none")
	if err != nil || exists {
		t.Errorf("PairExists 应为 false: %v %v", exists, err)
	}

	// UpdateHealth
	if err := repo.UpdateHealth(ctx, "rel_r1", model.HealthEndpointMissing); err != nil {
		t.Fatalf("UpdateHealth 失败: %v", err)
	}
	got, _ = repo.Get(ctx, "rel_r1")
	if got.Health != model.HealthEndpointMissing {
		t.Errorf("UpdateHealth 后 health = %q", got.Health)
	}
	if err := repo.UpdateHealth(ctx, "rel_none", model.HealthHealthy); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateHealth 不存在应返回 ErrNotFound, got %v", err)
	}

	// IncrementRevision
	rev, err := repo.IncrementRevision(ctx, "rel_r1")
	if err != nil || rev != 2 {
		t.Errorf("IncrementRevision = (%d, %v), 期望 (2, nil)", rev, err)
	}
	if _, err := repo.IncrementRevision(ctx, "rel_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("IncrementRevision 不存在应返回 ErrNotFound, got %v", err)
	}

	// List 分页（cursor 按最后一条 id，升序 + LIMIT+1）
	for _, s := range []string{"r2", "r3"} {
		fixtureRelation(t, db, s)
	}
	page1, cursor, err := repo.List(ctx, pageOf("", 2))
	if err != nil {
		t.Fatalf("List 第 1 页失败: %v", err)
	}
	if len(page1) != 2 || cursor != "rel_r2" {
		t.Errorf("List 第 1 页: len=%d cursor=%q, 期望 len=2 cursor=rel_r2", len(page1), cursor)
	}
	page2, cursor, err := repo.List(ctx, pageOf(cursor, 2))
	if err != nil {
		t.Fatalf("List 第 2 页失败: %v", err)
	}
	if len(page2) != 1 || cursor != "" {
		t.Errorf("List 第 2 页: len=%d cursor=%q, 期望 len=1 cursor=\"\"", len(page2), cursor)
	}
	if page2[0].RelationID != "rel_r3" {
		t.Errorf("List 第 2 页首条 = %q, 期望 rel_r3", page2[0].RelationID)
	}
}

// pageOf 构造分页参数。
func pageOf(cursor string, limit int) ports.PageRequest {
	return ports.PageRequest{Cursor: cursor, Limit: limit}
}

// ---- snapshot ----

func TestSnapshotRoundTripAndQueries(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewSnapshotRepository(db)
	relationID := fixtureRelation(t, db, "s1")

	snap := fixtureSnapshot("snap_p1", relationID, model.SideProject, "2026-08-22T10:00:00Z")
	if err := repo.Insert(ctx, snap); err != nil {
		t.Fatalf("Insert 快照失败: %v", err)
	}

	got, err := repo.Get(ctx, snap.SnapshotID)
	if err != nil {
		t.Fatalf("Get 快照失败: %v", err)
	}
	if !reflect.DeepEqual(got, snap) {
		t.Errorf("快照往返不一致:\n got  %+v\n want %+v", got, snap)
	}

	// GetForRelation：关系错配 / side 错配 → ErrNotFound
	if _, err := repo.GetForRelation(ctx, snap.SnapshotID, "rel_other", model.SideProject); !errors.Is(err, ErrNotFound) {
		t.Errorf("关系错配应返回 ErrNotFound, got %v", err)
	}
	if _, err := repo.GetForRelation(ctx, snap.SnapshotID, relationID, model.SideRuntime); !errors.Is(err, ErrNotFound) {
		t.Errorf("side 错配应返回 ErrNotFound, got %v", err)
	}
	if _, err := repo.GetForRelation(ctx, snap.SnapshotID, relationID, model.SideProject); err != nil {
		t.Errorf("正确匹配不应报错: %v", err)
	}
	if _, err := repo.Get(ctx, "snap_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在快照应返回 ErrNotFound, got %v", err)
	}

	// LatestByRelationSide：取 captured_at 最新
	snapOld := fixtureSnapshot("snap_p0", relationID, model.SideProject, "2026-08-21T09:00:00Z")
	if err := repo.Insert(ctx, snapOld); err != nil {
		t.Fatalf("Insert 旧快照失败: %v", err)
	}
	latest, ok, err := repo.LatestByRelationSide(ctx, relationID, model.SideProject)
	if err != nil || !ok {
		t.Fatalf("LatestByRelationSide 失败: ok=%v err=%v", ok, err)
	}
	if latest.SnapshotID != "snap_p1" {
		t.Errorf("最新快照 = %q, 期望 snap_p1", latest.SnapshotID)
	}
	// runtime 侧无快照 → 未命中
	if _, ok, _ := repo.LatestByRelationSide(ctx, relationID, model.SideRuntime); ok {
		t.Error("runtime 侧无快照不应命中")
	}
}

// ---- baseline ----

func TestBaselineRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewBaselineRepository(db)
	relationID := fixtureRelation(t, db, "b1")

	b := model.SyncBaseline{
		SchemaVersion:        model.CurrentSchemaVersion,
		BaselineID:           "base_1",
		RelationID:           relationID,
		CreatedAt:            "2026-08-22T11:00:00Z",
		NormalizationVersion: 1,
		Resources: map[model.ResourceID]model.BaselineResource{
			"mod:modrinth:AANobbMI": {
				State:         "present",
				LogicalDigest: "sha256:logical-a",
				ProjectRepresentation: &model.Representation{
					RelativePath: "mods/sodium.pw.toml", Format: "packwiz-mod-toml",
					Content: &model.ContentRef{Algorithm: "sha256", Digest: "aa11", Size: 1234},
				},
				Recoverability: model.RecoverabilityCAS,
			},
			"mod:curseforge:42": {
				State:          "absent",
				LogicalDigest:  "sha256:logical-b",
				Recoverability: model.RecoverabilityRedownload,
			},
		},
	}
	// 守卫在 repository 边界重算校验，fixture 必须携带真实 digest
	baselineDigest, err := normalize.BaselineDigest(b)
	if err != nil {
		t.Fatalf("重算基线 digest 失败: %v", err)
	}
	b.BaselineDigest = baselineDigest
	if err := repo.Insert(ctx, b); err != nil {
		t.Fatalf("Insert 基线失败: %v", err)
	}
	got, err := repo.Get(ctx, "base_1")
	if err != nil {
		t.Fatalf("Get 基线失败: %v", err)
	}
	if !reflect.DeepEqual(got, b) {
		t.Errorf("基线往返不一致:\n got  %+v\n want %+v", got, b)
	}
	if _, err := repo.Get(ctx, "base_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在基线应返回 ErrNotFound, got %v", err)
	}
}

// ---- plan ----

func TestPlanRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewPlanRepository(db)
	relationID := fixtureRelation(t, db, "p1")

	projectSnap := fixtureSnapshot("snap_pp", relationID, model.SideProject, "2026-08-22T12:00:00Z")
	runtimeSnap := fixtureSnapshot("snap_pr", relationID, model.SideRuntime, "2026-08-22T12:00:01Z")
	snapshots := NewSnapshotRepository(db)
	if err := snapshots.Insert(ctx, projectSnap); err != nil {
		t.Fatalf("Insert 项目侧快照失败: %v", err)
	}
	if err := snapshots.Insert(ctx, runtimeSnap); err != nil {
		t.Fatalf("Insert 运行时侧快照失败: %v", err)
	}

	plan := model.SyncPlan{
		SchemaVersion:              model.CurrentSchemaVersion,
		PlanID:                     "plan_1",
		RelationID:                 relationID,
		Kind:                       model.PlanSync,
		InputProjectSnapshotID:     projectSnap.SnapshotID,
		InputRuntimeSnapshotID:     runtimeSnap.SnapshotID,
		InputProjectSnapshotDigest: projectSnap.SnapshotDigest,
		InputRuntimeSnapshotDigest: runtimeSnap.SnapshotDigest,
		RelationRevision:           1,
		PolicyDigest:               "sha256:policy",
		ExpectedBindings:           model.ExpectedBindings{Project: "sha256:bp", Runtime: "sha256:br"},
		Status:                     model.PlanDraft,
		ExpiresAt:                  "2026-08-22T13:00:00Z",
		Operations: []model.PlannedOperation{
			{ID: "op_0001", Kind: model.OpWriteRuntime, ResourceID: "mod:modrinth:AANobbMI", Reversible: true},
		},
		Conflicts: []model.Conflict{
			{ResourceID: "file:config/x.ini", Kind: model.ConflictModifyModify, Detail: "两侧均修改"},
		},
		ConfirmationRequirements: []model.ConfirmationRequirement{
			{Code: "overwrite", Severity: "blocking", ResourceCount: 1},
		},
		Summary: model.PlanSummary{ResourceTotal: 2, ConflictCount: 1},
	}
	// 守卫在 repository 边界重算校验，fixture 必须携带真实 digest
	planDigest, err := normalize.PlanDigest(plan)
	if err != nil {
		t.Fatalf("重算计划 digest 失败: %v", err)
	}
	plan.PlanDigest = planDigest
	if err := repo.Insert(ctx, plan); err != nil {
		t.Fatalf("Insert 计划失败: %v", err)
	}
	got, err := repo.Get(ctx, "plan_1")
	if err != nil {
		t.Fatalf("Get 计划失败: %v", err)
	}
	if !reflect.DeepEqual(got, plan) {
		t.Errorf("计划往返不一致:\n got  %+v\n want %+v", got, plan)
	}
	// conflicts 表展开行存在
	var conflictCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM conflicts WHERE plan_id=?", "plan_1").Scan(&conflictCount); err != nil {
		t.Fatalf("读取 conflicts 失败: %v", err)
	}
	if conflictCount != 1 {
		t.Errorf("conflicts 行数 = %d, 期望 1", conflictCount)
	}
	if _, err := repo.Get(ctx, "plan_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在计划应返回 ErrNotFound, got %v", err)
	}
}

// ---- task ----

func TestTaskOptimisticLockAndQueries(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewTaskRepository(db)
	relationID := fixtureRelation(t, db, "t1")

	now := time.Now().UTC().Format(time.RFC3339)
	task := model.Task{
		TaskID: "task_1", RelationID: relationID, Sequence: 0,
		Kind: model.TaskKindScan, Status: model.TaskStatusQueued, Phase: "scan",
		MessageKey: "msg.task.scan", MessageArgs: []string{"prj_t1"},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Insert(ctx, task); err != nil {
		t.Fatalf("Insert 任务失败: %v", err)
	}
	got, err := repo.Get(ctx, "task_1")
	if err != nil {
		t.Fatalf("Get 任务失败: %v", err)
	}
	if !reflect.DeepEqual(got, task) {
		t.Errorf("任务往返不一致:\n got  %+v\n want %+v", got, task)
	}

	// sequence 0 → 1 更新成功
	updated := got
	updated.Sequence = 1
	updated.Status = model.TaskStatusRunning
	updated.UpdatedAt = now
	if err := repo.Update(ctx, updated); err != nil {
		t.Fatalf("Update sequence=1 失败: %v", err)
	}

	// 旧 sequence（仍为 1）重放 → 冲突
	stale := updated
	if err := repo.Update(ctx, stale); !errors.Is(err, ErrSequenceConflict) {
		t.Errorf("旧 sequence Update 应返回 ErrSequenceConflict, got %v", err)
	}

	// FindActiveByRelationAndKind：queued/running 命中，succeeded 忽略
	if found, ok, _ := repo.FindActiveByRelationAndKind(ctx, relationID, model.TaskKindScan); !ok || found.TaskID != "task_1" {
		t.Errorf("FindActive 应命中 running 任务: ok=%v got=%+v", ok, found)
	}
	if _, ok, _ := repo.FindActiveByRelationAndKind(ctx, relationID, model.TaskKindApply); ok {
		t.Error("FindActive 不应命中其它 kind")
	}

	done := updated
	done.Sequence = 2
	done.Status = model.TaskStatusSucceeded
	done.Outcome = model.TaskOutcomeExact
	done.UpdatedAt = now
	if err := repo.Update(ctx, done); err != nil {
		t.Fatalf("Update sequence=2 失败: %v", err)
	}
	if _, ok, _ := repo.FindActiveByRelationAndKind(ctx, relationID, model.TaskKindScan); ok {
		t.Error("succeeded 任务不应算活跃")
	}

	// ListByRelation：active 只含 queued/running
	queued := model.Task{
		TaskID: "task_2", RelationID: relationID, Sequence: 0,
		Kind: model.TaskKindApply, Status: model.TaskStatusQueued, Phase: "apply",
		MessageKey: "msg.task.apply", MessageArgs: nil, CanCancel: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Insert(ctx, queued); err != nil {
		t.Fatalf("Insert task_2 失败: %v", err)
	}
	active, cursor, err := repo.ListByRelation(ctx, relationID, true, pageOf("", 50))
	if err != nil || cursor != "" {
		t.Fatalf("ListByRelation active 失败: %v cursor=%q", err, cursor)
	}
	if len(active) != 1 || active[0].TaskID != "task_2" {
		t.Errorf("ListByRelation active 应只含 task_2: %+v", active)
	}
	all, _, err := repo.ListByRelation(ctx, relationID, false, pageOf("", 50))
	if err != nil {
		t.Fatalf("ListByRelation all 失败: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListByRelation all 应含 2 条, got %d", len(all))
	}
	// task_2 带 Problem 与 CanCancel 往返
	problem := queued
	problem.Sequence = 1
	problem.Status = model.TaskStatusFailed
	problem.Problem = &model.Problem{Code: "err.scan.io", Args: []string{"x"}, Detail: "boom"}
	if err := repo.Update(ctx, problem); err != nil {
		t.Fatalf("Update task_2 失败: %v", err)
	}
	got2, err := repo.Get(ctx, "task_2")
	if err != nil {
		t.Fatalf("Get task_2 失败: %v", err)
	}
	if got2.Problem == nil || got2.Problem.Code != "err.scan.io" || !got2.CanCancel {
		t.Errorf("task_2 Problem/CanCancel 往返不一致: %+v", got2)
	}
	if _, err := repo.Get(ctx, "task_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在任务应返回 ErrNotFound, got %v", err)
	}
}

// ---- mapping ----

func TestMappingSavePolicyBumpsRevision(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewMappingRepository(db)
	relations := NewRelationRepository(db)
	relationID := fixtureRelation(t, db, "m1")

	if _, err := repo.GetPolicy(ctx, relationID); !errors.Is(err, ErrNotFound) {
		t.Errorf("未保存前 GetPolicy 应返回 ErrNotFound, got %v", err)
	}

	policy := model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion,
		PolicyID:      "pol_m1",
		Revision:      1,
		Rules: []model.MappingRule{
			{ID: "rule-mod", ResourceKind: "mod", ProjectPrefix: "mods/", RuntimePrefix: "mods/",
				Direction: "bidirectional", Materialization: "copy", MergePolicy: "packwiz"},
		},
	}
	if err := repo.SavePolicy(ctx, relationID, policy); err != nil {
		t.Fatalf("SavePolicy 失败: %v", err)
	}

	rel, err := relations.Get(ctx, relationID)
	if err != nil {
		t.Fatalf("Get Relation 失败: %v", err)
	}
	if rel.Revision != 2 {
		t.Errorf("SavePolicy 后 relation.revision = %d, 期望 2（初始 1 + 1）", rel.Revision)
	}

	got, err := repo.GetPolicy(ctx, relationID)
	if err != nil {
		t.Fatalf("GetPolicy 失败: %v", err)
	}
	if !reflect.DeepEqual(got, policy) {
		t.Errorf("策略往返不一致:\n got  %+v\n want %+v", got, policy)
	}

	// 再保存一次 → revision 再 +1
	policy.Revision = 2
	if err := repo.SavePolicy(ctx, relationID, policy); err != nil {
		t.Fatalf("第二次 SavePolicy 失败: %v", err)
	}
	rel, _ = relations.Get(ctx, relationID)
	if rel.Revision != 3 {
		t.Errorf("第二次 SavePolicy 后 relation.revision = %d, 期望 3", rel.Revision)
	}

	// 关系不存在 → ErrNotFound 且不落 mappings 行
	if err := repo.SavePolicy(ctx, "rel_none", policy); !errors.Is(err, ErrNotFound) {
		t.Errorf("SavePolicy 不存在关系应返回 ErrNotFound, got %v", err)
	}
}

// TestMappingCreatePolicyInitialWriteNoBumpRevision 验证 ADR-0002 修订语义：
// CreatePolicy 写入创建时初始 policy 且不递增 revision（创建即第 1 代）；
// 重复初始写入被拒绝；其后的 SavePolicy（创建后的首次修改）才递增到 2。
func TestMappingCreatePolicyInitialWriteNoBumpRevision(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewMappingRepository(db)
	relations := NewRelationRepository(db)
	relationID := fixtureRelation(t, db, "m2")

	policy := model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion,
		PolicyID:      "pol_m2",
		Revision:      1,
		Rules: []model.MappingRule{
			{ID: "rule-mod", ResourceKind: "mod", ProjectPrefix: "mods/", RuntimePrefix: "mods/",
				Direction: "bidirectional", Materialization: "copy", MergePolicy: "packwiz",
				RuntimeLocalPolicy: "exclude"},
		},
	}
	if err := repo.CreatePolicy(ctx, relationID, policy); err != nil {
		t.Fatalf("CreatePolicy 失败: %v", err)
	}
	rel, err := relations.Get(ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Revision != 1 {
		t.Errorf("创建时初始写入后 revision = %d, 期望 1（不递增）", rel.Revision)
	}
	got, err := repo.GetPolicy(ctx, relationID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if !reflect.DeepEqual(got, policy) {
		t.Errorf("初始策略往返不一致:\n got  %+v\n want %+v", got, policy)
	}

	// 重复初始写入 → ErrDuplicate
	if err := repo.CreatePolicy(ctx, relationID, policy); !errors.Is(err, ErrDuplicate) {
		t.Errorf("重复 CreatePolicy 应返回 ErrDuplicate, got %v", err)
	}
	// 关系不存在 → ErrNotFound
	if err := repo.CreatePolicy(ctx, "rel_none", policy); !errors.Is(err, ErrNotFound) {
		t.Errorf("CreatePolicy 不存在关系应返回 ErrNotFound, got %v", err)
	}

	// 其后的 SavePolicy（创建后的首次修改）→ revision == 2
	if err := repo.SavePolicy(ctx, relationID, policy); err != nil {
		t.Fatalf("SavePolicy 失败: %v", err)
	}
	rel, _ = relations.Get(ctx, relationID)
	if rel.Revision != 2 {
		t.Errorf("首次修改后 revision = %d, 期望 2", rel.Revision)
	}
}

// ---- preparation ----

func TestPreparationLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewPreparationRepository(db)

	prep := model.RelationPreparation{
		SchemaVersion: model.CurrentSchemaVersion,
		PreparationID: "prep_1",
		CreatedAt:     "2026-08-22T10:00:00Z",
		ExpiresAt:     "2999-01-01T00:00:00Z",
		Input:         model.PrepareRelationInput{ProjectRoot: "D:/packs/demo", RuntimeInstanceDir: "D:/inst/demo", PolicySet: "default-v1"},
		Project: &model.Project{
			SchemaVersion: model.CurrentSchemaVersion, ProjectID: "prj_prep",
			Adapter: "packwiz", DisplayName: "Demo", RootPath: "D:/packs/demo",
			BindingFingerprint: "sha256:prep", CreatedAt: "2026-08-22T10:00:00Z",
		},
		Policy: model.MappingPolicy{
			SchemaVersion: model.CurrentSchemaVersion, PolicyID: "pol_prep", Revision: 1,
			Rules: []model.MappingRule{{ID: "rule-mod", ResourceKind: "mod", Direction: "bidirectional"}},
		},
		Checks: []model.PreparationCheck{
			{Code: "check.pack_toml", Passed: true, Severity: "blocking"},
			{Code: "check.instance_dir", Passed: false, Severity: "blocking", Detail: "目录不存在"},
		},
	}
	if err := repo.Insert(ctx, prep); err != nil {
		t.Fatalf("Insert 预检失败: %v", err)
	}
	got, err := repo.Get(ctx, "prep_1")
	if err != nil {
		t.Fatalf("Get 预检失败: %v", err)
	}
	if !reflect.DeepEqual(got, prep) {
		t.Errorf("预检往返不一致:\n got  %+v\n want %+v", got, prep)
	}

	// 消费成功
	if err := repo.MarkConsumed(ctx, "prep_1"); err != nil {
		t.Fatalf("首次 MarkConsumed 失败: %v", err)
	}
	// 二次消费 → ErrPreparationConsumed（拆码后已消费 ≠ 已过期，ADR-0003 决议 4）
	if err := repo.MarkConsumed(ctx, "prep_1"); !errors.Is(err, ErrPreparationConsumed) {
		t.Errorf("二次 MarkConsumed 应返回 ErrPreparationConsumed, got %v", err)
	}

	// 过期拒绝
	expired := prep
	expired.PreparationID = "prep_2"
	expired.ExpiresAt = "2000-01-01T00:00:00Z"
	if err := repo.Insert(ctx, expired); err != nil {
		t.Fatalf("Insert 过期预检失败: %v", err)
	}
	if err := repo.MarkConsumed(ctx, "prep_2"); !errors.Is(err, ErrPreparationExpired) {
		t.Errorf("过期 MarkConsumed 应返回 ErrPreparationExpired, got %v", err)
	}

	// 不存在
	if _, err := repo.Get(ctx, "prep_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在预检应返回 ErrNotFound, got %v", err)
	}
	if err := repo.MarkConsumed(ctx, "prep_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkConsumed 不存在应返回 ErrNotFound, got %v", err)
	}
}

// ---- hash cache ----

func TestHashCacheLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewHashCacheRepository(db)

	key := ports.HashCacheKey{
		RootFingerprint: "sha256:root", RelativePath: "mods/sodium.jar",
		SizeBytes: 1024, MtimeUnixNano: 1000, FileKey: "filekey-1",
	}
	if _, ok, err := repo.Lookup(ctx, key); err != nil || ok {
		t.Errorf("未保存前 Lookup 应未命中: ok=%v err=%v", ok, err)
	}
	if err := repo.Save(ctx, []ports.HashCacheEntry{{Key: key, Digest: "sha256:content-1"}}); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	digest, ok, err := repo.Lookup(ctx, key)
	if err != nil || !ok || digest != "sha256:content-1" {
		t.Errorf("Lookup 未命中: digest=%q ok=%v err=%v", digest, ok, err)
	}
	// 键任一字段不同 → 未命中
	other := key
	other.MtimeUnixNano++
	if _, ok, _ := repo.Lookup(ctx, other); ok {
		t.Error("mtime 变化后不应命中")
	}
	if err := repo.DeleteAll(ctx); err != nil {
		t.Fatalf("DeleteAll 失败: %v", err)
	}
	if _, ok, _ := repo.Lookup(ctx, key); ok {
		t.Error("DeleteAll 后不应命中")
	}
}

// ---- event ----

func TestEventAppendSequenceMonotonic(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewEventRepository(db)

	mk := func(id, taskID string) model.EventEnvelope {
		return model.EventEnvelope{
			SchemaVersion: model.CurrentSchemaVersion,
			EventID:       id, EventType: model.EventTaskUpdated,
			EmittedAt:  time.Now().UTC().Format(time.RFC3339),
			RelationID: "rel_e1", TaskID: taskID,
			Payload: []byte(`{"status":"running"}`),
		}
	}
	seq1, err := repo.Append(ctx, mk("evt_1", "task_1"))
	if err != nil {
		t.Fatalf("第一次 Append 失败: %v", err)
	}
	seq2, err := repo.Append(ctx, mk("evt_2", "task_1"))
	if err != nil {
		t.Fatalf("第二次 Append 失败: %v", err)
	}
	if seq1 != 1 || seq2 != 2 {
		t.Errorf("stream_sequence = (%d, %d), 期望 (1, 2)", seq1, seq2)
	}
}
