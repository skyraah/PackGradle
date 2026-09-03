package sync

// gc_snapshot_sweep_test.go 覆盖孤儿快照清扫挂接 GC 清扫阶段（票 #89，
// ADR-0011 §4）：判定三通道（存活提交 verified_* / 现存计划 input_* /
// 任一端最新）保快照、无引用中间扫描快照删除、resource_representations 随行
// 级联删；提交被修剪后其验证快照自然转孤儿一并删。

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// clnSnap 直插快照行 + 一条资源表示行（级联删断言的随行面）。
func clnSnap(t *testing.T, db *sql.DB, id, relationID, side, capturedAt string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO observed_snapshots(id, relation_id, side,
		binding_fingerprint, scanner_name, scanner_version, captured_at, snapshot_digest,
		normalization_version, policy_digest, resource_count, diagnostics_json)
		VALUES(?,?,?,'sha256:bf','tester','1',?,'sha256:sd',1,'sha256:pd',1,'[]')`,
		id, relationID, side, capturedAt); err != nil {
		t.Fatalf("插入快照 %s: %v", id, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO resource_representations(snapshot_id, resource_id,
		resource_kind, identity_provider, identity_key, identity_confidence, policy_id, relative_path,
		format, semantic_json) VALUES(?, 'file:a', 'text_file', 'path', 'a', 'exact', 'rule', 'a.txt',
		'ini', '{}')`, id); err != nil {
		t.Fatalf("插入快照 %s 资源表示: %v", id, err)
	}
}

// clnSnapExists 查快照行与其资源表示行存在性。
func clnSnapExists(t *testing.T, db *sql.DB, id string) (snap int, res int) {
	t.Helper()
	if err := db.QueryRow("SELECT COUNT(*) FROM observed_snapshots WHERE id=?", id).Scan(&snap); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM resource_representations WHERE snapshot_id=?", id).Scan(&res); err != nil {
		t.Fatal(err)
	}
	return snap, res
}

