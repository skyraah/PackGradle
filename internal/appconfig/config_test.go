package appconfig

import (
	"path/filepath"
	"testing"
)

// newTestConfig 用临时目录构造 ConfigManager，避免污染真实用户配置
func newTestConfig(t *testing.T) *ConfigManager {
	t.Helper()
	return NewConfigManagerAt(filepath.Join(t.TempDir(), "config.toml"))
}

// API Key 的保存、磁盘持久化与清除
func TestConfigApiKeyPersist(t *testing.T) {
	m := newTestConfig(t)

	if err := m.SetApiKey("abc-123-xyz"); err != nil {
		t.Fatalf("SetApiKey: %v", err)
	}
	if got := m.Get().CurseforgeApiKey; got != "abc-123-xyz" {
		t.Errorf("Get 应返回已保存的 key，实际 %q", got)
	}

	// 重新从磁盘加载，验证持久化
	m2 := &ConfigManager{path: m.path}
	if err := ReadToml(m2.path, &m2.cfg); err != nil {
		t.Fatalf("重新读取配置失败: %v", err)
	}
	if m2.cfg.CurseforgeApiKey != "abc-123-xyz" {
		t.Errorf("配置文件应包含 key，实际 %q", m2.cfg.CurseforgeApiKey)
	}

	// 空串清除
	if err := m.SetApiKey(""); err != nil {
		t.Fatalf("SetApiKey(\"\"): %v", err)
	}
	if got := m.Get().CurseforgeApiKey; got != "" {
		t.Errorf("清除后应为空，实际 %q", got)
	}
}

// 项目的新增、同名覆盖路径、查找与移除
func TestConfigProjects(t *testing.T) {
	m := newTestConfig(t)

	if err := m.AddProject(ProjectEntry{Name: "A", Path: `C:\packs\a`}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddProject(ProjectEntry{Name: "B", Path: `C:\packs\b`}); err != nil {
		t.Fatal(err)
	}
	// 同名项目更新路径，不新增条目
	if err := m.AddProject(ProjectEntry{Name: "A", Path: `C:\packs\a2`}); err != nil {
		t.Fatal(err)
	}
	if got := m.Get().Projects; len(got) != 2 {
		t.Fatalf("应有 2 个项目，实际 %d: %+v", len(got), got)
	}

	entry, ok := m.FindProject("A")
	if !ok || entry.Path != `C:\packs\a2` {
		t.Errorf("FindProject 应返回更新后的路径，实际 %+v ok=%v", entry, ok)
	}
	if _, ok := m.FindProject("Missing"); ok {
		t.Error("不存在的项目不应命中")
	}

	if err := m.RemoveProject("A"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.FindProject("A"); ok {
		t.Error("移除后不应再找到")
	}
	if got := m.Get().Projects; len(got) != 1 || got[0].Name != "B" {
		t.Errorf("移除后应只剩 B，实际 %+v", got)
	}
}

// 工具路径的保存、清除与非法工具名校验
func TestConfigSetToolPath(t *testing.T) {
	m := newTestConfig(t)

	if err := m.SetToolPath("packwiz", `C:\bin\packwiz.exe`); err != nil {
		t.Fatal(err)
	}
	if got := m.Get().PackwizPath; got != `C:\bin\packwiz.exe` {
		t.Errorf("packwiz 路径未保存，实际 %q", got)
	}
	if err := m.SetToolPath("prism-launcher", `C:\bin\prismlauncher.exe`); err != nil {
		t.Fatal(err)
	}
	if got := m.Get().PrismPath; got != `C:\bin\prismlauncher.exe` {
		t.Errorf("prism 路径未保存，实际 %q", got)
	}
	if err := m.SetToolPath("unknown", "x"); err == nil {
		t.Error("非法工具名应报错")
	}
	if err := m.SetToolPath("packwiz", ""); err != nil {
		t.Fatal(err)
	}
	if got := m.Get().PackwizPath; got != "" {
		t.Errorf("空串应清除路径，实际 %q", got)
	}
}

// 项目 ↔ 实例关联的增改查删与磁盘持久化
func TestConfigLinks(t *testing.T) {
	m := newTestConfig(t)
	if err := m.SetLink(ProjectLink{Project: "A", Instance: "inst-a"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetLink(ProjectLink{Project: "B", Instance: "inst-b"}); err != nil {
		t.Fatal(err)
	}
	// 同名项目覆盖
	if err := m.SetLink(ProjectLink{Project: "A", Instance: "inst-a2"}); err != nil {
		t.Fatal(err)
	}
	if got := m.Get().Links; len(got) != 2 {
		t.Fatalf("应有 2 条关联，实际 %d: %+v", len(got), got)
	}
	link, ok := m.FindLink("A")
	if !ok || link.Instance != "inst-a2" {
		t.Errorf("A 应关联 inst-a2，实际 %+v ok=%v", link, ok)
	}
	if err := m.RemoveLink("A"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.FindLink("A"); ok {
		t.Error("移除后 A 不应存在")
	}
}

// 目录关联对的增删查
func TestConfigDirLinks(t *testing.T) {
	m := newTestConfig(t)
	if err := m.AddDirLink(DirLink{Project: "A", Instance: "inst-a", ProjectDir: "config", InstanceDir: "config"}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddDirLink(DirLink{Project: "A", Instance: "inst-a", ProjectDir: "kubejs", InstanceDir: "kubejs"}); err != nil {
		t.Fatal(err)
	}
	// 同项目同目录覆盖
	if err := m.AddDirLink(DirLink{Project: "A", Instance: "inst-a", ProjectDir: "config", InstanceDir: "configs"}); err != nil {
		t.Fatal(err)
	}
	got := m.FindDirLinks("A")
	if len(got) != 2 {
		t.Fatalf("应有 2 条目录关联，实际 %d: %+v", len(got), got)
	}
	for _, l := range got {
		if l.ProjectDir == "config" && l.InstanceDir != "configs" {
			t.Errorf("config 应覆盖为 configs: %+v", l)
		}
	}
	if err := m.RemoveDirLink("A", "config"); err != nil {
		t.Fatal(err)
	}
	if got := m.FindDirLinks("A"); len(got) != 1 {
		t.Errorf("移除后应剩 1 条，实际 %d", len(got))
	}
}
