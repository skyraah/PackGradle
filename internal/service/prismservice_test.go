package service

import (
	"path/filepath"
	"testing"

	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
)

// makePrismFixture 在临时 APPDATA 下构造 Prism 数据目录（标准安装布局）：
// %APPDATA%\PrismLauncher\prismlauncher.cfg + instances\<id>\instance.cfg + mmc-pack.json，
// 返回数据目录与 instances 目录
func makePrismFixture(t *testing.T) (string, string) {
	t.Helper()
	appData := t.TempDir()
	withEnv(t, "APPDATA", appData)
	dataDir := filepath.Join(appData, "PrismLauncher")
	mustWriteFile(t, filepath.Join(dataDir, "prismlauncher.cfg"), "[General]\nInstanceDir=instances\n")
	instancesDir := filepath.Join(dataDir, "instances")
	mustWriteFile(t, filepath.Join(instancesDir, "Collapse", "instance.cfg"), "InstanceType=OneSix\nname=Collapse\n")
	mustWriteFile(t, filepath.Join(instancesDir, "Collapse", "mmc-pack.json"),
		`{"formatVersion": 1, "components": [{"uid": "net.minecraft", "version": "1.20.1"}, {"uid": "net.minecraftforge", "version": "47.4.10"}]}`)
	return dataDir, instancesDir
}

// 端到端：%APPDATA%\PrismLauncher 定位 → InstanceDir 解析 → 实例列表，
// 自动检测到的实例目录回写持久化到 config.toml
func TestListInstances(t *testing.T) {
	_, instancesDir := makePrismFixture(t)

	cm := newTestConfig(t)
	svc := NewPrismService(cm)
	got, err := svc.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("应扫描到 1 个实例，实际 %d: %+v", len(got), got)
	}
	if got[0].ID != "Collapse" || got[0].Minecraft != "1.20.1" || got[0].Modloader != "forge" {
		t.Errorf("实例解析错误: %+v", got[0])
	}
	if dir, err := svc.InstancesDir(); err != nil || dir != instancesDir {
		t.Errorf("InstancesDir 应返回 %q，实际 %q err=%v", instancesDir, dir, err)
	}
	// 自动检测结果应回写 config
	if saved := cm.Get().PrismInstancesDir; saved != instancesDir {
		t.Errorf("自动检测的实例目录应持久化到 config，实际 %q 期望 %q", saved, instancesDir)
	}
}

// Prism 未安装（%APPDATA%\PrismLauncher 不存在）：返回 err.prism.not_found
func TestListInstancesPrismNotFound(t *testing.T) {
	withEnv(t, "APPDATA", t.TempDir())

	svc := NewPrismService(newTestConfig(t))
	if _, err := svc.ListInstances(); errs.CodeOf(err) != "err.prism.not_found" {
		t.Errorf("应返回 err.prism.not_found，实际 %v", err)
	}
}

// Prism 数据目录存在但实例目录不存在：返回 err.prism.instances_dir_not_found
func TestListInstancesDirMissing(t *testing.T) {
	appData := t.TempDir()
	withEnv(t, "APPDATA", appData)
	// 仅创建数据目录，不含 instances
	mustWriteFile(t, filepath.Join(appData, "PrismLauncher", "prismlauncher.cfg"), "[General]\n")

	svc := NewPrismService(newTestConfig(t))
	if _, err := svc.ListInstances(); errs.CodeOf(err) != "err.prism.instances_dir_not_found" {
		t.Errorf("应返回 err.prism.instances_dir_not_found，实际 %v", err)
	}
}

// 单实例解析失败不中断列表（坏 mmc-pack.json 落入 Error）
func TestListInstancesToleratesBadInstance(t *testing.T) {
	appData := t.TempDir()
	withEnv(t, "APPDATA", appData)
	instancesDir := filepath.Join(appData, "PrismLauncher", "instances")
	mustWriteFile(t, filepath.Join(instancesDir, "bad", "instance.cfg"), "name=Bad\n")
	mustWriteFile(t, filepath.Join(instancesDir, "bad", "mmc-pack.json"), "{broken")

	svc := NewPrismService(newTestConfig(t))
	got, err := svc.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Error == "" {
		t.Errorf("坏实例应出现在列表且带 Error，实际 %+v", got)
	}
	if got[0].Name != "Bad" {
		t.Errorf("name 不受影响，实际 %q", got[0].Name)
	}
}

