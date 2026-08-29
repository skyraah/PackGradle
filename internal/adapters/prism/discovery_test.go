package prism

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"packgradle/internal/application/ports"
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

func TestDiscoverRuntimesMetadata(t *testing.T) {
	base := t.TempDir()
	inst := filepath.Join(base, "instances", "Fab")
	write(t, filepath.Join(inst, "instance.cfg"), "[General]\nname=Fab Instance\n")
	write(t, filepath.Join(inst, "mmc-pack.json"),
		`{"components":[{"uid":"net.minecraft","version":"1.20.1"},{"uid":"net.fabricmc.fabric-loader","version":"0.16.9"}]}`)
	write(t, filepath.Join(inst, "minecraft", "mods", "a.jar"), "jar")

	d := NewDiscovererWith(func() (string, error) { return filepath.Join(base, "instances"), nil })
	got, err := d.DiscoverRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("候选: %+v", got)
	}
	c := got[0]
	if c.InstanceID != "Fab" || c.DisplayName != "Fab Instance" ||
		c.Minecraft != "1.20.1" || c.Modloader != "fabric" {
		t.Fatalf("元数据: %+v", c)
	}
}

func TestDiscoverRuntimesIntendedVersionFallback(t *testing.T) {
	base := t.TempDir()
	inst := filepath.Join(base, "instances", "Vanilla")
	write(t, filepath.Join(inst, "instance.cfg"), "[General]\nname=V\nIntendedVersion=1.21.1\n")

	d := NewDiscovererWith(func() (string, error) { return filepath.Join(base, "instances"), nil })
	got, err := d.DiscoverRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Minecraft != "1.21.1" || got[0].Modloader != "" {
		t.Fatalf("IntendedVersion 回退: %+v", got)
	}
}

func TestDiscoverRuntimesInstancesDirError(t *testing.T) {
	d := NewDiscovererWith(func() (string, error) {
		return "", &ports.InstancesDirError{DataDir: "X:/missing"}
	})
	if _, err := d.DiscoverRuntimes(context.Background()); err == nil {
		t.Fatal("实例根不可定位应返回错误")
	} else {
		var ide *ports.InstancesDirError
		if !errors.As(err, &ide) || ide.DataDir != "X:/missing" {
			t.Fatalf("错误应携带数据目录: %v", err)
		}
	}
}

func TestDiscoverRuntimesRootUnreadable(t *testing.T) {
	base := t.TempDir()
	// instances 路径是一个文件 → ReadDir 失败
	write(t, filepath.Join(base, "instances"), "file")

	d := NewDiscovererWith(func() (string, error) { return filepath.Join(base, "instances"), nil })
	if _, err := d.DiscoverRuntimes(context.Background()); err == nil {
		t.Fatal("实例根不可读应返回错误")
	}
}
