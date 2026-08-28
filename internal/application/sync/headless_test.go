package sync_test

// headless 集成测试：真实 store（SQLite/CAS 布局）+ 真实 adapters + 真实用例，
// 不启动 Wails。覆盖 ROADMAP Step 6 退出条件与架构文档 §12.2 在 P1 可测的不变量：
// ① Register → Scan → PrepareSync → GetPlan 全链路；
// ② 重复扫描产生等价 snapshot digest；
// ③ 删除 hash cache 后可从端点重建同等快照；
// ④ 重复 Prepare 不写 Project/Runtime 文件系统；
// ⑤ 相同输入产生相同 plan digest；
// ⑥ 绑定失效 → rebind_required；revision/side 错配 → 结构化错误。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/packwiz"
	"packgradle/internal/adapters/prism"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/ids"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
	"packgradle/internal/store"
	"packgradle/internal/store/sqlite"
)

// ---- fixture ----

const (
	fxPackToml = `name = "Collapse"
author = "tester"
version = "1.0.0"
`
	fxIndex = `index = { file = "index.toml", hash-format = "sha256", hash = "0" }

[[files]]
file = "mods/sodium.pw.toml"
hash = "1"
metafile = true

[[files]]
file = "mods/jei.pw.toml"
hash = "2"
metafile = true

[[files]]
file = "mods/local.pw.toml"
hash = "3"
metafile = true
`
	fxSodium = `name = "Sodium"
filename = "sodium-0.6.5.jar"
side = "client"

[download]
url = "https://cdn.example/sodium.jar"
hash-format = "sha256"
hash = "aaabbbcccddd"

[update.modrinth]
mod-id = "AANobbMI"
`
	fxJEI = `name = "JEI"
filename = "jei-19.5.jar"
side = "both"
version = "19.5.0.3"

[download]
url = "https://media.example/jei.jar"
hash-format = "murmur2"
hash = "11223344"

[update.curseforge]
project-id = 228525
file-id = 5566778
`
	fxLocal = `name = "本地小玩意"
filename = "local-thing-1.0.jar"
`
	// runtime 侧 .index：无版本字段（与项目侧空版本一致）→ sodium 可 adopt_equal
	fxIndexSodium = `name = "Sodium"
filename = "sodium-0.6.5.jar"
side = "client"

[download]
hash-format = "sha256"
hash = "aaabbbcccddd"
`
	fxIndexJEIBroken = `name = "JEI" ` + "\x00 not valid toml [[["
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeFixtures(t *testing.T) (projectRoot, instanceDir, dataRoot string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instances", "Collapse")
	gameDir := filepath.Join(instanceDir, "minecraft")

	writeFile(t, filepath.Join(projectRoot, "pack.toml"), fxPackToml)
	writeFile(t, filepath.Join(projectRoot, "index.toml"), fxIndex)
	writeFile(t, filepath.Join(projectRoot, "mods", "sodium.pw.toml"), fxSodium)
	writeFile(t, filepath.Join(projectRoot, "mods", "jei.pw.toml"), fxJEI)
	writeFile(t, filepath.Join(projectRoot, "mods", "local.pw.toml"), fxLocal)

	writeFile(t, filepath.Join(instanceDir, "instance.cfg"), "[General]\nname=\"Collapse\"\niconKey=default\n")
	writeFile(t, filepath.Join(gameDir, "mods", "sodium-0.6.5.jar"), "fake sodium jar bytes")
	writeFile(t, filepath.Join(gameDir, "mods", "jei-19.5.jar"), "fake jei jar bytes")
	writeFile(t, filepath.Join(gameDir, "mods", "runtimeonly-1.0.jar"), "fake runtimeonly bytes")
	writeFile(t, filepath.Join(gameDir, "mods", ".index", "sodium-0.6.5.jar.pw.toml"), fxIndexSodium)
	writeFile(t, filepath.Join(gameDir, "mods", ".index", "jei-19.5.jar.pw.toml"), fxIndexJEIBroken)

	dataRoot = filepath.Join(base, "userdata")
	return
}

// newStack 用真实组件装配应用（headless：不启动 Wails，事件桥为 nil）。
func newStack(t *testing.T, dataRoot string) (*syncapp.App, *sql.DB) {
	t.Helper()
	if _, err := store.EnsureLayout(dataRoot); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(filepath.Join(dataRoot, "packgradle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(context.Background(), db, dataRoot); err != nil {
		t.Fatal(err)
	}
	app, err := syncapp.New(syncapp.AppDeps{
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
		ProjectScan:   packwiz.New(),
		RuntimeScan:   prism.New(),
		Hasher:        filesystem.NewHasher(),
		Fingerprinter: filesystem.NewFingerprinter(),
		Paths:         filesystem.PathNormalizer{},
		IDs:           ids.New,
		Now:           time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, db
}

func mustPrepareAndCreate(t *testing.T, app syncapp.Application, projectRoot, instanceDir string) view.RelationView {
	t.Helper()
	ctx := context.Background()
	prep, err := app.PrepareRelation(ctx, model.PrepareRelationInput{ProjectRoot: projectRoot, RuntimeInstanceDir: instanceDir})
	if err != nil {
		t.Fatalf("PrepareRelation: %v", err)
	}
	for _, c := range prep.Checks {
		if c.Severity == "blocking" && !c.Passed {
			t.Fatalf("预检 %s 未通过: %s", c.Code, c.Detail)
		}
	}
	rel, err := app.CreateRelation(ctx, prep.PreparationID)
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	return rel
}

// waitTask 轮询任务至终态（事件不是事实源）。
func waitTask(t *testing.T, app syncapp.Application, taskID string) view.TaskView {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		tv, err := app.GetTask(ctx, taskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		switch tv.Status {
		case model.TaskStatusSucceeded:
			return tv
		case model.TaskStatusFailed, model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
			t.Fatalf("任务终态 %s: %+v", tv.Status, tv.Problem)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("任务超时未结束")
	return view.TaskView{}
}

func scanAndWait(t *testing.T, app syncapp.Application, relationID string) {
	t.Helper()
	tv, err := app.StartScan(context.Background(), relationID)
	if err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	waitTask(t, app, tv.TaskID)
}

// snapshotEndpoints 递归采集目录指纹（rel -> sha256），用于「不写端点」断言。
func snapshotEndpoints(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			content, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			sum := sha256.Sum256(content)
			out[root+"|"+filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
			return nil
		})
	}
	return out
}

func errCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误但没有发生")
	}
	code := errs.CodeOf(err)
	if code == "" {
		t.Fatalf("错误不是结构化 AppError: %v", err)
	}
	return code
}

// ---- 测试 ----

func TestHeadlessFullChain(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()

	before := snapshotEndpoints(t, projectRoot, instanceDir)

	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	if rel.Health != string(model.HealthHealthy) {
		t.Fatalf("新建关系健康状态: %s", rel.Health)
	}
	if rel.PolicySet != "default-v1" || rel.Revision < 1 {
		t.Fatalf("关系字段: %+v", rel)
	}

	scanAndWait(t, app, rel.RelationID)

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.State.ScanState != "ready" {
		t.Fatalf("scan_state: %s", ws.State.ScanState)
	}
	if ws.State.BaselineState != "none" {
		t.Fatalf("baseline_state: %s", ws.State.BaselineState)
	}
	if ws.State.DiffState != "initialization_required" {
		t.Fatalf("diff_state: %s", ws.State.DiffState)
	}
	if ws.LatestProjectSnapshot == nil || ws.LatestRuntimeSnapshot == nil {
		t.Fatal("缺少最新快照摘要")
	}

	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	if plan.Kind != string(model.PlanInitialize) {
		t.Fatalf("plan kind: %s", plan.Kind)
	}
	if plan.PlanDigest == "" {
		t.Fatal("缺少 plan digest")
	}
	// 1 adopt_equal（sodium）+ 3 init_choice（jei/local/runtimeonly）
	if plan.Summary.AdoptEqualCount != 1 || plan.Summary.ConflictCount != 3 {
		t.Fatalf("summary: %+v conflicts=%d", plan.Summary, len(plan.Conflicts))
	}
	if len(plan.Operations) != 0 {
		t.Fatalf("初始化 draft 不应含自动操作: %d", len(plan.Operations))
	}

	got, err := app.GetPlan(ctx, plan.PlanID)
	if err != nil || got.PlanDigest != plan.PlanDigest {
		t.Fatalf("GetPlan: err=%v", err)
	}

	// ResolvePlan：jei 走项目侧、local skip、runtimeonly 走运行时侧
	resolutions := []model.Resolution{
		{ResourceID: "mod:curseforge:228525", Choice: model.ChoiceInitializeFromProject},
		{ResourceID: "mod:path:mods/local.pw.toml", Choice: model.ChoiceSkip},
		{ResourceID: "mod:jar:runtimeonly-1.0.jar", Choice: model.ChoiceInitializeFromRuntime},
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: plan.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	if resolved.PlanDigest == plan.PlanDigest {
		t.Fatal("resolved digest 必须不同于 draft")
	}
	if len(resolved.Operations) != 2 {
		t.Fatalf("resolved 操作数: %d", len(resolved.Operations))
	}
	if len(resolved.ConfirmationRequirements) == 0 {
		t.Fatal("缺少确认要求")
	}

	// 重复 Prepare 不写端点
	if after := snapshotEndpoints(t, projectRoot, instanceDir); !mapsEqual(before, after) {
		t.Fatal("只读用例修改了端点文件系统")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestHeadlessRepeatScanSameDigest(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	scanAndWait(t, app, rel.RelationID)
	ws1, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}

	// 不变量②：重复扫描 → 等价 snapshot digest
	scanAndWait(t, app, rel.RelationID)
	ws2, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws1.LatestProjectSnapshot.SnapshotDigest != ws2.LatestProjectSnapshot.SnapshotDigest ||
		ws1.LatestRuntimeSnapshot.SnapshotDigest != ws2.LatestRuntimeSnapshot.SnapshotDigest {
		t.Fatalf("重复扫描 digest 不等价:\nP %s vs %s\nR %s vs %s",
			ws1.LatestProjectSnapshot.SnapshotDigest, ws2.LatestProjectSnapshot.SnapshotDigest,
			ws1.LatestRuntimeSnapshot.SnapshotDigest, ws2.LatestRuntimeSnapshot.SnapshotDigest)
	}

	// 不变量③：删除 hash cache 后重建 → 仍等价
	if _, err := db.ExecContext(ctx, "DELETE FROM hash_cache"); err != nil {
		t.Fatal(err)
	}
	scanAndWait(t, app, rel.RelationID)
	ws3, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws1.LatestProjectSnapshot.SnapshotDigest != ws3.LatestProjectSnapshot.SnapshotDigest ||
		ws1.LatestRuntimeSnapshot.SnapshotDigest != ws3.LatestRuntimeSnapshot.SnapshotDigest {
		t.Fatal("删除 hash cache 后快照 digest 不等价（缓存被当成了事实来源）")
	}

	// 不变量⑤：相同输入两次 PrepareSync → 相同 plan digest
	input := view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws3.State.RelationRevision,
		InputProjectSnapshotID: ws3.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws3.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	}
	p1, err := app.PrepareSync(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := app.PrepareSync(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if p1.PlanDigest != p2.PlanDigest {
		t.Fatalf("相同输入 plan digest 不同: %s vs %s", p1.PlanDigest, p2.PlanDigest)
	}
}

func TestHeadlessErrorContracts(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}

	// revision 不匹配
	_, err = app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID: rel.RelationID, RelationRevision: rel.Revision + 100,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if code := errCode(t, err); code != "err.sync.revision_mismatch" {
		t.Fatalf("revision 错误码: %s", code)
	}

	// side 错配（项目快照当运行时快照用）
	_, err = app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID: rel.RelationID, RelationRevision: rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
	})
	if code := errCode(t, err); code != "err.sync.snapshot_not_found" {
		t.Fatalf("side 错配错误码: %s", code)
	}

	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID: rel.RelationID, RelationRevision: rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 非法 resolution：漏掉冲突
	_, err = app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: plan.PlanID, Resolutions: nil})
	if code := errCode(t, err); code != "err.plan.resolution_invalid" {
		t.Fatalf("resolution 错误码: %s", code)
	}

	// 不存在的 plan / relation / task
	if _, err = app.GetPlan(ctx, "plan_missing"); errCode(t, err) != "err.plan.not_found" {
		t.Fatal("plan.not_found")
	}
	if _, err = app.GetWorkspace(ctx, "rel_missing"); errCode(t, err) != "err.relation.not_found" {
		t.Fatal("relation.not_found")
	}
	if _, err = app.GetTask(ctx, "task_missing"); errCode(t, err) != "err.scan.task_not_found" {
		t.Fatal("task_not_found")
	}

	// 重复消费 preparation
	prep, err := app.PrepareRelation(ctx, model.PrepareRelationInput{ProjectRoot: projectRoot, RuntimeInstanceDir: filepath.Join(filepath.Dir(instanceDir), "NotExist")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.CreateRelation(ctx, prep.PreparationID) // 端点不存在 → blocking fail
	if code := errCode(t, err); code != "err.relation.invalid_endpoint" {
		t.Fatalf("端点校验错误码: %s", code)
	}

	// 绑定失效：直接篡改存储的 runtime 指纹 → StartScan 必须 rebind_required 且健康状态更新
	if _, err := db.ExecContext(ctx, "UPDATE runtimes SET binding_fingerprint = 'sha256:fake' WHERE id = ?", rel.Runtime.ID); err != nil {
		t.Fatal(err)
	}
	_, err = app.StartScan(ctx, rel.RelationID)
	if code := errCode(t, err); code != "err.relation.rebind_required" {
		t.Fatalf("rebind 错误码: %s", code)
	}
	ws2, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws2.State.RelationHealth != string(model.HealthRebindRequired) {
		t.Fatalf("健康状态未更新: %s", ws2.State.RelationHealth)
	}
}

// TestHeadlessZombieTaskRecovery 验证启动恢复：遗留的 running 僵尸任务被标记中断后，
// 该 Relation 可以重新 StartScan（复用活动任务语义不得变成永久锁死）。
func TestHeadlessZombieTaskRecovery(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	// 直接注入一条「进程崩溃时遗留」的 running 任务
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO tasks(id, relation_id, kind, status, phase, sequence, can_cancel,
		completed, total, message_key, message_args_json, created_at, updated_at)
		VALUES('task_zombie', ?, 'scan', 'running', 'scan_project', 3, 1, 1, 4, 'msg.task.scan.scanning_project', '[]', ?, ?)`,
		rel.RelationID, now, now); err != nil {
		t.Fatal(err)
	}

	// 僵尸存在时 StartScan 只会复用它（不启动新扫描）
	tv, err := app.StartScan(context.Background(), rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if tv.TaskID != "task_zombie" {
		t.Fatalf("应复用活动任务: %s", tv.TaskID)
	}

	// 启动恢复：僵尸被标记为 failed(err.scan.interrupted)
	if err := app.RecoverInterruptedTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := app.GetTask(context.Background(), "task_zombie")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TaskStatusFailed || got.Problem == nil || got.Problem.Code != "err.scan.interrupted" {
		t.Fatalf("僵尸任务恢复结果: %+v", got)
	}

	// 恢复后可重新扫描
	scanAndWait(t, app, rel.RelationID)
}

func TestHeadlessDuplicatePairBlocked(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	// 同一 pair 二次创建 → duplicate_pair
	ctx := context.Background()
	prep, err := app.PrepareRelation(ctx, model.PrepareRelationInput{ProjectRoot: projectRoot, RuntimeInstanceDir: instanceDir})
	if err != nil {
		t.Fatal(err)
	}
	var dupCheck *view.PreparationCheckView
	for i := range prep.Checks {
		if prep.Checks[i].Code == "check.pair.duplicate" {
			dupCheck = &prep.Checks[i]
		}
	}
	if dupCheck == nil || dupCheck.Passed {
		t.Fatalf("重复 pair 预检未拦截: %+v", dupCheck)
	}
	_, err = app.CreateRelation(ctx, prep.PreparationID)
	if code := errCode(t, err); code != "err.relation.invalid_endpoint" {
		t.Fatalf("重复 pair 创建错误码: %s", code)
	}
}
