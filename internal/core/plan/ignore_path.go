package plan

// 票 #100（ADR-0013）精确前缀路径口径唯一点：计划面快路径
//（exactPathIgnoreDirection）与提交期规则合成/翻转（application/sync 的
// flipExistingIgnoreRule）对「单文件规则治理哪个文件」的判定必须完全一致，
// 谓词在本包导出共用（S2：两处同形判定的收敛点）。路径归一化唯一入口是
// policy.NormalizeRelPath（S1），与扫描器匹配口径一致（小写、斜杠、去首尾
// 分隔）。

import (
	"packgradle/internal/application/policy"
	"packgradle/internal/core/model"
)

// ExactPathRuleForPath 报告规则是否「两侧前缀归一化后恰等于路径」的非 mod
// 文件规则——合成单文件 ignore 规则的形状判定（S2 共享谓词）。path 须已经
// policy.NormalizeRelPath 归一化；mod 语义规则恒不参与（前缀恒为 mods，不按
// 文件路径治理）。
func ExactPathRuleForPath(r model.MappingRule, path string) bool {
	if model.ResourceKind(r.ResourceKind) == model.ResourceMod {
		return false
	}
	return policy.NormalizeRelPath(r.ProjectPrefix) == path &&
		policy.NormalizeRelPath(r.RuntimePrefix) == path
}
