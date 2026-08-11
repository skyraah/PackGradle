package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 现代 packwiz 项目：pack.toml + index.toml（meta 文件索引）+ mods/ 下扁平的 .pw.toml。
// 验证通过 index.toml 的 mods/ 条目驱动扫描，并覆盖直接放入的 jar 与索引存在但文件缺失的情况。
func TestParsePackTomlWithIndex(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "pack.toml"), `name = "Collapse"
author = "PickAID"
version = "1.0.0"
pack-format = "packwiz:1.1.0"

[index]
file = "index.toml"
hash-format = "sha256"
hash = "abc"

[versions]
forge = "47.4.10"
minecraft = "1.20.1"
`)
	mustWriteFile(t, filepath.Join(dir, "index.toml"), `hash-format = "sha256"

[[files]]
file = "mods/create.pw.toml"
hash = "h1"
metafile = true

[[files]]
file = "mods/mekanism.pw.toml"
hash = "h2"
metafile = true

[[files]]
file = "mods/mcrd-cn.ksmcbrigade-1.20.1-4.jar"
hash = "h3"

[[files]]
file = "mods/ghost.pw.toml"
hash = "h4"
metafile = true

[[files]]
file = "mods/sodium.pw.toml"
hash = "h6"
metafile = true

[[files]]
file = "config/foo.toml"
hash = "h5"
`)
	mustWriteFile(t, filepath.Join(dir, "mods", "create.pw.toml"), `name = "Create"
filename = "create-1.20.1-6.0.8.jar"
side = "both"

[download]
hash-format = "sha1"
hash = "d1"
mode = "metadata:curseforge"

[update]
[update.curseforge]
file-id = 7178761
project-id = 328085
`)
	mustWriteFile(t, filepath.Join(dir, "mods", "mekanism.pw.toml"), `name = "Mekanism"
filename = "Mekanism-1.20.1-10.4.16.80.jar"
side = "both"
`)
	// modrinth 源的 mod：版本号存在 [update.modrinth] 表中
	mustWriteFile(t, filepath.Join(dir, "mods", "sodium.pw.toml"), `name = "Sodium"
filename = "sodium-fabric-0.5.8.jar"
side = "client"

[download]
hash-format = "sha1"
hash = "d3"
mode = "metadata:modrinth"

[update]
[update.modrinth]
mod-id = "AANobbMI"
version = "mc1.20.1-0.5.8"
`)
	// 直接放入的 jar（index 有记录、无元数据文件）
	mustWriteFile(t, filepath.Join(dir, "mods", "mcrd-cn.ksmcbrigade-1.20.1-4.jar"), "jar-bytes")
	// 注意：mods/ghost.pw.toml 不写入磁盘，模拟索引存在但文件缺失

	proj, err := parsePackToml(filepath.Join(dir, "pack.toml"))
	if err != nil {
		t.Fatalf("parsePackToml: %v", err)
	}
	if proj.Name != "Collapse" || proj.Minecraft != "1.20.1" || proj.Modloader != "forge" || proj.ModloaderVersion != "47.4.10" {
		t.Errorf("元信息解析不正确: %+v", proj)
	}
	if len(proj.Mods) != 5 {
		t.Fatalf("应扫描到 5 个 mod，实际 %d: %+v", len(proj.Mods), proj.Mods)
	}

	create := findMod(t, proj.Mods, "create")
	if create.Name != "Create" || create.Side != "both" || create.SideCN != "通用" ||
		create.File != "create-1.20.1-6.0.8.jar" || filepath.Base(create.Path) != "create.pw.toml" {
		t.Errorf("mod 元数据解析不正确: %+v", create)
	}
	// curseforge 源应提取 project-id / file-id
	if create.CfProjectID != 328085 || create.CfFileID != 7178761 {
		t.Errorf("curseforge 源 ID 提取不正确: %+v", create)
	}
	// curseforge 源没有版本信息
	if create.Version != "" {
		t.Errorf("curseforge 源不应有本地版本号: %+v", create)
	}

	mek := findMod(t, proj.Mods, "mekanism")
	if mek.Name != "Mekanism" || mek.File != "Mekanism-1.20.1-10.4.16.80.jar" {
		t.Errorf("mod 元数据解析不正确: %+v", mek)
	}

	// modrinth 源：版本应从 [update.modrinth] 表提取
	sodium := findMod(t, proj.Mods, "sodium")
	if sodium.Version != "mc1.20.1-0.5.8" || sodium.Side != "client" || sodium.SideCN != "客户端" {
		t.Errorf("[update.modrinth] 版本提取不正确: %+v", sodium)
	}
	// 非 curseforge 源不应有 CF ID
	if sodium.CfProjectID != 0 || sodium.CfFileID != 0 {
		t.Errorf("非 curseforge 源不应有 CF ID: %+v", sodium)
	}

	jar := findMod(t, proj.Mods, "mcrd-cn.ksmcbrigade-1.20.1-4")
	if jar.Name != "mcrd-cn.ksmcbrigade-1.20.1-4" || jar.File != "mcrd-cn.ksmcbrigade-1.20.1-4.jar" ||
		jar.Side != "" || jar.SideCN != "" {
		t.Errorf("直接放入的 jar 应仅以文件名展示: %+v", jar)
	}

	ghost := findMod(t, proj.Mods, "ghost")
	if ghost.Name != "ghost" || ghost.File != "ghost.pw.toml" || ghost.Path != "" {
		t.Errorf("索引存在但文件缺失的条目应保留并以文件名展示: %+v", ghost)
	}
}

// 旧式 packwiz 项目：无 index.toml，mods/<name>/pw.toml 目录结构，应回退到目录扫描
func TestParsePackTomlLegacyFallback(t *testing.T) {
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
	// 顶层无 version，应从 [update.fabric] 表回退提取
	if m.Version != "0.5.8" {
		t.Errorf("[update.fabric] 版本提取不正确: %+v", m)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findMod(t *testing.T, mods []ModInfo, id string) ModInfo {
	t.Helper()
	for _, m := range mods {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("未找到 mod: %s（实际: %+v）", id, mods)
	return ModInfo{}
}
