package sync

// 冲突决议「忽略」的策略面（票 #100，ADR-0013）：
//
//   - 忽略决议（ChoiceSkip）随 committed 事务合成为单文件 ignore 规则
//     （synthesizeIgnoreRules）：两侧前缀 = 资源相对路径，direction=ignore，
//     最长前缀胜出恰好覆盖该文件、与模板目录规则不并列；合成走既有规则集
//     整体保存（编译约束要求恰好一条 mod 规则等），SavePolicy 事务感知加入
//     外层事务，存储层联动递增 relations.revision（坑 A，预期行为）。
//   - direction=ignore 的差异面同口径过滤（ignoreDirectionFilter）：四处消费点
//     （verifyRescan / diff_state / changes 页 / QuickUpdate no_diff 判定）与计划
//     构建的 plan.ResourceDirection 完全同一实现，杜绝「计划静默但差异面显示」。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"packgradle/internal/application/policy"
	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/core/plan"
)

// directionIgnore 是 model.MappingRule.Direction 的 ignore 取值（词汇表归
// policy.Compile 的 legalDirections）。
const directionIgnore = "ignore"

// ignoreDirectionFilter 返回判定函数：资源在当前策略下方向为 ignore 时 true。
// 与计划构建的 resourceDirection 完全同口径（观察 PolicyID → 规则方向，
// project 侧观察优先回退 runtime 侧，无规则 bidirectional）。
func ignoreDirectionFilter(policySet model.MappingPolicy, snapP, snapR model.ObservedSnapshot) func(model.ResourceID) bool {
	return func(id model.ResourceID) bool {
		return plan.ResourceDirection(policySet, snapP, snapR, id) == directionIgnore
	}
}

// ignoreTarget 是单条忽略决议的合成输入：资源相对路径、观察类别与观察命中的
// 既有规则（方向改回的恢复锚点）。
type ignoreTarget struct {
	relPath string // root-relative（斜杠、原大小写；前缀写入值）
	relLower string // 小写形态（与扫描器匹配同口径的比较键）
	kind    model.ResourceKind
	govID   string // 观察 PolicyID（既有规则的 ID；可为空）
}

// ignoreTargetOf 从输入快照观察解析忽略目标：观察优先（RelativePath + Kind +
// PolicyID 与扫描器产出同源），双侧均无观察时按资源 ID 推导（file: 前缀内嵌
// 路径，Kind 按前缀）；mod 资源与不可定位路径返回 false——mod 不合成（编译器
// 禁文件规则入 mods/ 前缀，ADR-0013 §4）。
func ignoreTargetOf(id model.ResourceID, snapP, snapR model.ObservedSnapshot) (ignoreTarget, bool) {
	t := ignoreTarget{kind: model.ResourceTextFile}
	if obs := snapshotObs(snapP, id); obs != nil {
		t.relPath = obs.Representation.RelativePath
		t.kind = obs.Kind
		t.govID = obs.PolicyID
	} else if obs := snapshotObs(snapR, id); obs != nil {
		t.relPath = obs.Representation.RelativePath
		t.kind = obs.Kind
		t.govID = obs.PolicyID
	} else {
		if !strings.HasPrefix(string(id), "file:") {
			return ignoreTarget{}, false
		}
		t.relPath = strings.TrimPrefix(string(id), "file:")
		t.kind = normalize.KindOfResourceID(id)
	}
	t.relPath = strings.Trim(strings.ReplaceAll(t.relPath, "\\", "/"), "/")
	t.relLower = strings.ToLower(t.relPath)
	if t.relPath == "" || t.kind == model.ResourceMod {
		return ignoreTarget{}, false
	}
	return t, true
}

