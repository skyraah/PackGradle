package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
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
	withEnv(t, "PACKWIZ", "") // 清空可能存在的 PACKWIZ 变量，避免误命中
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
	withEnv(t, "PACKWIZ", "") // 清空可能存在的 PACKWIZ 变量，避免误命中
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
	withEnv(t, "PRISM", "") // 清空本机可能存在的 PRISM 变量，避免误命中
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
	withEnv(t, "PRISM", "") // 清空本机可能存在的 PRISM 变量，避免误命中
	withEnv(t, "ProgramFiles", dir)
	withEnv(t, "ProgramFiles(x86)", t.TempDir())
	withEnv(t, "LOCALAPPDATA", t.TempDir())

	svc := NewEnvService(newTestConfig(t))
	info := svc.detectPrism()

	if !info.Found || info.Source != "default-dir" {
		t.Fatalf("应从 Program Files 扫描到 prism，实际 found=%v source=%s", info.Found, info.Source)
	}
}

// prism 应通过 PRISM 环境变量检测到（用户以 %PRISM% 配置的常见方式）
func TestDetectPrismFromEnvVar(t *testing.T) {
	dir := t.TempDir()
	exe := fakeTool(t, dir, "prismlauncher")
	withEnv(t, "PRISM", dir)
	withEnv(t, "LOCALAPPDATA", t.TempDir())
	withEnv(t, "ProgramFiles", t.TempDir())
	withEnv(t, "ProgramFiles(x86)", t.TempDir())

	svc := NewEnvService(newTestConfig(t))
	info := svc.detectPrism()

	if !info.Found || info.Source != "env" || info.Path != exe {
		t.Fatalf("应从 PRISM 环境变量检测到 prism，实际 found=%v source=%s path=%s", info.Found, info.Source, info.Path)
	}
}

// packwiz 应通过 PACKWIZ 环境变量检测到
func TestDetectPackwizFromEnvVar(t *testing.T) {
	dir := t.TempDir()
	exe := fakeTool(t, dir, "packwiz")
	withEnv(t, "PACKWIZ", dir)
	withEnv(t, "PATH", t.TempDir())
	withEnv(t, "USERPROFILE", t.TempDir())

	svc := NewEnvService(newTestConfig(t))
	info := svc.detectPackwiz()

	if !info.Found || info.Source != "env" || info.Path != exe {
		t.Fatalf("应从 PACKWIZ 环境变量检测到 packwiz，实际 found=%v source=%s path=%s", info.Found, info.Source, info.Path)
	}
}

// 环境变量值也可直接指向 exe 文件本身
func TestDetectFromEnvVarPointingToFile(t *testing.T) {
	exe := fakeTool(t, t.TempDir(), "prismlauncher")
	withEnv(t, "PRISM", exe)
	withEnv(t, "LOCALAPPDATA", t.TempDir())
	withEnv(t, "ProgramFiles", t.TempDir())
	withEnv(t, "ProgramFiles(x86)", t.TempDir())

	svc := NewEnvService(newTestConfig(t))
	info := svc.detectPrism()

	if !info.Found || info.Source != "env" || info.Path != exe {
		t.Fatalf("PRISM 指向 exe 文件时应直接命中，实际 found=%v source=%s path=%s", info.Found, info.Source, info.Path)
	}
}

// normalizePathEntry 应正确规范化 PATH 条目
func TestNormalizePathEntry(t *testing.T) {
	cases := map[string]string{
		`C:\Foo\Bar\`:     `c:\foo\bar`,
		`C:\Foo\Bar`:      `c:\foo\bar`,
		`  C:\Foo\Bar  `:  `c:\foo\bar`,
		`C:\Foo\Bar\Baz\`: `c:\foo\bar\baz`,
	}
	for in, want := range cases {
		if got := normalizePathEntry(in); got != want {
			t.Errorf("normalizePathEntry(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// mergePathDirs 应识别 %VAR% 形式的 PATH 条目并去重
func TestMergePathDirs(t *testing.T) {
	prismDir := `C:\Users\test\AppData\Local\Programs\PrismLauncher`
	withEnv(t, "TESTPRISM", prismDir)

	cur := `C:\Windows;%TESTPRISM%;C:\MC\packwiz`
	newCur, added := mergePathDirs(cur, []string{prismDir, `D:\new\tool`})

	if len(added) != 1 || added[0] != `D:\new\tool` {
		t.Fatalf("只应新增 D:\\new\\tool，实际 added=%v", added)
	}
	want := cur + `;D:\new\tool`
	if newCur != want {
		t.Errorf("新 PATH = %q, 期望 %q", newCur, want)
	}
}

// mergePathDirs 处理空 PATH（全部新增）
func TestMergePathDirsEmpty(t *testing.T) {
	newCur, added := mergePathDirs("", []string{`C:\A`, `C:\B`})
	if len(added) != 2 {
		t.Fatalf("空 PATH 应新增全部，实际 added=%v", added)
	}
	if newCur != `C:\A;C:\B` {
		t.Errorf("新 PATH = %q, 期望 C:\\A;C:\\B", newCur)
	}
}

// mergePathDirs 大小写不敏感去重（无新增时返回原样）
func TestMergePathDirsCaseInsensitive(t *testing.T) {
	cur := `c:\windows`
	newCur, added := mergePathDirs(cur, []string{`C:\Windows`})
	if len(added) != 0 {
		t.Fatalf("大小写不同也应去重，实际 added=%v", added)
	}
	if newCur != cur {
		t.Errorf("无新增时应原样返回，实际 %q", newCur)
	}
}

// 完全找不到时返回 Found=false 且不写 config
func TestDetectNotFound(t *testing.T) {
	withEnv(t, "PATH", t.TempDir())
	withEnv(t, "USERPROFILE", t.TempDir())
	withEnv(t, "PACKWIZ", "")
	withEnv(t, "PRISM", "")
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

// CurseForge API Key 的保存、读取与清除
func TestApiKeySetGetClear(t *testing.T) {
	m := newTestConfig(t)
	svc := NewEnvService(m)

	if got := svc.GetApiKey(); got != "" {
		t.Fatalf("初始应为空，实际 %q", got)
	}

	if err := svc.SetApiKey("  abc-123-xyz  "); err != nil {
		t.Fatalf("SetApiKey: %v", err)
	}
	if got := svc.GetApiKey(); got != "abc-123-xyz" {
		t.Errorf("应保存去除首尾空白后的 key，实际 %q", got)
	}

	// 重新从磁盘加载，验证持久化
	m2 := &ConfigManager{path: m.path}
	if _, err := toml.DecodeFile(m2.path, &m2.cfg); err != nil {
		t.Fatalf("重新读取配置失败: %v", err)
	}
	if m2.cfg.CurseforgeApiKey != "abc-123-xyz" {
		t.Errorf("配置文件应包含 key，实际 %q", m2.cfg.CurseforgeApiKey)
	}

	// 空串清除
	if err := svc.SetApiKey(""); err != nil {
		t.Fatalf("SetApiKey(\"\"): %v", err)
	}
	if got := svc.GetApiKey(); got != "" {
		t.Errorf("清除后应为空，实际 %q", got)
	}
}
