package service

import (
	"os"
	"path/filepath"
	"testing"

	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
	"packgradle/internal/fsutil"
	"packgradle/internal/junction"
	"packgradle/internal/prism"
)

// validateRelDir：合法相对目录通过，绝对路径/.. 越界/空值拒绝
func TestValidateRelDir(t *testing.T) {
	valid := []string{"config", "kubejs", "config/sub"}
	for _, d := range valid {
		got, err := validateRelDir(d)
		if err != nil || got != d {
			t.Errorf("validateRelDir(%q) = %q, %v，期望通过", d, got, err)
		}
	}
	invalid := []string{"", "   ", "..", "../x", "a/../../b", `C:\Windows`, "/abs", "."}
	for _, d := range invalid {
		if _, err := validateRelDir(d); err == nil {
			t.Errorf("validateRelDir(%q) 应拒绝", d)
		}
	}
}

// normalizeFileListStrict：越界与绝对路径应整体拒绝
func TestNormalizeFileListStrict(t *testing.T) {
	got, err := normalizeFileListStrict([]string{"b.txt", "a.txt", "a.txt", "b.txt"})
	if err != nil || len(got) != 2 || got[0] != "a.txt" || got[1] != "b.txt" {
		t.Fatalf("合法清单应去重排序: got=%v err=%v", got, err)
	}
	for _, files := range [][]string{{"../x"}, {`C:\x`}, {"/x"}, {"a/../../b"}} {
		if _, err := normalizeFileListStrict(files); err == nil {
			t.Errorf("清单 %v 应拒绝", files)
		}
	}
}

// removeHardlinkFiles：仅删除仍指向项目侧同一文件的硬链接，
// 实例侧已被改写为独立文件时必须保留（避免误删游戏数据）
func TestRemoveHardlinkFilesProtectsIndependentFiles(t *testing.T) {
	projectDir := t.TempDir()
	gameDir := t.TempDir()
	mustWriteFile(t, filepath.Join(projectDir, "modlist.txt"), "project")
	if err := os.Link(filepath.Join(projectDir, "modlist.txt"), filepath.Join(gameDir, "modlist.txt")); err != nil {
		t.Fatal(err)
	}

	inst := prism.Instance{GameDir: gameDir}
	removeHardlinkFiles(inst, projectDir, []string{"modlist.txt"})
	if fsutil.Exists(filepath.Join(gameDir, "modlist.txt")) {
		t.Error("指向同一文件的硬链接应被删除")
	}

	// 游戏侧重新生成同名独立文件后，清理必须跳过（数据保护）
	mustWriteFile(t, filepath.Join(gameDir, "modlist.txt"), "game-own-copy")
	removeHardlinkFiles(inst, projectDir, []string{"modlist.txt"})
	data, err := os.ReadFile(filepath.Join(gameDir, "modlist.txt"))
	if err != nil || string(data) != "game-own-copy" {
		t.Errorf("实例侧独立文件不应被删除: data=%q err=%v", string(data), err)
	}
}

// SelectInstanceFiles（文件已在项目侧的 files 模式快速路径）：
// 由 junction 切到 files 后实例侧目录消失，选择项目侧文件应直接建硬链接并入清单
func TestSelectInstanceFilesFastPathFromProjectSide(t *testing.T) {
	_, _ = makePrismFixture(t)
	projectDir := t.TempDir()
	mustWriteFile(t, filepath.Join(projectDir, "config", "a.txt"), "A")

	cm := newTestConfig(t)
	if err := cm.AddProject(appconfig.ProjectEntry{Name: "proj", Path: projectDir}); err != nil {
		t.Fatal(err)
	}
	if err := appconfig.SaveProjectConfig(projectDir, appconfig.ProjectConfig{
		Instance: "Collapse",
		DirLinks: []appconfig.ProjectDirLink{{ProjectDir: "config", InstanceDir: "config", Mode: "files"}},
	}); err != nil {
		t.Fatal(err)
	}

	svc := &PrismService{config: cm, junctions: junction.NewMemoryManager()}
	results, err := svc.SelectInstanceFiles("proj", "config", []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "linked" {
		t.Fatalf("应直接建链成功: %+v", results)
	}

	pc, err := appconfig.LoadProjectConfig(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pc.DirLinks) != 1 || len(pc.DirLinks[0].Files) != 1 || pc.DirLinks[0].Files[0] != "a.txt" {
		t.Fatalf("清单应只包含成功文件: %+v", pc.DirLinks)
	}
	instSide := filepath.Join(cm.Get().PrismInstancesDir, "Collapse", "minecraft", "config", "a.txt")
	if !hardlinkPointsTo(instSide, filepath.Join(projectDir, "config", "a.txt")) {
		t.Error("实例侧应建立指向项目侧的硬链接")
	}
}

// SelectInstanceFiles：冲突跳过项不得写入清单（部分失败不再全量入库）
func TestSelectInstanceFilesSkipsConflictsFromManifest(t *testing.T) {
	_, _ = makePrismFixture(t)
	projectDir := t.TempDir()
	mustWriteFile(t, filepath.Join(projectDir, "config", "a.txt"), "project-copy")

	// 手动指定实例根目录，并预置一个两侧同名的独立文件（跳过场景）
	instancesDir := t.TempDir()
	mustWriteFile(t, filepath.Join(instancesDir, "Collapse", "instance.cfg"), "name=Collapse\n")
	mustWriteFile(t, filepath.Join(instancesDir, "Collapse", "minecraft", "config", "a.txt"), "game-copy")

	cm := newTestConfig(t)
	if err := cm.SetPrismInstancesPath(instancesDir); err != nil {
		t.Fatal(err)
	}
	if err := cm.AddProject(appconfig.ProjectEntry{Name: "proj", Path: projectDir}); err != nil {
		t.Fatal(err)
	}
	if err := appconfig.SaveProjectConfig(projectDir, appconfig.ProjectConfig{
		Instance: "Collapse",
		DirLinks: []appconfig.ProjectDirLink{{ProjectDir: "config", InstanceDir: "config", Mode: "files"}},
	}); err != nil {
		t.Fatal(err)
	}

	svc := &PrismService{config: cm, junctions: junction.NewMemoryManager()}
	results, err := svc.SelectInstanceFiles("proj", "config", []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "skipped" {
		t.Fatalf("两侧同名应跳过: %+v", results)
	}
	pc, err := appconfig.LoadProjectConfig(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pc.DirLinks) != 1 || len(pc.DirLinks[0].Files) != 0 {
		t.Fatalf("跳过的文件不应写入清单: %+v", pc.DirLinks)
	}
}

// 路径参数越界：服务方法应直接拒绝，而不是拼出项目外的路径
func TestPrismServiceRejectsPathTraversal(t *testing.T) {
	_, _ = makePrismFixture(t)
	projectDir := t.TempDir()
	mustWriteFile(t, filepath.Join(projectDir, "pack.toml"), "name = \"proj\"\n")
	cm := newTestConfig(t)
	if err := cm.AddProject(appconfig.ProjectEntry{Name: "proj", Path: projectDir}); err != nil {
		t.Fatal(err)
	}
	svc := &PrismService{config: cm, junctions: junction.NewMemoryManager()}
	if err := svc.AddDirLink("proj", "../outside"); errs.CodeOf(err) == "" {
		t.Errorf("AddDirLink 越界参数应返回结构化错误，实际 %v", err)
	}
	if _, err := svc.ListDirFiles("proj", ".."); errs.CodeOf(err) == "" {
		t.Errorf("ListDirFiles 越界参数应返回结构化错误，实际 %v", err)
	}
}