// synthesizeIgnoreRules 把本计划的「忽略」决议（ChoiceSkip）合成为单文件 ignore
// 规则并经 SavePolicy 随 committed 事务落库（ADR-0013 §2，票 #100）。manual 决议
// 不碰策略；无忽略决议零写入（revision 不动）。返回是否有策略写入（驱动事务
// 提交后的 kickWatch，ADR-0010 §3）。
//
// 规则复用优先：既有文件规则两侧前缀已恰好等于该路径且不带 glob 时只翻转方向
// （已恰为 ignore 则零写入——无谓 SavePolicy 会使 revision 无谓 +1，坑 A/C1；
// 避免等前缀并列触发 diag.mapping.collision）；同前缀但带 glob 的用户自建规则
// 不治理该文件（扫描口径），不翻转、不合成，该资源退回普通 skip 语义（C2）；
// 否则追加新规则——枚举字段照抄观察命中的现行生效规则（最长前缀胜出者），
// 避免合成值与模板语义漂移。保存前过 policy.Validate 编译约束全集（坑 B：
// 唯一 ID、恰好一条 mod 规则、文件规则禁入 mods/ 前缀、glob 可编译等），失败
// 即整场回滚进恢复面。
func (a *App) synthesizeIgnoreRules(ctx context.Context, repos ports.Repos, relationID string,
	plan model.SyncPlan, snapP, snapR model.ObservedSnapshot) (bool, error) {

	skipIDs := make([]model.ResourceID, 0, len(plan.Resolutions))
	for _, r := range plan.Resolutions {
		if r.Choice == model.ChoiceSkip {
			skipIDs = append(skipIDs, r.ResourceID)
		}
	}
	if len(skipIDs) == 0 {
		return false, nil
	}
	cur, err := repos.Mappings.GetPolicy(ctx, relationID)
	if err != nil {
		return false, fmt.Errorf("读取当前策略: %w", err)
	}
	next := cur
	changed := false
		for _, id := range skipIDs {
			t, ok := ignoreTargetOf(id, snapP, snapR)
			if !ok {
				// 刻意的静默 continue：合成不可达目标（实践上仅 mod 资源——
				// 编译器禁文件规则入 mods/ 前缀，ADR-0013 §4）不报错、不落
				// 规则，决议按普通 skip 语义吸收进基线（测试
				// TestModIgnoreResolutionLeavesNoRule 钉住该契约）。
				continue
			}
			handled, flipped := flipExistingIgnoreRule(&next, t)
			if handled {
				if flipped {
					changed = true
				}
				continue
			}
		gov := governingRule(next, t.govID)
		rule := model.MappingRule{
			ID:           uniqueIgnoreRuleID(next, t.relPath),
			ResourceKind: string(t.kind),
			// 两侧前缀 = 资源路径：WalkDir 起点即该文件、只访问自身，兄弟路径
			// 不受影响；最长前缀胜出压过模板目录规则、不并列。
			ProjectPrefix: t.relPath,
			RuntimePrefix: t.relPath,
			Direction:     directionIgnore,
		}
		if gov != nil {
			// 枚举字段照抄现行生效规则（模板规则或用户规则），保持合成值与
			// 既有语义一致；include/exclude 留空（前缀已精确到文件）。
			rule.Materialization = gov.Materialization
			rule.MergePolicy = gov.MergePolicy
			rule.RuntimeLocalPolicy = gov.RuntimeLocalPolicy
		} else {
			rule.Materialization = "copy"
			rule.MergePolicy = "manual"
			rule.RuntimeLocalPolicy = "exclude"
		}
		next.Rules = append(next.Rules, rule)
		changed = true
	}
	if !changed {
		return false, nil
	}
	if err := policy.Validate(next); err != nil {
		return false, fmt.Errorf("合成忽略规则未过编译校验: %w", err)
	}
	if err := repos.Mappings.SavePolicy(ctx, relationID, next); err != nil {
		return false, fmt.Errorf("保存合成策略: %w", err)
	}
	return true, nil
}

