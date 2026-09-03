package sqlite

// gc_repo_test.go 覆盖 ports.GCRepository 的 SQLite 实现（票 #64）：
// 级联删除的 FK 顺序与元数据重连、墓碑计数的读时推导口径、隔离态批量标记
// 的可重入性、安全窗口的未收口运行判定、活跃计划引用通道的对象面。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"packgradle/internal/core/model"
)

// gcFixture 是 GC 测试的公共夹具：1 关系 + N 提交链（各带结果基线与对象引用）。
type gcFixture struct {
	relationID string
	commitIDs  []string // oldest-first
	baselineID []string // 与 commitIDs 同序的结果基线
	taskIDs    []string // 与 commitIDs 同序的 apply 任务
	runIDs     []string // 与 commitIDs 同序的 committed 运行
}

// newGCFixture 造 n 提交连续链：每提交 result 基线 + apply 任务（AttachCommit）
// + committed 运行（AttachCommit）——墓碑计数与级联删除的真实前态。
// 返回夹具与测试库句柄（用毕由 close 关闭）。
func newGCFixture(t *testing.T, n int) (*gcFixture, *sql.DB, func()) {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	fx := &gcFixture{}
	relationID := fixtureRelation(t, db, "gc")
	fx.relationID = relationID

	snapshots := NewSnapshotRepository(db)
	projectSnap, runtimeSnap := insertSnapPair(t, snapshots, relationID, "gc")
	plan := fixturePlan(t, "plan_gc", relationID, projectSnap, runtimeSnap)
	if err := NewPlanRepository(db).Insert(ctx, plan); err != nil {
		t.Fatalf("插入计划失败: %v", err)
	}
	baselines := NewBaselineRepository(db)
	commits := NewCommitRepository(db)
	tasks := NewTaskRepository(db)
	runs := NewApplyRunRepository(db)
	now := time.Now().UTC().Format(time.RFC3339)

	var parentID string
	for i := 0; i < n; i++ {
		commitID := "gc_c" + string(rune('a'+i))
		baselineID := "gc_b" + string(rune('a'+i))
		taskID := "gc_t" + string(rune('a'+i))
		base := fixtureBaseline(t, baselineID, relationID, "")
		if err := baselines.Insert(ctx, base); err != nil {
			t.Fatalf("插入基线 %s 失败: %v", baselineID, err)
		}
		commit := model.SyncCommit{
			CommitID: commitID, RelationID: relationID, ParentCommitID: parentID,
			CreatedAt: now, PlanID: plan.PlanID,
			VerifiedProjectSnapshotID: projectSnap.SnapshotID,
			VerifiedRuntimeSnapshotID: runtimeSnap.SnapshotID,
			ResultBaselineID:          baselineID,
			CommitKind:                "sync", Completeness: model.TaskOutcomeExact,
		}
		if err := commits.Insert(ctx, commit); err != nil {
			t.Fatalf("插入提交 %s 失败: %v", commitID, err)
		}
		// before 保全对象引用（objects 行 + object_refs 行）。
		digest := "gc_digest_" + string(rune('a'+i))
		if _, err := db.ExecContext(ctx,
			"INSERT INTO objects(algorithm, digest, size, state, created_at) VALUES('sha256',?,10,'ready',?)",
			digest, now); err != nil {
			t.Fatalf("插入对象失败: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO object_refs(owner_type, owner_id, algorithm, digest, purpose, size)"+
				" VALUES('commit',?,'sha256',?,'before_preservation',10)", commitID, digest); err != nil {
			t.Fatalf("插入对象引用失败: %v", err)
		}
		task := model.Task{TaskID: taskID, RelationID: relationID, Kind: model.TaskKindApply,
			Status: model.TaskStatusSucceeded, Phase: "done", CreatedAt: now, UpdatedAt: now,
			CommitID: commitID}
		if err := tasks.Insert(ctx, task); err != nil {
			t.Fatalf("插入任务失败: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest, relation_revision, state,"+
				" preconditions_json, recovery_refs_json, operation_count, created_at, updated_at)"+
				" VALUES(?,?,?,'sha256:plan-digest',1,'committed','[]','[]',0,?,?)",
			taskID, relationID, plan.PlanID, now, now); err != nil {
			t.Fatalf("插入运行失败: %v", err)
		}
		if err := runs.AttachCommit(ctx, taskID, commitID, now); err != nil {
			t.Fatalf("AttachCommit 失败: %v", err)
		}

		fx.commitIDs = append(fx.commitIDs, commitID)
		fx.baselineID = append(fx.baselineID, baselineID)
		fx.taskIDs = append(fx.taskIDs, taskID)
		fx.runIDs = append(fx.runIDs, taskID)
		parentID = commitID
	}
	return fx, db, func() { db.Close() }
}

// TestGCApplyPruneCascade 覆盖级联删除执行（ADR-0007 §1/执行要点）：
// 裁前 2 提交后——提交/变化行/对象引用/结果基线行消失、被裁链首存活提交的
// parent 与 previous_baseline 重连置空、存活提交的引用行不受波及。
func TestGCApplyPruneCascade(t *testing.T) {
	fx, db, closeDB := newGCFixture(t, 5)
	defer closeDB()
	ctx := context.Background()
	repo := NewGCRepository(db)

	pruned := fx.commitIDs[:2]
	dropped := fx.baselineID[:2]
	if err := repo.ApplyPrune(ctx, fx.relationID, pruned, dropped, fx.commitIDs[2], fx.baselineID[2]); err != nil {
		t.Fatalf("ApplyPrune: %v", err)
	}

	chain, err := repo.RelationCommitsChain(ctx, fx.relationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("存活提交 %d，期望 3", len(chain))
	}
	first := chain[0]
	if first.CommitID != fx.commitIDs[2] {
		t.Fatalf("链首 %s，期望 %s", first.CommitID, fx.commitIDs[2])
	}
	if first.ParentCommitID != "" || first.PreviousBaselineID != "" {
		t.Fatalf("重连提交 parent=%q previous_baseline=%q，期望双置空", first.ParentCommitID, first.PreviousBaselineID)
	}
	for _, id := range pruned {
		for _, c := range chain {
			if c.CommitID == id {
				t.Fatalf("被裁提交 %s 仍在链上", id)
			}
		}
	}
	// 被裁基线行消失、存活基线保留。
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sync_baselines WHERE id IN ('gc_ba','gc_bb')").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("被裁基线残留 %d", n)
	}
	// 被裁提交的对象引用行随级联删除；存活提交的引用保留。
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM object_refs WHERE owner_id IN ('gc_ca','gc_cb')").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("被裁提交对象引用残留 %d", n)
	}
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM object_refs WHERE owner_id = 'gc_cc'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("存活提交对象引用应保留 1 条，got %d", n)
	}
}

