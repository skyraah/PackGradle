package sqlite

// schema v6 迁移与仓库层契约测试（契约 06 §6，票 #57）：
// relations.authorized_apply 加列（默认 0）、apply_runs state CHECK 增 'failed'
// 表重建（v5 先例：legacy_alter_table + 拷贝时校验 CHECK + Verify 外键兜底）、
// 既有数据保全、迁移幂等零副作用、零新表（预留枚举不动）。

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"packgradle/internal/core/model"
)

func TestSchemaV6ColumnContract(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// 目标版本断言：本票交付 schema v6（动态断言 + 显式钉版双保险）
	if SchemaVersion() != 6 {
		t.Fatalf("SchemaVersion() = %d, 期望 6", SchemaVersion())
	}
	if v := userVersion(t, db); v != 6 {
		t.Fatalf("user_version = %d, 期望 6", v)
	}

	// relations.authorized_apply 列定义：NOT NULL、默认 0
	if !columnExists(t, db, "relations", "authorized_apply") {
		t.Fatal("relations 缺少 authorized_apply 列")
	}
	rows, err := db.Query(`PRAGMA table_info(relations)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	colFound := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "authorized_apply" {
			colFound = true
			if notnull != 1 || dflt.String != "0" {
				t.Errorf("authorized_apply 列定义 notnull=%d dflt=%q, 期望 NOT NULL DEFAULT 0", notnull, dflt.String)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !colFound {
		t.Fatal("PRAGMA table_info 未报出 authorized_apply 列")
	}

	// 新建关系默认关闭；显式开关可写 0/1
	relationID := fixtureRelation(t, db, "v6")
	relations := NewRelationRepository(db)
	rel, err := relations.Get(ctx, relationID)
	if err != nil {
		t.Fatalf("读取关系失败: %v", err)
	}
	if rel.AuthorizedApply {
		t.Error("新建关系 authorized_apply 应默认 false")
	}
	if err := relations.UpdateAuthorizedApply(ctx, relationID, true); err != nil {
		t.Fatalf("开启授权开关失败: %v", err)
	}
	if rel, err = relations.Get(ctx, relationID); err != nil || !rel.AuthorizedApply {
		t.Errorf("开启后投影应 true, got %v err=%v", rel.AuthorizedApply, err)
	}
	if err := relations.UpdateAuthorizedApply(ctx, relationID, false); err != nil {
		t.Fatalf("关闭授权开关失败: %v", err)
	}
	if rel, err = relations.Get(ctx, relationID); err != nil || rel.AuthorizedApply {
		t.Errorf("关闭后投影应 false, got %v err=%v", rel.AuthorizedApply, err)
	}
	if err := relations.UpdateAuthorizedApply(ctx, "rel_none", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在关系应返回 ErrNotFound, got %v", err)
	}

	// apply_runs 七态 CHECK：六既有态 + failed 放行，非法值拒绝
	fx := fixtureApplyScaffold(t, db, "v6run")
	runs := NewApplyRunRepository(db)
	if err := runs.Insert(ctx, fixtureApplyRun(fx)); err != nil {
		t.Fatalf("写入运行头失败: %v", err)
	}
	for _, state := range []string{
		model.ApplyRunPrepared, model.ApplyRunStaged, model.ApplyRunApplying,
		model.ApplyRunVerifying, model.ApplyRunCommitted, model.ApplyRunRecoveryRequired,
		"failed", // v6 新增终局（契约 06 §6 Q8）；字面量直用，状态机语义归后续票
	} {
		if _, err := db.Exec(`UPDATE apply_runs SET state=? WHERE task_id=?`, state, fx.taskID); err != nil {
			t.Errorf("CHECK 应放行合法 state=%s: %v", state, err)
		}
	}
	if _, err := db.Exec(`UPDATE apply_runs SET state='flying' WHERE task_id=?`, fx.taskID); err == nil {
		t.Error("CHECK 应拒绝非法 apply_runs.state='flying'")
	}

	// 重建后索引在位
	if !indexExists(t, db, "apply_runs", "idx_apply_runs_relation_state") {
		t.Error("apply_runs 重建后缺少 idx_apply_runs_relation_state 索引")
	}

	// 零新表：v6 拷贝表无残留
	var residue int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE '%_v6'`).Scan(&residue); err != nil {
		t.Fatal(err)
	}
	if residue != 0 {
		t.Errorf("发现 %d 张 v6 拷贝残留表", residue)
	}

	// 预留枚举不动：tasks.kind 含 restore/gc、objects.state 含 quarantined（v1 起预留）
	tasksDDL := tableDDL(t, db, "tasks")
	for _, want := range []string{"'restore'", "'gc'"} {
		if !strings.Contains(tasksDDL, want) {
			t.Errorf("tasks.kind CHECK 应仍预留 %s", want)
		}
	}
	objectsDDL := tableDDL(t, db, "objects")
	if !strings.Contains(objectsDDL, "'quarantined'") {
		t.Error("objects.state CHECK 应仍预留 'quarantined'")
	}
}

