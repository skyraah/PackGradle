package sync_test

// 票 #100 忽略/手动处理决议链 headless 集成（ADR-0013）：
//   - 忽略链端到端：初始化计划 ChoiceSkip → committed → 单文件 ignore 规则落库
//     （revision +1，坑 A）→ 提交详情 ignored 分列（坑 D）→ 四面安静
//     （changes / diff_state / QuickUpdate no_diff）→ 再改该文件仍安静 →
//     下一次 apply 不被忽略资源炸场（verifyRescan 策略盲修正）；
//   - 恢复链：受管范围页把规则方向改回 → 差异恢复、从既有基线续算（不重初始化）；
//   - 手动处理链：ChoiceManual → partial + 基线吸收、策略零写入 → 单侧再变
//     以普通变更回到计划；
//   - 忽略决议未走到提交不留规则（验收 4）；忽略跨重绑存活（验收 9）。
//
// 复用 T37 的纯文本 config/ 夹具与链路助手（b.toml 是运行实例侧独有文件 =
// 忽略目标；t36/t37/t12 助手同包可用）。

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

const (
	t100IgnoreTarget   = "file:config/b.toml" // makeApplyFixtures 中仅运行实例侧存在
	t100IgnorePath     = "config/b.toml"
	t100RuntimeBEditV1 = "b = \"runtime edited once\"\n"
	t100RuntimeBEditV2 = "b = \"runtime edited twice\"\n"
)

// t100IgnorePolicyRule 返回策略中前缀两侧等于忽略目标路径的 ignore 规则（有界
// 断言用：不断言整个规则集，只找目标规则）。
func t100IgnorePolicyRule(t *testing.T, app syncapp.Application, relationID string) *model.MappingRule {
	t.Helper()
	pol, err := app.GetMappingPolicy(context.Background(), relationID)
	if err != nil {
		t.Fatalf("GetMappingPolicy: %v", err)
	}
	for i := range pol.Rules {
		r := pol.Rules[i]
		if r.Direction == "ignore" && r.ProjectPrefix == t100IgnorePath && r.RuntimePrefix == t100IgnorePath {
			return &pol.Rules[i]
		}
	}
	return nil
}

// t100IgnoreDirectionRuleCount 统计当前策略中 direction=ignore 的规则数。
func t100IgnoreDirectionRuleCount(t *testing.T, app syncapp.Application, relationID string) int {
	t.Helper()
	pol, err := app.GetMappingPolicy(context.Background(), relationID)
	if err != nil {
		t.Fatalf("GetMappingPolicy: %v", err)
	}
	n := 0
	for _, r := range pol.Rules {
		if r.Direction == "ignore" {
			n++
		}
	}
	return n
}

// t100ChangesRow 在变更浏览中查找指定资源的行（不存在返回 false）。
func t100ChangesRow(t *testing.T, app syncapp.Application, relationID, resourceID string) (view.ChangeView, bool) {
	t.Helper()
	page, err := app.GetChanges(context.Background(), view.GetChangesInput{RelationID: relationID})
	if err != nil {
		t.Fatalf("GetChanges: %v", err)
	}
	for _, it := range page.Items {
		if it.ResourceID == resourceID {
			return it, true
		}
	}
	return view.ChangeView{}, false
}

// t100AssertQuiet 断言忽略资源四面安静（票 #100 验收 1）：changes 无该资源行、
// diff_state 非 dirty/conflicted、QuickUpdate 短路 no_diff。
func t100AssertQuiet(t *testing.T, app syncapp.Application, relationID string) {
	t.Helper()
	ctx := context.Background()
	if _, found := t100ChangesRow(t, app, relationID, t100IgnoreTarget); found {
		t.Fatal("changes 页仍显示已忽略资源")
	}
	ws := mustWorkspace(t, app, relationID)
	switch ws.State.DiffState {
	case "dirty", "conflicted":
		t.Fatalf("diff_state = %s，已忽略资源不应计入", ws.State.DiffState)
	}
	res, err := app.QuickUpdate(ctx, view.QuickUpdateInput{RelationID: relationID})
	if err != nil {
		t.Fatalf("QuickUpdate: %v", err)
	}
	if res.Outcome != syncapp.QuickUpdateNoDiff {
		t.Fatalf("QuickUpdate outcome = %s，期望 no_diff（忽略资源不算 actionable）", res.Outcome)
	}
}

