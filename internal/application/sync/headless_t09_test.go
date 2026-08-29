package sync_test

// T09（票 #19）验收面：资源级变更 GetChanges（契约 03 §2.2）：
// ① 初始化 Diff：adopt_equal / init_choice 可见（含冲突证据与逐资源诊断）；
// ② 有基线：三态行 + 无操作资源（noop）可见，summary 全量计数不受筛选影响；
// ③ 筛选（classification/resource_kind/path_prefix）+ 非法值 → err.sync.invalid_filter；
// ④ 分页：resource_id 字节序、cursor 连续、summary 跨页稳定；
// ⑤ 快照对校验：显式 ID 跨侧/跨关系 → err.changes.snapshot_pair_invalid；
//    缺省无快照 → err.sync.snapshot_not_found。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/store/sqlite"
)

// 三态构造的 fixture 变体（在 makeFixtures 产物上覆写）：
// project jei 单侧改版本 → modify；sodium 双端异改版本 → conflict；
// runtimeonly jar 删除 → delete 候选。

// baselineFromSnapshots 以两侧最新快照构造基线（表示逐资源复制），
// 供 headless 场景把 relation 指向 head baseline（P1 无 Apply，测试直写列）。
const fxJEIVersionBumped = `name = "JEI"
filename = "jei-19.5.jar"
side = "both"
version = "19.5.0.4"

[download]
url = "https://media.example/jei.jar"
hash-format = "murmur2"
hash = "11223344"

[update.curseforge]
project-id = 228525
file-id = 5566778
`

const fxSodiumVersionBumped = `name = "Sodium"
filename = "sodium-0.6.5.jar"
side = "client"
version = "0.6.6"

[download]
url = "https://cdn.example/sodium.jar"
hash-format = "sha256"
hash = "aaabbbcccddd"

[update.modrinth]
mod-id = "AANobbMI"
`

const fxIndexSodiumVersionBumped = `name = "Sodium"
filename = "sodium-0.6.5.jar"
side = "client"
version = "0.6.5"

[download]
hash-format = "sha256"
hash = "aaabbbcccddd"
`

func baselineFromSnapshots(t *testing.T, relationID string, snapP, snapR model.ObservedSnapshot) model.SyncBaseline {
	t.Helper()
	b := model.SyncBaseline{
		SchemaVersion:        model.CurrentSchemaVersion,
		BaselineID:           "base_test_" + relationID,
		RelationID:           relationID,
		CreatedAt:            snapP.CapturedAt,
		NormalizationVersion: snapP.NormalizationVersion,
		Resources:            make(map[model.ResourceID]model.BaselineResource),
	}
	for id, obs := range snapP.Resources {
		rep := obs.Representation
		b.Resources[id] = model.BaselineResource{State: "present", ProjectRepresentation: &rep}
	}
	for id, obs := range snapR.Resources {
		rep := obs.Representation
		res, ok := b.Resources[id]
		if !ok {
			res = model.BaselineResource{State: "present"}
		}
		res.RuntimeRepresentation = &rep
		b.Resources[id] = res
	}
	digest, err := normalize.BaselineDigest(b)
	if err != nil {
		t.Fatalf("BaselineDigest: %v", err)
	}
	b.BaselineDigest = digest
	return b
}

// mustGetChanges 调 GetChanges 并断言 items 按 resource_id 字节序、slice 归一。
func mustGetChanges(t *testing.T, app syncapp.Application, input view.GetChangesInput) view.ChangesPage {
	t.Helper()
	page, err := app.GetChanges(context.Background(), input)
	if err != nil {
		t.Fatalf("GetChanges: %v", err)
	}
	if page.Items == nil {
		t.Fatal("items 应归一为空切片而非 nil")
	}
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i-1].ResourceID >= page.Items[i].ResourceID {
			t.Fatalf("items 未按 resource_id 字节序: %q >= %q", page.Items[i-1].ResourceID, page.Items[i].ResourceID)
		}
	}
	for _, it := range page.Items {
		if it.Conflicts == nil || it.Diagnostics == nil {
			t.Fatalf("conflicts/diagnostics 应归一空切片: %+v", it)
		}
	}
	return page
}

// findChange 按 resource_id 取行。
func findChange(page view.ChangesPage, resourceID string) (view.ChangeView, bool) {
	for _, it := range page.Items {
		if it.ResourceID == resourceID {
			return it, true
		}
	}
	return view.ChangeView{}, false
}

