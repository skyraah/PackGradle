//go:build windows

package junction

import (
	"os"
	"path/filepath"
	"testing"
)

// Windows 真实 junction 往返：创建 → 检测 → 目标解析 → 删除。
// 需要 NTFS 卷（t.TempDir 默认在系统盘，通常为 NTFS）。
func TestWindowsManagerLifecycle(t *testing.T) {
	m := NewWindowsManager()
	base := t.TempDir()
	link := filepath.Join(base, "linked-dir")
	target := filepath.Join(base, "target-dir")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := m.Create(link, target); err != nil {
		// 非 NTFS 卷（exFAT/网络盘）不支持 junction
		t.Skipf("创建 junction 失败（可能非 NTFS 卷）: %v", err)
	}
	defer func() {
		// 清理：仅删除链接本身
		_ = os.Remove(link)
	}()

	if isJ, err := m.IsJunction(link); err != nil || !isJ {
		t.Fatalf("创建后应为 junction，实际 %v err=%v", isJ, err)
	}
	got, err := m.TargetOf(link)
	if err != nil {
		t.Fatal(err)
	}
	if !pathsEqual(got, target) {
		t.Errorf("TargetOf 应返回 %q，实际 %q", target, got)
	}

	// 删除链接后目标内容完好
	if err := m.Remove(link); err != nil {
		t.Fatal(err)
	}
	if isJ, _ := m.IsJunction(link); isJ {
		t.Error("移除后不应是 junction")
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("目标目录应完好: %v", err)
	}
}

// 普通目录不是 junction
func TestWindowsManagerRealDirNotJunction(t *testing.T) {
	m := NewWindowsManager()
	dir := t.TempDir()
	if isJ, _ := m.IsJunction(dir); isJ {
		t.Error("普通目录不应是 junction")
	}
	// 删除普通目录被拒绝
	if err := m.Remove(dir); err == nil {
		t.Error("删除普通目录应被拒绝")
	}
}

func pathsEqual(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}
