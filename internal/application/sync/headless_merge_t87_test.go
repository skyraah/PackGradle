package sync_test

// P4 合并判定面集成（票 #87）：双侧同改 → PrepareSync → GetPlan 断言分类与
// detail 块证据（ADR-0009 §2/§3/§4/§5；契约 07 §3.3；验收规格 §3.1）。
// 操作面自票 #93 起接入：merged_clean 行以 write_merged 操作承载默认推荐
//（双端前置条件、reversible），summary.merged_clean_count 独立计数。
//
// 覆盖：
//  1. 干净合并：同一 toml 两侧互不重叠改动 → merged_clean（summary 计数、
//     write_merged 操作面、零冲突、GetChanges 行分类与计数）；
//  2. 真冲突：两侧同段不同改动 → conflict_modify + Conflict.Detail 承载
//     hunk JSON 块证据（域词汇 project/base/runtime + 起始行号）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/store"
	"packgradle/internal/store/objectstore"
	"packgradle/internal/store/sqlite"
)

// t87Handmade 是含手工注释、键序、空行与缩进的 toml 样本（ADR-0009 §2
// 字节级保真口径的 fixture），两个互不重叠锚点段供双改注入。
const t87Handmade = `# 手工注释：测试样本头部。
# 第二行注释。

[graphics]
fancy_graphics = false
  render_distance = 12   # 行内注释 + 缩进键序


[audio]
master_volume = 0.8

[project_anchor]
project_marker = "untouched"

[runtime_anchor]
runtime_marker = "untouched"
`

// t87WriteRuntimeSide 把样本写到运行端（project 侧由初次同步的 write_project
// 拷贝落盘）：这保证基线内容字节进 CAS（adopt_equal 路径不产生写操作、
// 不入 CAS，合并判定的 Base 侧会取不到对象），与验收链「初次同步 → 双改」
// 的时序一致（验收规格 §3.2 场景 1）。
func t87WriteRuntimeSide(t *testing.T, instanceDir, content string) {
	t.Helper()
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "handmade.toml"), content)
}

// t87ReplaceIn 把文件中 from 的首次出现替换为 to（双侧注入改动用）。
func t87ReplaceIn(t *testing.T, path, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), from, to, 1)
	if edited == string(raw) {
		t.Fatalf("注入改动未命中: %s", path)
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
}

// t87EnableConfigAndInitialSync 启用 config 受管规则并完成初始化同步（基线）：
// 三冲突全 skip（allow_partial 缺省）→ 初始化计划零操作、apply 空跑收口建
// head baseline（handmade.toml 双侧同指纹进基线）。本票不关心 mod 取向——
// jei 是 download 行、runtimeonly 无项目侧字节，取侧会把执行链拖进
// download/copy 语义，与合并判定面无关。
func t87EnableConfigAndInitialSync(t *testing.T, app syncapp.Application, rel *view.RelationView) {
	t.Helper()
	ctx := context.Background()
	pol, err := app.GetMappingPolicy(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	rules := append([]model.MappingRule{}, pol.Rules...)
	rules = append(rules, model.MappingRule{
		ID: "config", ResourceKind: "text_file",
		ProjectPrefix: "config", RuntimePrefix: "config",
		Direction: "bidirectional", Materialization: "copy",
		MergePolicy: "manual", RuntimeLocalPolicy: "exclude",
	})
	pv, err := app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: rel.RelationID, ExpectedRevision: pol.RelationRevision, Rules: rules,
	})
	if err != nil {
		t.Fatal(err)
	}
	rel.Revision = pv.RelationRevision

	scanAndWait(t, app, rel.RelationID)

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatalf("PrepareSync(初始化): %v", err)
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: []model.Resolution{
		{ResourceID: "mod:curseforge:228525", Choice: model.ChoiceSkip},
		{ResourceID: "mod:path:mods/local.pw.toml", Choice: model.ChoiceSkip},
		{ResourceID: "mod:jar:runtimeonly-1.0.jar", Choice: model.ChoiceSkip},
		// handmade.toml 从运行侧初始化（write_project 拷贝）：字节经暂存入 CAS，
		// 基线 Content 指向的对象存在——合并判定 Base 侧可取。
		{ResourceID: "file:config/handmade.toml", Choice: model.ChoiceInitializeFromRuntime},
	}})
	if err != nil {
		t.Fatalf("ResolvePlan(初始化): %v", err)
	}
	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan(初始化): %v", err)
	}
	waitTask(t, app, tv.TaskID)
}