// TestHeadlessGetChangesInitialization 无基线初始化 Diff：adopt_equal 与
// init_choice 可见、init_choice 携带冲突证据、runtime_local 诊断挂到对应资源行。
func TestHeadlessGetChangesInitialization(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	// 未扫描：缺省取最新快照 → 两侧均无 → snapshot_not_found（args {0}=side）
	if _, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID}); errCode(t, err) != "err.sync.snapshot_not_found" {
		t.Fatalf("未扫描应 snapshot_not_found，得到 %v", err)
	}
	// 未知关系 → err.relation.not_found
	if _, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: "rel_missing"}); errCode(t, err) != "err.relation.not_found" {
		t.Fatalf("未知关系应 relation.not_found，得到 %v", err)
	}

	scanAndWait(t, app, rel.RelationID)
	page := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID})

	// 初始化 Diff：sodium 双端等值 → adopt_equal；jei（语义不同）与单侧资源 → init_choice
	if page.Summary.Total != 4 || page.Summary.AdoptEqualCount != 1 || page.Summary.InitChoiceCount != 3 {
		t.Fatalf("init summary 不符: %+v", page.Summary)
	}
	if page.Summary.CreateCount != 0 || page.Summary.ModifyCount != 0 || page.Summary.DeleteCount != 0 || page.Summary.ConflictCount != 0 {
		t.Fatalf("init 场景不应有 create/modify/delete/conflict 计数: %+v", page.Summary)
	}
	if page.NextCursor != "" {
		t.Fatalf("默认分页应单页取全，next_cursor=%q", page.NextCursor)
	}

	// adopt_equal 可见且无冲突
	sodium, ok := findChange(page, "mod:modrinth:AANobbMI")
	if !ok || sodium.Classification != "adopt_equal" {
		t.Fatalf("sodium 应为 adopt_equal: %+v", sodium)
	}
	if len(sodium.Conflicts) != 0 {
		t.Fatalf("adopt_equal 不应携带冲突: %+v", sodium.Conflicts)
	}
	if sodium.Project == nil || sodium.Runtime == nil || sodium.Base != nil {
		t.Fatalf("adopt_equal 行应双侧表示齐全且无基线: %+v", sodium)
	}

	// init_choice 携带 initialize_choice 冲突证据（表示三态可见 → 初始化方向预览）
	jei, ok := findChange(page, "mod:curseforge:228525")
	if !ok || jei.Classification != "init_choice" {
		t.Fatalf("jei 应为 init_choice: %+v", jei)
	}
	if len(jei.Conflicts) != 1 || jei.Conflicts[0].Kind != model.ConflictInitialize {
		t.Fatalf("init_choice 应带 initialize_choice 冲突: %+v", jei.Conflicts)
	}
	if jei.Conflicts[0].Project == nil || jei.Conflicts[0].Runtime == nil {
		t.Fatalf("初始化冲突证据应含双侧表示: %+v", jei.Conflicts[0])
	}

	// runtime_local 诊断挂到 runtime-only 资源行（mod:jar:runtimeonly-1.0.jar）
	ro, ok := findChange(page, "mod:jar:runtimeonly-1.0.jar")
	if !ok || ro.Classification != "init_choice" {
		t.Fatalf("runtimeonly 应为 init_choice: %+v", ro)
	}
	diagFound := false
	for _, d := range ro.Diagnostics {
		if d.Code == "diag.scan.runtime_local" {
			diagFound = true
		}
	}
	if !diagFound {
		t.Fatalf("runtimeonly 行应携带 runtime_local 诊断: %+v", ro.Diagnostics)
	}
}

