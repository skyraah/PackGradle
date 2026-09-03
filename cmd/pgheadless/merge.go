package main

// pgheadless -merge（P4 票 #93；验收规格 §3.2 四场景；场景④票 #98 补齐）：
// 合并执行面 A 口径断言链。独立 fixture 与数据目录（Taskfile acceptance:merge
// 供数）：
//
//	场景① merged_clean 全链：初次同步 → 手工注释样本双侧互不重叠改动 +
//	   mod metafile 注入手工注释（语义不变、内容实测变）→ PrepareSync 断言
//	   merged_clean_count/write_merged 操作面（双端前置条件 + reversible）→
//	   take_merged 误用于非 merged 行断言 err.plan.resolution_invalid →
//	   resolve take_merged → confirm → committed → 断言双端落盘字节=确定性
//	   重算产物（merge.Texts 同算法复算）、未冲突区域字节级不变、产物入 CAS、
//	   mod metafile 新内容入 CAS（提交收口期摄取通道，票 #93 泛化面）。
//	场景② 回滚零网络：先回滚到初次提交（回到合并前字节），再回滚到场景①
//	   合并提交 → merged 行 restorable_from_cas → resolve exact → confirm →
//	   committed（本链零 CDN 配置，committed 即零网络零用户介入的证据）。
//	场景③ 授权模式口径：授权开态 → QuickUpdate 含 merged_clean 行随非冲突
//	   批量免确认直达 apply_started/committed → 落盘字节=重算产物；再构造
//	   同段真冲突 → QuickUpdate 永不自动、停 awaiting_confirmation（红线①）。
//	场景④ 预览与错误码（票 #94 GetMergedPreview；#93 链上断言移交 #98）：
//	   a) resolved 计划的 merged 行预览「所见即所写」——content=确认后落盘
//	      字节（同算法同输入）、base_content=基线全文；b) 对冲突行预览 →
//	      err.merge.not_mergeable（{0}=resource_id）；c) SQL 手术置过期的新
//	      merged draft 仍可预览（只读，零有效期校验）且 content=重算产物。
//
// -metrics 时记录 merge 分相（diff3/校验/写盘，LastApplyTiming 供数）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/core/merge"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
	"packgradle/internal/perffixture"
)

// mergePollTimeout 是链内 apply/restore 任务的轮询超时（小 fixture，定值足够）。
const mergePollTimeout = 2 * time.Minute

// mergeChainStats 是 -merge 链路的度量产物（metricsRecord 的 merge 段）。
type mergeChainStats struct {
	Kind string `json:"kind"`
	// Merge 分相（场景①合并提交的 LastApplyTiming；只记录不设门槛）。
	MergeDiff3MS    int64 `json:"merge_diff3_ms"`
	MergeValidateMS int64 `json:"merge_validate_ms"`
	MergeWriteMS    int64 `json:"merge_write_ms"`
	MergeOps        int   `json:"merge_ops"`
	ChainTotalMS    int64 `json:"chain_total_ms"`
}

