package main

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestConfig 用临时目录构造 ConfigManager，避免污染真实用户配置
func newTestConfig(t *testing.T) *ConfigManager {
	t.Helper()
	return &ConfigManager{path: filepath.Join(t.TempDir(), "config.toml")}
}

// fakeTool 在临时目录中创建一个假的可执行文件，返回其完整路径
func fakeTool(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".exe")
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// withEnv 临时覆盖环境变量，测试结束后恢复
func withEnv(t *testing.T, key, value string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// packwiz 应从环境变量 PATH 中检测到，并持久化到 config.toml
func TestDetectPackwizFromPathAndPersist(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "packwiz")
	withEnv(t, "PATH", dir+";"+os.Getenv("PATH"))

	svc := NewEnvService(newTestConfig(t))
	info := svc.detectPackwiz()

	if !info.Found || info.Source != "path" {
		t.Fatalf("应从 PATH 检测到 packwiz，实际 found=%v source=%s", info.Found, info.Source)
	}
	if got := svc.config.Get().PackwizPath; got != info.Path {
		t.Errorf("检测到的路径应写入 config.toml，config=%q 期望=%q", got, info.Path)
	}
}

// packwiz 的 fallback：PATH 中没有时，应检测 %USERPROFILE%\go\bin
func TestDetectPackwizFromGoBin(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, filepath.Join(dir, "go", "bin"), "packwiz")
	withEnv(t, "USERPROFILE", dir)
	// 清空 PATH，避免命中系统里真实安装的 packwiz
	withEnv(t, "PATH", t.TempDir()) // 指向无 packwiz 的临时目录

	svc := NewEnvService(newTestConfig(t))
	info := svc.detectPackwiz()

	if !info.Found || info.Source != "default-dir" {
		t.Fatalf("应从 %%USERPROFILE%%\\go\\bin 检测到 packwiz，实际 found=%v source=%s", info.Found, info.Source)
	}
}

// config.toml 中已保存的有效路径应优先使用，不被覆盖
func TestDetectPackwizPrefersConfigPath(t *testing.T) {
	saved := fakeTool(t, t.TempDir(), "packwiz")
	// PATH 里放另一个 packwiz，验证 config 优先
	other := fakeTool(t, t.TempDir(), "packwiz")
	withEnv(t, "PATH", filepath.Dir(other)+";"+os.Getenv("PATH"))

	cm := newTestConfig(t)
	if err := cm.SetToolPath("packwiz", saved); err != nil {
		t.Fatal(err)
	}
	svc := NewEnvService(cm)
	info := svc.detectPackwiz()

	if !info.Found || info.Source != "config" || info.Path != saved {
		t.Fatalf("应优先使用 config 中的路径 %q，实际 path=%q source=%s", saved, info.Path, info.Source)
	}
	if got := cm.Get().PackwizPath; got != saved {
		t.Errorf("config 路径不应被覆盖，config=%q", got)
	}
}

// config.toml 中的路径无效（文件被删除）时，应重新检测并覆盖写入
func TestDetectPackwizRefallsWhenConfigInvalid(t *testing.T) {
	dir := t.TempDir()
	found := fakeTool(t, dir, "packwiz")
	withEnv(t, "PATH", dir+";"+os.Getenv("PATH"))

	cm := newTestConfig(t)
	// 写入一个不存在的路径模拟 config 失效
	if err := cm.SetToolPath("packwiz", filepath.Join(t.TempDir(), "missing.exe")); err != nil {
		t.Fatal(err)
	}
	svc := NewEnvService(cm)
	info := svc.detectPackwiz()

	if !info.Found || info.Source != "path" {
		t.Fatalf("config 失效后应从 PATH 重新检测，实际 found=%v source=%s", info.Found, info.Source)
	}
	if got := cm.Get().PackwizPath; got != found {
		t.Errorf("重新检测到的路径应覆盖写入 config，config=%q 期望=%q", got, found)
	}
}

// prism 应从默认路径 %LOCALAPPDATA%\Programs\PrismLauncher 检测到
func TestDetectPrismFromLocalAppData(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, filepath.Join(dir, "Programs", "PrismLauncher"), "prismlauncher")
	withEnv(t, "LOCALAPPDATA", dir)
	withEnv(t, "ProgramFiles", t.TempDir())
	withEnv(t, "ProgramFiles(x86)", t.TempDir())

	svc := NewEnvService(newTestConfig(t))
	info := svc.detectPrism()

	if !info.Found || info.Source != "default-dir" {
		t.Fatalf("应从默认路径检测到 prism，实际 found=%v source=%s", info.Found, info.Source)
	}
	if got := svc.config.Get().PrismPath; got != info.Path {
		t.Errorf("检测到的路径应写入 config.toml，config=%q", got)
	}
}

// prism 的 fallback：Program Files 下带空格目录（如 "Prism Launcher"）也应命中
func TestDetectPrismFromProgramFilesScan(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, filepath.Join(dir, "Prism Launcher"), "prismlauncher")
	withEnv(t, "ProgramFiles", dir)
	withEnv(t, "ProgramFiles(x86)", t.TempDir())
	withEnv(t, "LOCALAPPDATA", t.TempDir())

	svc := NewEnvService(newTestConfig(t))
	info := svc.detectPrism()

	if !info.Found || info.Source != "default-dir" {
		t.Fatalf("应从 Program Files 扫描到 prism，实际 found=%v source=%s", info.Found, info.Source)
	}
}

// 完全找不到时返回 Found=false 且不写 config
func TestDetectNotFound(t *testing.T) {
	withEnv(t, "PATH", t.TempDir())
	withEnv(t, "USERPROFILE", t.TempDir())
	withEnv(t, "LOCALAPPDATA", t.TempDir())
	withEnv(t, "ProgramFiles", t.TempDir())
	withEnv(t, "ProgramFiles(x86)", t.TempDir())

	svc := NewEnvService(newTestConfig(t))
	if info := svc.detectPackwiz(); info.Found {
		t.Error("packwiz 不应被检测到")
	}
	if info := svc.detectPrism(); info.Found {
		t.Error("prism 不应被检测到")
	}
}
