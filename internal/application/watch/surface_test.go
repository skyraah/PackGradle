package watch

import (
	"path/filepath"
	"testing"

	"packgradle/internal/application/policy"
	"packgradle/internal/core/model"
)

// 监听面 = MappingPolicy 的函数（P4 验收规格 §4.1，票 #92；ADR-0010 §3）：
// 项目侧 pack.toml + 管辖前缀目录、运行侧 minecraft/ 同名目录、排除集、
// direction=ignore 不监听、事件匹配语义（大小写不敏感、段边界严格）。

// TestSurfaceForDefaultV1WithSuggestions 监听面计算：default-v1 + 全部建议规则
// → 项目侧 pack.toml + mods/config/kubejs/scripts/defaultconfigs，运行侧
// minecraft/（Runtime.RootPath）下同名目录。
func TestSurfaceForDefaultV1WithSuggestions(t *testing.T) {
	pol, err := policy.MergeSuggestions(policy.DefaultV1(), []string{"config", "kubejs", "scripts", "defaultconfigs"})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := `C:\packs\Collapse`
	runtimeRoot := `C:\instances\Collapse\minecraft`
	targets := SurfaceFor(pol, projectRoot, runtimeRoot, "rel_x")

	want := []SurfaceTarget{
		{RelationID: "rel_x", Dir: projectRoot, File: "pack.toml"},
		{RelationID: "rel_x", Dir: filepath.Join(projectRoot, "mods")},
		{RelationID: "rel_x", Dir: filepath.Join(runtimeRoot, "mods")},
		{RelationID: "rel_x", Dir: filepath.Join(projectRoot, "config")},
		{RelationID: "rel_x", Dir: filepath.Join(runtimeRoot, "config")},
		{RelationID: "rel_x", Dir: filepath.Join(projectRoot, "kubejs")},
		{RelationID: "rel_x", Dir: filepath.Join(runtimeRoot, "kubejs")},
		{RelationID: "rel_x", Dir: filepath.Join(projectRoot, "scripts")},
		{RelationID: "rel_x", Dir: filepath.Join(runtimeRoot, "scripts")},
		{RelationID: "rel_x", Dir: filepath.Join(projectRoot, "defaultconfigs")},
		{RelationID: "rel_x", Dir: filepath.Join(runtimeRoot, "defaultconfigs")},
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %d 项, 期望 %d 项: %+v", len(targets), len(want), targets)
	}
	for i, w := range want {
		if targets[i] != w {
			t.Errorf("targets[%d] = %+v, 期望 %+v", i, targets[i], w)
		}
	}
}

// TestSurfaceForIgnoresIgnoreDirectionAndEmptyRoots direction=ignore 的规则不监听；
// 根路径为空的一侧不产出目标（端点漂移期无可挂面）。
func TestSurfaceForIgnoresIgnoreDirectionAndEmptyRoots(t *testing.T) {
	pol := model.MappingPolicy{
		Rules: []model.MappingRule{
			{ID: "mods", ProjectPrefix: "mods", RuntimePrefix: "mods", Direction: "bidirectional"},
			{ID: "noise", ProjectPrefix: "noise", RuntimePrefix: "noise", Direction: "ignore"},
		},
	}
	targets := SurfaceFor(pol, `C:\packs\Collapse`, "", "rel_x")
	if len(targets) != 2 { // pack.toml + 项目 mods；noise 与运行侧均不产出
		t.Fatalf("targets = %+v", targets)
	}
	for _, tg := range targets {
		if tg.Dir == "" || containsStr(tg.Dir, "noise") {
			t.Errorf("ignore 规则不应产出监听目标: %+v", tg)
		}
	}
	// 双根皆空：无目标
	if got := SurfaceFor(pol, "", "", "rel_x"); len(got) != 0 {
		t.Fatalf("空根不应产出目标: %+v", got)
	}
}

// TestExcludedDirNames 递归补挂排除集：mods/.index、logs/saves 等非管辖树、
// Prism 自有元数据。
func TestExcludedDirNames(t *testing.T) {
	for _, name := range []string{".index", "logs", "saves", ".mmc", ".fabric", "LOGS"} {
		if !ExcludedDirName(name) {
			t.Errorf("%q 应在排除集内", name)
		}
	}
	for _, name := range []string{"mods", "config", "resourcepacks", "index"} {
		if ExcludedDirName(name) {
			t.Errorf("%q 不应被排除", name)
		}
	}
}

// TestEventMatchesTarget 事件匹配语义：管辖子树内触发、兄弟目录不触发；
// pack.toml 目标只认文件自身；Windows 大小写不敏感。
func TestEventMatchesTarget(t *testing.T) {
	fileTarget := SurfaceTarget{RelationID: "rel_x", Dir: `C:\packs\Collapse`, File: "pack.toml"}
	dirTarget := SurfaceTarget{RelationID: "rel_x", Dir: `C:\packs\Collapse\mods`}

	cases := []struct {
		name      string
		eventPath string
		target    SurfaceTarget
		want      bool
	}{
		{"pack.toml 自身", `C:\packs\Collapse\pack.toml`, fileTarget, true},
		{"项目根其他文件不触发", `C:\packs\Collapse\packwiz.toml`, fileTarget, false},
		{"项目根目录事件不触发文件目标", `C:\packs\Collapse`, fileTarget, false},
		{"大小写不敏感", `c:\packs\collapse\PACK.TOML`, fileTarget, true},
		{"管辖子树内", `C:\packs\Collapse\mods\a.jar`, dirTarget, true},
		{"目标目录本身", `C:\packs\Collapse\mods`, dirTarget, true},
		{"兄弟目录不触发", `C:\packs\Collapse\config\x.toml`, dirTarget, false},
		{"段边界严格（前缀撞名不触发）", `C:\packs\Collapse\mods2\x.jar`, dirTarget, false},
	}
	for _, c := range cases {
		if got := eventMatchesTarget(c.eventPath, c.target); got != c.want {
			t.Errorf("%s: eventMatchesTarget(%q) = %v, 期望 %v", c.name, c.eventPath, got, c.want)
		}
	}
}

// TestAncestorHelpers 回退重挂的祖先/子树判定（目录再现向下迁移的依据）。
func TestAncestorHelpers(t *testing.T) {
	if !isStrictAncestor(`C:\packs\Collapse`, `C:\packs\Collapse\mods`) {
		t.Error("父目录应为目标严格祖先")
	}
	if !isStrictAncestor(`C:\packs\Collapse`, `C:\PACKS\COLLAPSE\MODS`) {
		t.Error("祖先判定应大小写不敏感")
	}
	if isStrictAncestor(`C:\packs\Collapse\mods`, `C:\packs\Collapse\mods`) {
		t.Error("自身不是严格祖先")
	}
	if isStrictAncestor(`C:\packs\Collapse\mods`, `C:\packs\Collapse\mods2`) {
		t.Error("段边界严格：mods2 不是 mods 的子树")
	}
	if !isStrictUnder(`C:\packs\Collapse\mods\sub\x.jar`, `C:\packs\Collapse\mods`) {
		t.Error("子树内路径应判真")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
