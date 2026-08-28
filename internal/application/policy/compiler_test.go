package policy

import (
	"errors"
	"reflect"
	"testing"

	"packgradle/internal/core/model"
)

// validRule 返回一条合法的文件规则，测试按需覆盖字段。
func validRule(id string) model.MappingRule {
	return model.MappingRule{
		ID:                 id,
		ResourceKind:       string(model.ResourceTextFile),
		ProjectPrefix:      "config",
		RuntimePrefix:      "config",
		Direction:          "bidirectional",
		Materialization:    "copy",
		MergePolicy:        "manual",
		RuntimeLocalPolicy: "exclude",
	}
}

// modRule 返回合法的 mod 语义规则。
func modRule() model.MappingRule {
	return model.MappingRule{
		ID:                 "mods",
		ResourceKind:       string(model.ResourceMod),
		ProjectPrefix:      "mods",
		RuntimePrefix:      "mods",
		Direction:          "bidirectional",
		Materialization:    "copy",
		MergePolicy:        "packwiz",
		RuntimeLocalPolicy: "exclude",
	}
}

func TestCompileDefaultV1(t *testing.T) {
	c, err := Compile(DefaultV1())
	if err != nil {
		t.Fatalf("default-v1 应编译通过: %v", err)
	}
	if c.ModRuleID() != "mods" {
		t.Errorf("ModRuleID = %q, want mods", c.ModRuleID())
	}
	if len(c.FileRules()) != 0 {
		t.Errorf("default-v1 不应有文件规则, got %d", len(c.FileRules()))
	}
}

func TestCompileRejectsInvalidRules(t *testing.T) {
	cases := []struct {
		name  string
		rule  model.MappingRule
		field string
	}{
		{"空规则 ID", func() model.MappingRule { r := validRule("x"); r.ID = ""; return r }(), "id"},
		{"未知资源类型", func() model.MappingRule { r := validRule("x"); r.ResourceKind = "registry"; return r }(), "resource_kind"},
		{"未知方向", func() model.MappingRule { r := validRule("x"); r.Direction = "both_ways"; return r }(), "direction"},
		{"未知物化方式", func() model.MappingRule { r := validRule("x"); r.Materialization = "hardlink"; return r }(), "materialization"},
		{"未知合并策略", func() model.MappingRule { r := validRule("x"); r.MergePolicy = "json_merge"; return r }(), "merge_policy"},
		{"未知运行时本地策略", func() model.MappingRule { r := validRule("x"); r.RuntimeLocalPolicy = "sync"; return r }(), "runtime_local"},
		{"文件规则前缀为空", func() model.MappingRule { r := validRule("x"); r.ProjectPrefix = ""; return r }(), "project_prefix"},
		{"前缀越界（..）", func() model.MappingRule { r := validRule("x"); r.RuntimePrefix = "../escape"; return r }(), "runtime_prefix"},
		{"前缀绝对路径", func() model.MappingRule { r := validRule("x"); r.ProjectPrefix = "/etc"; return r }(), "project_prefix"},
		{"前缀盘符", func() model.MappingRule { r := validRule("x"); r.ProjectPrefix = "C:/mods2"; return r }(), "project_prefix"},
		{"前缀保留 mods", func() model.MappingRule {
			r := validRule("x")
			r.ProjectPrefix = "mods"
			r.RuntimePrefix = "mods"
			return r
		}(), "project_prefix"},
		{"前缀保留 mods 子树", func() model.MappingRule { r := validRule("x"); r.RuntimePrefix = "mods/shaderpacks"; return r }(), "runtime_prefix"},
		{"非法 include glob", func() model.MappingRule { r := validRule("x"); r.Include = []string{"config/[a"}; return r }(), "include"},
		{"非法 exclude glob", func() model.MappingRule { r := validRule("x"); r.Exclude = []string{"../out"}; return r }(), "exclude"},
		{"mod 规则前缀非 mods", func() model.MappingRule { r := modRule(); r.ID = "mods2"; r.ProjectPrefix = "modz"; return r }(), "project_prefix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := model.MappingPolicy{
				SchemaVersion: model.CurrentSchemaVersion,
				PolicyID:      "p1",
				Revision:      1,
				Rules:         []model.MappingRule{modRule(), c.rule},
			}
			_, err := Compile(p)
			if err == nil {
				t.Fatal("应编译失败")
			}
			var re *RuleError
			if !errors.As(err, &re) {
				t.Fatalf("应返回 *RuleError, got %T: %v", err, err)
			}
			if re.Field != c.field {
				t.Errorf("RuleError.Field = %q, want %q (%v)", re.Field, c.field, re)
			}
		})
	}
}