// t100InitialSyncWithChoice 完成初始化同步：a/c 从项目侧初始化，b 按 choice
// 决议（ChoiceSkip=忽略 / ChoiceManual=手动处理）。返回 apply 任务终态。
func t100InitialSyncWithChoice(t *testing.T, app syncapp.Application, rel view.RelationView,
	choice model.ResolutionChoice) view.TaskView {

	t.Helper()
	choices := map[model.ResourceID]model.ResolutionChoice{
		"file:config/a.toml": model.ChoiceInitializeFromProject,
		t100IgnoreTarget:     choice,
		"file:config/c.toml": model.ChoiceInitializeFromProject,
	}
	resolved := mustResolveApplyPlan(t, app, rel, choices)
	tv := mustConfirm(t, app, resolved.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		t.Fatalf("初始化提交未成功: %s %+v", final.Status, final.Problem)
	}
	return final
}

// TestIgnoreResolutionSynthesizesSingleFileRule 忽略链端到端（票 #100 验收
// 1/4/5，坑 A/D）：决议只是草稿意图（未提交无规则）→ 提交事务合成单文件
// ignore 规则、relation revision +1、其它 draft 投影 stale → 提交详情 ignored
// 分列 → 四面安静 → 再改该文件仍安静。
func TestIgnoreResolutionSynthesizesSingleFileRule(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir) // 建议规则并入初始 policy，revision=1

	// 未提交的 resolved 计划（b 选择忽略）+ 一张未决议的 draft D2（验 stale 投影）
	choices := map[model.ResourceID]model.ResolutionChoice{
		"file:config/a.toml": model.ChoiceInitializeFromProject,
		t100IgnoreTarget:     model.ChoiceSkip,
		"file:config/c.toml": model.ChoiceInitializeFromProject,
	}
	resolved := mustResolveApplyPlan(t, app, rel, choices)
	ws := mustWorkspace(t, app, rel.RelationID)
	d2, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatalf("PrepareSync(D2): %v", err)
	}

	// 验收 4（前半）：决议未走到提交，不留任何规则
	if n := t100IgnoreDirectionRuleCount(t, app, rel.RelationID); n != 0 {
		t.Fatalf("提交前策略已有 %d 条 ignore 规则，决议只是草稿意图", n)
	}

	// 提交：partial（skip 资源计入剩余差异，ADR-0013 §1）
	tv := mustConfirm(t, app, resolved.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		t.Fatalf("初始化提交未成功: %s %+v", final.Status, final.Problem)
	}
	if final.Outcome != model.TaskOutcomePartial {
		t.Fatalf("含忽略决议的提交 outcome = %s，期望 partial", final.Outcome)
	}

	// 验收 1：单文件 ignore 规则落库；验收 5：revision +1（1→2，坑 A）
	rule := t100IgnorePolicyRule(t, app, rel.RelationID)
	if rule == nil {
		t.Fatalf("受管范围未出现 %s 的 ignore 规则", t100IgnorePath)
	}
	if rule.ResourceKind != "text_file" {
		t.Fatalf("合成规则类别 = %s，期望照抄观察类别 text_file", rule.ResourceKind)
	}
	if n := t100IgnoreDirectionRuleCount(t, app, rel.RelationID); n != 1 {
		t.Fatalf("ignore 规则数 = %d，期望恰 1（mod 的 skip 决议不合成）", n)
	}
	if got := mustWorkspace(t, app, rel.RelationID).State.RelationRevision; got != 2 {
		t.Fatalf("提交后 relation revision = %d，期望 2（SavePolicy 联动 +1）", got)
	}
	stale, err := app.GetPlan(ctx, d2.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != "stale" {
		t.Fatalf("revision 前进后其它 draft 应投影 stale，got %s", stale.Status)
	}

	// 坑 D：提交详情「已忽略」分列（与 skipped 的物化取数剔除项无关）
	commit, err := app.GetCommit(ctx, rel.RelationID, final.CommitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.Ignored) != 1 || commit.Ignored[0].ResourceID != t100IgnoreTarget {
		t.Fatalf("提交详情 ignored 分列不符: %+v", commit.Ignored)
	}
	if len(commit.Manual) != 0 {
		t.Fatalf("手动处理分列应为空: %+v", commit.Manual)
	}

	// 验收 1（续）：changes/diff_state/快速更新均安静
	t100AssertQuiet(t, app, rel.RelationID)

	// 此后再改该文件仍安静（忽略 = 持久移出受管范围）
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "b.toml"), t100RuntimeBEditV1)
	mustScanAndWait(t, app, rel.RelationID)
	t100AssertQuiet(t, app, rel.RelationID)
}

