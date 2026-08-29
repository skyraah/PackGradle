package sync_test

// T12（票 #22）重绑闭环 headless 集成测试：
// ① 项目侧端到端：路径移动 → 预检（指纹变化/legacy 识别/影响计数）→ Apply 原位更新
//    端点、健康恢复、基线重置（reinitialize 不继承）→ 修订号不变（ADR-0002 决议 2）
//    → 重扫在新位置成功；
// ② 旧计划失效：rebind 不递增修订号，GetPlan 由 expected_bindings 指纹失配投影
//    stale（契约 03 §2.4），内容继续可读；
// ③ 运行实例侧：实例目录迁移 → 游戏目录/adapter identity/display_name 同步更新；
// ④ 单事务（ADR-0003）：占用注入 → Apply 中途失败零残留（预检未消费、端点行不变），
//    排除冲突后同一预检重试成功；
// ⑤ 错误契约：已应用 → rebind_prep_consumed；已过期 → rebind_prep_expired；
//    side 非法 → rebind_invalid_side；预检不存在 → rebind_prep_not_found；
//    重绑到他人端点 → 预检拦截（invalid_endpoint）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// prepareRebind 执行重绑预检并断言全部 blocking 检查通过（严格失败）。
func prepareRebind(t *testing.T, app syncapp.Application, relationID, side, rootPath string) view.RebindPreparationView {
	t.Helper()
	prep, err := app.PrepareRebind(context.Background(), view.PrepareRebindInput{
		RelationID: relationID, Side: side, RootPath: rootPath,
	})
	if err != nil {
		t.Fatalf("PrepareRebind: %v", err)
	}
	for _, c := range prep.Checks {
		if c.Severity == "blocking" && !c.Passed {
			t.Fatalf("重绑预检 %s 未通过: %s", c.Code, c.Detail)
		}
	}
	return prep
}

// mustApplyRebind 消费重绑预检（严格失败）。
func mustApplyRebind(t *testing.T, app syncapp.Application, preparationID string) view.RelationView {
	t.Helper()
	rel, err := app.ApplyRebind(context.Background(), preparationID)
	if err != nil {
		t.Fatalf("ApplyRebind: %v", err)
	}
	return rel
}

