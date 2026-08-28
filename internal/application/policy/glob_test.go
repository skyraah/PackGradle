package policy

import "testing"

func TestGlobMatchPath(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// 字面匹配
		{"config", "config", true},
		{"config", "config/foo.ini", true}, // 祖先目录命中（既有语义：目录模式含子树）
		{"config", "configx/a.txt", false}, // 前缀字符串相同但非完整段
		{"config", "other/config", false},
		// 单段通配
		{"config/*.toml", "config/a.toml", true},
		{"config/*.toml", "config/a/b.toml", false}, // * 不跨段
		{"config/*.toml", "config/a/b/c.toml", false},
		{"config/?.ini", "config/a.ini", true},
		{"config/?.ini", "config/ab.ini", false},
		{"*", "a.txt", true},
		{"*", "dir/a.txt", true}, // 祖先语义：* 命中祖先目录 dir（与既有扫描行为一致）
		{"*", "dir/sub/a.txt", true},
		// ** 跨段
		{"**", "a.txt", true},
		{"**", "dir/sub/a.txt", true},
		{"config/**", "config/a.toml", true},
		{"config/**", "config/a/b.toml", true},
		{"config/**", "other/a.toml", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/b/c", true}, // 祖先语义：模式命中祖先 a/x/b
		{"a/**/b", "x/a/b", false},  // 根段不匹配
		{"**/*.toml", "x.toml", true},
		{"**/*.toml", "a/b/x.toml", true},
		// 字符类
		{"config/[abc].ini", "config/a.ini", true},
		{"config/[abc].ini", "config/d.ini", false},
		{"config/[a-z].ini", "config/m.ini", true},
		{"config/[!a].ini", "config/b.ini", false}, // [! 不支持（与 path.Match 的 [^ 一致性优先）
		{"config/[^a].ini", "config/b.ini", true},
		{"config/[^a].ini", "config/a.ini", false},
		{"config/[a.].ini", "config/a.ini", true},
	}
	for _, c := range cases {
		g, err := CompileGlob(c.pattern)
		if err != nil {
			t.Fatalf("CompileGlob(%q): %v", c.pattern, err)
		}
		if got := g.MatchPath(c.path); got != c.want {
			t.Errorf("glob %q MatchPath(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestCompileGlobNormalizes(t *testing.T) {
	// 大小写与反斜杠归一化
	g, err := CompileGlob("Config\\Sub\\*.TOML")
	if err != nil {
		t.Fatalf("CompileGlob: %v", err)
	}
	if !g.MatchPath("config/sub/a.toml") {
		t.Error("归一化后的模式应匹配小写斜杠路径")
	}
	if g.Pattern() != "config/sub/*.toml" {
		t.Errorf("Pattern() = %q, want 归一化形式", g.Pattern())
	}
}

func TestCompileGlobRejectsInvalid(t *testing.T) {
	patterns := []string{
		"",               // 空
		"/abs/path",      // 绝对路径
		"..",             // 越界
		"config/../mods", // 含 .. 段
		"./config",       // 含 . 段
		"C:/config",      // 盘符（冒号）
		"config/:x",      // 冒号
		"config/[abc",    // 未闭合字符类
		"config/[]",      // 空字符类
		"config/a[b",     // 未闭合
	}
	for _, p := range patterns {
		if _, err := CompileGlob(p); err == nil {
			t.Errorf("CompileGlob(%q) 应报错", p)
		}
	}
}
