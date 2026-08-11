package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 构造一个迷你 packwiz 项目结构，验证 pack.toml 与 pw.toml 的解析
func TestParsePackTomlAndScanMods(t *testing.T) {
	dir := t.TempDir()
	packToml := filepath.Join(dir, "pack.toml")
	packContent := `name = "Test Pack"
author = "tester"
version = "0.1.0"
pack-format = "packwiz:1.1.0"

[index]
hash-format = "sha256"
hash = "abc"

[versions]
minecraft = "1.20.1"
fabric = "0.15.11"
`
	if err := os.WriteFile(packToml, []byte(packContent), 0o644); err != nil {
		t.Fatal(err)
	}
	modsDir := filepath.Join(dir, "mods", "sodium")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pwContent := `name = "Sodium"
filename = "sodium-fabric-0.5.8.jar"
side = "both"

[download]
hash-format = "sha1"
hash = "def"

[update]
[update.fabric]
mod-id = "sodium"
version = "0.5.8"
`
	if err := os.WriteFile(filepath.Join(modsDir, "pw.toml"), []byte(pwContent), 0o644); err != nil {
		t.Fatal(err)
	}

	proj, err := parsePackToml(packToml)
	if err != nil {
		t.Fatalf("parsePackToml: %v", err)
	}
	if proj.Name != "Test Pack" || proj.Minecraft != "1.20.1" || proj.Modloader != "fabric" || proj.ModloaderVersion != "0.15.11" {
		t.Errorf("元信息解析不正确: %+v", proj)
	}
	if len(proj.Mods) != 1 {
		t.Fatalf("应扫描到 1 个 mod，实际 %d", len(proj.Mods))
	}
	m := proj.Mods[0]
	if m.Name != "Sodium" || m.Side != "both" || m.SideCN != "通用" || m.File != "sodium-fabric-0.5.8.jar" {
		t.Errorf("mod 解析不正确: %+v", m)
	}
}