func TestCompileRejectsDuplicateRuleID(t *testing.T) {
	p := model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion,
		PolicyID:      "p1",
		Revision:      1,
		Rules:         []model.MappingRule{modRule(), validRule("mods")},
	}
	_, err := Compile(p)
	var re *RuleError
	if !errors.As(err, &re) || re.Field != "id" {
		t.Fatalf("重复规则 ID 应报 id 字段错误, got %v", err)
	}
}

func TestCompileRejectsDuplicateModRule(t *testing.T) {
	p := model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion,
		PolicyID:      "p1",
		Revision:      1,
		Rules:         []model.MappingRule{modRule(), modRule()},
	}
	if _, err := Compile(p); err == nil {
		t.Fatal("两条 mod 规则应编译失败（mod 语义规则至多一条）")
	}
}

func TestCompileRejectsEmptyPolicyID(t *testing.T) {
	p := model.MappingPolicy{SchemaVersion: model.CurrentSchemaVersion, Rules: []model.MappingRule{modRule()}}
	if _, err := Compile(p); err == nil {
		t.Fatal("空 PolicyID 应编译失败")
	}
}

func TestCompileRequiresModRule(t *testing.T) {
	// 缺少 mod 语义规则 → mods/ 观察将带空 PolicyID、方向失控，编译期拒绝
	p := model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion,
		PolicyID:      "p1",
		Revision:      1,
		Rules:         []model.MappingRule{validRule("config")},
	}
	_, err := Compile(p)
	var re *RuleError
	if !errors.As(err, &re) || re.Field != "resource_kind" {
		t.Fatalf("缺少 mod 语义规则应编译失败, got %v", err)
	}
}

func TestCompileNormalizesPrefixes(t *testing.T) {
	r := validRule("cfg")
	r.ProjectPrefix = "Config/"
	r.RuntimePrefix = "CONFIG\\Sub"
	c, err := Compile(model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion, PolicyID: "p1", Revision: 1,
		Rules: []model.MappingRule{modRule(), r},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	fr := c.FileRules()[0]
	if fr.Rule.ID != "cfg" {
		t.Fatalf("FileRules 顺序错误: %+v", fr)
	}
	if got := fr.SidePrefix(model.SideProject); got != "config" {
		t.Errorf("project prefix = %q, want config", got)
	}
	if got := fr.SidePrefix(model.SideRuntime); got != "config/sub" {
		t.Errorf("runtime prefix = %q, want config/sub", got)
	}
}

func TestResolveFileRuleMostSpecificWins(t *testing.T) {
	shallowRule := validRule("config")
	deepRule := validRule("config-deep")
	deepRule.ProjectPrefix = "config/deep"
	deepRule.RuntimePrefix = "config/deep"
	c := mustCompile(t, model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion, PolicyID: "p1", Revision: 1,
		Rules: []model.MappingRule{modRule(), shallowRule, deepRule},
	})
	shallow := &c.fileRules[0]
	deep := &c.fileRules[1]

	cands := []*CompiledFileRule{shallow, deep}
	winner, collision := ResolveFileRule(model.SideProject, cands)
	if collision != nil {
		t.Fatalf("嵌套前缀应最具体胜出, got collision %v", collision)
	}
	if winner.Rule.ID != "config-deep" {
		t.Errorf("winner = %q, want config-deep", winner.Rule.ID)
	}
	// runtime 侧对称
	if winner, collision = ResolveFileRule(model.SideRuntime, cands); collision != nil || winner.Rule.ID != "config-deep" {
		t.Errorf("runtime 侧决议错误: winner=%v collision=%v", winner, collision)
	}
}

