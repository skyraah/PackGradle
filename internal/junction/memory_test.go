package junction

import (
	"os"
	"path/filepath"
	"testing"
)

// memory 假实现：Create → IsJunction/TargetOf → Remove 生命周期
func TestMemoryManagerLifecycle(t *testing.T) {
	m := NewMemoryManager()
	link := filepath.Join(t.TempDir(), "link")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if isJ, _ := m.IsJunction(link); isJ {
		t.Error("初始不应是 junction")
	}
	if err := m.Create(link, target); err != nil {
		t.Fatal(err)
	}
	if isJ, _ := m.IsJunction(link); !isJ {
		t.Error("创建后应为 junction")
	}
	if got, err := m.TargetOf(link); err != nil || got != target {
		t.Errorf("TargetOf 应返回 %q，实际 %q err=%v", target, got, err)
	}
	// 重复创建拒绝
	if err := m.Create(link, target); err == nil {
		t.Error("重复创建应报错")
	}
	if err := m.Remove(link); err != nil {
		t.Fatal(err)
	}
	if isJ, _ := m.IsJunction(link); isJ {
		t.Error("移除后不应是 junction")
	}
	// 移除不存在的链接报错
	if err := m.Remove(link); err == nil {
		t.Error("移除不存在的链接应报错")
	}
}

// memory 假实现：目标不存在时拒绝
func TestMemoryManagerCreateMissingTarget(t *testing.T) {
	m := NewMemoryManager()
	err := m.Create(filepath.Join(t.TempDir(), "link"), filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Error("目标不存在应报错")
	}
}