// TestApplySucceedsWithIgnoredDivergingResource 策略盲修正的关键回归（ADR-0013
// §3 第 1 处）：忽略资源提交后被改动（与基线分歧）时，下一次 apply 的
// verifyRescan 不得以 unselected violation 炸场。
func TestApplySucceedsWithIgnoredDivergingResource(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	t100InitialSyncWithChoice(t, app, rel, model.ChoiceSkip)

	// 忽略资源被外部改动（分歧）+ 正常资源单侧修改（本轮要同步的真实变更）
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "b.toml"), t100RuntimeBEditV1)
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "a.toml"), "a = \"runtime edit\"\n")
	mustScanAndWait(t, app, rel.RelationID)

	// 忽略资源不进计划（计划面既有剔除），正常变更进计划
	plan := mustResolveApplyPlan(t, app, rel, nil)
	if len(plan.Operations) != 1 || plan.Operations[0].ResourceID != "file:config/a.toml" {
		t.Fatalf("计划应只含 a.toml 的普通写操作（忽略资源剔除）: %+v", plan.Operations)
	}
	tv := mustConfirm(t, app, plan.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		t.Fatalf("忽略资源分歧时下一次 apply 被炸场: %s %+v", final.Status, final.Problem)
	}
	if final.Outcome != model.TaskOutcomeExact {
		t.Fatalf("已移出受管范围的资源不应计入剩余差异，outcome = %s", final.Outcome)
	}
}

// TestIgnoreRuleDirectionRestoreResumesFromBaseline 验收 2：受管范围页把规则
// 方向改回 → 差异恢复出现，从既有基线续算（不重初始化）。
func TestIgnoreRuleDirectionRestoreResumesFromBaseline(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	t100InitialSyncWithChoice(t, app, rel, model.ChoiceSkip)

	// 「受管范围页改回方向」：翻转该规则 direction（编辑既有规则已够，票 #100）
	pol, err := app.GetMappingPolicy(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	rules := append([]model.MappingRule{}, pol.Rules...)
	restored := false
	for i := range rules {
		if rules[i].ProjectPrefix == t100IgnorePath && rules[i].RuntimePrefix == t100IgnorePath {
			rules[i].Direction = "bidirectional"
			restored = true
		}
	}
	if !restored {
		t.Fatal("未找到待改回的 ignore 规则")
	}
	if _, err := app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: rel.RelationID, ExpectedRevision: pol.RelationRevision, Rules: rules,
	}); err != nil {
		t.Fatalf("UpdateMappingPolicy(改回方向): %v", err)
	}

	// 方向改回即恢复差异面：未改动时以 noop 行出现（基线仍记录该资源）
	mustScanAndWait(t, app, rel.RelationID)
	row, found := t100ChangesRow(t, app, rel.RelationID, t100IgnoreTarget)
	if !found {
		t.Fatal("方向改回后差异面未恢复该资源")
	}
	if row.Classification != "noop" {
		t.Fatalf("未改动的忽略资源恢复后分类 = %s，期望 noop（基线续算）", row.Classification)
	}

	// runtime 再改：普通变更回到计划（runtime_to_project → write_project，
	// 而非重新初始化）
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "b.toml"), t100RuntimeBEditV1)
	mustScanAndWait(t, app, rel.RelationID)
	row, found = t100ChangesRow(t, app, rel.RelationID, t100IgnoreTarget)
	if !found || row.Classification != "runtime_to_project" {
		t.Fatalf("改动后变更行不符: found=%v classification=%s", found, row.Classification)
	}
	ws := mustWorkspace(t, app, rel.RelationID)
	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	if plan.Kind != string(model.PlanSync) {
		t.Fatalf("应从既有基线续算（sync 计划），got kind=%s", plan.Kind)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Kind != "write_project" ||
		plan.Operations[0].ResourceID != t100IgnoreTarget {
		t.Fatalf("应生成单条 write_project 普通操作: %+v", plan.Operations)
	}
}