// t87PrepareSync 在双端最新快照上产出新 draft。
func t87PrepareSync(t *testing.T, app syncapp.Application, rel view.RelationView) view.SyncPlanView {
	t.Helper()
	ctx := context.Background()
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	return plan
}

// t87SeedCAS 把基线内容字节预置进 CAS（合并判定 Base 侧按基线表示的内容
// 摘要从 CAS 取字节）。生产路径由「提交收口期内容摄取」承担（ADR-0012 §8
// 对 mod metafile 的裁定语义；非 mod 文本的摄取缺口在票 #87 残留注记）——
// 测试以预置对象模拟「内容已保全」的存量形态，聚焦判定与投影链路本身。
func t87SeedCAS(t *testing.T, dataRoot, content string) {
	t.Helper()
	layout, err := store.EnsureLayout(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(layout.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cas, err := objectstore.Open(layout.ObjectsDir, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cas.Put(context.Background(), strings.NewReader(content)); err != nil {
		t.Fatalf("预置 CAS 对象: %v", err)
	}
}

// TestMergePlanMergedClean 干净合并链：双侧互不重叠改动 → merged_clean 计数
// 进计划摘要与变更浏览，零冲突零操作（不接执行面的计划面呈现）。
func TestMergePlanMergedClean(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	t87WriteRuntimeSide(t, instanceDir, t87Handmade)
	app, _ := newStack(t, dataRoot)
	t87SeedCAS(t, dataRoot, t87Handmade)

	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	t87EnableConfigAndInitialSync(t, app, &rel)

	projFile := filepath.Join(projectRoot, "config", "handmade.toml")
	rtFile := filepath.Join(instanceDir, "minecraft", "config", "handmade.toml")
	t87ReplaceIn(t, projFile, `project_marker = "untouched"`, `project_marker = "edited-by-project"`)
	t87ReplaceIn(t, rtFile, `runtime_marker = "untouched"`, `runtime_marker = "edited-by-runtime"`)
	scanAndWait(t, app, rel.RelationID)

	plan := t87PrepareSync(t, app, rel)
	if plan.Kind != string(model.PlanSync) {
		t.Fatalf("plan kind = %s", plan.Kind)
	}
	if plan.Summary.MergedCleanCount != 1 {
		t.Fatalf("merged_clean_count = %d，期望 1（summary=%+v）", plan.Summary.MergedCleanCount, plan.Summary)
	}
	if plan.Summary.ConflictCount != 0 || len(plan.Conflicts) != 0 {
		t.Fatalf("干净合并不得产生冲突: summary=%+v conflicts=%+v", plan.Summary, plan.Conflicts)
	}
	if plan.Summary.ModifyCount != 0 {
		t.Fatalf("merged_clean 不并入 modify 计数: %+v", plan.Summary)
	}
	// 操作面（票 #93 接入后）：draft 以 write_merged 承载默认推荐——一资源一
	// 操作、reversible=true、双端前置条件；不并入 create/modify/delete 计数。
	if len(plan.Operations) != 1 {
		t.Fatalf("merged_clean 行应恰有 1 条操作: %+v", plan.Operations)
	}
	if plan.Operations[0].Kind != "write_merged" || !plan.Operations[0].Reversible {
		t.Fatalf("merged_clean 操作应为 reversible write_merged: %+v", plan.Operations[0])
	}
	if len(plan.Operations[0].Preconditions) != 2 {
		t.Fatalf("write_merged 应断言双端前置条件: %+v", plan.Operations[0].Preconditions)
	}
	if plan.Summary.CreateCount != 0 || plan.Summary.DeleteCount != 0 {
		t.Fatalf("merged_clean 不并入 create/delete 计数: %+v", plan.Summary)
	}

	// GetPlan：计划持久化后摘要证据可读
	got, err := app.GetPlan(context.Background(), plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.MergedCleanCount != 1 || got.Summary.ConflictCount != 0 {
		t.Fatalf("GetPlan 摘要不一致: %+v", got.Summary)
	}

	// GetChanges：merged_clean 行分类 + 全量计数 + 筛选枚举可用
	page, err := app.GetChanges(context.Background(), view.GetChangesInput{RelationID: rel.RelationID, Classification: "merged_clean"})
	if err != nil {
		t.Fatalf("GetChanges(merged_clean 筛选): %v", err)
	}
	if page.Summary.MergedCleanCount != 1 || len(page.Items) != 1 || page.Items[0].Classification != "merged_clean" {
		t.Fatalf("变更浏览 merged_clean 行不符: summary=%+v items=%+v", page.Summary, page.Items)
	}
	if n := len(page.Items[0].Conflicts); n != 0 {
		t.Fatalf("merged_clean 行不应有冲突证据: %d", n)
	}
}

// TestMergePlanConflictHunkEvidence 真冲突链：双侧同段不同改动 →
// conflict_modify + Detail 承载定形 hunk JSON（域词汇 + 起始行号）。
func TestMergePlanConflictHunkEvidence(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	t87WriteRuntimeSide(t, instanceDir, t87Handmade)
	app, _ := newStack(t, dataRoot)
	t87SeedCAS(t, dataRoot, t87Handmade)

	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	t87EnableConfigAndInitialSync(t, app, &rel)

	projFile := filepath.Join(projectRoot, "config", "handmade.toml")
	rtFile := filepath.Join(instanceDir, "minecraft", "config", "handmade.toml")
	t87ReplaceIn(t, projFile, `project_marker = "untouched"`, `project_marker = "project-side"`)
	t87ReplaceIn(t, rtFile, `project_marker = "untouched"`, `project_marker = "runtime-side"`)
	scanAndWait(t, app, rel.RelationID)

	plan := t87PrepareSync(t, app, rel)
	if plan.Summary.MergedCleanCount != 0 {
		t.Fatalf("真冲突不得计 merged_clean: %+v", plan.Summary)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Kind != model.ConflictModifyModify {
		t.Fatalf("期望 1 条 modify_modify 冲突: %+v", plan.Conflicts)
	}
	assertT87Hunks(t, plan.Conflicts[0].Detail, "project_marker")

	got, err := app.GetPlan(context.Background(), plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != 1 {
		t.Fatalf("GetPlan 冲突数 = %d", len(got.Conflicts))
	}
	assertT87Hunks(t, got.Conflicts[0].Detail, "project_marker")

	// 变更浏览同口径：conflict_modify 行携带同一 detail 证据。
	page, err := app.GetChanges(context.Background(), view.GetChangesInput{RelationID: rel.RelationID})
	if err != nil {
		t.Fatal(err)
	}
	var conflictRow *view.ChangeView
	for i := range page.Items {
		if page.Items[i].Classification == "conflict_modify" {
			conflictRow = &page.Items[i]
		}
	}
	if conflictRow == nil || len(conflictRow.Conflicts) != 1 {
		t.Fatalf("变更浏览缺 conflict_modify 行: %+v", page.Items)
	}
	assertT87Hunks(t, conflictRow.Conflicts[0].Detail, "project_marker")
}

// assertT87Hunks 解析 Conflict.Detail 并断言 hunk JSON 定形：
// {"hunks":[{"project":{"start",lines},"base":{...},"runtime":{...}}]}，
// 三侧证据齐备、起始行号 1 起始、且三侧行片段围绕标记行。
func assertT87Hunks(t *testing.T, detail, markerSubstr string) {
	t.Helper()
	if detail == "" {
		t.Fatal("冲突 Detail 为空（应承载 hunk JSON）")
	}
	var top struct {
		Hunks []map[string]struct {
			Start int      `json:"start"`
			Lines []string `json:"lines"`
		} `json:"hunks"`
	}
	if err := json.Unmarshal([]byte(detail), &top); err != nil {
		t.Fatalf("Detail 非定形 hunk JSON: %v\n%s", err, detail)
	}
	if len(top.Hunks) == 0 {
		t.Fatalf("hunks 为空: %s", detail)
	}
	found := map[string]bool{}
	for _, side := range []string{"project", "base", "runtime"} {
		hs := top.Hunks[0][side]
		if hs.Start < 1 || len(hs.Lines) == 0 {
			t.Fatalf("%s 侧证据不符: %+v", side, hs)
		}
		for _, l := range hs.Lines {
			if strings.Contains(l, markerSubstr) {
				found[side] = true
			}
		}
	}
	if !found["project"] || !found["runtime"] || !found["base"] {
		t.Fatalf("三侧证据应都包含标记行: %+v（detail=%s）", found, detail)
	}
}
