package sync_test

import (
	"context"
	"path/filepath"
	"testing"

	"packgradle/internal/core/model"
)

// TestPrepareRelationNormalizesRelativeRoots 验证端点路径规范化管线：
// 相对输入被绝对化为 canonical realpath 后登记（P0-4 强制入口）。
func TestPrepareRelationNormalizesRelativeRoots(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	t.Chdir(filepath.Dir(projectRoot)) // base 目录下以相对路径发起

	prep, err := app.PrepareRelation(context.Background(), model.PrepareRelationInput{
		ProjectRoot:        "project",
		RuntimeInstanceDir: filepath.Join("instances", "Collapse"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range prep.Checks {
		if c.Severity == "blocking" && !c.Passed {
			t.Fatalf("预检 %s 未通过: %s", c.Code, c.Detail)
		}
	}
	if !filepath.IsAbs(prep.Project.RootPath) {
		t.Fatalf("登记的项目根应为绝对路径: %q", prep.Project.RootPath)
	}
	if prep.Project.RootPath != projectRoot {
		t.Fatalf("登记的项目根应等于 realpath: %q != %q", prep.Project.RootPath, projectRoot)
	}
	wantGame := filepath.Join(instanceDir, "minecraft")
	if prep.Runtime.RootPath != wantGame {
		t.Fatalf("登记的游戏目录应等于 realpath: %q != %q", prep.Runtime.RootPath, wantGame)
	}
}

// TestPrepareRelationUnreachableRootFailsCheck 验证不可达端点落入
// check.endpoint.readable 检查失败（而非硬错误），不产出端点草稿。
func TestPrepareRelationUnreachableRootFailsCheck(t *testing.T) {
	_, _, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)

	prep, err := app.PrepareRelation(context.Background(), model.PrepareRelationInput{
		ProjectRoot:        filepath.Join(t.TempDir(), "不存在"),
		RuntimeInstanceDir: filepath.Join(t.TempDir(), "也不存在"),
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{}
	for _, c := range prep.Checks {
		if c.Severity == "blocking" && !c.Passed {
			failed[c.Code] = true
		}
	}
	if !failed["check.endpoint.readable"] {
		t.Fatalf("不可达端点应使 check.endpoint.readable 失败: %+v", prep.Checks)
	}
	if prep.Project != nil || prep.Runtime != nil {
		t.Fatalf("不可达端点不应产出端点草稿: %+v %+v", prep.Project, prep.Runtime)
	}
}
