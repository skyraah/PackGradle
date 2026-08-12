package envutil

import (
	"os"
	"path/filepath"
	"testing"
)

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

// FindExecutable 查找链：config 路径优先
func TestFindExecutablePrefersConfig(t *testing.T) {
	saved := fakeTool(t, t.TempDir(), "packwiz")
	other := fakeTool(t, t.TempDir(), "packwiz")
	withEnv(t, "PACKWIZ_TEST", filepath.Dir(other))

	got, source, ok := FindExecutable(saved, "packwiz", "PACKWIZ_TEST", t.TempDir())
	if !ok || source != "config" || got != saved {
		t.Fatalf("应优先返回 config 路径 %q，实际 path=%q source=%s ok=%v", saved, got, source, ok)
	}
}

// FindExecutable 查找链：config 缺失时走环境变量 → PATH → 候选目录
func TestFindExecutableChain(t *testing.T) {
	// 环境变量
	envDir := t.TempDir()
	exe := fakeTool(t, envDir, "packwiz")
	withEnv(t, "PACKWIZ_TEST", envDir)
	got, source, ok := FindExecutable("", "packwiz", "PACKWIZ_TEST", t.TempDir())
	if !ok || source != "env" || got != exe {
		t.Fatalf("应从环境变量找到，实际 path=%q source=%s ok=%v", got, source, ok)
	}

	// PATH
	withEnv(t, "PACKWIZ_TEST", "")
	pathDir := t.TempDir()
	pathExe := fakeTool(t, pathDir, "packwiz")
	withEnv(t, "PATH", pathDir+";"+os.Getenv("PATH"))
	got, source, ok = FindExecutable("", "packwiz", "PACKWIZ_TEST", t.TempDir())
	if !ok || source != "path" || got != pathExe {
		t.Fatalf("应从 PATH 找到，实际 path=%q source=%s ok=%v", got, source, ok)
	}

	// 候选目录（清空 PATH 避免误命中）
	withEnv(t, "PATH", t.TempDir())
	candDir := t.TempDir()
	candExe := fakeTool(t, candDir, "packwiz")
	got, source, ok = FindExecutable("", "packwiz", "PACKWIZ_TEST", t.TempDir(), candDir)
	if !ok || source != "default-dir" || got != candExe {
		t.Fatalf("应从候选目录找到，实际 path=%q source=%s ok=%v", got, source, ok)
	}

	// 全部找不到
	withEnv(t, "PATH", t.TempDir())
	if _, _, ok := FindExecutable("", "packwiz", "PACKWIZ_TEST", t.TempDir()); ok {
		t.Error("全部缺失时不应找到")
	}
}

// 环境变量值也可直接指向 exe 文件本身
func TestFindExecutableEnvVarPointsToFile(t *testing.T) {
	exe := fakeTool(t, t.TempDir(), "packwiz")
	withEnv(t, "PACKWIZ_TEST", exe)
	got, source, ok := FindExecutable("", "packwiz", "PACKWIZ_TEST", t.TempDir())
	if !ok || source != "env" || got != exe {
		t.Fatalf("环境变量指向 exe 文件时应直接命中，实际 path=%q source=%s ok=%v", got, source, ok)
	}
}