// TestHeadlessGetChangesFiltersAndPagination 筛选与分页：summary 不受筛选影响；
// 非法筛选值 → err.sync.invalid_filter；cursor 按字节序连续推进且不重复。
func TestHeadlessGetChangesFiltersAndPagination(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	all := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID})

	// classification 筛选：只留 adopt_equal，summary 仍为全量
	filtered := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID, Classification: "adopt_equal"})
	if len(filtered.Items) != 1 || filtered.Items[0].Classification != "adopt_equal" {
		t.Fatalf("adopt_equal 筛选应 1 行: %+v", filtered.Items)
	}
	if filtered.Summary != all.Summary {
		t.Fatalf("summary 不受筛选影响: filtered=%+v all=%+v", filtered.Summary, all.Summary)
	}

	// resource_kind 筛选：fixture 全部为 mod
	mods := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID, ResourceKind: "mod"})
	if len(mods.Items) != len(all.Items) {
		t.Fatalf("resource_kind=mod 应取全 mod 行: %d vs %d", len(mods.Items), len(all.Items))
	}
	textOnly := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID, ResourceKind: "text_file"})
	if len(textOnly.Items) != 0 {
		t.Fatalf("text_file 筛选应为空: %+v", textOnly.Items)
	}

	// path_prefix 筛选：mods/jei 前缀只命中 jei 行（project 侧 mods/jei.pw.toml）
	byPath := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID, PathPrefix: "mods/jei"})
	if len(byPath.Items) != 1 || !strings.HasPrefix(byPath.Items[0].RelativePath, "mods/jei") {
		t.Fatalf("mods/jei 前缀筛选应命中 1 行: %+v", byPath.Items)
	}

	// 非法筛选值 → err.sync.invalid_filter
	if _, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID, Classification: "bogus"}); errCode(t, err) != "err.sync.invalid_filter" {
		t.Fatalf("非法 classification 应 invalid_filter，得到 %v", err)
	}
	if _, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID, ResourceKind: "exe"}); errCode(t, err) != "err.sync.invalid_filter" {
		t.Fatalf("非法 resource_kind 应 invalid_filter，得到 %v", err)
	}

	// 分页：limit=1 逐页走完，cursor 严格推进且与全量集合一致
	seen := make(map[string]bool)
	cursor := ""
	pages := 0
	lastSummary := view.ChangesSummary{}
	for {
		page, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID, Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		lastSummary = page.Summary
		if len(page.Items) > 1 {
			t.Fatalf("limit=1 页面溢出: %d 行", len(page.Items))
		}
		for _, it := range page.Items {
			if seen[it.ResourceID] {
				t.Fatalf("分页重复资源 %s", it.ResourceID)
			}
			seen[it.ResourceID] = true
		}
		if page.NextCursor == "" {
			break
		}
		if len(page.Items) == 1 && page.Items[0].ResourceID != page.NextCursor {
			// cursor 取自本页最后一条（limit=1 时即唯一一条）
			t.Fatalf("cursor 应为页内最后一条: cursor=%q item=%q", page.NextCursor, page.Items[0].ResourceID)
		}
		cursor = page.NextCursor
		if pages > 100 {
			t.Fatal("分页未收敛")
		}
	}
	if pages != len(all.Items) {
		t.Fatalf("limit=1 应 %d 页，得到 %d", len(all.Items), pages)
	}
	if len(seen) != len(all.Items) || lastSummary.Total != all.Summary.Total {
		t.Fatalf("分页集合应与全量一致: seen=%d all=%d", len(seen), len(all.Items))
	}

	// 筛选 + 分页组合：init_choice 3 行 limit=2 → 两页
	choicePages := 0
	choiceSeen := 0
	cursor = ""
	for {
		page, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID, Classification: "init_choice", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		choicePages++
		choiceSeen += len(page.Items)
		if page.Summary.InitChoiceCount != 3 {
			t.Fatalf("分页 summary 应稳定: %+v", page.Summary)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if choicePages > 10 {
			t.Fatal("分页未收敛")
		}
	}
	if choicePages != 2 || choiceSeen != 3 {
		t.Fatalf("init_choice limit=2 应 2 页 3 行，得到 %d 页 %d 行", choicePages, choiceSeen)
	}
}