// TestMigrateV5ToV6RebuildsApplyRuns 验证 v5 → v6 升级（契约 06 §6，票 #57）：
// 既有数据保全（authorized_apply 默认 0、apply_runs 行原样保留）、failed 进 CHECK、
// 重复迁移零副作用（沿 v4→v5 重建测试先例）。
func TestMigrateV5ToV6RebuildsApplyRuns(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "packgradle.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer db.Close()

	// 手工搭出 v5 库（v1 全量 + v2..v5 顺序执行）
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("建 v1 schema 失败: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatalf("置 user_version 失败: %v", err)
	}
	for _, stmt := range []string{schemaV2, schemaV3, schemaV4, schemaV5} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("手工执行 v2..v5 失败: %v", err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version=5"); err != nil {
		t.Fatalf("置 user_version=5 失败: %v", err)
	}

	// v5 阶段预置数据：raw 关系（v1 列集，无 authorized_apply）+ 快照对 + 计划 +
	// 任务 + 运行头（staged 态）。仓库列清单随最新 schema 演进，关系行不能走
	// 仓库写路径（fixtureRelationRaw 注记）；计划/任务/运行头表 v5 未变可走仓库。
	ctx := context.Background()
	relationID := fixtureRelationRaw(t, db, "mig")
	snapshots := NewSnapshotRepository(db)
	snapP, snapR := insertSnapPair(t, snapshots, relationID, "mig")
	plan := fixturePlan(t, "plan_mig", relationID, snapP, snapR)
	if err := NewPlanRepository(db).Insert(ctx, plan); err != nil {
		t.Fatalf("插入计划失败: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := NewTaskRepository(db).Insert(ctx, model.Task{
		TaskID: "task_mig", RelationID: relationID, Kind: model.TaskKindApply,
		Status: model.TaskStatusQueued, Phase: "apply", PlanID: plan.PlanID,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	const taskID = "task_mig"
	const planID = "plan_mig"
	if _, err := db.Exec(`INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest,
		relation_revision, state, preconditions_json, recovery_refs_json, operation_count, created_at, updated_at)
		VALUES(?,?,?,?,1,'staged','[]','[]',3,?,?)`,
		taskID, relationID, planID, "sha256:plan-mig", guardTestTime, guardTestTime); err != nil {
		t.Fatalf("预置 v5 运行头失败: %v", err)
	}

	if err := Migrate(ctx, db, filepath.Join(dir, "backup")); err != nil {
		t.Fatalf("v5→v6 迁移失败: %v", err)
	}
	if v := userVersion(t, db); v != 6 {
		t.Fatalf("迁移后 user_version = %d, 期望 6", v)
	}

	// 既有数据保全：关系行原样（authorized_apply 迁移默认 0）
	relations := NewRelationRepository(db)
	rel, err := relations.Get(ctx, relationID)
	if err != nil {
		t.Fatalf("迁移后读取关系失败: %v", err)
	}
	if rel.AuthorizedApply {
		t.Error("既有关系 authorized_apply 应默认 false")
	}
	if rel.PolicySet != "default-v1" || rel.Revision != 1 {
		t.Errorf("既有关系行被改写: %+v", rel)
	}

	// 运行头原样保留（表重建不丢数据）
	run, err := NewApplyRunRepository(db).Get(ctx, taskID)
	if err != nil {
		t.Fatalf("迁移后读取运行头失败: %v", err)
	}
	if run.State != model.ApplyRunStaged || run.OperationCount != 3 || run.PlanID != planID {
		t.Errorf("迁移后运行头不一致: %+v", run)
	}

	// failed 终局在 CHECK 内（staged→failed 拷贝后可写；状态机语义归后续票）
	if _, err := db.Exec(`UPDATE apply_runs SET state='failed' WHERE task_id=?`, taskID); err != nil {
		t.Errorf("v6 后 CHECK 应放行 state='failed': %v", err)
	}

	// 升级前备份已生成（VACUUM INTO 先例沿用）
	matches, err := filepath.Glob(filepath.Join(dir, "backup", "packgradle.db.bak-*"))
	if err != nil || len(matches) == 0 {
		t.Errorf("升级前应生成 VACUUM INTO 备份: %v %v", matches, err)
	}

	// 重复迁移零副作用：行数、版本、迁移账目不变
	var migrationsBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationsBefore); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, filepath.Join(dir, "backup")); err != nil {
		t.Fatalf("重复 Migrate 应幂等: %v", err)
	}
	if v := userVersion(t, db); v != 6 {
		t.Errorf("重复迁移后 user_version = %d, 期望 6", v)
	}
	var migrations, runsCount, relCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	// v1..v5 为手工搭库（未经 applyMigration，无账目行），Migrate 只补 v6 一行；
	// 重复迁移后账目不得再增长。
	if migrations != migrationsBefore || migrations != 1 {
		t.Errorf("schema_migrations 行数 = %d（迁移前 %d）, 期望恒为 1", migrations, migrationsBefore)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM apply_runs`).Scan(&runsCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM relations`).Scan(&relCount); err != nil {
		t.Fatal(err)
	}
	if runsCount != 1 || relCount != 1 {
		t.Errorf("重复迁移后行数漂移: apply_runs=%d relations=%d, 期望 1/1", runsCount, relCount)
	}
}

// indexExists 报告表上是否存在指定索引（重建迁移后索引在位断言用）。
func indexExists(t *testing.T, db *sql.DB, table, index string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND name=? AND tbl_name=?`, index, table).Scan(&n); err != nil {
		t.Fatalf("查询索引失败: %v", err)
	}
	return n > 0
}

// tableDDL 返回建表语句原文（预留枚举未被迁移触碰的断言用）。
func tableDDL(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
		t.Fatalf("读取 %s 建表语句失败: %v", table, err)
	}
	return ddl
}
