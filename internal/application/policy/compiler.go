package policy

import (
	"fmt"
	"sort"
	"strings"

	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// 合法枚举（编译期校验，检视报告 P0-5）。与 model.MappingRule 字段一一对应。
var (
	legalDirections = map[string]bool{
		"bidirectional": true, "project_to_runtime": true,
		"runtime_to_project": true, "ignore": true,
	}
	legalResourceKinds = map[string]bool{
		string(model.ResourceMod):        true,
		string(model.ResourceTextFile):   true,
		string(model.ResourceBinaryFile): true,
	}
	legalMaterializations = map[string]bool{"copy": true}
	legalMergePolicies    = map[string]bool{
		"manual": true, "text_diff3": true, "toml_semantic": true, "packwiz": true,
	}
	legalRuntimeLocal = map[string]bool{"exclude": true, "report": true}
)

// modsPrefix 是 mod 语义规则保留的前缀；文件规则不得进入（扫描器对 mods/
// 恒走 index.toml / jar 语义，不走文件规则）。
const modsPrefix = "mods"

// RuleError 是策略编译期的结构化校验错误：定位到规则与字段，
// application 层映射为 err.mapping.compile_failed（契约 03 §3，args {0}=rule_id，
// 字段与原因进 detail）。
type RuleError struct {
	RuleID string // 违规规则 ID（规则 ID 本身非法时为已知部分或空）
	Field  string // id | resource_kind | direction | materialization | merge_policy | runtime_local | project_prefix | runtime_prefix | include | exclude
	Value  string // 原始值
	Reason string // 违规原因（调试用；用户文案由前端按 code 渲染）
}

func (e *RuleError) Error() string {
	return fmt.Sprintf("policy: 规则 %q 字段 %s 非法（值 %q）: %s", e.RuleID, e.Field, e.Value, e.Reason)
}

func ruleErr(ruleID, field, value, reason string) *RuleError {
	return &RuleError{RuleID: ruleID, Field: field, Value: value, Reason: reason}
}

// CompiledFileRule 是编译后的文件映射规则：前缀已归一化、include/exclude
// glob 已编译。Matches/决议逻辑集中在 compiler，扫描器只做文件系统遍历。
type CompiledFileRule struct {
	Rule model.MappingRule

	projectPrefix string
	runtimePrefix string
	include       []*Glob
	exclude       []*Glob
}

// SidePrefix 返回规则在指定端的归一化前缀。
func (r *CompiledFileRule) SidePrefix(side model.Side) string {
	if side == model.SideRuntime {
		return r.runtimePrefix
	}
	return r.projectPrefix
}

// Matches 报告归一化路径（小写、'/' 分隔）是否被该规则受管：
// exclude 任一命中（含祖先目录）即排除；include 为空表示全收，非空时至少一项命中。
func (r *CompiledFileRule) Matches(relLower string) bool {
	for _, g := range r.exclude {
		if g.MatchPath(relLower) {
			return false
		}
	}
	if len(r.include) == 0 {
		return true
	}
	for _, g := range r.include {
		if g.MatchPath(relLower) {
			return true
		}
	}
	return false
}

// Compiled 是编译后的 MappingPolicy：全部规则通过编译期校验，glob 已编译，
// 决议器就绪。扫描器消费本结构；编译失败（*RuleError）意味着策略本身非法。
type Compiled struct {
	policy    model.MappingPolicy
	modRuleID string
	fileRules []CompiledFileRule
}

// ModRuleID 返回 mod 语义规则 ID；策略未声明时为空。
func (c *Compiled) ModRuleID() string { return c.modRuleID }

// FileRules 返回编译后的文件规则（声明顺序）。
func (c *Compiled) FileRules() []CompiledFileRule { return c.fileRules }


// Compile 编译并校验 MappingPolicy（检视报告 P0-5 的编译器入口）：
//   - 规则 ID 非空且唯一；方向、资源类型、物化、合并策略、运行时本地策略为合法枚举；
//   - 前缀 root-relative（无绝对路径、'..'、'.'、盘符），文件规则两侧前缀必填；
//   - mods/ 前缀恒由 mod 语义规则保留，文件规则不得进入；mod 规则前缀必须是 mods
//     且恰好一条（缺失时 mods 观察将带空 PolicyID、方向失控回退 bidirectional）；
//   - include/exclude glob 编译证明（非法模式编译期拒绝）。
//
// 任一规则非法返回 *RuleError；扫描/保存策略前必须先通过本函数。
func Compile(p model.MappingPolicy) (*Compiled, error) {
	if strings.TrimSpace(p.PolicyID) == "" {
		return nil, ruleErr("", "policy_id", p.PolicyID, "策略 ID 不能为空")
	}
	c := &Compiled{policy: p}
	seenIDs := make(map[string]bool, len(p.Rules))
	modCount := 0
	for i := range p.Rules {
		r := p.Rules[i]
		if err := compileRule(&r, seenIDs, &modCount); err != nil {
			return nil, err
		}
		switch model.ResourceKind(r.ResourceKind) {
		case model.ResourceMod:
			c.modRuleID = r.ID
		default:
			fr, err := compileFileRule(r)
			if err != nil {
				return nil, err
			}
			c.fileRules = append(c.fileRules, *fr)
		}
	}
	if modCount == 0 {
		return nil, ruleErr("", "resource_kind", "", "缺少 mod 语义规则（mods/ 恒由语义适配器管理）")
	}
	if modCount > 1 {
		return nil, ruleErr(c.modRuleID, "resource_kind", "mod", "mod 语义规则至多一条")
	}
	return c, nil
}

// Validate 只校验不保留编译产物；策略写入路径用它做编译期守门。
func Validate(p model.MappingPolicy) error {
	_, err := Compile(p)
	return err
}

// compileRule 校验单条规则的枚举字段、ID 唯一性与前缀形状（不构建 glob）。
func compileRule(r *model.MappingRule, seenIDs map[string]bool, modCount *int) error {
	if strings.TrimSpace(r.ID) == "" {
		return ruleErr("", "id", r.ID, "规则 ID 不能为空")
	}
	if seenIDs[r.ID] {
		return ruleErr(r.ID, "id", r.ID, "规则 ID 重复")
	}
	seenIDs[r.ID] = true
	if !legalResourceKinds[r.ResourceKind] {
		return ruleErr(r.ID, "resource_kind", r.ResourceKind, "未知资源类型")
	}
	if !legalDirections[r.Direction] {
		return ruleErr(r.ID, "direction", r.Direction, "未知映射方向")
	}
	if !legalMaterializations[r.Materialization] {
		return ruleErr(r.ID, "materialization", r.Materialization, "未知物化方式")
	}
	if !legalMergePolicies[r.MergePolicy] {
		return ruleErr(r.ID, "merge_policy", r.MergePolicy, "未知合并策略")
	}
	if !legalRuntimeLocal[r.RuntimeLocalPolicy] {
		return ruleErr(r.ID, "runtime_local", r.RuntimeLocalPolicy, "未知运行时本地策略")
	}
	if model.ResourceKind(r.ResourceKind) == model.ResourceMod {
		*modCount++
		for _, field := range []string{"project_prefix", "runtime_prefix"} {
			if got := normalize.NormalizeRelPath(rulePrefix(r, field)); got != modsPrefix {
				return ruleErr(r.ID, field, rulePrefix(r, field), "mod 语义规则前缀必须是 "+modsPrefix)
			}
		}
		// mod 规则不消费 include/exclude，但保持 glob 编译证明的 uniform 校验
		for _, field := range []struct {
			name     string
			patterns []string
		}{{"include", r.Include}, {"exclude", r.Exclude}} {
			for _, g := range field.patterns {
				if _, err := CompileGlob(g); err != nil {
					return ruleErr(r.ID, field.name, g, err.Error())
				}
			}
		}
		return nil
	}
	for _, field := range []string{"project_prefix", "runtime_prefix"} {
		if err := validateFilePrefix(r.ID, field, rulePrefix(r, field)); err != nil {
			return err
		}
	}
	return nil
}

// rulePrefix 取规则指定侧的前缀原始值。
func rulePrefix(r *model.MappingRule, field string) string {
	if field == "runtime_prefix" {
		return r.RuntimePrefix
	}
	return r.ProjectPrefix
}

// validateFilePrefix 校验文件规则前缀：root-relative、非空、不进入 mods 保留前缀。
func validateFilePrefix(ruleID, field, prefix string) error {
	if strings.TrimSpace(prefix) == "" {
		return ruleErr(ruleID, field, prefix, "文件规则前缀不能为空")
	}
	if strings.HasPrefix(strings.ReplaceAll(prefix, "\\", "/"), "/") {
		return ruleErr(ruleID, field, prefix, "前缀必须是 root 相对路径，不能是绝对路径")
	}
	normalized := normalize.NormalizeRelPath(prefix)
	if err := validateRelSegments(normalized, "前缀"); err != nil {
		return ruleErr(ruleID, field, prefix, err.Error())
	}
	if normalized == modsPrefix || strings.HasPrefix(normalized, modsPrefix+"/") {
		return ruleErr(ruleID, field, prefix, "mods 前缀恒由 mod 语义规则管理，文件规则不得进入")
	}
	return nil
}

// compileFileRule 编译文件规则的 include/exclude glob。
func compileFileRule(r model.MappingRule) (*CompiledFileRule, error) {
	fr := &CompiledFileRule{
		Rule:          r,
		projectPrefix: normalize.NormalizeRelPath(r.ProjectPrefix),
		runtimePrefix: normalize.NormalizeRelPath(r.RuntimePrefix),
	}
	for _, g := range r.Include {
		glob, err := CompileGlob(g)
		if err != nil {
			return nil, ruleErr(r.ID, "include", g, err.Error())
		}
		fr.include = append(fr.include, glob)
	}
	for _, g := range r.Exclude {
		glob, err := CompileGlob(g)
		if err != nil {
			return nil, ruleErr(r.ID, "exclude", g, err.Error())
		}
		fr.exclude = append(fr.exclude, glob)
	}
	return fr, nil
}

// ResolveFileRule 从候选规则中按「最具体优先」决议受管归属（检视报告 P0-5）：
// 候选里指定端前缀最长者胜出；最长前缀并列时无法唯一决议，返回 winner=nil 与
// 并列规则 ID 证据（字节序）。
// include/exclude 的互斥区分发生在上游候选收集阶段（调用方只把 Matches 命中的
// 规则放入候选）；到达本函数仍并列的候选就是真正的碰撞，不做二次消解。
func ResolveFileRule(side model.Side, cands []*CompiledFileRule) (winner *CompiledFileRule, collision []string) {
	if len(cands) == 0 {
		return nil, nil
	}
	maxLen := -1
	var tied []*CompiledFileRule
	for _, c := range cands {
		if n := len(c.SidePrefix(side)); n > maxLen {
			maxLen = n
			tied = []*CompiledFileRule{c}
		} else if n == maxLen {
			tied = append(tied, c)
		}
	}
	if len(tied) == 1 {
		return tied[0], nil
	}
	ids := make([]string, 0, len(tied))
	for _, c := range tied {
		ids = append(ids, c.Rule.ID)
	}
	sort.Strings(ids)
	return nil, ids
}