// TestManualResolutionAbsorbsIntoBaseline 验收 3：手动处理 → partial + 基线吸收、
// 策略零写入；此后单侧再变以普通变更回到计划。
func TestManualResolutionAbsorbsIntoBaseline(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	final := t100InitialSyncWithChoice(t, app, rel, model.ChoiceManual)
	if final.Outcome != model.TaskOutcomePartial {
		t.Fatalf("手动处理提交 outcome = %s，期望 partial", final.Outcome)
	}

	// 策略零写入（manual 不碰策略，坑 A 不触发）：无 ignore 规则、revision 不动
	if n := t100IgnoreDirectionRuleCount(t, app, rel.RelationID); n != 0 {
		t.Fatalf("manual 决议不应合成规则，发现 %d 条", n)
	}
	if got := mustWorkspace(t, app, rel.RelationID).State.RelationRevision; got != 1 {
		t.Fatalf("manual 决议后 revision = %d，期望不动（1）", got)
	}

	// 基线吸收：双端现状记入基线 → diff_state clean、changes 行 noop；
	// 提交详情 manual 分列（坑 D）
	commit, err := app.GetCommit(ctx, rel.RelationID, final.CommitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.Manual) != 1 || commit.Manual[0].ResourceID != t100IgnoreTarget {
		t.Fatalf("提交详情 manual 分列不符: %+v", commit.Manual)
	}
	if len(commit.Ignored) != 0 {
		t.Fatalf("忽略分列应为空: %+v", commit.Ignored)
	}
	if ws := mustWorkspace(t, app, rel.RelationID); ws.State.DiffState != "clean" {
		t.Fatalf("吸收后 diff_state = %s，期望 clean", ws.State.DiffState)
	}
	if row, found := t100ChangesRow(t, app, rel.RelationID, t100IgnoreTarget); !found || row.Classification != "noop" {
		t.Fatalf("吸收资源变更行不符: found=%v classification=%s", found, row.Classification)
	}

	// 此后单侧再变 → 普通写操作（不重新冲突、不重新初始化）
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "b.toml"), t100RuntimeBEditV2)
	mustScanAndWait(t, app, rel.RelationID)
	row, found := t100ChangesRow(t, app, rel.RelationID, t100IgnoreTarget)
	if !found || row.Classification != "runtime_to_project" {
		t.Fatalf("手动处理后单侧再变分类不符: found=%v classification=%s", found, row.Classification)
	}
	ws := mustWorkspace(t, app, rel.RelationID)
	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Kind != "write_project" {
		t.Fatalf("单侧再变应生成普通 write_project: %+v", plan.Operations)
	}
}

