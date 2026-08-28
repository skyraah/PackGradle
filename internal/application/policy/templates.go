// Package policy 提供 MappingPolicy 模板。
// default-v1 只包含 mods 语义规则；文件前缀模板仅作为建议，用户确认后才写入 Relation。
package policy

import (
	"fmt"

	"packgradle/internal/core/model"
)

// DefaultPolicySet 是空 policySet 输入时的默认值。
const DefaultPolicySet = "default-v1"

// Template 返回指定策略集的模板；未知策略集返回错误（调用方映射 err.mapping.unknown_policy）。
func Template(policySet string) (model.MappingPolicy, error) {
	switch policySet {
	case "", DefaultPolicySet:
		return DefaultV1(), nil
	default:
		return model.MappingPolicy{}, fmt.Errorf("policy: 未知策略集 %q", policySet)
	}
}

// DefaultV1 返回 default-v1：仅 mods 语义规则。
func DefaultV1() model.MappingPolicy {
	return model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion,
		PolicyID:      DefaultPolicySet,
		Revision:      1,
		Rules: []model.MappingRule{
			{
				ID:                 "mods",
				ResourceKind:       string(model.ResourceMod),
				ProjectPrefix:      "mods",
				RuntimePrefix:      "mods",
				Direction:          "bidirectional",
				Materialization:    "copy",
				MergePolicy:        "packwiz",
				RuntimeLocalPolicy: "exclude",
			},
		},
	}
}

// Suggestions 返回建议（默认不激活）的文件前缀模板片段，
// 供 /workspaces/new 页面勾选后并入 Relation 的 policy。
func Suggestions() []model.MappingRule {
	return []model.MappingRule{
		fileRule("config", "config", model.ResourceTextFile),
		fileRule("kubejs", "kubejs", model.ResourceTextFile),
		fileRule("scripts", "scripts", model.ResourceTextFile),
		fileRule("defaultconfigs", "defaultconfigs", model.ResourceTextFile),
	}
}

func fileRule(id, prefix string, kind model.ResourceKind) model.MappingRule {
	return model.MappingRule{
		ID:                 id,
		ResourceKind:       string(kind),
		ProjectPrefix:      prefix,
		RuntimePrefix:      prefix,
		Direction:          "bidirectional",
		Materialization:    "copy",
		MergePolicy:        "manual",
		RuntimeLocalPolicy: "exclude",
	}
}