// 手动指定实例目录优先于自动定位：APPDATA 为空也能列出实例
func TestManualInstancesPathPreferred(t *testing.T) {
	withEnv(t, "APPDATA", t.TempDir()) // 自动定位必然失败
	manual := t.TempDir()
	mustWriteFile(t, filepath.Join(manual, "Custom", "instance.cfg"), "name=Custom\n")
	mustWriteFile(t, filepath.Join(manual, "Custom", "mmc-pack.json"),
		`{"formatVersion": 1, "components": [{"uid": "net.minecraft", "version": "1.20.1"}]}`)

	svc := NewPrismService(newTestConfig(t))
	if err := svc.SetInstancesPath(manual); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "Custom" {
		t.Errorf("应使用手动路径扫描到 Custom，实际 %+v", got)
	}
	if dir, err := svc.InstancesDir(); err != nil || dir != manual {
		t.Errorf("InstancesDir 应返回手动路径 %q，实际 %q err=%v", manual, dir, err)
	}
	if got := svc.GetInstancesPath(); got != manual {
		t.Errorf("手动路径应持久化，实际 %q", got)
	}
}

// 手动路径不存在：拒绝保存
func TestSetInstancesPathInvalid(t *testing.T) {
	svc := NewPrismService(newTestConfig(t))
	err := svc.SetInstancesPath(filepath.Join(t.TempDir(), "missing"))
	if errs.CodeOf(err) != "err.prism.path_invalid" {
		t.Errorf("应返回 err.prism.path_invalid，实际 %v", err)
	}
}

// 手动路径失效（目录被删）时回退自动定位；清除手动路径后恢复自动定位
func TestManualInstancesPathFallbackAndClear(t *testing.T) {
	appData := t.TempDir()
	withEnv(t, "APPDATA", appData)
	mustWriteFile(t, filepath.Join(appData, "PrismLauncher", "prismlauncher.cfg"), "[General]\nInstanceDir=instances\n")
	mustWriteFile(t, filepath.Join(appData, "PrismLauncher", "instances", "Auto", "instance.cfg"), "name=Auto\n")

	svc := NewPrismService(newTestConfig(t))
	// 手动路径指向后创建的空目录：目录存在所以优先，但无实例
	manual := t.TempDir()
	if err := svc.SetInstancesPath(manual); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.ListInstances(); len(got) != 0 {
		t.Errorf("手动路径（空目录）应优先，实际 %+v", got)
	}
	// 清除手动路径：恢复自动定位
	if err := svc.SetInstancesPath(""); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "Auto" {
		t.Errorf("清除后应恢复自动定位扫描到 Auto，实际 %+v", got)
	}
}

// makeProject 构造一个已导入的 packwiz 项目（含 config/kubejs 目录），返回项目名
func makeProject(t *testing.T, cm *appconfig.ConfigManager, name string) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "pack.toml"),
		"name = \""+name+"\"\n[versions]\nminecraft = \"1.20.1\"\nforge = \"47.4.10\"\n")
	mustWriteFile(t, filepath.Join(dir, "config", ".keep"), "x")
	mustWriteFile(t, filepath.Join(dir, "kubejs", ".keep"), "x")
	if err := cm.AddProject(appconfig.ProjectEntry{Name: name, Path: dir}); err != nil {
		t.Fatal(err)
	}
	return name
}

// 关联成功：config 持久化 + GetLinks 组装（含实例信息）
func TestLinkProject(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := NewPrismService(cm)
	proj := makeProject(t, cm, "Collapse")

	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	link, ok := cm.FindLink(proj)
	if !ok || link.Instance != "Collapse" {
		t.Fatalf("关联应持久化，实际 %+v ok=%v", link, ok)
	}
	views := svc.GetLinks()
	if len(views) != 1 || views[0].InstanceName != "Collapse" || !views[0].InstanceValid {
		t.Errorf("GetLinks 应组装实例信息: %+v", views)
	}
	if views[0].ProjectPath == "" {
		t.Error("ProjectPath 不应为空")
	}
}

// 关联校验：项目不存在 / 实例不存在
func TestLinkProjectValidation(t *testing.T) {
	_, _ = makePrismFixture(t)
	svc := NewPrismService(newTestConfig(t))

	if err := svc.LinkProject("missing", "Collapse"); errs.CodeOf(err) != "err.proj.not_found" {
		t.Errorf("项目不存在应报 err.proj.not_found，实际 %v", err)
	}
	cm := newTestConfig(t)
	proj := makeProject(t, cm, "Proj")
	svc2 := NewPrismService(cm)
	if err := svc2.LinkProject(proj, "no-such-instance"); errs.CodeOf(err) != "err.prism.instance_not_found" {
		t.Errorf("实例不存在应报 err.prism.instance_not_found，实际 %v", err)
	}
}