// TestIgnoreSurvivesRebind 验收 9：忽略语义跨重绑存活——重绑清基线不碰策略，
// 忽略规则保留、基线重走初始化、忽略资源不再回到计划。
func TestIgnoreSurvivesRebind(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	t100InitialSyncWithChoice(t, app, rel, model.ChoiceSkip)

	// 项目侧端点整体移动 → 重绑（t12 先例：原位更新、基线重置、修订号不动）
	movedRoot := projectRoot + "-moved"
	if err := os.Rename(projectRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	prep := prepareRebind(t, app, rel.RelationID, "project", movedRoot)
	got := mustApplyRebind(t, app, prep.PreparationID)
	if got.Revision != 2 {
		t.Fatalf("重绑后 revision = %d，期望不动（2，ADR-0002 决议 2）", got.Revision)
	}

	// 策略保留：ignore 规则跨重绑存活
	if t100IgnorePolicyRule(t, app, rel.RelationID) == nil {
		t.Fatal("重绑后 ignore 规则丢失（ADR-0003：重绑不触碰策略）")
	}

	// 基线重走初始化
	ctx := context.Background()
	ws := mustWorkspace(t, app, rel.RelationID)
	if ws.State.BaselineState != "none" || ws.State.DiffState != "initialization_required" {
		t.Fatalf("重绑后应重走初始化: baseline=%s diff=%s", ws.State.BaselineState, ws.State.DiffState)
	}

	// 重扫后新初始化计划：忽略资源仍被计划面剔除（不回到冲突决议）
	mustScanAndWait(t, app, rel.RelationID)
	ws = mustWorkspace(t, app, rel.RelationID)
	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	for _, c := range plan.Conflicts {
		if c.ResourceID == t100IgnoreTarget {
			t.Fatal("忽略语义跨重绑存活：被忽略资源不应重新进入初始化冲突")
		}
	}
}

// TestModIgnoreResolutionLeavesNoRule C3（双轴评审）：ignoreTargetOf 对合成
// 不可达目标静默 continue 是刻意的（实践上仅 mod 可达——编译器禁文件规则入
// mods/ 前缀，ADR-0013 §4）。契约：mod 资源选忽略 → 无规则落库、无错误、
// 提交成功，决议按普通 skip 语义吸收进基线。注意 mod 资源 ID 是 `mod:` 前缀
//（不是 `mods/` 路径）。夹具复用 t06/t87 的 packwiz 链（makeFixtures 含 mod
// 冲突）；提交链取零操作形态（三个 mod 冲突全选忽略——mod 跨侧拷贝无下载
// 直链时引擎不可执行，恰好证明 mod 忽略只走吸收语义）。
func TestModIgnoreResolutionLeavesNoRule(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	resolutions := make([]model.Resolution, 0, len(draft.Conflicts))
	for _, c := range draft.Conflicts {
		resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: model.ChoiceSkip})
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	final := waitTask(t, app, tv.TaskID) // 任一失败终态都会 Fatal → 「无错误」契约
	if final.Outcome != model.TaskOutcomePartial {
		t.Fatalf("mod 忽略决议提交 outcome = %s，期望 partial（skip 语义计入剩余差异）", final.Outcome)
	}

	// 无 ignore 规则落库、revision 不动（策略零写入）
	if n := t100IgnoreDirectionRuleCount(t, app, rel.RelationID); n != 0 {
		t.Fatalf("mod 忽略决议不应合成规则，发现 %d 条", n)
	}
	if got := mustWorkspace(t, app, rel.RelationID).State.RelationRevision; got != 1 {
		t.Fatalf("mod 忽略决议后 revision = %d，期望不动（1）", got)
	}
}