// TestGCPrunedBeforeCount 覆盖墓碑计数的读时推导：committed 运行数 − 现存提交数。
// 裁 2 后 = 5 − 3 = 2（tasks/apply_runs 的 commit_id 置空不影响推导）。
func TestGCPrunedBeforeCount(t *testing.T) {
	fx, db, closeDB := newGCFixture(t, 5)
	defer closeDB()
	ctx := context.Background()
	repo := NewGCRepository(db)

	if err := repo.ApplyPrune(ctx, fx.relationID, fx.commitIDs[:2], fx.baselineID[:2],
		fx.commitIDs[2], fx.baselineID[2]); err != nil {
		t.Fatal(err)
	}
	n, err := repo.PrunedBeforeCount(ctx, fx.relationID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("墓碑计数 %d，期望 2", n)
	}
	// 无裁剪的关系为 0。
	other := fixtureRelation(t, db, "gc2")
	m, err := repo.PrunedBeforeCount(ctx, other)
	if err != nil || m != 0 {
		t.Fatalf("无裁剪关系墓碑 = %d/%v，期望 0", m, err)
	}
}

// TestGCQuarantineObjects 覆盖隔离批量标记与可重入（ADR-0007 §5 步骤 1）：
// 只标 ready、同批重跑零变化（WHERE state='ready' 守卫）、ListQuarantined 对账。
func TestGCQuarantineObjects(t *testing.T) {
	_, db, closeDB := newGCFixture(t, 3)
	defer closeDB()
	ctx := context.Background()
	repo := NewGCRepository(db)

	ready, err := repo.ReadyDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 3 {
		t.Fatalf("ready %d，期望 3", len(ready))
	}
	marked, err := repo.QuarantineObjects(ctx, ready[:2])
	if err != nil {
		t.Fatal(err)
	}
	if marked != 2 {
		t.Fatalf("首标 %d，期望 2", marked)
	}
	// 可重入：同批重跑，ready 守卫令标记数为 0。
	marked, err = repo.QuarantineObjects(ctx, ready[:2])
	if err != nil {
		t.Fatal(err)
	}
	if marked != 0 {
		t.Fatalf("重入标记 %d，期望 0", marked)
	}
	quarantined, err := repo.ListQuarantined(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantined) != 2 {
		t.Fatalf("隔离区 %d，期望 2", len(quarantined))
	}
	// 复活：quarantined→ready。
	if err := repo.RestoreObject(ctx, ready[0]); err != nil {
		t.Fatalf("RestoreObject: %v", err)
	}
	quarantined, _ = repo.ListQuarantined(ctx)
	if len(quarantined) != 1 {
		t.Fatalf("复活后隔离区 %d，期望 1", len(quarantined))
	}
}

