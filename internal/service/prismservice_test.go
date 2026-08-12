package service

import (
	"os"
	"path/filepath"
	"testing"

	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
	"packgradle/internal/junction"
	"packgradle/internal/pgignore"
	"packgradle/internal/prism"
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
	// 关联应持久化在项目目录下的 packgradle.toml（而非全局 config）
	pc, err := appconfig.LoadProjectConfig(cm.Get().Projects[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Instance != "Collapse" {
		t.Fatalf("关联应持久化到项目 packgradle.toml，实际 %+v", pc)
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
	pc, err := appconfig.LoadProjectConfig(cm.Get().Projects[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Instance != "" {
		t.Error("解除后关联应清除")
	}
	if len(pc.DirLinks) != 0 {
		t.Errorf("解除后目录关联应清空，实际 %+v", pc.DirLinks)
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
	// 直接写项目 packgradle.toml 关联一个不存在的实例
	entry, _ := cm.FindProject(proj)
	if err := appconfig.SaveProjectConfig(entry.Path, appconfig.ProjectConfig{Instance: "gone"}); err != nil {
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

// makeLinkProject 构造「已关联实例」的项目：含 config/kubejs 目录与 modlist.txt 顶层文件，
// 返回项目名与 packgradle.toml 路径
func makeLinkProject(t *testing.T, cm *appconfig.ConfigManager, name string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "pack.toml"),
		"name = \""+name+"\"\n[versions]\nminecraft = \"1.20.1\"\nforge = \"47.4.10\"\n")
	mustWriteFile(t, filepath.Join(dir, "config", "a.cfg"), "a")
	mustWriteFile(t, filepath.Join(dir, "kubejs", "b.js"), "b")
	mustWriteFile(t, filepath.Join(dir, "modlist.txt"), "mod list")
	// 默认 .pgignore（模拟导入时创建）
	if _, err := pgignore.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	// .pgignore 排除项
	mustWriteFile(t, filepath.Join(dir, ".git", "keep"), "x")
	mustWriteFile(t, filepath.Join(dir, "index.toml"), "x")
	// mods 目录（内建排除）
	mustWriteFile(t, filepath.Join(dir, "mods", "x.pw.toml"), "x")
	if err := cm.AddProject(appconfig.ProjectEntry{Name: name, Path: dir}); err != nil {
		t.Fatal(err)
	}
	return name, filepath.Join(dir, "packgradle.toml")
}

// newPrismServiceWithMemory 注入内存 junction 管理器（避免真实建链）
func newPrismServiceWithMemory(cm *appconfig.ConfigManager) *PrismService {
	return &PrismService{config: cm, junctions: junction.NewMemoryManager()}
}

// 一键关联：目录建 junction、文件建硬链接、mods/.pgignore 命中项排除
func TestCreateAllLinks(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, pcPath := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}

	results, err := svc.CreateAllLinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]prism.LinkResult{}
	for _, r := range results {
		byName[r.Name] = r
	}
	// 目录 junction
	if r, ok := byName["config"]; !ok || r.Status != "linked" || !r.IsDir {
		t.Errorf("config 应建链: %+v", results)
	}
	if r, ok := byName["kubejs"]; !ok || r.Status != "linked" {
		t.Errorf("kubejs 应建链: %+v", results)
	}
	// 文件硬链接
	if r, ok := byName["modlist.txt"]; !ok || r.Status != "linked" || r.IsDir {
		t.Errorf("modlist.txt 应硬链接: %+v", results)
	}
	// 内建排除 mods 与 .pgignore 命中项
	for _, excluded := range []string{"mods", ".git", "index.toml", "pack.toml", "packgradle.toml"} {
		if _, ok := byName[excluded]; ok {
			t.Errorf("%s 应被排除: %+v", excluded, results)
		}
	}
	// 持久化到 packgradle.toml
	pc, err := appconfig.LoadProjectConfig(filepath.Dir(pcPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(pc.DirLinks) != 2 || len(pc.FileLinks) != 1 {
		t.Errorf("应持久化 2 目录 + 1 文件链接，实际 %+v", pc)
	}
}

// 未关联项目拒绝一键关联
func TestCreateAllLinksNotLinked(t *testing.T) {
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, _ := makeLinkProject(t, cm, "Proj")
	if _, err := svc.CreateAllLinks(proj); errs.CodeOf(err) != "err.link.not_found" {
		t.Errorf("未关联应报 err.link.not_found，实际 %v", err)
	}
}

// 实例侧已有内容：一键关联不自动处理，标记为需手动链接
func TestCreateAllLinksInstanceSideOccupied(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, _ := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	// 实例侧游戏目录已有 config 真实内容（模拟游戏生成）
	instancesDir, _ := svc.InstancesDir()
	gameDir := filepath.Join(instancesDir, "Collapse", "minecraft")
	mustWriteFile(t, filepath.Join(gameDir, "config", "game.cfg"), "game")

	results, err := svc.CreateAllLinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Name == "config" {
			if r.Status != "manual" {
				t.Errorf("实例侧已占用应标记需手动链接: %+v", r)
			}
			return
		}
	}
	t.Error("未找到 config 条目")
}

// 幂等：已链目录再次一键关联返回 existing 且不重复持久化
func TestCreateAllLinksIdempotent(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, pcPath := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAllLinks(proj); err != nil {
		t.Fatal(err)
	}
	results, err := svc.CreateAllLinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status == "linked" {
			t.Errorf("二次执行不应再 linked: %+v", r)
		}
	}
	pc, _ := appconfig.LoadProjectConfig(filepath.Dir(pcPath))
	if len(pc.DirLinks) != 2 {
		t.Errorf("目录关联不应重复: %+v", pc)
	}
}

// 解除关联：删除全部链接（junction + 硬链接）并清空配置
func TestUnlinkRemovesLinks(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, _ := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAllLinks(proj); err != nil {
		t.Fatal(err)
	}
	// 解除前链接存在
	instancesDir, _ := svc.InstancesDir()
	gameDir := filepath.Join(instancesDir, "Collapse", "minecraft")
	if isJ, _ := svc.junctions.IsJunction(filepath.Join(gameDir, "config")); !isJ {
		t.Error("解除前 config 应为 junction")
	}
	if err := svc.UnlinkProject(proj); err != nil {
		t.Fatal(err)
	}
	if isJ, _ := svc.junctions.IsJunction(filepath.Join(gameDir, "config")); isJ {
		t.Error("解除后 config 链接应删除")
	}
	// 配置应清空
	entry, _ := cm.FindProject(proj)
	pc, _ := appconfig.LoadProjectConfig(entry.Path)
	if pc.Instance != "" || len(pc.DirLinks) != 0 || len(pc.FileLinks) != 0 {
		t.Errorf("解除后配置应清空: %+v", pc)
	}
}

// 删除项目联动清理：关联的 packgradle.toml 与已建链接一并清除
func TestRemoveProjectCleansLinks(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	// 共享同一内存 junction 管理器（建链与清理须互见）
	mem := junction.NewMemoryManager()
	svc := &PrismService{config: cm, junctions: mem}
	pw := &PackwizService{config: cm, junctions: mem}
	proj, pcPath := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAllLinks(proj); err != nil {
		t.Fatal(err)
	}
	// 项目列表中删除
	got, err := pw.RemoveProject(proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("移除后列表应为空，实际 %+v", got)
	}
	// 关联配置（packgradle.toml）应被删除
	if _, err := os.Stat(pcPath); !os.IsNotExist(err) {
		t.Errorf("packgradle.toml 应被清理: %v", err)
	}
	// 已建链接应被删除
	instancesDir, _ := svc.InstancesDir()
	gameDir := filepath.Join(instancesDir, "Collapse", "minecraft")
	if isJ, _ := svc.junctions.IsJunction(filepath.Join(gameDir, "config")); isJ {
		t.Error("链接应随项目删除被清理")
	}
}

// 未关联项目删除：packgradle.toml 不存在时静默跳过
func TestRemoveProjectNoLinks(t *testing.T) {
	cm := newTestConfig(t)
	proj, _ := makeLinkProject(t, cm, "Plain")
	pw := NewPackwizService(cm)
	got, err := pw.RemoveProject(proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("移除后列表应为空，实际 %+v", got)
	}
}

// makeInstanceGameDir 在 fixture 实例的游戏目录创建子目录
func makeInstanceGameDir(t *testing.T, svc *PrismService, dir string) string {
	t.Helper()
	instancesDir, _ := svc.InstancesDir()
	gameDir := filepath.Join(instancesDir, "Collapse", "minecraft")
	mustWriteFile(t, filepath.Join(gameDir, dir, "keep.txt"), "keep")
	return gameDir
}

// 手动链接：实例侧非空目录 → 复制到项目目录（同名跳过）→ 删原目录 → 建链
func TestManualLinkDirCopiesContent(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, _ := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	// 实例侧 config 已有内容：game.cfg + 子目录
	instancesDir, _ := svc.InstancesDir()
	gameDir := filepath.Join(instancesDir, "Collapse", "minecraft")
	mustWriteFile(t, filepath.Join(gameDir, "config", "game.cfg"), "game-data")
	mustWriteFile(t, filepath.Join(gameDir, "config", "sub", "inner.txt"), "inner")

	res, err := svc.ManualLinkDir(proj, "config")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "linked" {
		t.Fatalf("应建链成功，实际 %+v", res)
	}
	// 实例侧 config 应为 junction（memory 管理器）
	if isJ, _ := svc.junctions.IsJunction(filepath.Join(gameDir, "config")); !isJ {
		t.Error("实例侧 config 应为 junction")
	}
	// 内容已复制到项目目录（项目侧权威保留 + 新增并入）
	entry, _ := cm.FindProject(proj)
	projConfig := filepath.Join(entry.Path, "config")
	if _, err := os.Stat(filepath.Join(projConfig, "game.cfg")); err != nil {
		t.Errorf("game.cfg 应复制到项目目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projConfig, "sub", "inner.txt")); err != nil {
		t.Errorf("子目录内容应递归复制: %v", err)
	}
	// 项目侧已有文件不被覆盖
	mustWriteFile(t, filepath.Join(projConfig, "a.cfg"), "project-auth")
	// 再次手动链接（此时实例侧已是 junction 指向项目）：existing 幂等
	res2, err := svc.ManualLinkDir(proj, "config")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != "existing" {
		t.Errorf("已链应幂等返回 existing，实际 %+v", res2)
	}
}

// 手动链接：实例侧空目录 → 直接删除后建链（无复制）
func TestManualLinkDirEmptyDir(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, _ := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	instancesDir, _ := svc.InstancesDir()
	gameDir := filepath.Join(instancesDir, "Collapse", "minecraft")
	if err := os.MkdirAll(filepath.Join(gameDir, "kubejs"), 0o755); err != nil {
		t.Fatal(err) // 空目录
	}

	res, err := svc.ManualLinkDir(proj, "kubejs")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "linked" {
		t.Fatalf("空目录应直接删除后建链，实际 %+v", res)
	}
	if isJ, _ := svc.junctions.IsJunction(filepath.Join(gameDir, "kubejs")); !isJ {
		t.Error("kubejs 应为 junction")
	}
}

// 手动链接：实例侧目录不存在 → 直接建链
func TestManualLinkDirNotExists(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, _ := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ManualLinkDir(proj, "kubejs")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "linked" {
		t.Fatalf("实例侧不存在应直接建链，实际 %+v", res)
	}
	// 建链记录持久化（供解除时清理）
	entry, _ := cm.FindProject(proj)
	pc, _ := appconfig.LoadProjectConfig(entry.Path)
	if len(pc.DirLinks) == 0 {
		t.Error("建链记录应持久化")
	}
}

// 手动链接：未关联 / 实例侧是文件
func TestManualLinkDirValidation(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	proj, _ := makeLinkProject(t, cm, "Collapse")

	// 未关联
	if _, err := svc.ManualLinkDir(proj, "config"); errs.CodeOf(err) != "err.link.not_found" {
		t.Errorf("未关联应报 err.link.not_found，实际 %v", err)
	}
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}
	// 实例侧是文件
	instancesDir, _ := svc.InstancesDir()
	gameDir := filepath.Join(instancesDir, "Collapse", "minecraft")
	mustWriteFile(t, filepath.Join(gameDir, "config"), "file-not-dir")

	res, err := svc.ManualLinkDir(proj, "config")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "error" {
		t.Errorf("实例侧是文件应报错，实际 %+v", res)
	}
}