// flipExistingIgnoreRule 处理「两侧前缀已恰好等于目标路径」的既有文件规则
// （恢复后再次忽略 / 策略漂移的幂等路径），返回（是否命中既有规则, 是否本次
// 实际翻转了方向=有写入）：
//
//   - 无既有精确前缀规则 → (false, false)，调用方合成新规则；
//   - 方向非 ignore 且不带 glob → 就地翻转，(true, true)；
//   - 已恰为 ignore → (true, false)：无实际变化，零写入——返回 flipped=true
//     会触发无谓 SavePolicy（revision 无谓 +1、同关系其它计划无谓 stale，
//     坑 A）（C1）；
//   - 同前缀但带 glob（用户自建规则）→ (true, false) 且不触碰该规则（C2）：
//     glob 经扫描 Matches 裁决（managedfiles 候选收集）后该规则可能不治理
//     目标文件，翻转它既可能误改无关治理、也不能并列合成同前缀新规则（等
//     前缀并列触发 diag.mapping.collision）——该资源退回普通 skip 语义
//     （留在差异面，行为等同 ADR-0013 之前），不翻转、不合成。
func flipExistingIgnoreRule(next *model.MappingPolicy, t ignoreTarget) (handled, flipped bool) {
	for i := range next.Rules {
		r := &next.Rules[i]
		// S2 共享谓词：与计划面 exactPathIgnoreDirection 同口径（前缀归一化
		// 走 normalize.NormalizeRelPath，mod 规则不参与）。
		if !plan.ExactPathRuleForPath(*r, t.relLower) {
			continue
		}
		// C2 资格收窄：合成规则的 include/exclude 恒空；带 glob 的同前缀规则
		// 不治理该文件（扫描口径），回退不翻转、不合成。
		if len(r.Include) != 0 || len(r.Exclude) != 0 {
			return true, false
		}
		if r.Direction != directionIgnore {
			r.Direction = directionIgnore
			return true, true
		}
		return true, false
	}
	return false, false
}

// governingRule 按 ID 查观察命中的现行生效规则；mod 语义规则不算（其枚举字段
// 不适用于文件规则）。
func governingRule(p model.MappingPolicy, ruleID string) *model.MappingRule {
	if ruleID == "" {
		return nil
	}
	for i := range p.Rules {
		if p.Rules[i].ID == ruleID && model.ResourceKind(p.Rules[i].ResourceKind) != model.ResourceMod {
			return &p.Rules[i]
		}
	}
	return nil
}

// uniqueIgnoreRuleID 生成确定性的单文件规则 ID（ignore-<路径>）；既有 ID 冲突
// 时追加序号后缀（-2、-3…有界循环）。
func uniqueIgnoreRuleID(p model.MappingPolicy, relPath string) string {
	base := "ignore-" + relPath
	taken := make(map[string]bool, len(p.Rules))
	for _, r := range p.Rules {
		taken[r.ID] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		id := fmt.Sprintf("%s-%d", base, n)
		if !taken[id] {
			return id
		}
	}
}

// decisionEntry 是用户决议资源的提交摘要单行（与 skippedEntry 的物化取数剔除
// 项是两个清单，坑 D）。
type decisionEntry struct {
	ResourceID string `json:"resource_id"`
}

// decisionEntries 把计划的忽略（ChoiceSkip）与手动处理（ChoiceManual）决议
// 编译为提交摘要的两个清单（按资源 ID 字节序，plan.Resolutions 已序，防御性
// 再排序保证确定性）。
func decisionEntries(resolutions []model.Resolution) (ignored, manual []decisionEntry) {
	for _, r := range resolutions {
		switch r.Choice {
		case model.ChoiceSkip:
			ignored = append(ignored, decisionEntry{ResourceID: string(r.ResourceID)})
		case model.ChoiceManual:
			manual = append(manual, decisionEntry{ResourceID: string(r.ResourceID)})
		}
	}
	sort.Slice(ignored, func(i, j int) bool { return ignored[i].ResourceID < ignored[j].ResourceID })
	sort.Slice(manual, func(i, j int) bool { return manual[i].ResourceID < manual[j].ResourceID })
	return ignored, manual
}