// TestGlobbedExactPrefixRuleFallsBackToPlainSkip C2（双轴评审）端到端：用户
// 自建「同前缀带 glob」规则（include 不命中目标 → 扫描口径下不治理该文件，
// direction=ignore 做成最尖锐形态）存在时，忽略决议不翻转该规则、不并列合成
// 新规则（等前缀并列触发 diag.mapping.collision）——该资源退回普通 skip 语义
// 留在差异面，策略零写入（revision 不动）。
func TestGlobbedExactPrefixRuleFallsBackToPlainSkip(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	// 注入用户规则：前缀恰等 b.toml 路径 + include glob（不命中 b.toml）
	pol, err := app.GetMappingPolicy(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	rules := append([]model.MappingRule{}, pol.Rules...)
	rules = append(rules, model.MappingRule{
		ID: "user-b-glob", ResourceKind: "text_file",
		ProjectPrefix: t100IgnorePath, RuntimePrefix: t100IgnorePath,
		Include: []string{"bak.toml"}, Direction: "ignore",
		Materialization: "copy", MergePolicy: "manual", RuntimeLocalPolicy: "exclude",
	})
	pv, err := app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: rel.RelationID, ExpectedRevision: pol.RelationRevision, Rules: rules,
	})
	if err != nil {
		t.Fatalf("UpdateMappingPolicy(注入带 glob 用户规则): %v", err)
	}
	rel.Revision = pv.RelationRevision

	mustScanAndWait(t, app, rel.RelationID)

	// C2 计划面口径的间接锁定：b.toml 冲突仍进初始化计划（若快路径误判
	// ignore，该冲突会被整体剔除、决议缺失）
	choices := map[model.ResourceID]model.ResolutionChoice{
		"file:config/a.toml": model.ChoiceInitializeFromProject,
		t100IgnoreTarget:     model.ChoiceSkip,
		"file:config/c.toml": model.ChoiceInitializeFromProject,
	}
	resolved := mustResolveApplyPlan(t, app, rel, choices)
	tv := mustConfirm(t, app, resolved.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		t.Fatalf("提交未成功: %s %+v", final.Status, final.Problem)
	}

	// 规则面零变化：无合成规则、用户规则原样（方向与 glob 未被触碰）
	after, err := app.GetMappingPolicy(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Rules) != len(rules) {
		t.Fatalf("提交后规则数 = %d，期望 %d（不翻转、不合成）", len(after.Rules), len(rules))
	}
	for _, r := range after.Rules {
		if r.ID == "user-b-glob" {
			if r.Direction != "ignore" || len(r.Include) != 1 || r.Include[0] != "bak.toml" {
				t.Fatalf("用户规则被触碰: direction=%s include=%v", r.Direction, r.Include)
			}
		}
	}
	// 策略零写入：revision 不动（坑 A 不触发，C1 同款语义）
	if got := mustWorkspace(t, app, rel.RelationID).State.RelationRevision; got != rel.Revision {
		t.Fatalf("提交后 revision = %d，期望不动（%d，零 SavePolicy）", got, rel.Revision)
	}

	// 资源留在差异面（普通 skip 语义吸收后 noop 行仍在）
	if row, found := t100ChangesRow(t, app, rel.RelationID, t100IgnoreTarget); !found || row.Classification != "noop" {
		t.Fatalf("带 glob 回退后资源应留在差异面: found=%v classification=%s", found, row.Classification)
	}
}

// TestExpiredPlanWithIgnoreResolutionRejected T1（双轴评审，验收 4 后半直接
// 用例）：含忽略决议的 resolved 计划过期后 ConfirmPlan 被拒——决议只是草稿
// 意图（ADR-0013 §0 Q11-a），不留任何规则落库、revision 不动。过期注入沿
// t94 先例：假时钟闭包越过计划 TTL（既有过期机制，ResolvePlan 时点重置 TTL）。
func TestExpiredPlanWithIgnoreResolutionRejected(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	now := time.Now()
	app, _ := newStack(t, dataRoot, func(d *syncapp.AppDeps) { d.Now = func() time.Time { return now } })
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	choices := map[model.ResourceID]model.ResolutionChoice{
		"file:config/a.toml": model.ChoiceInitializeFromProject,
		t100IgnoreTarget:     model.ChoiceSkip,
		"file:config/c.toml": model.ChoiceInitializeFromProject,
	}
	resolved := mustResolveApplyPlan(t, app, rel, choices)

	// 越过计划 TTL（planTTL=15m）→ ConfirmPlan 的过期门拒绝
	now = now.Add(16 * time.Minute)
	if _, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID}); errCode(t, err) != syncapp.CodePlanExpired {
		t.Fatalf("过期计划确认应被拒 err.plan.expired: %v", err)
	}

	// 决议蒸发：无 ignore 规则落库、revision 不动
	if n := t100IgnoreDirectionRuleCount(t, app, rel.RelationID); n != 0 {
		t.Fatalf("过期计划不得留下任何 ignore 规则，发现 %d 条", n)
	}
	if got := mustWorkspace(t, app, rel.RelationID).State.RelationRevision; got != 1 {
		t.Fatalf("过期拒绝后 revision = %d，期望不动（1）", got)
	}
	// 读取投影按过期呈现（契约 05 §5）
	got, err := app.GetPlan(ctx, resolved.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "expired" {
		t.Fatalf("过期计划投影 status = %s，期望 expired", got.Status)
	}
}
