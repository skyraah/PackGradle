package appconfig

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"packgradle/internal/errs"
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

// 项目级配置（packgradle.toml）的读写与磁盘持久化
func TestProjectConfigReadWrite(t *testing.T) {
	dir := t.TempDir()
	pc := ProjectConfig{Instance: "inst-a"}
	pc.DirLinks = append(pc.DirLinks, ProjectDirLink{ProjectDir: "config", InstanceDir: "config"})
	if err := SaveProjectConfig(dir, pc); err != nil {
		t.Fatal(err)
	}
	// 文件应位于项目根目录，与 pack.toml 同层
	if _, err := os.Stat(filepath.Join(dir, "packgradle.toml")); err != nil {
		t.Error("packgradle.toml 应位于项目根目录")
	}
	got, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Instance != "inst-a" || len(got.DirLinks) != 1 || got.DirLinks[0].ProjectDir != "config" {
		t.Errorf("读取结果错误: %+v", got)
	}
}

// 项目级配置：文件不存在时返回零值（未关联）
func TestProjectConfigMissing(t *testing.T) {
	got, err := LoadProjectConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got.Instance != "" || len(got.DirLinks) != 0 {
		t.Errorf("缺失文件应返回零值，实际 %+v", got)
	}
}

// 旧版全局 [[links]]/[[dir_links]] 一次性迁移到项目级 packgradle.toml
func TestMigrateLegacyProjectConfigs(t *testing.T) {
	m := newTestConfig(t)
	projDir := t.TempDir()
	m.cfg.Projects = []ProjectEntry{{Name: "A", Path: projDir}}
	m.cfg.LegacyLinks = []legacyLink{{Project: "A", Instance: "inst-a"}}
	m.cfg.LegacyDirLinks = []legacyDirLink{
		{Project: "A", Instance: "inst-a", ProjectDir: "config", InstanceDir: "config"},
		{Project: "A", Instance: "inst-a", ProjectDir: "kubejs", InstanceDir: "kubejs"},
	}
	if err := m.MigrateLegacyProjectConfigs(); err != nil {
		t.Fatal(err)
	}
	pc, err := LoadProjectConfig(projDir)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Instance != "inst-a" || len(pc.DirLinks) != 2 {
		t.Errorf("项目级配置应包含迁移数据，实际 %+v", pc)
	}
	// 全局旧字段应清空并落盘
	if len(m.cfg.LegacyLinks) != 0 || len(m.cfg.LegacyDirLinks) != 0 {
		t.Errorf("迁移后全局旧字段应清空: %+v", m.cfg)
	}
	// 幂等：再次执行无副作用
	if err := m.MigrateLegacyProjectConfigs(); err != nil {
		t.Fatal(err)
	}
}

// 迁移时项目已不存在则跳过
func TestMigrateLegacySkipsMissingProject(t *testing.T) {
	m := newTestConfig(t)
	m.cfg.LegacyLinks = []legacyLink{{Project: "Gone", Instance: "inst-a"}}
	if err := m.MigrateLegacyProjectConfigs(); err != nil {
		t.Fatal(err)
	}
	if len(m.cfg.LegacyLinks) != 0 {
		t.Error("不存在的项目应跳过且清空旧字段")
	}
}

// [download] concurrency 加载层校验（ADR-0008 §4）：未配置取默认 6，
// 合法 1–16，越界（0/17 等）加载报错（AC：0/17 拒绝）
func TestConfigDownloadConcurrency(t *testing.T) {
	writeConfig := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("未配置取默认6", func(t *testing.T) {
		cfg, err := LoadConfigFrom(writeConfig(t, ""))
		if err != nil {
			t.Fatalf("空配置应加载成功: %v", err)
		}
		if got := cfg.Download.EffectiveConcurrency(); got != 6 {
			t.Fatalf("未配置应取默认 6，实际 %d", got)
		}
	})

	t.Run("边界1与16合法", func(t *testing.T) {
		for _, n := range []int{1, 16, 6} {
			cfg, err := LoadConfigFrom(writeConfig(t, "[download]\nconcurrency = "+itoa(n)+"\n"))
			if err != nil {
				t.Fatalf("concurrency = %d 应合法: %v", n, err)
			}
			if got := cfg.Download.EffectiveConcurrency(); got != n {
				t.Fatalf("concurrency = %d 应生效，实际 %d", n, got)
			}
		}
	})

	t.Run("越界0与17拒绝", func(t *testing.T) {
		for _, n := range []int{0, 17, -3, 100} {
			_, err := LoadConfigFrom(writeConfig(t, "[download]\nconcurrency = "+itoa(n)+"\n"))
			if err == nil {
				t.Fatalf("concurrency = %d 应被拒绝", n)
			}
			if code := errs.CodeOf(err); code != "err.config.download_concurrency_invalid" {
				t.Fatalf("越界 %d 错误码不符: %q", n, code)
			}
		}
	})

	t.Run("越界值不落盘改写", func(t *testing.T) {
		// 加载失败路径：配置文件保持原样（fail fast 由用户修复，不静默改写）
		path := writeConfig(t, "[download]\nconcurrency = 0\n")
		if _, err := LoadConfigFrom(path); err == nil {
			t.Fatal("0 应被拒绝")
		}
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "concurrency = 0") {
			t.Fatalf("加载失败不得改写配置文件: %v", err)
		}
	})
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