// runMergeChain 执行 -merge 三场景断言链。rel 为已登记 Relation（首次扫描前
// 手工样本已双侧落位——pgfixture Generate 同款时序）。
func runMergeChain(ctx context.Context, stack *bootstrap.Stack, app syncapp.Application,
	rel view.RelationView, projectRoot, instanceDir string) (*mergeChainStats, error) {

	chainStart := time.Now()
	projFile := filepath.Join(projectRoot, filepath.FromSlash(perffixture.HandmadeTomlRel))
	rtFile := filepath.Join(instanceDir, "minecraft", filepath.FromSlash(perffixture.HandmadeTomlRel))

	fmt.Println("== -merge == 场景链开始（merged_clean 全链 / 回滚零网络 / 授权模式）")

	// ---- R0：启用 config 受管规则 + 初次同步（手工样本双侧同字节 → adopt_equal
	// 入基线；提交收口期摄取通道把项目侧内容字节送入 CAS——合并 Base 侧与
	// 回滚 restorable_from_cas 的实存前提）----
	if err := mrgEnableConfigRule(ctx, app, rel.RelationID); err != nil {
		return nil, fmt.Errorf("R0 config 规则: %w", err)
	}
	c0, err := mrgApplyRound(ctx, app, rel)
	if err != nil {
		return nil, fmt.Errorf("R0 初次同步: %w", err)
	}
	baseBytes, err := os.ReadFile(projFile)
	if err != nil {
		return nil, err
	}

	// ---- 场景① merged_clean 全链 ----
	rawProj, err := os.ReadFile(projFile)
	if err != nil {
		return nil, err
	}
	rawRt, err := os.ReadFile(rtFile)
	if err != nil {
		return nil, err
	}
	projBefore := mrgMustReplace(string(rawProj), `project_marker = "untouched"`, `project_marker = "edited-by-project"`)
	rtBefore := mrgMustReplace(string(rawRt), `runtime_marker = "untouched"`, `runtime_marker = "edited-by-runtime"`)
	if err := os.WriteFile(projFile, []byte(projBefore), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(rtFile, []byte(rtBefore), 0o644); err != nil {
		return nil, err
	}
	// mod metafile 手工注释（语义面不变、内容实测变）：合并提交收口期摄取
	// 通道覆盖 mod metafile 的证据行（ADR-0005 §7 文本例外，票 #93 泛化面）。
	metaFile, err := mrgFirstMetafile(projectRoot)
	if err != nil {
		return nil, err
	}
	metaEdited, err := mrgAppendMetafileComment(metaFile)
	if err != nil {
		return nil, err
	}
	metaDigest := sha256Hex(metaEdited)

	// 双改后先扫描（察觉上游变更），再在双端最新快照上出计划。
	if _, err := app.StartScan(ctx, rel.RelationID); err != nil {
		return nil, err
	}
	waitScan(ctx, app, rel.RelationID)
	draft, err := mrgPrepareSync(ctx, app, rel)
	if err != nil {
		return nil, fmt.Errorf("场景① PrepareSync: %w", err)
	}
	if draft.Summary.MergedCleanCount != 1 || draft.Summary.ConflictCount != 0 {
		return nil, fmt.Errorf("场景① summary = %+v，期望 merged_clean=1 冲突=0", draft.Summary)
	}
	if len(draft.Operations) != 1 || draft.Operations[0].Kind != "write_merged" || !draft.Operations[0].Reversible {
		return nil, fmt.Errorf("场景① 操作面 %+v，期望单条 reversible write_merged", draft.Operations)
	}
	if len(draft.Operations[0].Preconditions) != 2 {
		return nil, fmt.Errorf("场景① write_merged 前置条件应断言双端: %+v", draft.Operations[0].Preconditions)
	}
	mergedResource := draft.Operations[0].ResourceID
	// take_merged 误用于非 merged 行 → 既有 err.plan.resolution_invalid。
	if _, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: []model.Resolution{
		{ResourceID: "file:config/not-a-merged-row.toml", Choice: model.ChoiceTakeMerged},
	}}); err == nil || errs.CodeOf(err) != "err.plan.resolution_invalid" {
		return nil, fmt.Errorf("场景① take_merged 于非 merged 行应 resolution_invalid: %v", err)
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: []model.Resolution{
		{ResourceID: mergedResource, Choice: model.ChoiceTakeMerged},
	}})
	if err != nil {
		return nil, fmt.Errorf("场景① resolve take_merged: %w", err)
	}
	if len(resolved.ConfirmationRequirements) != 0 {
		return nil, fmt.Errorf("场景① merged_clean 属非冲突操作，确认要求应为空: %+v", resolved.ConfirmationRequirements)
	}
	// 确定性重算产物（同算法同输入同输出——harness 以 merge.Texts 复算）。
	product := string(merge.Texts(baseBytes, []byte(projBefore), []byte(rtBefore)).Merged)
	if len(merge.Texts(baseBytes, []byte(projBefore), []byte(rtBefore)).Hunks) != 0 {
		return nil, fmt.Errorf("场景① 夹具自检：样本应干净合并")
	}
	// ---- 场景④a 预览「所见即所写」（票 #98 补齐 §3.2 场景④；#94）：
	// resolved 计划的 merged 行预览 content=确认后落盘字节（同算法同输入），
	// base_content=基线全文。取数在 confirm 前——预览三侧走端点活文件并复核
	// 计划输入快照指纹（merge_preview.go 口径），confirm 后活文件被合并产物
	// 覆盖即属外部写者竞态语义，预览断言必须在落盘前完成。----
	pv, err := app.GetMergedPreview(ctx, view.GetMergedPreviewInput{
		PlanID: resolved.PlanID, ResourceID: string(mergedResource),
	})
	if err != nil {
		return nil, fmt.Errorf("场景④a GetMergedPreview: %w", err)
	}
	if pv.Content != product {
		return nil, fmt.Errorf("场景④a 预览 content≠确定性重算产物（所见即所写不成立）\n got=%q\nwant=%q", pv.Content, product)
	}
	if pv.BaseContent != string(baseBytes) {
		return nil, fmt.Errorf("场景④a 预览 base_content≠基线全文（标注锚点不成立）\n got=%q\nwant=%q", pv.BaseContent, string(baseBytes))
	}
	fmt.Printf("== 场景④a 合并预览所见即所写 == 通过（content=%dB=落盘产物，base_content=%dB=基线全文）\n", len(pv.Content), len(pv.BaseContent))
	c1, err := mrgConfirmAndWait(ctx, app, resolved.PlanID)
	if err != nil {
		return nil, fmt.Errorf("场景① confirm/apply: %w", err)
	}
	for path, want := range map[string]string{projFile: product, rtFile: product} {
		got, rerr := os.ReadFile(path)
		if rerr != nil || string(got) != want {
			return nil, fmt.Errorf("场景① 落盘字节≠确定性重算产物 %s（err=%v）\n got=%q\nwant=%q", path, rerr, string(got), want)
		}
	}
	// 未冲突区域字节级不变（对落盘文件直接断，红线③）：手工注释/键序/空行/缩进。
	for _, marker := range []string{
		"# 手工注释样本：玩家手写风格的配置文件。",
		"  render_distance = 12   # 行内注释 + 缩进键序样本",
		"[graphics]",
		"master_volume = 0.8",
		"\n\n[audio]",
	} {
		if !strings.Contains(product, marker) {
			return nil, fmt.Errorf("场景① 未冲突区域被改写: %q 不在产物中", marker)
		}
	}
	// 产物入 CAS（红线②）+ mod metafile 新内容入 CAS（提交收口期摄取）。
	for _, d := range []struct{ label, digest string }{
		{"合并产物", sha256Hex([]byte(product))},
		{"mod metafile", metaDigest},
	} {
		ok, herr := stack.CAS.Has(ctx, d.digest)
		if herr != nil || !ok {
			return nil, fmt.Errorf("场景① %s digest %s 应入 CAS: ok=%v err=%v", d.label, d.digest[:12], ok, herr)
		}
	}
	timing1 := stack.SyncApp.LastApplyTiming()
	fmt.Printf("== 场景① merged_clean 全链 == 通过（commit=%s product=%dB merge分相 diff3=%dms 校验=%dms 写盘=%dms ops=%d）\n",
		c1, len(product), timing1.MergeDiff3MS, timing1.MergeValidateMS, timing1.MergeWriteMS, timing1.MergeOps)

	// ---- 场景② 回滚零网络 ----
	// 先回滚到初次提交（双端回到合并前字节），再回滚到合并提交——merged 行
	// 由目标基线内容实存判定 restorable_from_cas，取 CAS 字节写回，零网络。
	draftR0, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c0})
	if err != nil {
		return nil, fmt.Errorf("场景② PrepareRestore(c0): %w", err)
	}
	if err := mrgAssertMarker(draftR0, string(mergedResource), model.MarkerRestorableFromCAS); err != nil {
		return nil, fmt.Errorf("场景②: %w", err)
	}
	resolvedR0, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draftR0.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return nil, fmt.Errorf("场景② resolve(c0): %w", err)
	}
	if _, err := mrgConfirmRestoreAndWait(ctx, app, resolvedR0.PlanID); err != nil {
		return nil, fmt.Errorf("场景② restore(c0): %w", err)
	}
	draftR1, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return nil, fmt.Errorf("场景② PrepareRestore(c1): %w", err)
	}
	if err := mrgAssertMarker(draftR1, string(mergedResource), model.MarkerRestorableFromCAS); err != nil {
		return nil, fmt.Errorf("场景② merged 行: %w", err)
	}
	if !draftR1.ExactFeasible {
		return nil, fmt.Errorf("场景② 计划应全部就绪（exact feasible）")
	}
	resolvedR1, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draftR1.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return nil, fmt.Errorf("场景② resolve(c1): %w", err)
	}
	if _, err := mrgConfirmRestoreAndWait(ctx, app, resolvedR1.PlanID); err != nil {
		return nil, fmt.Errorf("场景② restore(c1): %w", err)
	}
	for _, path := range []string{projFile, rtFile} {
		got, rerr := os.ReadFile(path)
		if rerr != nil || string(got) != product {
			return nil, fmt.Errorf("场景② 回滚后字节≠合并产物 %s（err=%v）", path, rerr)
		}
	}
	fmt.Printf("== 场景② 回滚零网络 == 通过（merged 行 restorable_from_cas → committed，零 CDN 配置零用户字节）\n")

	// ---- 场景③ 授权模式口径（#86 QuickUpdate 用例）----
	if _, err := app.SetWorkspaceAuthorized(ctx, rel.RelationID, true); err != nil {
		return nil, fmt.Errorf("场景③ 开启授权: %w", err)
	}
	// 第三轮双改（锚点取未冲突区域，互不重叠 → merged_clean）。
	cur, err := os.ReadFile(projFile)
	if err != nil {
		return nil, err
	}
	proj3 := mrgMustReplace(string(cur), "render_distance = 12", "render_distance = 20")
	rt3 := mrgMustReplace(string(cur), "master_volume = 0.8", "master_volume = 0.55")
	if err := os.WriteFile(projFile, []byte(proj3), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(rtFile, []byte(rt3), 0o644); err != nil {
		return nil, err
	}
	qu, err := app.QuickUpdate(ctx, view.QuickUpdateInput{RelationID: rel.RelationID})
	if err != nil {
		return nil, fmt.Errorf("场景③ QuickUpdate: %w", err)
	}
	if qu.Outcome != syncapp.QuickUpdateApplyStarted {
		return nil, fmt.Errorf("场景③ 授权开态 merged_clean 应随非冲突批量免确认直达: outcome=%s", qu.Outcome)
	}
	if _, err := waitTask(ctx, app, qu.ApplyTaskID, mrgWaitOpts()); err != nil {
		return nil, fmt.Errorf("场景③ apply: %w", err)
	}
	product3 := string(merge.Texts([]byte(product), []byte(proj3), []byte(rt3)).Merged)
	for _, path := range []string{projFile, rtFile} {
		got, rerr := os.ReadFile(path)
		if rerr != nil || string(got) != product3 {
			return nil, fmt.Errorf("场景③ 落盘字节≠重算产物 %s（err=%v）", path, rerr)
		}
	}
	fmt.Printf("== 场景③a 授权开态快速更新 == 通过（merged_clean 免确认直达 committed）\n")

	// 冲突行永不自动（红线①）：双侧同段不同改动 → QuickUpdate 停
	// awaiting_confirmation，计划停留 draft 且冲突证据齐备。
	projC := mrgMustReplace(product3, "fancy_graphics = false", "fancy_graphics = true")
	rtC := mrgMustReplace(product3, "fancy_graphics = false", "fancy_graphics = false # tuned-by-runtime")
	if err := os.WriteFile(projFile, []byte(projC), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(rtFile, []byte(rtC), 0o644); err != nil {
		return nil, err
	}
	qu2, err := app.QuickUpdate(ctx, view.QuickUpdateInput{RelationID: rel.RelationID})
	if err != nil {
		return nil, fmt.Errorf("场景③ 冲突 QuickUpdate: %w", err)
	}
	if qu2.Outcome != syncapp.QuickUpdateAwaitingConfirmation {
		return nil, fmt.Errorf("场景③ 含冲突差异必须停靠: outcome=%s（红线①）", qu2.Outcome)
	}
	docked, err := app.GetPlan(ctx, qu2.PlanID)
	if err != nil {
		return nil, err
	}
	if docked.Status != "draft" || len(docked.Conflicts) != 1 {
		return nil, fmt.Errorf("场景③ 停靠计划应为 draft 且含 1 冲突: status=%s conflicts=%d", docked.Status, len(docked.Conflicts))
	}
	if !strings.Contains(docked.Conflicts[0].Detail, `"hunks"`) {
		return nil, fmt.Errorf("场景③ 冲突证据应承载 hunk JSON: %s", docked.Conflicts[0].Detail)
	}
	active, err := app.ListTasks(ctx, rel.RelationID, true, ports.PageRequest{Limit: 10})
	if err != nil {
		return nil, err
	}
	if len(active.Items) != 0 {
		return nil, fmt.Errorf("场景③ 停靠后不得有活跃任务（红线①）: %d", len(active.Items))
	}
	fmt.Printf("== 场景③b 含冲突停靠 == 通过（永不自动，停 awaiting_confirmation）\n")

	// ---- 场景④c 非 merged 行预览拒绝（票 #98 补齐 §3.2 场景④）：冲突行
	// 预览 → err.merge.not_mergeable（{0}=resource_id）。取数在文件再改动前
	// ——预览按计划锁定快照复核活文件指纹，改动后属竞态语义（透传），不再是
	// not_mergeable 的干净证据。----
	conflictResource := string(docked.Conflicts[0].ResourceID)
	_, pvErr := app.GetMergedPreview(ctx, view.GetMergedPreviewInput{
		PlanID: qu2.PlanID, ResourceID: conflictResource,
	})
	if pvErr == nil || errs.CodeOf(pvErr) != "err.merge.not_mergeable" {
		return nil, fmt.Errorf("场景④c 冲突行预览应 err.merge.not_mergeable: %v", pvErr)
	}
	args := errs.ArgsOf(pvErr)
	if len(args) == 0 || args[0] != conflictResource {
		return nil, fmt.Errorf("场景④c not_mergeable 的 {0} 应为 resource_id: %v（want %s）", args, conflictResource)
	}
	fmt.Printf("== 场景④c 非 merged 行预览拒绝 == 通过（err.merge.not_mergeable {0}=%s）\n", conflictResource)

	// ---- 场景④b stale/expired 计划仍可预览（只读；票 #98 补齐）：还原双侧
	// 到 product3 解除③b 冲突差异 → 再做一轮互不重叠双改 → 新 draft 含
	// merged_clean 行 → SQL 手术置 expires_at 过期（造数手术，沿 #95/#96 先例）
	// → 预览零有效期校验仍返回，content=同算法重算产物。----
	if err := os.WriteFile(projFile, []byte(product3), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(rtFile, []byte(product3), 0o644); err != nil {
		return nil, err
	}
	proj4 := mrgMustReplace(product3, "render_distance = 20", "render_distance = 22")
	rt4 := mrgMustReplace(product3, "master_volume = 0.55", "master_volume = 0.40")
	if err := os.WriteFile(projFile, []byte(proj4), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(rtFile, []byte(rt4), 0o644); err != nil {
		return nil, err
	}
	if _, err := app.StartScan(ctx, rel.RelationID); err != nil {
		return nil, err
	}
	waitScan(ctx, app, rel.RelationID)
	draft4, err := mrgPrepareSync(ctx, app, rel)
	if err != nil {
		return nil, fmt.Errorf("场景④b PrepareSync: %w", err)
	}
	if draft4.Summary.MergedCleanCount != 1 || draft4.Summary.ConflictCount != 0 {
		return nil, fmt.Errorf("场景④b summary = %+v，期望 merged_clean=1 冲突=0", draft4.Summary)
	}
	if len(draft4.Operations) != 1 || draft4.Operations[0].Kind != "write_merged" {
		return nil, fmt.Errorf("场景④b 操作面 %+v，期望单条 write_merged", draft4.Operations)
	}
	// 过期手术（造数手术，沿 #95/#96 先例）：status 置 confirmed + expires_at
	// 置过去——「过期的已确认计划」（用户故事 9「过期待确认计划仍可预览」的字
	// 面）。选 confirmed 而非 draft/resolved 过期是确定性要求：惰性清理
	// DeleteExpiredPlans 只物理删过期/过时的 draft/resolved 行，而④b 扫描任务
	// 的终态钩子异步触发清理，与断言存在调度竞态；confirmed 行不在判定域内，
	// 预览（零状态/零有效期校验）读到的一定是过期计划本身。
	if _, err := stack.DB.ExecContext(ctx,
		"UPDATE sync_plans SET status='confirmed', expires_at=? WHERE id=?",
		"2000-01-01T00:00:00Z", draft4.PlanID); err != nil {
		return nil, fmt.Errorf("场景④b 过期手术: %w", err)
	}
	pv4, err := app.GetMergedPreview(ctx, view.GetMergedPreviewInput{
		PlanID: draft4.PlanID, ResourceID: string(draft4.Operations[0].ResourceID),
	})
	if err != nil {
		return nil, fmt.Errorf("场景④b 过期计划预览应只读可用: %w", err)
	}
	product4 := string(merge.Texts([]byte(product3), []byte(proj4), []byte(rt4)).Merged)
	if pv4.Content != product4 || pv4.BaseContent != product3 {
		return nil, fmt.Errorf("场景④b 过期计划预览≠重算产物（content 一致=%v base 一致=%v）",
			pv4.Content == product4, pv4.BaseContent == product3)
	}
	fmt.Printf("== 场景④b stale/expired 计划预览 == 通过（只读可预览，content=重算产物）\n")

	stats := &mergeChainStats{
		Kind:            "merge",
		MergeDiff3MS:    timing1.MergeDiff3MS,
		MergeValidateMS: timing1.MergeValidateMS,
		MergeWriteMS:    timing1.MergeWriteMS,
		MergeOps:        timing1.MergeOps,
		ChainTotalMS:    time.Since(chainStart).Milliseconds(),
	}
	return stats, nil
}

// ---- 链内辅助（pgheadless 包内复用 waitScan/waitTask/applyResolutions）----

// mrgEnableConfigRule 确保 config 受管规则在场（acceptance:headless 的
// PrepareRelation 建议已带 config/kubejs/scripts 规则；幂等补齐——已有同前缀
// 规则即零改动，避免规则 ID 重复被编译器拒绝）。
func mrgEnableConfigRule(ctx context.Context, app syncapp.Application, relationID string) error {
	pol, err := app.GetMappingPolicy(ctx, relationID)
	if err != nil {
		return err
	}
	for _, r := range pol.Rules {
		if r.ProjectPrefix == "config" || r.ID == "config" {
			return nil
		}
	}
	rules := append([]model.MappingRule{}, pol.Rules...)
	rules = append(rules, model.MappingRule{
		ID: "config", ResourceKind: "text_file",
		ProjectPrefix: "config", RuntimePrefix: "config",
		Direction: "bidirectional", Materialization: "copy",
		MergePolicy: "manual", RuntimeLocalPolicy: "exclude",
	})
	_, err = app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: relationID, ExpectedRevision: pol.RelationRevision, Rules: rules,
	})
	return err
}

// mrgApplyRound 执行一轮「扫描 → 计划 → 决议（mod 冲突 skip）→ confirm →
// committed」并返回提交 ID。
func mrgApplyRound(ctx context.Context, app syncapp.Application, rel view.RelationView) (string, error) {
	if _, err := app.StartScan(ctx, rel.RelationID); err != nil {
		return "", err
	}
	waitScan(ctx, app, rel.RelationID)
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return "", err
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.Relation.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		return "", err
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{
		PlanID: draft.PlanID, Resolutions: applyResolutions(draft.Conflicts),
	})
	if err != nil {
		return "", err
	}
	return mrgConfirmAndWait(ctx, app, resolved.PlanID)
}