// insertGCRun 插入最小任务+运行对（apply_runs.task_id 外键要求 tasks 行存在）。
func insertGCRun(t *testing.T, db *sql.DB, relationID, taskID, planID, state string, acknowledged bool) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	task := model.Task{TaskID: taskID, RelationID: relationID, Kind: model.TaskKindApply,
		Status: model.TaskStatusRunning, Phase: "probe", CreatedAt: now, UpdatedAt: now}
	if err := NewTaskRepository(db).Insert(ctx, task); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	ack := "NULL"
	if acknowledged {
		ack = "'" + now + "'"
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest, relation_revision, state,"+
			" preconditions_json, recovery_refs_json, operation_count, created_at, updated_at, acknowledged_at)"+
			" VALUES(?,?,?,'sha256:p',1,?,'[]','[]',1,?,?,"+ack+")",
		taskID, relationID, planID, state, now, now); err != nil {
		t.Fatalf("插入运行失败: %v", err)
	}
}

// TestGCHasUnresolvedRuns 覆盖安全窗口构成项口径（ADR-0007 §3）：
// 活跃运行占窗；recovery_required 未确认占窗、已确认不占；committed/failed 不占。
func TestGCHasUnresolvedRuns(t *testing.T) {
	fx, db, closeDB := newGCFixture(t, 3)
	defer closeDB()
	ctx := context.Background()
	repo := NewGCRepository(db)

	// 全 committed → 窗口开。
	open, err := repo.HasUnresolvedRuns(ctx)
	if err != nil || open {
		t.Fatalf("全 committed 窗口应开: %v/%v", open, err)
	}
	// 插入 staged 运行 → 占窗。
	now := time.Now().UTC().Format(time.RFC3339)
	insertGCRun(t, db, fx.relationID, "gc_t_run_staged", "plan_gc", model.ApplyRunStaged, false)
	if open, _ = repo.HasUnresolvedRuns(ctx); !open {
		t.Fatalf("staged 运行应占窗")
	}
	// recovery_required 未确认 → 占窗；确认后 → 不占。
	insertGCRun(t, db, fx.relationID, "gc_t_run_rec", "plan_gc", model.ApplyRunRecoveryRequired, false)
	if open, _ = repo.HasUnresolvedRuns(ctx); !open {
		t.Fatalf("未确认 recovery 应占窗")
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE apply_runs SET acknowledged_at=? WHERE task_id='gc_t_run_rec'", now); err != nil {
		t.Fatal(err)
	}
	// staged 仍占窗；撤走 staged 后已确认 recovery 不占。
	if _, err := db.ExecContext(ctx,
		"UPDATE apply_runs SET state='committed' WHERE task_id='gc_t_run_staged'"); err != nil {
		t.Fatal(err)
	}
	if open, _ = repo.HasUnresolvedRuns(ctx); open {
		t.Fatalf("已确认 recovery 不应占窗")
	}
}

