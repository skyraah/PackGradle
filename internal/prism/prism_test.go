package prism

import (
	"os"
	"path/filepath"
	"testing"
)

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

// mustWriteFile 创建文件（自动创建父目录）
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 数据目录固定为 %APPDATA%\PrismLauncher（标准安装布局，配置文件所在）
func TestDataDir(t *testing.T) {
	appData := t.TempDir()
	withEnv(t, "APPDATA", appData)
	if got := DataDir(); got != filepath.Join(appData, "PrismLauncher") {
		t.Errorf("数据目录应为 %%APPDATA%%\\PrismLauncher，实际 %q", got)
	}
}

// InstanceDir 相对路径：相对数据目录解析（默认 instances）
func TestInstancesDirRelative(t *testing.T) {
	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "prismlauncher.cfg"), "[General]\nInstanceDir=instances\n")
	got, err := InstancesDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dataDir, "instances"); got != want {
		t.Errorf("相对路径应 join 数据目录，实际 %q 期望 %q", got, want)
	}
}

// InstanceDir 绝对路径：原样使用
func TestInstancesDirAbsolute(t *testing.T) {
	dataDir := t.TempDir()
	custom := filepath.Join(t.TempDir(), "custom-instances")
	mustWriteFile(t, filepath.Join(dataDir, "prismlauncher.cfg"), "InstanceDir="+custom+"\n")
	got, err := InstancesDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != custom {
		t.Errorf("绝对路径应原样使用，实际 %q 期望 %q", got, custom)
	}
}

// 无配置文件：按默认布局 instances
func TestInstancesDirDefaultWhenNoCfg(t *testing.T) {
	dataDir := t.TempDir()
	got, err := InstancesDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dataDir, "instances"); got != want {
		t.Errorf("无配置应回退默认，实际 %q 期望 %q", got, want)
	}
}

// 容忍 BOM 与注释行，字段缺失回退默认
func TestInstancesDirBOMAndMissing(t *testing.T) {
	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "prismlauncher.cfg"), "\ufeff[General]\n# 注释\n; 分号注释\n")
	got, err := InstancesDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dataDir, "instances"); got != want {
		t.Errorf("无 InstanceDir 应回退默认，实际 %q 期望 %q", got, want)
	}
}