func TestResolveFileRuleCollision(t *testing.T) {
	c := mustCompile(t, model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion, PolicyID: "p1", Revision: 1,
		Rules: []model.MappingRule{
			modRule(),
			validRule("aaa"),
			validRule("zzz"),
		},
	})
	// 两条规则同前缀 → 同路径无法唯一决议
	a := &c.fileRules[0]
	z := &c.fileRules[1]
	winner, collision := ResolveFileRule(model.SideProject, []*CompiledFileRule{z, a})
	if winner != nil {
		t.Errorf("同前缀并列不应有 winner, got %q", winner.Rule.ID)
	}
	if !reflect.DeepEqual(collision, []string{"aaa", "zzz"}) {
		t.Errorf("collision 证据 = %v, want [aaa zzz]（字节序）", collision)
	}
}

func TestResolveFileRuleIncludeExcludeDisambiguates(t *testing.T) {
	// 同前缀但 include/exclude 互斥时可以唯一决议
	r1 := validRule("toml-only")
	r1.Include = []string{"config/*.toml"}
	r2 := validRule("rest")
	r2.Exclude = []string{"config/*.toml"}
	c := mustCompile(t, model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion, PolicyID: "p1", Revision: 1,
		Rules: []model.MappingRule{modRule(), r1, r2},
	})
	tomlOnly := &c.fileRules[0]
	rest := &c.fileRules[1]

	if !tomlOnly.Matches("config/a.toml") || rest.Matches("config/a.toml") {
		t.Fatal("include/exclude 划分不正确")
	}
	if !rest.Matches("config/b.json") || tomlOnly.Matches("config/b.json") {
		t.Fatal("rest 规则划分不正确")
	}
	// 模拟扫描器流程：先按 Matches 过滤候选，再决议
	filter := func(path string) []*CompiledFileRule {
		var cands []*CompiledFileRule
		for i := range c.fileRules {
			if c.fileRules[i].Matches(path) {
				cands = append(cands, &c.fileRules[i])
			}
		}
		return cands
	}
	winner, collision := ResolveFileRule(model.SideProject, filter("config/a.toml"))
	if collision != nil || winner == nil || winner.Rule.ID != "toml-only" {
		t.Fatalf("互斥划分应唯一决议: winner=%v collision=%v", winner, collision)
	}
	winner, collision = ResolveFileRule(model.SideProject, filter("config/b.json"))
	if collision != nil || winner == nil || winner.Rule.ID != "rest" {
		t.Fatalf("b.json 应唯一决议给 rest: winner=%v collision=%v", winner, collision)
	}
}

func TestResolveFileRuleEmptyCandidates(t *testing.T) {
	winner, collision := ResolveFileRule(model.SideProject, nil)
	if winner != nil || collision != nil {
		t.Fatalf("空候选应返回 nil/nil, got %v %v", winner, collision)
	}
}

func TestTemplateAlwaysCompiles(t *testing.T) {
	// 模板与建议片段合并后（前端勾选场景）必须可编译
	tpl := DefaultV1()
	tpl.Rules = append(tpl.Rules, Suggestions()...)
	if _, err := Compile(tpl); err != nil {
		t.Fatalf("default-v1 + suggestions 应编译通过: %v", err)
	}
}

func mustCompile(t *testing.T, p model.MappingPolicy) *Compiled {
	t.Helper()
	c, err := Compile(p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return c
}
