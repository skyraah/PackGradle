package runtime_test

// runtime 用例包 headless 测试：真实 store + 真实 adapters + 真实用例。
// 覆盖契约 03 §2.5：发现（Prism 实例目录 + 元数据）、幂等登记（adapter identity，
// 同名异路径身份冲突拒绝）、健康检查（只读）与错误码映射。

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/prism"
	"packgradle/internal/application/endpoint"
	"packgradle/internal/application/ports"
	"packgradle/internal/application/runtime"
	"packgradle/internal/application/view"
	"packgradle/internal/core/ids"
	"packgradle/internal/errs"
	"packgradle/internal/store"
	"packgradle/internal/store/sqlite"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeInstance 构造一个 Prism 实例目录（instance.cfg + minecraft/mods）。
func makeInstance(t *testing.T, base, name, mcVersion string) (instanceDir, gameDir string) {
	t.Helper()
	instanceDir = filepath.Join(base, "instances", name)
	gameDir = filepath.Join(instanceDir, "minecraft")
	writeFile(t, filepath.Join(instanceDir, "instance.cfg"),
		"[General]\nname="+name+"\niconKey=default\n")
	if mcVersion != "" {
		writeFile(t, filepath.Join(instanceDir, "mmc-pack.json"),
			`{"components":[{"uid":"net.minecraft","version":"`+mcVersion+`"},{"uid":"net.fabricmc.fabric-loader","version":"0.16.9"}]}`)
	}
	writeFile(t, filepath.Join(gameDir, "mods", "sodium.jar"), "fake jar")
	return
}

func newApp(t *testing.T, discovery ports.RuntimeDiscovery) (*runtime.App, *sql.DB) {
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
	app, err := runtime.New(runtime.Deps{
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

func TestDiscoverRuntimesEnumeratesInstances(t *testing.T) {
	base := t.TempDir()
	makeInstance(t, base, "Collapse", "1.20.1")
	makeInstance(t, base, "Vanilla", "1.21.1")
	writeFile(t, filepath.Join(base, "instances", "notes.txt"), "not an instance")

	app, _ := newApp(t, prism.NewDiscovererWith(func() (string, error) {
		return filepath.Join(base, "instances"), nil
	}))
	cands, err := app.DiscoverRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("候选数: %d (%+v)", len(cands), cands)
	}
	// DiscoverInstances 按 name 升序
	if cands[0].InstanceID != "Collapse" || cands[1].InstanceID != "Vanilla" {
		t.Fatalf("候选: %+v", cands)
	}
	if cands[0].Minecraft != "1.20.1" || cands[0].Modloader != "fabric" {
		t.Fatalf("候选元数据: %+v", cands[0])
	}
	if cands[0].Registered || cands[1].Registered {
		t.Fatal("未登记候选不应标记 registered")
	}
	if !strings.HasSuffix(filepath.ToSlash(cands[0].GameDir), "/minecraft") {
		t.Fatalf("候选 game_dir: %+v", cands[0])
	}
}

func TestDiscoverRuntimesMarksRegistered(t *testing.T) {
	base := t.TempDir()
	instanceDir, _ := makeInstance(t, base, "Collapse", "1.20.1")
	app, _ := newApp(t, prism.NewDiscovererWith(func() (string, error) {
		return filepath.Join(base, "instances"), nil
	}))
	ctx := context.Background()

	ep, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: instanceDir})
	if err != nil {
		t.Fatal(err)
	}
	cands, err := app.DiscoverRuntimes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || !cands[0].Registered || cands[0].EndpointID != ep.ID {
		t.Fatalf("已登记候选: %+v (期望 %s)", cands, ep.ID)
	}
}

func TestDiscoverRuntimesInstancesDirMissing(t *testing.T) {
	app, _ := newApp(t, prism.NewDiscovererWith(func() (string, error) {
		return "", &ports.InstancesDirError{DataDir: "C:/missing/PrismLauncher"}
	}))
	_, err := app.DiscoverRuntimes(context.Background())
	if code := errCode(t, err); code != endpoint.CodeInstancesDirNotFound {
		t.Fatalf("错误码: %s", code)
	}
	// args {0} = 尝试的数据目录
	var ae *errs.AppError
	if !errors.As(err, &ae) || len(ae.Args) != 1 || ae.Args[0] != "C:/missing/PrismLauncher" {
		t.Fatalf("错误 args: %#v", err)
	}
}

// ---- 登记 ----