// TestGCEngineSweepsOrphanSnapshots 覆盖快照清扫三通道判定与级联删：
// 计划输入对、提交验证对、任一端最新对保留；无引用中间扫描对判孤儿删除
//（observed_snapshots + resource_representations 同删）。
func TestGCEngineSweepsOrphanSnapshots(t *testing.T) {
	app, db, _, _ := newGCEngineStack(t)
	ctx := context.Background()
	relationID := gcInsertRelation(t, db, "snap")

	ts := func(offset int) string {
		return time.Date(2026, 9, 3, 10, offset, 0, 0, time.UTC).Format(time.RFC3339)
	}
	// 计划输入对（10:00）→ 计划行引用。
	clnSnap(t, db, "snap_plan_p", relationID, "project", ts(0))
	clnSnap(t, db, "snap_plan_r", relationID, "runtime", ts(0))
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_plans(id, relation_id, kind,
		input_project_snapshot_id, input_runtime_snapshot_id, relation_revision, plan_digest, status,
		expires_at, normalization_version, plan_json) VALUES('plan_snap',?, 'sync','snap_plan_p',
		'snap_plan_r',1,'sha256:p','draft','2999-01-01T00:00:00Z',1,'{}')`, relationID); err != nil {
		t.Fatal(err)
	}
	// 中间扫描对（11:00）→ 无任何引用 → 孤儿。
	clnSnap(t, db, "snap_old_p", relationID, "project", ts(60))
	clnSnap(t, db, "snap_old_r", relationID, "runtime", ts(60))
	// 任一端最新对（12:00）→ 最新通道保留。
	clnSnap(t, db, "snap_new_p", relationID, "project", ts(120))
	clnSnap(t, db, "snap_new_r", relationID, "runtime", ts(120))

	tv, err := app.RequestGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gcWaitTask(t, app, tv.TaskID, "succeeded", 10*time.Second)

	if snap, res := clnSnapExists(t, db, "snap_old_p"); snap != 0 || res != 0 {
		t.Fatalf("孤儿快照 snap_old_p 应随资源表示级联删（snap=%d res=%d）", snap, res)
	}
	if snap, _ := clnSnapExists(t, db, "snap_old_r"); snap != 0 {
		t.Fatalf("孤儿快照 snap_old_r 应删除")
	}
	for _, id := range []string{"snap_plan_p", "snap_plan_r", "snap_new_p", "snap_new_r"} {
		if snap, _ := clnSnapExists(t, db, id); snap != 1 {
			t.Fatalf("受保护快照 %s 应保留", id)
		}
	}
}

// TestGCEnginePrunedCommitSnapshotTurnsOrphan 覆盖「提交被修剪 → 其验证快照
// 自然转孤儿一并删」（ADR-0011 §4）：提交存活时验证快照受保护；提交行消失
//（GC 修剪后的等价状态）后，下一轮 GC 清扫删除之。
func TestGCEnginePrunedCommitSnapshotTurnsOrphan(t *testing.T) {
	app, db, _, _ := newGCEngineStack(t)
	ctx := context.Background()
	relationID := gcInsertRelation(t, db, "prune")

	ts := func(offset int) string {
		return time.Date(2026, 9, 3, 10, offset, 0, 0, time.UTC).Format(time.RFC3339)
	}
	// 计划（输入对 10:00）+ 基线 + 提交（验证对 11:00，结果基线）。
	clnSnap(t, db, "snap_in_p", relationID, "project", ts(0))
	clnSnap(t, db, "snap_in_r", relationID, "runtime", ts(0))
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_plans(id, relation_id, kind,
		input_project_snapshot_id, input_runtime_snapshot_id, relation_revision, plan_digest, status,
		expires_at, normalization_version, plan_json) VALUES('plan_prune',?, 'sync','snap_in_p',
		'snap_in_r',1,'sha256:p','draft','2999-01-01T00:00:00Z',1,'{}')`, relationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_baselines(id, relation_id, created_at,
		baseline_digest, normalization_version) VALUES('base_prune',?,?,'sha256:b',1)`,
		relationID, ts(30)); err != nil {
		t.Fatal(err)
	}
	clnSnap(t, db, "snap_ver_p", relationID, "project", ts(60))
	clnSnap(t, db, "snap_ver_r", relationID, "runtime", ts(60))
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_commits(id, relation_id, created_at, plan_id,
		verified_project_snapshot_id, verified_runtime_snapshot_id, result_baseline_id, commit_kind,
		completeness, remaining_change_count, summary_json)
		VALUES('commit_prune',?,?,'plan_prune','snap_ver_p','snap_ver_r','base_prune','sync','exact',0,'{}')`,
		relationID, ts(90)); err != nil {
		t.Fatal(err)
	}
	// 最新对（12:00）。
	clnSnap(t, db, "snap_head_p", relationID, "project", ts(120))
	clnSnap(t, db, "snap_head_r", relationID, "runtime", ts(120))

	// 第一轮 GC：单提交链在 K=3 保底下不裁 → 验证快照受提交引用保护。
	tv, err := app.RequestGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gcWaitTask(t, app, tv.TaskID, "succeeded", 10*time.Second)
	if snap, _ := clnSnapExists(t, db, "snap_ver_p"); snap != 1 {
		t.Fatalf("存活提交的验证快照应保留")
	}

	// 提交被修剪（GC 级联后的等价账面：提交行与逐资源变化行已不在）。
	if _, err := db.ExecContext(ctx, "DELETE FROM sync_commits WHERE id='commit_prune'"); err != nil {
		t.Fatal(err)
	}

	// 第二轮 GC：验证快照失去提交引用 → 孤儿，一并删。
	tv, err = app.RequestGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gcWaitTask(t, app, tv.TaskID, "succeeded", 10*time.Second)
	if snap, res := clnSnapExists(t, db, "snap_ver_p"); snap != 0 || res != 0 {
		t.Fatalf("被修剪提交的验证快照应转孤儿删除（snap=%d res=%d）", snap, res)
	}
	if snap, _ := clnSnapExists(t, db, "snap_ver_r"); snap != 0 {
		t.Fatalf("被修剪提交的验证快照（运行端）应删除")
	}
	// 计划输入对与最新对不受波及。
	for _, id := range []string{"snap_in_p", "snap_in_r", "snap_head_p", "snap_head_r"} {
		if snap, _ := clnSnapExists(t, db, id); snap != 1 {
			t.Fatalf("受保护快照 %s 应保留", id)
		}
	}
}