// TestGCPlanBaseDigestHits 覆盖活跃计划引用通道的对象面：活跃计划 base 基线
// 的资源 digest 命中 objects 的部分进入保护；已消费（有 committed 运行）计划
// 的 base 不再保护。
func TestGCPlanBaseDigestHits(t *testing.T) {
	fx, db, closeDB := newGCFixture(t, 2)
	defer closeDB()
	ctx := context.Background()
	repo := NewGCRepository(db)

	// 基线资源 logical_digest（fixture 为 sha256:logical-* 形状）不入 objects；
	// 插入命中行后通道应返回它。
	if _, err := db.ExecContext(ctx,
		"INSERT INTO objects(algorithm, digest, size, state, created_at) VALUES('sha256','sha256:logical-a',5,'ready',?)",
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// fx 的提交 0 的结果基线 base=gc_ba：再造一个「活跃计划」指向它。
	_, err := db.ExecContext(ctx, `
INSERT INTO sync_plans(id, relation_id, kind, base_baseline_id, input_project_snapshot_id,
	input_runtime_snapshot_id, relation_revision, plan_digest, status, expires_at,
	normalization_version, plan_json)
SELECT 'plan_gc_active', relation_id, 'sync', 'gc_ba', input_project_snapshot_id,
	input_runtime_snapshot_id, relation_revision, 'sha256:active', 'draft', '2999-01-01', normalization_version, plan_json
FROM sync_plans WHERE id='plan_gc'`)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := repo.PlanBaseDigestHits(ctx, []string{fx.relationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0] != "sha256:logical-a" {
		t.Fatalf("活跃计划对象通道 = %v，期望 [sha256:logical-a]", hits)
	}
	// 计划被 committed 运行消费后不再保护。
	insertGCRun(t, db, fx.relationID, "gc_t_consume", "plan_gc_active", model.ApplyRunCommitted, false)
	hits, err = repo.PlanBaseDigestHits(ctx, []string{fx.relationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("已消费计划的 base 不应保护，got %v", hits)
	}
}

// TestGCBaselineContentDigestHits 覆盖基线表示 Content 引用形态（ADR-0012
// §3/§8.6，票 #88）：project/runtime 两侧表示 JSON 内 content.digest 命中
// objects 的部分进入对账可达闭包；无 Content 的表示与未落账目的 digest 不返回。
func TestGCBaselineContentDigestHits(t *testing.T) {
	fx, db, closeDB := newGCFixture(t, 2)
	defer closeDB()
	ctx := context.Background()
	repo := NewGCRepository(db)
	now := time.Now().UTC().Format(time.RFC3339)

	// fixture 基线（gc_ba/gc_bb）的项目侧表示带 Content digest "aa11"；
	// 落账目行后通道应返回它。表示无 Content 的资源与未落账目 digest 不返回。
	if _, err := db.ExecContext(ctx,
		"INSERT INTO objects(algorithm, digest, size, state, created_at) VALUES('sha256','aa11',296,'ready',?)", now); err != nil {
		t.Fatal(err)
	}
	hits, err := repo.BaselineContentDigestHits(ctx, []string{fx.relationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0] != "aa11" {
		t.Fatalf("基线内容通道 = %v，期望 [aa11]", hits)
	}

	// runtime 侧表示的 Content 引用同样入闭包：给 gc_bb 补 runtime 表示 JSON。
	if _, err := db.ExecContext(ctx, `
UPDATE baseline_resources SET runtime_representation_json =
'{"relative_path":"mods/x-1.2.2.jar","format":"jar","content":{"algorithm":"sha256","digest":"bb22","size":10}}'
WHERE baseline_id='gc_bb'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO objects(algorithm, digest, size, state, created_at) VALUES('sha256','bb22',10,'ready',?)", now); err != nil {
		t.Fatal(err)
	}
	hits, err = repo.BaselineContentDigestHits(ctx, []string{fx.relationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("基线内容通道 = %v，期望 [aa11 bb22] 双侧命中", hits)
	}

	// 其他关系的基线不进闭包。
	hits, err = repo.BaselineContentDigestHits(ctx, []string{"rel_other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("其他关系基线不应入闭包，got %v", hits)
	}
}