// mrgConfirmAndWait 确认 sync 计划并等待 committed；返回提交 ID。
func mrgConfirmAndWait(ctx context.Context, app syncapp.Application, planID string) (string, error) {
	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: planID})
	if err != nil {
		return "", err
	}
	final, err := waitTask(ctx, app, tv.TaskID, mrgWaitOpts())
	if err != nil {
		return "", err
	}
	if final.Status != model.TaskStatusSucceeded {
		return "", fmt.Errorf("apply 任务终态 %s（problem=%v）", final.Status, problemText(final.Problem))
	}
	return final.CommitID, nil
}

// mrgConfirmRestoreAndWait 确认回滚计划并等待 committed；返回提交 ID。
func mrgConfirmRestoreAndWait(ctx context.Context, app syncapp.Application, planID string) (string, error) {
	tv, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: planID})
	if err != nil {
		return "", err
	}
	final, err := waitTask(ctx, app, tv.TaskID, mrgWaitOpts())
	if err != nil {
		return "", err
	}
	if final.Status != model.TaskStatusSucceeded {
		return "", fmt.Errorf("restore 任务终态 %s（problem=%v）", final.Status, problemText(final.Problem))
	}
	return final.CommitID, nil
}

// mrgWaitOpts 是 merge 链的轮询选项（apply 节奏 + 链超时；内存采样不启用）。
func mrgWaitOpts() taskWait {
	return taskWait{interval: applyPollInterval, timeout: mergePollTimeout, onPhase: applyPollProgress}
}

