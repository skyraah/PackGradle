package project_test

// project 用例包 headless 测试：真实 store（SQLite）+ 真实 adapters + 真实用例。
// 覆盖契约 03 §2.5：发现（有限深度 + 登记状态判定）、幂等登记（稳定 opaque ID）、
// 健康检查（路径存在性 + 绑定指纹匹配，只读不改状态）与错误码映射。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/packwiz"
	"packgradle/internal/application/endpoint"
	"packgradle/internal/application/project"
	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/ids"
	"packgradle/internal/errs"
	"packgradle/internal/store"
	"packgradle/internal/store/sqlite"
)

// ---- fixture ----

const fxPackToml = `name = "Collapse Pack"
author = "tester"
version = "1.0.0"

[versions]
minecraft = "1.20.1"
fabric = "0.15.11"
`

const fxPackTomlForge = `name = "Deep Forge"
[versions]
minecraft = "1.21.1"
forge = "52.0.24"
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeFixtures 构造发现目录：depth1 项目（含 pack 元数据）、depth2 项目、
// 非项目目录（无 pack.toml）。返回 parentDir 与两个项目根。
func makeFixtures(t *testing.T) (parentDir, projectA, projectB string) {
	t.Helper()
	base := t.TempDir()
	parentDir = filepath.Join(base, "packs")
	projectA = filepath.Join(parentDir, "Collapse")
	projectB = filepath.Join(parentDir, "nested", "Deep")

	writeFile(t, filepath.Join(projectA, "pack.toml"), fxPackToml)
	writeFile(t, filepath.Join(projectA, "index.toml"), "index = { file = \"index.toml\" }")
	writeFile(t, filepath.Join(projectB, "pack.toml"), fxPackTomlForge)
	writeFile(t, filepath.Join(parentDir, "NotAProject", "readme.txt"), "nothing here")
	return
}

func newApp(t *testing.T, discovery ports.ProjectDiscovery) (*project.App, *sql.DB) {
	t.Helper()
	dataRoot := t.TempDir()
	if _, err := store.EnsureLayout(dataRoot); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(filepath.Join(dataRoot, "packgradle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(context.Background(), db, dataRoot); err != nil {
		t.Fatal(err)
	}
	app, err := project.New(project.Deps{
		Endpoints:     sqlite.NewEndpointRepository(db),
		Paths:         filesystem.PathNormalizer{},
		Fingerprinter: filesystem.NewFingerprinter(),
		Discovery:     discovery,
		IDs:           ids.New,
		Now:           time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, db
}

func errCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误但没有发生")
	}
	code := errs.CodeOf(err)
	if code == "" {
		t.Fatalf("错误不是结构化 AppError: %v", err)
	}
	return code
}

// ---- 发现 ----

func TestDiscoverProjectsFindsCandidates(t *testing.T) {
	parentDir, projectA, _ := makeFixtures(t)
	app, _ := newApp(t, packwiz.New())
	ctx := context.Background()

	cands, err := app.DiscoverProjects(ctx, parentDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("候选数: %d (%+v)", len(cands), cands)
	}
	// display_name 升序（大小写不敏感）
	if cands[0].DisplayName != "Collapse Pack" || cands[1].DisplayName != "Deep Forge" {
		t.Fatalf("候选排序/名称: %+v", cands)
	}
	a, b := cands[0], cands[1]
	if !strings.HasSuffix(filepath.ToSlash(a.PackTomlPath), filepath.ToSlash(projectA)+"/pack.toml") &&
		!strings.EqualFold(a.PackTomlPath, filepath.Join(projectA, "pack.toml")) {
		t.Fatalf("候选 A pack.toml 路径: %s (期望前缀 %s)", a.PackTomlPath, projectA)
	}
	if a.Minecraft != "1.20.1" || a.Modloader != "fabric" {
		t.Fatalf("候选 A 元数据: %+v", a)
	}
	if b.Minecraft != "1.21.1" || b.Modloader != "forge" {
		t.Fatalf("候选 B 元数据: %+v", b)
	}
	if a.Registered || b.Registered {
		t.Fatal("未登记候选不应标记 registered")
	}
}

func TestDiscoverProjectsMarksRegistered(t *testing.T) {
	parentDir, _, _ := makeFixtures(t)
	app, _ := newApp(t, packwiz.New())
	ctx := context.Background()

	ep, err := app.RegisterProject(ctx, view.RegisterEndpointInput{RootPath: filepath.Join(parentDir, "Collapse")})
	if err != nil {
		t.Fatal(err)
	}

	cands, err := app.DiscoverProjects(ctx, parentDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cands {
		if c.DisplayName == "Collapse Pack" {
			found = true
			if !c.Registered || c.EndpointID != ep.ID {
				t.Fatalf("已登记候选状态: %+v (期望 endpoint %s)", c, ep.ID)
			}
		} else if c.Registered {
			t.Fatalf("未登记候选被标记: %+v", c)
		}
	}
	if !found {
		t.Fatal("发现结果缺少已登记项目")
	}
}

func TestDiscoverProjectsParentUnreadable(t *testing.T) {
	app, _ := newApp(t, packwiz.New())
	_, err := app.DiscoverProjects(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if code := errCode(t, err); code != endpoint.CodeDiscoveryFailed {
		t.Fatalf("错误码: %s", code)
	}
}

// ---- 登记 ----

func TestRegisterProjectIdempotentStableID(t *testing.T) {
	_, projectA, _ := makeFixtures(t)
	app, db := newApp(t, packwiz.New())
	ctx := context.Background()

	ep1, err := app.RegisterProject(ctx, view.RegisterEndpointInput{RootPath: projectA})
	if err != nil {
		t.Fatal(err)
	}
	if ep1.ID == "" || !strings.HasPrefix(ep1.ID, "prj_") {
		t.Fatalf("端点 ID 非稳定 opaque 前缀: %+v", ep1)
	}
	if ep1.Adapter != "packwiz" || ep1.DisplayName != "Collapse Pack" {
		t.Fatalf("端点投影: %+v", ep1)
	}
	// 登记使用规范化后的绝对路径
	if !filepath.IsAbs(ep1.RootPath) {
		t.Fatalf("RootPath 未规范化: %s", ep1.RootPath)
	}

	// 重复登记：同 fingerprint 命中 → 返回既有端点，不新建
	ep2, err := app.RegisterProject(ctx, view.RegisterEndpointInput{RootPath: projectA})
	if err != nil {
		t.Fatal(err)
	}
	if ep2.ID != ep1.ID {
		t.Fatalf("重复登记产生新 ID: %s vs %s", ep1.ID, ep2.ID)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&n); err != nil || n != 1 {
		t.Fatalf("projects 行数: %d err=%v", n, err)
	}
}

func TestRegisterProjectInvalidPaths(t *testing.T) {
	base := t.TempDir()
	noPack := filepath.Join(base, "EmptyDir")
	if err := os.MkdirAll(noPack, 0o755); err != nil {
		t.Fatal(err)
	}

	app, _ := newApp(t, packwiz.New())
	ctx := context.Background()

	if _, err := app.RegisterProject(ctx, view.RegisterEndpointInput{RootPath: filepath.Join(base, "Missing")}); errCode(t, err) != endpoint.CodeInvalidPath {
		t.Fatal("不存在路径")
	}
	if _, err := app.RegisterProject(ctx, view.RegisterEndpointInput{RootPath: noPack}); errCode(t, err) != endpoint.CodeInvalidPath {
		t.Fatal("缺 pack.toml 路径")
	}
}

// ---- 健康 ----

func TestGetProjectHealth(t *testing.T) {
	_, projectA, _ := makeFixtures(t)
	app, db := newApp(t, packwiz.New())
	ctx := context.Background()

	ep, err := app.RegisterProject(ctx, view.RegisterEndpointInput{RootPath: projectA})
	if err != nil {
		t.Fatal(err)
	}

	// 健康：路径存在 + 指纹匹配
	h, err := app.GetProjectHealth(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != endpoint.StatusOK || !h.PathExists || !h.FingerprintMatches || h.EndpointID != ep.ID || h.CheckedAt == "" {
		t.Fatalf("健康投影: %+v", h)
	}

	// 指纹不匹配：直接篡改存储指纹（模拟端点被移动/重建）
	if _, err := db.Exec(`UPDATE projects SET binding_fingerprint='sha256:stale' WHERE id=?`, ep.ID); err != nil {
		t.Fatal(err)
	}
	h, err = app.GetProjectHealth(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != endpoint.StatusIdentityMismatch || !h.PathExists || h.FingerprintMatches {
		t.Fatalf("指纹不匹配投影: %+v", h)
	}

	// 路径消失
	if err := os.RemoveAll(projectA); err != nil {
		t.Fatal(err)
	}
	h, err = app.GetProjectHealth(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != endpoint.StatusMissing || h.PathExists || h.FingerprintMatches {
		t.Fatalf("路径消失投影: %+v", h)
	}

	// 不存在的端点
	if _, err := app.GetProjectHealth(ctx, "prj_missing"); errCode(t, err) != endpoint.CodeNotFound {
		t.Fatal("not_found 错误码")
	}
}

// TestRegisterRelativePathNormalizes 验证相对路径输入经规范化管线登记为绝对路径。
func TestRegisterRelativePathNormalizes(t *testing.T) {
	_, projectA, _ := makeFixtures(t)
	app, _ := newApp(t, packwiz.New())

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(projectA)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	ep, err := app.RegisterProject(context.Background(), view.RegisterEndpointInput{RootPath: "Collapse"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(ep.RootPath) || !strings.EqualFold(filepath.Base(ep.RootPath), "Collapse") {
		t.Fatalf("相对路径未规范化: %s", ep.RootPath)
	}
}
