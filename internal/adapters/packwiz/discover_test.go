package packwiz

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const discoverPack = `name = "Named Pack"
[versions]
minecraft = "1.20.1"
fabric = "0.15.11"
`

func TestDiscoverProjectsDepthAndSkipIntoProject(t *testing.T) {
	base := t.TempDir()
	// 深度 1 项目
	write(t, filepath.Join(base, "alpha", "pack.toml"), discoverPack)
	// 深度 2 项目
	write(t, filepath.Join(base, "nested", "beta", "pack.toml"), discoverPack)
	// 项目内嵌套 pack.toml：项目根是发现单元，不再向内递归
	write(t, filepath.Join(base, "alpha", "inner", "pack.toml"), discoverPack)
	// 深度 4：超出 maxDiscoverDepth，不报
	deep := filepath.Join(base, "a", "b", "c", "d")
	write(t, filepath.Join(deep, "pack.toml"), discoverPack)

	got, err := (&Scanner{}).DiscoverProjects(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	roots := map[string]bool{}
	for _, c := range got {
		roots[c.RootPath] = true
		if c.Minecraft != "1.20.1" || c.Modloader != "fabric" || c.DisplayName != "Named Pack" {
			t.Fatalf("元数据提取: %+v", c)
		}
	}
	if len(got) != 2 || !roots[filepath.Join(base, "alpha")] || !roots[filepath.Join(base, "nested", "beta")] {
		t.Fatalf("候选应恰为 depth1+depth2 两个项目: %+v", got)
	}
}

func TestDiscoverProjectsParentIsProjectRoot(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "pack.toml"), discoverPack)

	got, err := (&Scanner{}).DiscoverProjects(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RootPath != base {
		t.Fatalf("parentDir 自身是项目根: %+v", got)
	}
}

func TestDiscoverProjectsUnreadableSubdirSkipped(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "good", "pack.toml"), discoverPack)
	// 深度 1 处放一个同名「文件」，ReadDir 遇到它只作为非目录跳过；
	// 再造一个无权限目录验证跳过语义（Windows 下以文件模拟不可读更可靠，
	// 这里仅验证：坏条目不导致整体失败）
	write(t, filepath.Join(base, "blocked"), "plain file blocks the path")

	got, err := (&Scanner{}).DiscoverProjects(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("坏条目不应中断发现: %+v", got)
	}
}

func TestDiscoverProjectsBrokenPackTomlStillCandidate(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "broken", "pack.toml"), "not [valid toml")

	got, err := (&Scanner{}).DiscoverProjects(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("解析失败仍应保留路径事实候选: %+v", got)
	}
	if got[0].DisplayName != "broken" || got[0].Minecraft != "" {
		t.Fatalf("低置信度候选回退目录名: %+v", got)
	}
}

func TestDiscoverProjectsParentUnreadable(t *testing.T) {
	if _, err := (&Scanner{}).DiscoverProjects(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("父目录不可读应返回错误")
	}
}
