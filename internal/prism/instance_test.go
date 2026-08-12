package prism

import (
	"path/filepath"
	"strings"
	"testing"
)

// makeInstance 构造一个测试实例目录，返回 instances 根目录
func makeInstance(t *testing.T, instancesDir, id, cfgName, mmcPack string) {
	t.Helper()
	dir := filepath.Join(instancesDir, id)
	cfg := "InstanceType=OneSix\n"
	if cfgName != "" {
		cfg += "name=" + cfgName + "\n"
	}
	mustWriteFile(t, filepath.Join(dir, "instance.cfg"), cfg)
	if mmcPack != "" {
		mustWriteFile(t, filepath.Join(dir, "mmc-pack.json"), mmcPack)
	}
}

// 标准实例：forge + instgroups 分组 + name 显示名
func TestScanInstancesBasic(t *testing.T) {
	instancesDir := t.TempDir()
	makeInstance(t, instancesDir, "Collapse", "整合包", `{
		"formatVersion": 1,
		"components": [
			{"uid": "net.minecraft", "version": "1.20.1"},
			{"uid": "net.minecraftforge", "version": "47.4.10"}
		]
	}`)
	// 无 instance.cfg 的杂物目录（如 cache/）应被忽略
	mustWriteFile(t, filepath.Join(instancesDir, "cache", "x.dat"), "x")
	// instgroups.json 分组文件
	mustWriteFile(t, filepath.Join(instancesDir, "instgroups.json"),
		`{"formatVersion": "1", "groups": {"git": {"instances": ["Collapse"]}}}`)

	got := ScanInstances(instancesDir)
	if len(got) != 1 {
		t.Fatalf("应扫描到 1 个实例，实际 %d: %+v", len(got), got)
	}
	inst := got[0]
	if inst.ID != "Collapse" || inst.Name != "整合包" {
		t.Errorf("ID/Name 错误: %+v", inst)
	}
	if inst.Minecraft != "1.20.1" || inst.Modloader != "forge" || inst.ModloaderVersion != "47.4.10" {
		t.Errorf("版本信息错误: %+v", inst)
	}
	if inst.Group != "git" {
		t.Errorf("分组错误: %q", inst.Group)
	}
	if inst.GameDir != filepath.Join(instancesDir, "Collapse", "minecraft") {
		t.Errorf("游戏目录错误: %q", inst.GameDir)
	}
	if inst.Error != "" {
		t.Errorf("不应有错误: %q", inst.Error)
	}
}

// 各类加载器 uid 映射 + vanilla（无加载器）
func TestLoaderUIDMapping(t *testing.T) {
	cases := []struct {
		uid, loader string
	}{
		{"net.minecraftforge", "forge"},
		{"net.neoforged", "neoforge"},
		{"net.fabricmc.fabric-loader", "fabric"},
		{"org.quiltmc.quilt-loader", "quilt"},
		{"com.mumfrey.liteloader", "liteloader"},
	}
	for _, c := range cases {
		instancesDir := t.TempDir()
		makeInstance(t, instancesDir, "test", "", `{
			"formatVersion": 1,
			"components": [
				{"uid": "net.minecraft", "version": "1.21"},
				{"uid": "`+c.uid+`", "version": "9.9.9"}
			]
		}`)
		got := ScanInstances(instancesDir)
		if len(got) != 1 || got[0].Modloader != c.loader || got[0].ModloaderVersion != "9.9.9" {
			t.Errorf("uid %s → loader=%q, 实际 %+v", c.uid, c.loader, got)
		}
	}

	// vanilla：无加载器组件
	instancesDir := t.TempDir()
	makeInstance(t, instancesDir, "vanilla", "", `{
		"formatVersion": 1,
		"components": [{"uid": "net.minecraft", "version": "1.20.4"}]
	}`)
	got := ScanInstances(instancesDir)
	if len(got) != 1 || got[0].Modloader != "" {
		t.Errorf("vanilla 实例 Modloader 应为空，实际 %+v", got)
	}
}

// instance.cfg 缺失 name：回退实例目录名
func TestNameFallbackToDir(t *testing.T) {
	instancesDir := t.TempDir()
	makeInstance(t, instancesDir, "NoName", "", "")
	got := ScanInstances(instancesDir)
	if len(got) != 1 || got[0].Name != "NoName" {
		t.Errorf("name 缺失应回退目录名，实际 %+v", got)
	}
}

// 坏 mmc-pack.json：错误内嵌，不中断列表
func TestBadMMCPackTolerated(t *testing.T) {
	instancesDir := t.TempDir()
	makeInstance(t, instancesDir, "bad", "BadPack", `{"formatVersion": 1, "components": [`)
	got := ScanInstances(instancesDir)
	if len(got) != 1 {
		t.Fatalf("坏实例也应出现在列表，实际 %d", len(got))
	}
	if got[0].Error == "" {
		t.Error("坏 mmc-pack 应填充 Error")
	}
	if !strings.Contains(got[0].Error, "err.prism.mmcpack_parse") {
		t.Errorf("Error 应为错误码 JSON，实际 %q", got[0].Error)
	}
	// name 仍应可用
	if got[0].Name != "BadPack" {
		t.Errorf("name 不受 mmc-pack 失败影响，实际 %q", got[0].Name)
	}
}

// 实例名含空格与特殊字符（真实场景如 [Client]Create-Delight-...）
func TestNameWithSpacesAndBrackets(t *testing.T) {
	instancesDir := t.TempDir()
	makeInstance(t, instancesDir, "[Client]Create-Delight-Remake-v0.4.8.15", "整合包 测试", "")
	got := ScanInstances(instancesDir)
	if len(got) != 1 || got[0].Name != "整合包 测试" {
		t.Errorf("含空格/中文 name 解析失败: %+v", got)
	}
}

// instgroups.json 缺失：分组为空串
func TestNoInstGroups(t *testing.T) {
	instancesDir := t.TempDir()
	makeInstance(t, instancesDir, "a", "A", "")
	got := ScanInstances(instancesDir)
	if len(got) != 1 || got[0].Group != "" {
		t.Errorf("无 instgroups.json 时 Group 应为空，实际 %+v", got)
	}
}

// 按名称不区分大小写排序
func TestInstancesSorted(t *testing.T) {
	instancesDir := t.TempDir()
	makeInstance(t, instancesDir, "zeta", "", "")
	makeInstance(t, instancesDir, "Alpha", "", "")
	got := ScanInstances(instancesDir)
	if len(got) != 2 || got[0].ID != "Alpha" || got[1].ID != "zeta" {
		t.Errorf("应按名称排序: %+v", got)
	}
}