// prepareDraftPlan 以两侧最新快照生成 draft 计划（重绑影响的数据源）。
func prepareDraftPlan(t *testing.T, app syncapp.Application, relationID string) view.SyncPlanView {
	t.Helper()
	ctx := context.Background()
	ws, err := app.GetWorkspace(ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             relationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	return plan
}

func TestHeadlessRebindProjectSide(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)
	draft := prepareDraftPlan(t, app, rel.RelationID)

	// 注入基线（P1 无 Apply 产生方，直接落库）验证 reinitialize：Apply 后清除
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO sync_baselines(id, relation_id, created_at, baseline_digest, normalization_version)
		VALUES('base_x', ?, ?, 'sha256:b', 1)`, rel.RelationID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE relations SET head_baseline_id='base_x' WHERE id=?`, rel.RelationID); err != nil {
		t.Fatal(err)
	}
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.State.BaselineState != "ready" {
		t.Fatalf("前置条件失败: 注入基线后 baseline_state = %s", ws.State.BaselineState)
	}

	// 端点根路径整体移动
	movedRoot := projectRoot + "-moved"
	if err := os.Rename(projectRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	// 旧架构痕迹随目录一起移动（识别不覆盖）
	writeFile(t, filepath.Join(movedRoot, "packgradle.toml"), "legacy = true\n")

	prep := prepareRebind(t, app, rel.RelationID, "project", movedRoot)
	if !prep.FingerprintChanged {
		t.Fatal("路径移动后绑定指纹应变化")
	}
	if prep.BaselineInheritance != model.BaselineInheritanceReinitialize {
		t.Fatalf("baseline_inheritance = %s, 期望 reinitialize", prep.BaselineInheritance)
	}
	if prep.InvalidatedPlanCount != 1 {
		t.Fatalf("invalidated_plan_count = %d, 期望 1", prep.InvalidatedPlanCount)
	}
	if prep.OldEndpoint.ID != rel.Project.ID || prep.NewEndpoint.ID != rel.Project.ID {
		t.Fatalf("新旧端点应共享同一端点 ID（原位更新）: %s vs %s", prep.OldEndpoint.ID, prep.NewEndpoint.ID)
	}
	if filepath.Base(prep.NewEndpoint.RootPath) != "project-moved" {
		t.Fatalf("新端点根路径 = %s", prep.NewEndpoint.RootPath)
	}
	var legacy *view.PreparationCheckView
	for i := range prep.Checks {
		if prep.Checks[i].Code == "check.legacy.materialization" {
			legacy = &prep.Checks[i]
		}
	}
	if legacy == nil || !legacy.Passed || legacy.Severity != "warning" || legacy.Detail == "" {
		t.Fatalf("legacy 痕迹应识别为警告且不阻断: %+v", legacy)
	}

	got := mustApplyRebind(t, app, prep.PreparationID)
	// 原位更新：端点 ID 不变、根路径随新位置；健康恢复；修订号不动（ADR-0002 决议 2）
	if got.Project.ID != rel.Project.ID || filepath.Base(got.Project.RootPath) != "project-moved" {
		t.Fatalf("重绑后项目端点: %+v", got.Project)
	}
	if got.Revision != 1 {
		t.Fatalf("重绑后 revision = %d, 期望仍为 1", got.Revision)
	}
	if got.Health != string(model.HealthHealthy) {
		t.Fatalf("重绑后健康 = %s", got.Health)
	}

	// reinitialize：基线重置 → baseline_state=none、diff_state=initialization_required
	ws2, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws2.State.BaselineState != "none" {
		t.Fatalf("重绑后 baseline_state = %s, 期望 none", ws2.State.BaselineState)
	}
	if ws2.State.DiffState != "initialization_required" {
		t.Fatalf("重绑后 diff_state = %s, 期望 initialization_required", ws2.State.DiffState)
	}

	// 旧计划失效由 expected_bindings 指纹校验承担（修订号未前进），内容继续可读
	stale, err := app.GetPlan(ctx, draft.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != "stale" {
		t.Fatalf("重绑后旧计划状态 = %s, 期望 stale", stale.Status)
	}
	if stale.RequestedExactness != "exact" || len(stale.Operations) != len(draft.Operations) {
		t.Fatal("stale 计划内容应继续可读")
	}

	// 重扫在新位置成功
	scanAndWait(t, app, rel.RelationID)
	ws3, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws3.State.ScanState != "ready" {
		t.Fatalf("新位置重扫后 scan_state = %s", ws3.State.ScanState)
	}
}

func TestHeadlessRebindRuntimeSide(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	// 实例目录整体移动（游戏目录随行）
	movedInstance := filepath.Join(filepath.Dir(instanceDir), "Collapse-moved")
	if err := os.Rename(instanceDir, movedInstance); err != nil {
		t.Fatal(err)
	}

	prep := prepareRebind(t, app, rel.RelationID, "runtime", movedInstance)
	if !prep.FingerprintChanged {
		t.Fatal("实例目录移动后绑定指纹应变化")
	}
	if prep.NewEndpoint.AdapterIdentity != "collapse-moved" {
		t.Fatalf("新 adapter identity = %s, 期望 collapse-moved", prep.NewEndpoint.AdapterIdentity)
	}
	if filepath.Base(prep.NewEndpoint.InstanceDir) != "Collapse-moved" {
		t.Fatalf("新实例目录 = %s", prep.NewEndpoint.InstanceDir)
	}

	got := mustApplyRebind(t, app, prep.PreparationID)
	if got.Runtime.AdapterIdentity != "collapse-moved" {
		t.Fatalf("重绑后 adapter identity = %s", got.Runtime.AdapterIdentity)
	}
	if filepath.Base(got.Runtime.RootPath) != "minecraft" || filepath.Base(got.Runtime.InstanceDir) != "Collapse-moved" {
		t.Fatalf("重绑后运行实例端点: %+v", got.Runtime)
	}
	if got.Revision != 1 {
		t.Fatalf("重绑后 revision = %d, 期望仍为 1", got.Revision)
	}

	// 项目侧不受影响；新位置重扫成功
	scanAndWait(t, app, rel.RelationID)
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.State.ScanState != "ready" || ws.State.DiffState != "initialization_required" {
		t.Fatalf("重绑后工作区状态: %+v", ws.State)
	}
}

// TestHeadlessRebindSingleTransaction 验证 ADR-0003 单事务 doctrine 在重绑流的落点：
// Apply 中途失败 → 消费回滚 + 端点行/关系不变（零残留）；排除冲突后同一预检重试成功。
// 失败注入 = 另一项目端点行抢先登记新根路径（UNIQUE(adapter, root_path) 在更新段触发）。
func TestHeadlessRebindSingleTransaction(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	movedRoot := projectRoot + "-moved"
	if err := os.Rename(projectRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	prep := prepareRebind(t, app, rel.RelationID, "project", movedRoot)

	// 注入冲突：新根路径已被其他项目端点行登记（指纹不同，占用检查不拦，更新段撞 UNIQUE）
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO projects(id, adapter, display_name, root_path, binding_fingerprint, created_at)
		VALUES('prj_conflict','packwiz','Conflict', ?, 'sha256:conflict', ?)`,
		prep.NewEndpoint.RootPath, now); err != nil {
		t.Fatal(err)
	}

	_, err := app.ApplyRebind(ctx, prep.PreparationID)
	if code := errCode(t, err); code != "err.relation.duplicate_pair" {
		t.Fatalf("撞行错误码: %s", code)
	}

	// 零残留：预检未消费、端点行不变、关系健康/基线引用不变
	var consumedAt interface{}
	if err := db.QueryRow("SELECT consumed_at FROM rebind_preparations WHERE preparation_id=?", prep.PreparationID).Scan(&consumedAt); err != nil {
		t.Fatal(err)
	}
	if consumedAt != nil {
		t.Fatalf("失败后预检消费应回滚, consumed_at = %v", consumedAt)
	}
	var rootPath string
	if err := db.QueryRow("SELECT root_path FROM projects WHERE id=?", rel.Project.ID).Scan(&rootPath); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(rootPath) != "project" {
		t.Fatalf("失败后端点行不应变更, root_path = %s", rootPath)
	}
	var health, headBaseline string
	if err := db.QueryRow("SELECT health, COALESCE(head_baseline_id,'') FROM relations WHERE id=?", rel.RelationID).Scan(&health, &headBaseline); err != nil {
		t.Fatal(err)
	}
	if health != string(model.HealthHealthy) || headBaseline != "" {
		t.Fatalf("失败后关系不变量被破坏: health=%s head_baseline=%s", health, headBaseline)
	}

	// 排除冲突后同一 preparationID 重试成功
	if _, err := db.Exec(`DELETE FROM projects WHERE id='prj_conflict'`); err != nil {
		t.Fatal(err)
	}
	got := mustApplyRebind(t, app, prep.PreparationID)
	if filepath.Base(got.Project.RootPath) != "project-moved" {
		t.Fatalf("重试后项目端点: %+v", got.Project)
	}

	// 已应用的预检二次消费 → rebind_prep_consumed（引导刷新）
	_, err = app.ApplyRebind(ctx, prep.PreparationID)
	if code := errCode(t, err); code != "err.relation.rebind_prep_consumed" {
		t.Fatalf("二次应用错误码: %s", code)
	}
}

func TestHeadlessRebindErrorContracts(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	// side 非 project/runtime
	_, err := app.PrepareRebind(ctx, view.PrepareRebindInput{RelationID: rel.RelationID, Side: "both", RootPath: projectRoot})
	if code := errCode(t, err); code != "err.relation.rebind_invalid_side" {
		t.Fatalf("非法 side 错误码: %s", code)
	}

	// 预检不存在
	if _, err = app.ApplyRebind(ctx, "prep_missing"); errCode(t, err) != "err.relation.rebind_prep_not_found" {
		t.Fatal("rebind_prep_not_found")
	}

	// 重绑到他人端点：第二个工作区的项目根被预检拦截（check.pair.duplicate）
	otherProject := filepath.Join(filepath.Dir(projectRoot), "project-b")
	otherInstance := filepath.Join(filepath.Dir(instanceDir), "Collapse-b")
	writeFile(t, filepath.Join(otherProject, "pack.toml"), fxPackToml)
	writeFile(t, filepath.Join(otherInstance, "instance.cfg"), "[General]\nname=\"Collapse-b\"\n")
	writeFile(t, filepath.Join(otherInstance, "minecraft", "placeholder.txt"), "x")
	mustPrepareAndCreate(t, app, otherProject, otherInstance)

	prepB, err := app.PrepareRebind(ctx, view.PrepareRebindInput{
		RelationID: rel.RelationID, Side: "project", RootPath: otherProject,
	})
	if err != nil {
		t.Fatal(err)
	}
	var dupCheck *view.PreparationCheckView
	for i := range prepB.Checks {
		if prepB.Checks[i].Code == "check.pair.duplicate" {
			dupCheck = &prepB.Checks[i]
		}
	}
	if dupCheck == nil || dupCheck.Passed {
		t.Fatalf("重绑到他人端点应被占用检查拦截: %+v", dupCheck)
	}
	if _, err = app.ApplyRebind(ctx, prepB.PreparationID); errCode(t, err) != "err.relation.invalid_endpoint" {
		t.Fatal("blocking 未通过的预检应被 Apply 拦截且消费回滚")
	}
	var consumedAt interface{}
	if err := db.QueryRow("SELECT consumed_at FROM rebind_preparations WHERE preparation_id=?", prepB.PreparationID).Scan(&consumedAt); err != nil {
		t.Fatal(err)
	}
	if consumedAt != nil {
		t.Fatal("拦截后预检消费应回滚（保持可重试直至过期）")
	}

	// 路径碰撞（指纹不同）：新根路径已登记为其他项目端点 → 预检期即拦截，
	// 不必等到 Apply 撞 UNIQUE(adapter, root_path)
	clashRoot := filepath.Join(filepath.Dir(projectRoot), "project-clash")
	writeFile(t, filepath.Join(clashRoot, "pack.toml"), fxPackToml)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO projects(id, adapter, display_name, root_path, binding_fingerprint, created_at)
		VALUES('prj_clash','packwiz','Clash', ?, 'sha256:clash', ?)`, clashRoot, now); err != nil {
		t.Fatal(err)
	}
	prepC, err := app.PrepareRebind(ctx, view.PrepareRebindInput{
		RelationID: rel.RelationID, Side: "project", RootPath: clashRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	dupCheck = nil
	for i := range prepC.Checks {
		if prepC.Checks[i].Code == "check.pair.duplicate" {
			dupCheck = &prepC.Checks[i]
		}
	}
	if dupCheck == nil || dupCheck.Passed {
		t.Fatalf("根路径碰撞应在预检期拦截: %+v", dupCheck)
	}

	// 已过期 → rebind_prep_expired，且消费不落库（同一预检不可复活，引导重新预检）
	expired := prepareRebind(t, app, rel.RelationID, "project", projectRoot)
	if _, err := db.Exec(`UPDATE rebind_preparations SET expires_at='2000-01-01T00:00:00Z' WHERE preparation_id=?`, expired.PreparationID); err != nil {
		t.Fatal(err)
	}
	_, err = app.ApplyRebind(ctx, expired.PreparationID)
	if code := errCode(t, err); code != "err.relation.rebind_prep_expired" {
		t.Fatalf("已过期错误码: %s", code)
	}
}