// TestHeadlessGetChangesBaselineThreeStates 有基线场景：基线与两侧一致 → 全部 noop
// （无操作资源可见，Base 表示齐备）；此后 project 单侧改 → modify、runtime 单侧删
// → delete、双端异改 → conflict。显式快照对跨侧/跨关系 → snapshot_pair_invalid。
func TestHeadlessGetChangesBaselineThreeStates(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	snapRepo := sqlite.NewSnapshotRepository(db)
	snapP, err := snapRepo.Get(ctx, ws.LatestProjectSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	snapR, err := snapRepo.Get(ctx, ws.LatestRuntimeSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}

	// 显式快照对：跨侧（project 位置传 runtime 快照）→ err.changes.snapshot_pair_invalid
	_, err = app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID, ProjectSnapshotID: snapR.SnapshotID})
	if errCode(t, err) != "err.changes.snapshot_pair_invalid" {
		t.Fatalf("跨侧快照对应 snapshot_pair_invalid，得到 %v", err)
	}
	// 跨关系（另一个真实关系的快照）→ 同码，不泄漏快照存在性。
	// 第二个 fixture 实例目录需改名（adapter identity 按实例目录名幂等，同名拒绝登记）
	otherProject, otherInstance, _ := makeFixtures(t)
	altInstance := filepath.Join(filepath.Dir(otherInstance), "CollapseAlt")
	if err := os.Rename(otherInstance, altInstance); err != nil {
		t.Fatal(err)
	}
	otherRel := mustPrepareAndCreate(t, app, otherProject, altInstance)
	scanAndWait(t, app, otherRel.RelationID)
	otherWs, err := app.GetWorkspace(ctx, otherRel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID, ProjectSnapshotID: otherWs.LatestProjectSnapshot.SnapshotID})
	if errCode(t, err) != "err.changes.snapshot_pair_invalid" {
		t.Fatalf("跨关系快照对应 snapshot_pair_invalid，得到 %v", err)
	}
	// 显式传入正确的相对两侧 → 与缺省（两侧最新）同结果
	explicit, err := app.GetChanges(ctx, view.GetChangesInput{
		RelationID:        rel.RelationID,
		ProjectSnapshotID: snapP.SnapshotID,
		RuntimeSnapshotID: snapR.SnapshotID,
	})
	if err != nil {
		t.Fatal(err)
	}
	deflt := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID})
	if explicit.Summary != deflt.Summary {
		t.Fatalf("显式快照对应与缺省同结果: %+v vs %+v", explicit.Summary, deflt.Summary)
	}

	// 挂基线（P1 无 Apply，测试直写 head_baseline_id）→ 基线与两侧一致 → 全 noop
	b := baselineFromSnapshots(t, rel.RelationID, snapP, snapR)
	if err := sqlite.NewBaselineRepository(db).Insert(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE relations SET head_baseline_id=? WHERE id=?`, b.BaselineID, rel.RelationID); err != nil {
		t.Fatal(err)
	}
	noopPage := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID})
	if noopPage.Summary.Total != 4 || noopPage.Summary.NoopCount != 4 {
		t.Fatalf("基线一致应全 noop（无操作资源可见）: %+v", noopPage.Summary)
	}
	for _, it := range noopPage.Items {
		if it.Classification != "noop" || it.Base == nil {
			t.Fatalf("noop 行应带基线表示: %+v", it)
		}
	}

	// 三态构造：jei project 侧改版本 → modify；删 runtimeonly jar → delete；
	// sodium 双端异改（0.6.6 vs 0.6.5）→ conflict；local 不动 → noop
	writeFile(t, filepath.Join(projectRoot, "mods", "jei.pw.toml"), fxJEIVersionBumped)
	writeFile(t, filepath.Join(projectRoot, "mods", "sodium.pw.toml"), fxSodiumVersionBumped)
	writeFile(t, filepath.Join(instanceDir, "minecraft", "mods", ".index", "sodium-0.6.5.jar.pw.toml"), fxIndexSodiumVersionBumped)
	if err := os.Remove(filepath.Join(instanceDir, "minecraft", "mods", "runtimeonly-1.0.jar")); err != nil {
		t.Fatal(err)
	}
	scanAndWait(t, app, rel.RelationID)

	page := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID})
	s := page.Summary
	if s.Total != 4 || s.CreateCount != 0 || s.ModifyCount != 1 || s.DeleteCount != 1 || s.ConflictCount != 1 || s.NoopCount != 1 {
		t.Fatalf("三态 summary 不符: %+v", s)
	}
	if jei, ok := findChange(page, "mod:curseforge:228525"); !ok || jei.Classification != "project_to_runtime" {
		t.Fatalf("jei 应为 project_to_runtime: %+v", jei)
	}
	if ro, ok := findChange(page, "mod:jar:runtimeonly-1.0.jar"); !ok || ro.Classification != "remove_project_candidate" {
		t.Fatalf("runtimeonly 应为 remove_project_candidate: %+v", ro)
	} else if ro.Runtime != nil || ro.Project != nil || ro.Base == nil {
		t.Fatalf("删除候选行双侧应缺席且带基线证据: %+v", ro)
	}
	if sodium, ok := findChange(page, "mod:modrinth:AANobbMI"); !ok || sodium.Classification != "conflict_modify" {
		t.Fatalf("sodium 应为 conflict_modify: %+v", sodium)
	} else if len(sodium.Conflicts) != 1 || sodium.Conflicts[0].Kind != model.ConflictModifyModify {
		t.Fatalf("conflict_modify 应带 modify_modify 冲突证据: %+v", sodium.Conflicts)
	} else if sodium.Conflicts[0].Base == nil {
		t.Fatalf("modify 冲突证据应含基线表示: %+v", sodium.Conflicts[0])
	}
	if local, ok := findChange(page, "mod:path:mods/local.pw.toml"); !ok || local.Classification != "noop" {
		t.Fatalf("local 应为 noop: %+v", local)
	}

	// 筛选三态行：classification=conflict_modify 只留冲突行，summary 仍全量
	conflictsOnly := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID, Classification: "conflict_modify"})
	if len(conflictsOnly.Items) != 1 || conflictsOnly.Summary.ConflictCount != 1 || conflictsOnly.Summary.Total != 4 {
		t.Fatalf("conflict 筛选不符: %d 行 summary=%+v", len(conflictsOnly.Items), conflictsOnly.Summary)
	}
}
