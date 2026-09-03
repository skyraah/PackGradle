package fsnotifywatch

// fsnotify adapter 测试（票 #92）：真实 fsnotify 真目录写盘，断言事件翻译与
// 单 watcher 多目录注册（Windows 后端行为已由冻结树 legacy 验证）。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/application/ports"
)

func waitForEvent(t *testing.T, src *Source, wantPath string) ports.DirEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-src.Events():
			if ev.Path == wantPath {
				return ev
			}
			continue
		case err := <-src.Errors():
			t.Fatalf("事件源错误: %v", err)
		case <-deadline:
			t.Fatalf("等待事件超时: %s", wantPath)
		}
	}
}

// TestSourceSingleWatcherMultiDirs 单 watcher 多目录：两个目录各注册，各自写盘
// 事件都经端口通道到达，操作位翻译正确。
func TestSourceSingleWatcherMultiDirs(t *testing.T) {
	base := t.TempDir()
	dirA := filepath.Join(base, "a")
	dirB := filepath.Join(base, "b")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	src, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	for _, d := range []string{dirA, dirB} {
		if err := src.Add(d); err != nil {
			t.Fatalf("Add(%s): %v", d, err)
		}
	}

	if err := os.WriteFile(filepath.Join(dirA, "x.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "y.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}

	evA := waitForEvent(t, src, filepath.Join(dirA, "x.txt"))
	if !evA.Op.Has(ports.DirCreate) && !evA.Op.Has(ports.DirWrite) {
		t.Fatalf("事件位集缺 Create/Write: %v", evA)
	}
	waitForEvent(t, src, filepath.Join(dirB, "y.txt"))
}

// TestSourceRemoveStopsEvents 注销后不再收到该目录事件（挂卸的落盘侧语义）。
func TestSourceRemoveStopsEvents(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	src, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	if err := src.Add(dir); err != nil {
		t.Fatal(err)
	}
	if err := src.Remove(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "late.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-src.Events():
		t.Fatalf("注销后仍收到事件: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}
