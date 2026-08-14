package prism

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"packgradle/internal/fsutil"
)

// 创建 fabric 实例：instance.cfg / mmc-pack.json / minecraft 骨架齐全
func TestCreateMinimalInstance(t *testing.T) {
	instancesDir := t.TempDir()
	inst, err := CreateMinimalInstance(instancesDir, CreateRequest{
		Name:             "MyPack",
		Minecraft:        "1.20.1",
		Modloader:        "fabric",
		ModloaderVersion: "0.15.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ID != "MyPack" || inst.Name != "MyPack" {
		t.Errorf("ID/Name 错误: %+v", inst)
	}
	if info, err := os.Stat(filepath.Join(instancesDir, "MyPack", "minecraft")); err != nil || !info.IsDir() {
		t.Error("应创建 minecraft 游戏目录骨架")
	}

	// instance.cfg 内容
	cfg, err := os.ReadFile(filepath.Join(instancesDir, "MyPack", "instance.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(cfg); got != "InstanceType=OneSix\nname=MyPack\n" {
		t.Errorf("instance.cfg 内容错误: %q", got)
	}

	// mmc-pack.json 内容：camelCase 字段 + 组件顺序
	data, err := os.ReadFile(filepath.Join(instancesDir, "MyPack", "mmc-pack.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pack mmcPackOut
	if err := json.Unmarshal(data, &pack); err != nil {
		t.Fatal(err)
	}
	if pack.FormatVersion != 1 || len(pack.Components) != 2 {
		t.Fatalf("formatVersion/components 错误: %+v", pack)
	}
	if pack.Components[0].UID != "net.minecraft" || pack.Components[0].Version != "1.20.1" || !pack.Components[0].Important {
		t.Errorf("net.minecraft 组件错误: %+v", pack.Components[0])
	}
	if pack.Components[1].UID != "net.fabricmc.fabric-loader" || pack.Components[1].Version != "0.15.11" {
		t.Errorf("fabric 组件错误: %+v", pack.Components[1])
	}
}

// vanilla 实例（无加载器）：只写 net.minecraft 组件
func TestCreateVanillaInstance(t *testing.T) {
	instancesDir := t.TempDir()
	inst, err := CreateMinimalInstance(instancesDir, CreateRequest{Name: "Vanilla", Minecraft: "1.20.4"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Modloader != "" || inst.ModloaderVersion != "" {
		t.Errorf("vanilla 实例不应有加载器: %+v", inst)
	}
	data, err := os.ReadFile(filepath.Join(instancesDir, "Vanilla", "mmc-pack.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pack mmcPackOut
	if err := json.Unmarshal(data, &pack); err != nil {
		t.Fatal(err)
	}
	if len(pack.Components) != 1 || pack.Components[0].UID != "net.minecraft" {
		t.Errorf("应只有 net.minecraft 组件: %+v", pack.Components)
	}
}

// 重名拒绝（不覆盖已有实例）
func TestCreateInstanceExists(t *testing.T) {
	instancesDir := t.TempDir()
	if _, err := CreateMinimalInstance(instancesDir, CreateRequest{Name: "Dup", Minecraft: "1.20.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMinimalInstance(instancesDir, CreateRequest{Name: "Dup", Minecraft: "1.20.1"}); err == nil {
		t.Fatal("重名创建应报错")
	}
}

// 非法字符替换 + 空名拒绝
func TestCreateInstanceSanitize(t *testing.T) {
	instancesDir := t.TempDir()
	inst, err := CreateMinimalInstance(instancesDir, CreateRequest{Name: `Pack:Test/One`, Minecraft: "1.20.1"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ID != "Pack_Test_One" {
		t.Errorf("非法字符应替换为 _，实际 %q", inst.ID)
	}
	if _, err := CreateMinimalInstance(instancesDir, CreateRequest{Name: "   ", Minecraft: "1.20.1"}); err == nil {
		t.Fatal("空名应拒绝")
	}
}

// 不支持的加载器拒绝
func TestCreateInstanceLoaderUnsupported(t *testing.T) {
	instancesDir := t.TempDir()
	_, err := CreateMinimalInstance(instancesDir, CreateRequest{Name: "X", Minecraft: "1.20.1", Modloader: "bogus", ModloaderVersion: "1"})
	if err == nil {
		t.Fatal("不支持的加载器应报错")
	}
}

// 目录已存在时拒绝创建且不触碰已有内容
func TestCreateInstanceDirExists(t *testing.T) {
	instancesDir := t.TempDir()
	mustWriteFile(t, filepath.Join(instancesDir, "Existing", "keep.txt"), "x")
	if _, err := CreateMinimalInstance(instancesDir, CreateRequest{Name: "Existing", Minecraft: "1.20.1"}); err == nil {
		t.Fatal("目录已存在应报错")
	}
	if !fsutil.Exists(filepath.Join(instancesDir, "Existing", "keep.txt")) {
		t.Error("已有内容不应被改动")
	}
}
