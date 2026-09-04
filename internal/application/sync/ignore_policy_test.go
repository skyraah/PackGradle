package sync

// 票 #100 双轴评审：ignore 策略面纯函数单测（C1 幂等零写入 / C2 资格收窄回退）。
// 端到端链路（合成、回退、过期、mod 边界）见 headless_ignore_t100_test.go
//（sync_test 外部测试包）。

import (
	"testing"

	"packgradle/internal/core/model"
)

// t100BTarget 是与 exactBRule 同路径的合成输入（relLower 已按扫描口径归一化）。
var t100BTarget = ignoreTarget{
	relPath:  "config/b.toml",
	relLower: "config/b.toml",
	kind:     model.ResourceTextFile,
}

// exactBRule 构造既有规则；前缀大小写与尾分隔符故意不归一，锁定谓词经
// normalize.NormalizeRelPath 的归一化口径（小写、斜杠、去首尾分隔）与扫描一致。
func exactBRule(direction string, include, exclude []string) model.MappingRule {
	return model.MappingRule{
		ID:                 "existing-b",
		ResourceKind:       string(model.ResourceTextFile),
		ProjectPrefix:      `config\B.TOML`,
		RuntimePrefix:      "config/b.toml/",
		Include:            include,
		Exclude:            exclude,
		Direction:          direction,
		Materialization:    "copy",
		MergePolicy:        "manual",
		RuntimeLocalPolicy: "exclude",
	}
}

// TestFlipExistingIgnoreRule 表驱动（C1/C2）：命中既有精确前缀规则的各形态
// 与未命中交给合成路径的出口。flipped=true 才有写入；flipped=false 时规则
// 必须保持原样（C1 零写入 / C2 不触碰用户规则）。
func TestFlipExistingIgnoreRule(t *testing.T) {
	cases := []struct {
		name          string
		rule          model.MappingRule
		wantHandled   bool
		wantFlipped   bool
		wantDirection string
	}{
		{
			// C1 反例（修复前行为）：已恰为 ignore 也报「有变化」→ 无谓 SavePolicy
			name:          "已恰为ignore_零写入",
			rule:          exactBRule(directionIgnore, nil, nil),
			wantHandled:   true,
			wantFlipped:   false,
			wantDirection: directionIgnore,
		},
		{
			name:          "方向不同_翻转并写ignore",
			rule:          exactBRule("project_to_runtime", nil, nil),
			wantHandled:   true,
			wantFlipped:   true,
			wantDirection: directionIgnore,
		},
		{
			// C2：同前缀带 glob 的用户自建规则不治理该文件（扫描 Matches 口径）
			// ——不翻转、交给回退（调用方不合成），方向维持原样。
			name:          "同前缀带include_回退不翻转",
			rule:          exactBRule("project_to_runtime", []string{"bak.toml"}, nil),
			wantHandled:   true,
			wantFlipped:   false,
			wantDirection: "project_to_runtime",
		},
		{
			name:          "同前缀带exclude_同样回退",
			rule:          exactBRule(directionIgnore, nil, []string{"*.secret"}),
			wantHandled:   true,
			wantFlipped:   false,
			wantDirection: directionIgnore,
		},
		{
			name: "前缀不同_交给合成路径",
			rule: func() model.MappingRule {
				r := exactBRule("bidirectional", nil, nil)
				r.ProjectPrefix = "config/other.toml"
				r.RuntimePrefix = "config/other.toml"
				return r
			}(),
			wantHandled:   false,
			wantFlipped:   false,
			wantDirection: "bidirectional",
		},
		{
			// mod 语义规则不参与路径裁决（S2 谓词排除；前缀恒为 mods 实际不可达）
			name: "mod类别规则_不参与",
			rule: func() model.MappingRule {
				r := exactBRule("bidirectional", nil, nil)
				r.ResourceKind = string(model.ResourceMod)
				return r
			}(),
			wantHandled:   false,
			wantFlipped:   false,
			wantDirection: "bidirectional",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := model.MappingPolicy{Rules: []model.MappingRule{tc.rule}}
			handled, flipped := flipExistingIgnoreRule(&p, t100BTarget)
			if handled != tc.wantHandled || flipped != tc.wantFlipped {
				t.Fatalf("flipExistingIgnoreRule = (%v, %v)，期望 (%v, %v)",
					handled, flipped, tc.wantHandled, tc.wantFlipped)
			}
			if p.Rules[0].Direction != tc.wantDirection {
				t.Fatalf("规则方向 = %s，期望 %s（flipped=%v 时必须维持原样）",
					p.Rules[0].Direction, tc.wantDirection, flipped)
			}
			if tc.wantFlipped && p.Rules[0].Direction != directionIgnore {
				t.Fatalf("翻转后方向 = %s，期望 ignore", p.Rules[0].Direction)
			}
		})
	}
}

// TestFlipExistingIgnoreRuleScansAllRules 精确前缀规则不在首位时仍被找到
// （与修复前同口径：首个前缀命中者胜）。
func TestFlipExistingIgnoreRuleScansAllRules(t *testing.T) {
	other := model.MappingRule{
		ID: "config", ResourceKind: "text_file",
		ProjectPrefix: "config", RuntimePrefix: "config",
		Direction: directionIgnore,
	}
	target := exactBRule("project_to_runtime", nil, nil)
	p := model.MappingPolicy{Rules: []model.MappingRule{other, target}}
	handled, flipped := flipExistingIgnoreRule(&p, t100BTarget)
	if !handled || !flipped {
		t.Fatalf("flipExistingIgnoreRule = (%v, %v)，期望 (true, true)", handled, flipped)
	}
	if p.Rules[1].Direction != directionIgnore {
		t.Fatalf("目标规则方向 = %s，期望已翻转", p.Rules[1].Direction)
	}
	if p.Rules[0].Direction != directionIgnore {
		t.Fatal("无关规则不得被触碰")
	}
	if len(p.Rules) != 2 {
		t.Fatalf("规则数 = %d，期望不合成新规则", len(p.Rules))
	}
}
