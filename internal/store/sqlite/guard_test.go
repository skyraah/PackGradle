package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// guard 契约测试（检视报告 P0-3 / 票 #12）：
// 跨 Relation、跨 side、伪造 digest、错误 parent 的对象引用写入必须被拒并返回结构化错误。

const guardTestTime = "2026-08-29T10:00:00Z"

// fixturePlan 构造引用指定快照的有效计划，digest 由 normalize.PlanDigest 重算生成。
func fixturePlan(t *testing.T, id, relationID string, projectSnap, runtimeSnap model.ObservedSnapshot) model.SyncPlan {
	t.Helper()
	p := model.SyncPlan{
		SchemaVersion:              model.CurrentSchemaVersion,
		PlanID:                     id,
		RelationID:                 relationID,
		Kind:                       model.PlanSync,
		InputProjectSnapshotID:     projectSnap.SnapshotID,
		InputRuntimeSnapshotID:     runtimeSnap.SnapshotID,
		InputProjectSnapshotDigest: projectSnap.SnapshotDigest,
		InputRuntimeSnapshotDigest: runtimeSnap.SnapshotDigest,
		RelationRevision:           1,
		PolicyDigest:               "sha256:policy",
		ExpectedBindings:           model.ExpectedBindings{Project: "sha256:bp-" + relationID, Runtime: "sha256:br-" + relationID},
		Status:                     model.PlanDraft,
		ExpiresAt:                  "2999-01-01T00:00:00Z",
		Operations: []model.PlannedOperation{
			{ID: "op_0001", Kind: model.OpWriteRuntime, ResourceID: "mod:modrinth:AANobbMI", Reversible: true},
		},
		Conflicts: []model.Conflict{
			{ResourceID: "file:config/x.ini", Kind: model.ConflictModifyModify, Detail: "两侧均修改"},
		},
	}
	digest, err := normalize.PlanDigest(p)
	if err != nil {
		t.Fatalf("重算计划 digest 失败: %v", err)
	}
	p.PlanDigest = digest
	return p
}

// fixtureBaseline 构造有效基线，digest 由 normalize.BaselineDigest 重算生成。
func fixtureBaseline(t *testing.T, id, relationID, parentID string) model.SyncBaseline {
	t.Helper()
	b := model.SyncBaseline{
		SchemaVersion:        model.CurrentSchemaVersion,
		BaselineID:           id,
		RelationID:           relationID,
		ParentBaselineID:     parentID,
		CreatedAt:            guardTestTime,
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
	digest, err := normalize.BaselineDigest(b)
	if err != nil {
		t.Fatalf("重算基线 digest 失败: %v", err)
	}
	b.BaselineDigest = digest
	return b
}

// insertSnapPair 为 relation 插入 project/runtime 两侧快照。
func insertSnapPair(t *testing.T, repo *SnapshotRepository, relationID, tag string) (project, runtime model.ObservedSnapshot) {
	t.Helper()
	ctx := context.Background()
	project = fixtureSnapshot("snap_"+tag+"_p", relationID, model.SideProject, guardTestTime)
	runtime = fixtureSnapshot("snap_"+tag+"_r", relationID, model.SideRuntime, guardTestTime)
	if err := repo.Insert(ctx, project); err != nil {
		t.Fatalf("插入项目侧快照失败: %v", err)
	}
	if err := repo.Insert(ctx, runtime); err != nil {
		t.Fatalf("插入运行时侧快照失败: %v", err)
	}
	return project, runtime
}

// ---- Plan 守卫 ----

func TestPlanGuardRejectsCrossRelationSnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fixtureRelation(t, db, "g1")
	fixtureRelation(t, db, "g2")
	snapshots := NewSnapshotRepository(db)
	snapP2, snapR2 := insertSnapPair(t, snapshots, "rel_g2", "g2")

	// 快照属于 rel_g2，计划却声明 rel_g1
	plan := fixturePlan(t, "plan_g1", "rel_g1", snapP2, snapR2)
	if err := NewPlanRepository(db).Insert(ctx, plan); !errors.Is(err, ErrCrossRelation) {
		t.Fatalf("跨 Relation 输入快照应返回 ErrCrossRelation, got %v", err)
	}
}

func TestPlanGuardRejectsSideMismatch(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	relationID := fixtureRelation(t, db, "sm")
	snapshots := NewSnapshotRepository(db)
	snapP, snapR := insertSnapPair(t, snapshots, relationID, "sm")

	// 项目侧输入位放了 runtime 快照
	plan := fixturePlan(t, "plan_sm1", relationID, snapR, snapR)
	if err := NewPlanRepository(db).Insert(ctx, plan); !errors.Is(err, ErrSideMismatch) {
		t.Fatalf("项目侧输入 runtime 快照应返回 ErrSideMismatch, got %v", err)
	}
	// 运行时侧输入位放了 project 快照
	plan2 := fixturePlan(t, "plan_sm2", relationID, snapP, snapP)
	if err := NewPlanRepository(db).Insert(ctx, plan2); !errors.Is(err, ErrSideMismatch) {
		t.Fatalf("运行时侧输入 project 快照应返回 ErrSideMismatch, got %v", err)
	}
}

