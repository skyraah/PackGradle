package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureLayoutIdempotent 验证 EnsureLayout 幂等（两次调用均成功）且子目录齐全。
func TestEnsureLayoutIdempotent(t *testing.T) {
	root := t.TempDir()

	first, err := EnsureLayout(root)
	if err != nil {
		t.Fatalf("第一次 EnsureLayout 失败: %v", err)
	}
	second, err := EnsureLayout(root)
	if err != nil {
		t.Fatalf("第二次 EnsureLayout 失败（应幂等）: %v", err)
	}
	if first != second {
		t.Fatalf("两次布局不一致: %+v vs %+v", first, second)
	}

	if first.DBPath != filepath.Join(root, "packgradle.db") {
		t.Errorf("DBPath = %q, 期望 %q", first.DBPath, filepath.Join(root, "packgradle.db"))
	}
	for name, dir := range map[string]string{
		"objects": first.ObjectsDir,
		"staging": first.StagingDir,
		"logs":    first.LogsDir,
		"exports": first.ExportsDir,
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("子目录 %s 不存在: %v", name, err)
		}
		if !info.IsDir() {
			t.Errorf("%s 路径 %q 不是目录", name, dir)
		}
	}
}

// TestEnsureLayoutNestedRoot 验证根目录本身不存在时也能创建完整布局。
func TestEnsureLayoutNestedRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "nested", "PackGradle")

	layout, err := EnsureLayout(root)
	if err != nil {
		t.Fatalf("嵌套根目录 EnsureLayout 失败: %v", err)
	}
	if _, err := os.Stat(layout.StagingDir); err != nil {
		t.Errorf("staging 目录未创建: %v", err)
	}
}
