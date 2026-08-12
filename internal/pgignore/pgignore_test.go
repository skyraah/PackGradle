package pgignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 导入时创建默认 .pgignore，已存在不覆盖
func TestEnsureCreatesDefault(t *testing.T) {
	dir := t.TempDir()
	created, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("首次应创建")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".pgignore"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, entry := range []string{".git", ".cache", "index.toml", "pack.toml", "packgradle.toml", ".pgignore"} {
		if !strings.Contains(content, entry) {
			t.Errorf("默认内容应包含 %s", entry)
		}
	}
	// 再次调用不覆盖
	created2, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Error("已存在时不应重复创建")
	}
}

// gitignore 标准规则匹配（相对项目根的条目名）
func TestMatcherDefault(t *testing.T) {
	dir := t.TempDir()
	if _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	m := Load(dir)
	cases := []struct {
		path string
		want bool
	}{
		{".git", true},
		{".cache", true},
		{"index.toml", true},
		{"pack.toml", true},
		{"packgradle.toml", true},
		{".pgignore", true},
		{"config", false},
		{"kubejs", false},
		{"texturepacks", false},
		{"mods", false}, // mods 由服务层内建排除，不在 ignore 规则中
	}
	for _, c := range cases {
		if got := m.Matches(c.path); got != c.want {
			t.Errorf("Matches(%q) = %v，期望 %v", c.path, got, c.want)
		}
	}
}

// 无 .pgignore：空匹配器（全部不忽略）
func TestMatcherMissing(t *testing.T) {
	m := Load(t.TempDir())
	if m.Matches("anything") {
		t.Error("缺失文件时不应匹配任何条目")
	}
}

// 用户自定义规则生效（如追加 mods 或通配符）
func TestMatcherCustomRules(t *testing.T) {
	dir := t.TempDir()
	content := DefaultContent + "\n*.log\ntemp/\n"
	if err := os.WriteFile(filepath.Join(dir, ".pgignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Load(dir)
	if !m.Matches("debug.log") {
		t.Error("*.log 应命中")
	}
	if !m.Matches("temp") {
		t.Error("temp/ 应命中目录")
	}
	if m.Matches("config") {
		t.Error("config 不应命中")
	}
}