func TestPlanGuardRejectsForgedPlanDigest(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	relationID := fixtureRelation(t, db, "fd")
	snapshots := NewSnapshotRepository(db)
	snapP, snapR := insertSnapPair(t, snapshots, relationID, "fd")

	plan := fixturePlan(t, "plan_fd", relationID, snapP, snapR)
	plan.PlanDigest = "sha256:forged"
	if err := NewPlanRepository(db).Insert(ctx, plan); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("伪造计划 digest 应返回 ErrDigestMismatch, got %v", err)
	}
}

func TestPlanGuardRejectsForgedInputSnapshotDigest(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	relationID := fixtureRelation(t, db, "fi")
	snapshots := NewSnapshotRepository(db)
	snapP, snapR := insertSnapPair(t, snapshots, relationID, "fi")

	// 声称的输入快照 digest 与库中持久化值不符（digest 链断裂）
	plan := fixturePlan(t, "plan_fi", relationID, snapP, snapR)
	plan.InputProjectSnapshotDigest = "sha256:forged"
	if err := NewPlanRepository(db).Insert(ctx, plan); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("伪造输入快照 digest 应返回 ErrDigestMismatch, got %v", err)
	}
}

func TestPlanGuardRejectsCrossRelationBaseBaseline(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fixtureRelation(t, db, "cb")
	fixtureRelation(t, db, "cc")
	snapshots := NewSnapshotRepository(db)
	snapP, snapR := insertSnapPair(t, snapshots, "rel_cb", "cb")

	// 基线属于 rel_cc（先落库，digest 合法可通过基线守卫）
	base := fixtureBaseline(t, "base_cc", "rel_cc", "")
	if err := NewBaselineRepository(db).Insert(ctx, base); err != nil {
		t.Fatalf("插入他关系基线失败: %v", err)
	}
	plan := fixturePlan(t, "plan_cb", "rel_cb", snapP, snapR)
	plan.BaseBaselineID = base.BaselineID
	plan.BaseBaselineDigest = base.BaselineDigest
	if err := NewPlanRepository(db).Insert(ctx, plan); !errors.Is(err, ErrCrossRelation) {
		t.Fatalf("跨 Relation base 基线应返回 ErrCrossRelation, got %v", err)
	}
}

func TestPlanGuardRejectsCrossRelationResolvedFrom(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fixtureRelation(t, db, "cr")
	fixtureRelation(t, db, "cs")
	snapshots := NewSnapshotRepository(db)
	snapP2, snapR2 := insertSnapPair(t, snapshots, "rel_cs", "cs")

	// 源计划属于 rel_cs，先落库
	source := fixturePlan(t, "plan_cs", "rel_cs", snapP2, snapR2)
	if err := NewPlanRepository(db).Insert(ctx, source); err != nil {
		t.Fatalf("插入源计划失败: %v", err)
	}

	snapP1, snapR1 := insertSnapPair(t, snapshots, "rel_cr", "cr")
	plan := fixturePlan(t, "plan_cr", "rel_cr", snapP1, snapR1)
	plan.ResolvedFromPlanID = source.PlanID
	if err := NewPlanRepository(db).Insert(ctx, plan); !errors.Is(err, ErrCrossRelation) {
		t.Fatalf("跨 Relation resolved_from 计划应返回 ErrCrossRelation, got %v", err)
	}
}

// ---- Baseline 守卫 ----

func TestBaselineGuardRejectsParentCrossRelation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fixtureRelation(t, db, "bp")
	fixtureRelation(t, db, "bq")

	parent := fixtureBaseline(t, "base_bp", "rel_bp", "")
	if err := NewBaselineRepository(db).Insert(ctx, parent); err != nil {
		t.Fatalf("插入父基线失败: %v", err)
	}
	child := fixtureBaseline(t, "base_bq", "rel_bq", parent.BaselineID)
	if err := NewBaselineRepository(db).Insert(ctx, child); !errors.Is(err, ErrParentMismatch) {
		t.Fatalf("错误 parent（跨 Relation）应返回 ErrParentMismatch, got %v", err)
	}
}

func TestBaselineGuardRejectsForgedDigest(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	relationID := fixtureRelation(t, db, "bf")

	b := fixtureBaseline(t, "base_bf", relationID, "")
	b.BaselineDigest = "sha256:forged"
	if err := NewBaselineRepository(db).Insert(ctx, b); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("伪造基线 digest 应返回 ErrDigestMismatch, got %v", err)
	}
}

// ---- Task 引用守卫 ----

