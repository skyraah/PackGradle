package sync_test

// T07（票 #17）验收面：
// ① features/availability 内嵌 WorkspaceDTO（契约 03 §2.1；能力=false 不注册）；
// ② hash cache 采集 FileKey + 命中计数/命中率可查询（检视 P1-5：热扫描命中证明）；
// ③ ignored/unsupported/runtime_local 诊断随扫描产出；
// ④ mapping_collision 等诊断经 GetSnapshotDiagnostics 在快照中可查。

import (
	"context"
	"testing"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// assertAvailability 按动作断言可用性与原因码。
func assertAvailability(t *testing.T, avail []view.ActionAvailabilityView, action string, wantAvailable bool, wantReason string) {
	t.Helper()
	for _, a := range avail {
		if a.Action != action {
			continue
		}
		if a.Available != wantAvailable || a.ReasonCode != wantReason {
			t.Fatalf("%s 可用性 = (%v, %q), 期望 (%v, %q)", action, a.Available, a.ReasonCode, wantAvailable, wantReason)
		}
		return
	}
	t.Fatalf("availability 缺少动作 %s: %+v", action, avail)
}

// assertNoApplyActions 能力=false 的动作不注册（apply_sync/prepare_restore/apply_restore）。
func assertNoApplyActions(t *testing.T, avail []view.ActionAvailabilityView) {
	t.Helper()
	for _, a := range avail {
		switch a.Action {
		case "apply_sync", "prepare_restore", "apply_restore":
			t.Fatalf("未实现能力不应出现在 availability: %s", a.Action)
		}
	}
}

func TestHeadlessWorkspaceFeaturesAndAvailability(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	w, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	// features P1 固定值
	f := w.Features
	if !f.Scan || !f.SyncPreview || !f.ConflictInspection || f.ConflictResolution != "choose_side" {
		t.Fatalf("features 固定值不符: %+v", f)
	}
	if f.SyncApply || f.HistoryView || f.RestorePreview || f.RestoreApply {
		t.Fatalf("未实现能力应为 false: %+v", f)
	}
	if len(f.MaterializationModes) != 0 {
		t.Fatalf("materialization_modes 应为空数组: %v", f.MaterializationModes)
	}
	// availability：未扫描 → scan/rebind 可用；prepare_sync 因 scan_state 非 ready 不可用
	assertNoApplyActions(t, w.Availability)
	assertAvailability(t, w.Availability, "scan", true, "")
	assertAvailability(t, w.Availability, "prepare_sync", false, "err.scan.incomplete")
	assertAvailability(t, w.Availability, "rebind", true, "")

	// 扫描中 → scan/rebind/prepare_sync 均因活跃任务不可用
	tv, err := app.StartScan(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	wRunning, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	assertAvailability(t, wRunning.Availability, "scan", false, "err.scan.already_running")
	assertAvailability(t, wRunning.Availability, "rebind", false, "err.scan.already_running")

	waitTask(t, app, tv.TaskID)

	// 就绪 → prepare_sync 可用；scan_state 走 ready
	wReady, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if wReady.State.ScanState != "ready" {
		t.Fatalf("scan_state = %q, 期望 ready", wReady.State.ScanState)
	}
	assertAvailability(t, wReady.Availability, "prepare_sync", true, "")
	assertAvailability(t, wReady.Availability, "scan", true, "")

	// 列表与详情同构：内嵌 features/availability
	page, err := app.ListWorkspaces(ctx, ports.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("工作区列表应 1 项，得到 %d", len(page.Items))
	}
	if page.Items[0].Features.Scan != true || len(page.Items[0].Availability) != 3 {
		t.Fatalf("列表项未内嵌 features/availability: %+v", page.Items[0])
	}
}

// TestHeadlessHashCacheFileKeyAndStats 命中统计：冷扫描全 miss，热扫描命中
// （P1-5：证明 size+mtime+file identity 未变时复用）；FileKey 落入 hash_cache。
func TestHeadlessHashCacheFileKeyAndStats(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	scanAndWait(t, app, rel.RelationID)
	cold, err := app.GetHashCacheStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Hits != 0 || cold.Misses == 0 {
		t.Fatalf("冷扫描应全 miss: %+v", cold)
	}

	scanAndWait(t, app, rel.RelationID)
	hot, err := app.GetHashCacheStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hot.Misses < cold.Misses || hot.Hits == 0 {
		t.Fatalf("热扫描应命中: cold=%+v hot=%+v", cold, hot)
	}
	if hot.HitRatio <= 0 || hot.HitRatio > 1 {
		t.Fatalf("命中率应在 (0,1]: %v", hot.HitRatio)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hash_cache WHERE file_key != ''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("hash_cache 应存在非空 file_key（FileKey 采集）")
	}
}

// TestHeadlessSnapshotDiagnosticsQuery 诊断在快照中可查：runtime 侧本地 jar
// 产出 runtime_local 诊断；跨 Relation / 未知 ID → err.sync.snapshot_not_found。
func TestHeadlessSnapshotDiagnosticsQuery(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	diags, err := app.GetSnapshotDiagnostics(ctx, rel.RelationID, ws.LatestRuntimeSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Code == "diag.scan.runtime_local" && d.ResourceID == model.ResourceID("mod:jar:runtimeonly-1.0.jar") {
			found = true
		}
	}
	if !found {
		t.Fatalf("runtime 快照应含 runtime_local 诊断: %+v", diags)
	}

	// 项目侧快照诊断可查（fixture 无特殊诊断，返回空列表而非错误）
	pDiags, err := app.GetSnapshotDiagnostics(ctx, rel.RelationID, ws.LatestProjectSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if pDiags == nil {
		t.Fatal("诊断列表应归一为空切片而非 nil")
	}

	// 跨 Relation 防护与未知 ID 均按 not found（不泄漏其它关系的快照存在性）
	if _, err := app.GetSnapshotDiagnostics(ctx, "rel_missing", ws.LatestRuntimeSnapshot.SnapshotID); errCode(t, err) != "err.sync.snapshot_not_found" {
		t.Fatalf("跨 Relation 查询应 snapshot_not_found，得到 %v", err)
	}
	if _, err := app.GetSnapshotDiagnostics(ctx, rel.RelationID, "snap_missing"); errCode(t, err) != "err.sync.snapshot_not_found" {
		t.Fatalf("未知快照应 snapshot_not_found，得到 %v", err)
	}
}