// mrgPrepareSync 在双端最新快照上产出 exact draft。
func mrgPrepareSync(ctx context.Context, app syncapp.Application, rel view.RelationView) (view.SyncPlanView, error) {
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return view.SyncPlanView{}, err
	}
	return app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.Relation.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
}

// mrgAssertMarker 断言回滚计划指定资源行的标记。
func mrgAssertMarker(p view.RestorePlanView, resourceID string, want model.RestoreMarker) error {
	for _, it := range p.Items {
		if it.ResourceID == model.ResourceID(resourceID) {
			if it.Marker != want {
				return fmt.Errorf("%s 行 marker=%s（reason=%s），期望 %s", resourceID, it.Marker, it.MarkerReason, want)
			}
			return nil
		}
	}
	return fmt.Errorf("回滚计划缺 %s 行（items=%d）", resourceID, len(p.Items))
}

// mrgFirstMetafile 返回项目侧第一个 .pw.toml（字典序；确定性夹具）。
func mrgFirstMetafile(projectRoot string) (string, error) {
	modsDir := filepath.Join(projectRoot, "mods")
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pw.toml") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("fixture 项目侧无 mod metafile")
	}
	sort.Strings(names)
	return filepath.Join(modsDir, names[0]), nil
}

// mrgAppendMetafileComment 向 metafile 追加一行手工注释并落盘（packwiz 解析
// 忽略注释：声明 hash 不变 → mod 语义摘要不变 → 资源行 noop；内容实测摘要
// 变化经扫描捕获进表示 Content，由提交收口期摄取通道入 CAS）。
func mrgAppendMetafileComment(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	edited := append(raw, []byte("# 玩家手工备注（合并提交收口期摄取断言）\n")...)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		return nil, err
	}
	return edited, nil
}

// mrgMustReplace 字符串替换必须恰好命中（防夹具形态漂移静默错编）。
func mrgMustReplace(s, from, to string) string {
	if !strings.Contains(s, from) {
		panic("merge 链夹具改动未命中锚点: " + from)
	}
	return strings.Replace(s, from, to, 1)
}