func TestTaskGuardRejectsCrossRelationPlanRef(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	fixtureRelation(t, db, "tp")
	fixtureRelation(t, db, "tq")
	snapshots := NewSnapshotRepository(db)
	snapP2, snapR2 := insertSnapPair(t, snapshots, "rel_tq", "tq")

	plan := fixturePlan(t, "plan_tq", "rel_tq", snapP2, snapR2)
	if err := NewPlanRepository(db).Insert(ctx, plan); err != nil {
		t.Fatalf("插入计划失败: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	tasks := NewTaskRepository(db)
	task := model.Task{
		TaskID: "task_tp", RelationID: "rel_tp", Kind: model.TaskKindApply,
		Status: model.TaskStatusQueued, Phase: "apply", PlanID: plan.PlanID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tasks.Insert(ctx, task); !errors.Is(err, ErrCrossRelation) {
		t.Fatalf("任务引用他 Relation 计划应返回 ErrCrossRelation, got %v", err)
	}

	// Update 路径同样拒绝
	ok := task
	ok.TaskID = "task_tq"
	ok.RelationID = "rel_tq"
	if err := tasks.Insert(ctx, ok); err != nil {
		t.Fatalf("同 Relation 计划引用不应被拒: %v", err)
	}
	updated := ok
	updated.Sequence = 1
	updated.PlanID = plan.PlanID
	updated.Status = model.TaskStatusRunning
	if err := tasks.Update(ctx, updated); err != nil {
		t.Fatalf("Update 同 Relation 计划引用不应被拒: %v", err)
	}
	badUpdate := updated
	badUpdate.Sequence = 2
	badUpdate.RelationID = "rel_tp" // 换关系后引用他关系计划
	if err := tasks.Update(ctx, badUpdate); !errors.Is(err, ErrCrossRelation) {
		t.Fatalf("Update 跨 Relation 计划引用应返回 ErrCrossRelation, got %v", err)
	}
}

// ---- v1 → v2 迁移：tasks.plan_id/commit_id 外键生效 ----

func TestMigrateV1ToV2EnforcesTaskReferences(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "packgradle.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer db.Close()

	// 手工搭出 v1 库
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("建 v1 schema 失败: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatalf("置 user_version 失败: %v", err)
	}

	ctx := context.Background()
	relationID := fixtureRelation(t, db, "mv")
	snapshots := NewSnapshotRepository(db)
	snapP, snapR := insertSnapPair(t, snapshots, relationID, "mv")
	plan := fixturePlan(t, "plan_mv", relationID, snapP, snapR)
	if err := NewPlanRepository(db).Insert(ctx, plan); err != nil {
		t.Fatalf("插入计划失败: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	task := model.Task{
		TaskID: "task_mv", RelationID: relationID, Kind: model.TaskKindScan,
		Status: model.TaskStatusSucceeded, Phase: "done", Sequence: 2,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := NewTaskRepository(db).Insert(ctx, task); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	// Phase 2 才会写 operation_journal，这里预置一行验证表重建不丢数据
	if _, err := db.Exec(`INSERT INTO operation_journal
		(task_id, operation_id, ordinal, status, target_relative_path, ownership_proof_json, operation_json)
		VALUES('task_mv','op_1',0,'done','mods/x.jar','{}','{}')`); err != nil {
		t.Fatalf("预置 journal 行失败: %v", err)
	}

	if err := Migrate(ctx, db, filepath.Join(dir, "backup")); err != nil {
		t.Fatalf("v1→v2 迁移失败: %v", err)
	}
	if v := userVersion(t, db); v != 2 {
		t.Fatalf("迁移后 user_version = %d, 期望 2", v)
	}

	// 旧任务与 journal 行原样保留
	got, err := NewTaskRepository(db).Get(ctx, "task_mv")
	if err != nil {
		t.Fatalf("迁移后读取任务失败: %v", err)
	}
	if got.Kind != model.TaskKindScan || got.Status != model.TaskStatusSucceeded || got.Sequence != 2 {
		t.Errorf("迁移后任务数据不一致: %+v", got)
	}
	var journalCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM operation_journal WHERE task_id='task_mv'").Scan(&journalCount); err != nil {
		t.Fatalf("读取 journal 失败: %v", err)
	}
	if journalCount != 1 {
		t.Errorf("迁移后 journal 行数 = %d, 期望 1", journalCount)
	}

	// v2 后 plan_id 外键生效：悬挂计划引用被拒，且错误可区分（非 ErrRelationNotFound）
	bad := model.Task{
		TaskID: "task_mv_bad", RelationID: relationID, Kind: model.TaskKindApply,
		Status: model.TaskStatusQueued, Phase: "apply", PlanID: "plan_none",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := NewTaskRepository(db).Insert(ctx, bad); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("悬挂 plan_id 应返回 ErrPlanNotFound, got %v", err)
	}
	badCommit := bad
	badCommit.TaskID = "task_mv_bad2"
	badCommit.PlanID = ""
	badCommit.CommitID = "commit_none"
	if err := NewTaskRepository(db).Insert(ctx, badCommit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("悬挂 commit_id 应返回 ErrNotFound, got %v", err)
	}
}