func TestRegisterRuntimeIdempotentStableID(t *testing.T) {
	base := t.TempDir()
	instanceDir, _ := makeInstance(t, base, "Collapse", "1.20.1")
	app, db := newApp(t, prism.NewDiscovererWith(func() (string, error) {
		return filepath.Join(base, "instances"), nil
	}))
	ctx := context.Background()

	ep1, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: instanceDir})
	if err != nil {
		t.Fatal(err)
	}
	if ep1.ID == "" || !strings.HasPrefix(ep1.ID, "run_") {
		t.Fatalf("端点 ID 非稳定 opaque 前缀: %+v", ep1)
	}
	if ep1.Adapter != "prism" || ep1.AdapterIdentity != "collapse" || ep1.DisplayName != "Collapse" {
		t.Fatalf("端点投影: %+v", ep1)
	}
	// RootPath 是规范化后的游戏目录
	if !strings.EqualFold(filepath.Base(ep1.RootPath), "minecraft") {
		t.Fatalf("RootPath 应指向游戏目录: %s", ep1.RootPath)
	}

	ep2, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: instanceDir})
	if err != nil {
		t.Fatal(err)
	}
	if ep2.ID != ep1.ID {
		t.Fatalf("重复登记产生新 ID: %s vs %s", ep1.ID, ep2.ID)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM runtimes").Scan(&n); err != nil || n != 1 {
		t.Fatalf("runtimes 行数: %d err=%v", n, err)
	}
}

func TestRegisterRuntimeIdentityCollisionDifferentPath(t *testing.T) {
	base := t.TempDir()
	// 两个不同安装位置的同名实例目录
	dirA, _ := makeInstance(t, filepath.Join(base, "install1"), "Collapse", "1.20.1")
	other := filepath.Join(base, "install2")
	writeFile(t, filepath.Join(other, "instances", "Collapse", "instance.cfg"), "[General]\nname=\"Collapse\"\n")
	writeFile(t, filepath.Join(other, "instances", "Collapse", "minecraft", "mods", "x.jar"), "x")
	dirB := filepath.Join(other, "instances", "Collapse")

	app, _ := newApp(t, prism.NewDiscovererWith(func() (string, error) {
		return "", &ports.InstancesDirError{DataDir: base}
	}))
	ctx := context.Background()
	if _, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: dirA}); err != nil {
		t.Fatal(err)
	}
	_, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: dirB})
	if code := errCode(t, err); code != endpoint.CodeIdentityMismatch {
		t.Fatalf("同名异路径登记错误码: %s", code)
	}
}

func TestRegisterRuntimeInvalidPaths(t *testing.T) {
	base := t.TempDir()
	noCfg := filepath.Join(base, "NoCfg")
	writeFile(t, filepath.Join(noCfg, "minecraft", "mods", "x.jar"), "x")
	noGameDir := filepath.Join(base, "NoGameDir")
	writeFile(t, filepath.Join(noGameDir, "instance.cfg"), "[General]\nname=\"X\"\n")

	app, _ := newApp(t, prism.NewDiscovererWith(func() (string, error) {
		return "", &ports.InstancesDirError{DataDir: base}
	}))
	ctx := context.Background()

	if _, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: filepath.Join(base, "Missing")}); errCode(t, err) != endpoint.CodeInvalidPath {
		t.Fatal("不存在路径")
	}
	if _, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: noCfg}); errCode(t, err) != endpoint.CodeInvalidPath {
		t.Fatal("缺 instance.cfg")
	}
	if _, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: noGameDir}); errCode(t, err) != endpoint.CodeInvalidPath {
		t.Fatal("缺 minecraft 游戏目录")
	}
}

// ---- 健康 ----

func TestGetRuntimeHealth(t *testing.T) {
	base := t.TempDir()
	instanceDir, gameDir := makeInstance(t, base, "Collapse", "1.20.1")
	app, db := newApp(t, prism.NewDiscovererWith(func() (string, error) {
		return filepath.Join(base, "instances"), nil
	}))
	ctx := context.Background()

	ep, err := app.RegisterRuntime(ctx, view.RegisterEndpointInput{RootPath: instanceDir})
	if err != nil {
		t.Fatal(err)
	}

	h, err := app.GetRuntimeHealth(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != endpoint.StatusOK || !h.PathExists || !h.FingerprintMatches {
		t.Fatalf("健康投影: %+v", h)
	}

	// 指纹不匹配
	if _, err := db.Exec(`UPDATE runtimes SET binding_fingerprint='sha256:stale' WHERE id=?`, ep.ID); err != nil {
		t.Fatal(err)
	}
	h, err = app.GetRuntimeHealth(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != endpoint.StatusIdentityMismatch || h.FingerprintMatches {
		t.Fatalf("指纹不匹配投影: %+v", h)
	}

	// 游戏目录消失
	if err := os.RemoveAll(gameDir); err != nil {
		t.Fatal(err)
	}
	h, err = app.GetRuntimeHealth(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != endpoint.StatusMissing || h.PathExists {
		t.Fatalf("路径消失投影: %+v", h)
	}

	if _, err := app.GetRuntimeHealth(ctx, "run_missing"); errCode(t, err) != endpoint.CodeNotFound {
		t.Fatal("not_found 错误码")
	}
}