// 解除关联：links + dir_links 一并清除
func TestUnlinkProject(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := NewPrismService(cm)
	proj := makeProject(t, cm, "Collapse")

	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddDirLink(proj, "config"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnlinkProject(proj); err != nil {
		t.Fatal(err)
	}
	if _, ok := cm.FindLink(proj); ok {
		t.Error("解除后关联不应存在")
	}
	if got := cm.FindDirLinks(proj); len(got) != 0 {
		t.Errorf("解除后目录关联应清空，实际 %+v", got)
	}
	// 未关联时解除报错
	if err := svc.UnlinkProject(proj); errs.CodeOf(err) != "err.link.not_found" {
		t.Errorf("未关联解除应报 err.link.not_found，实际 %v", err)
	}
}

// 实例被删除/改名：GetLinks 标记失效不崩溃
func TestGetLinksInstanceInvalid(t *testing.T) {
	cm := newTestConfig(t)
	svc := NewPrismService(cm)
	proj := makeProject(t, cm, "Proj")
	// 直接写 config 关联一个不存在的实例
	if err := cm.SetLink(appconfig.ProjectLink{Project: proj, Instance: "gone"}); err != nil {
		t.Fatal(err)
	}
	views := svc.GetLinks()
	if len(views) != 1 || views[0].InstanceValid || views[0].InstanceName != "" {
		t.Errorf("实例失效应标记 InstanceValid=false: %+v", views)
	}
}

// 程序创建实例：组件取自项目 pack.toml
func TestCreateInstance(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := NewPrismService(cm)
	proj := makeProject(t, cm, "NewPack")

	inst, err := svc.CreateInstance(proj)
	if err != nil {
		t.Fatal(err)
	}
	if inst.ID != "NewPack" || inst.Minecraft != "1.20.1" || inst.Modloader != "forge" || inst.ModloaderVersion != "47.4.10" {
		t.Errorf("实例组件应取自项目 pack.toml: %+v", inst)
	}
	if !isDir(filepath.Join(inst.Path, "minecraft")) {
		t.Error("应创建 minecraft 骨架")
	}
	// 创建后可立即关联
	if err := svc.LinkProject(proj, inst.ID); err != nil {
		t.Fatal(err)
	}
	// 重复创建拒绝
	if _, err := svc.CreateInstance(proj); errs.CodeOf(err) != "err.prism.instance_exists" {
		t.Errorf("重复创建应报 err.prism.instance_exists，实际 %v", err)
	}
}

// 目录关联：未关联拒绝 / 项目目录不存在拒绝 / 成功 + 实态视图
func TestDirLinks(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := NewPrismService(cm)
	proj := makeProject(t, cm, "Collapse")

	// 未关联项目拒绝
	if err := svc.AddDirLink(proj, "config"); errs.CodeOf(err) != "err.link.not_found" {
		t.Errorf("未关联应报 err.link.not_found，实际 %v", err)
	}
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	// 项目侧目录不存在拒绝
	if err := svc.AddDirLink(proj, "no-such-dir"); errs.CodeOf(err) != "err.sync.dir_not_exists" {
		t.Errorf("目录不存在应报 err.sync.dir_not_exists，实际 %v", err)
	}
	// 成功添加
	if err := svc.AddDirLink(proj, "config"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddDirLink(proj, "kubejs"); err != nil {
		t.Fatal(err)
	}
	views := svc.ListDirLinks(proj)
	if len(views) != 2 {
		t.Fatalf("应有 2 条目录关联，实际 %d: %+v", len(views), views)
	}
	for _, v := range views {
		if !v.ProjectExists {
			t.Errorf("项目侧目录应存在: %+v", v)
		}
		if v.InstanceExists {
			t.Errorf("实例侧目录（fixture 未创建 minecraft/config）应不存在: %+v", v)
		}
	}
	// 移除
	if err := svc.RemoveDirLink(proj, "config"); err != nil {
		t.Fatal(err)
	}
	if got := svc.ListDirLinks(proj); len(got) != 1 {
		t.Errorf("移除后应剩 1 条，实际 %d", len(got))
	}
}
