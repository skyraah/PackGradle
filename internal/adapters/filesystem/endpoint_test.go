package filesystem

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// requireDirLink 在 link 处创建指向 target 的目录链接（Windows junction /
// 其他平台 symlink）；创建失败（权限/平台限制）跳过测试。
func requireDirLink(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		if err != nil {
			t.Skipf("本机无法创建 junction: %v: %s", err, out)
		}
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("本机无法创建 symlink: %v", err)
	}
}

func TestNormalizeEndpointPathAbsolutizesRelative(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "endpoint")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	got, err := NormalizeEndpointPath("endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("相对输入应绝对化: %q", got)
	}
	if got != filepath.Clean(sub) && got != sub {
		t.Fatalf("解析结果 %s != %s", got, sub)
	}
}

func TestNormalizeEndpointPathRealizesLink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-dir")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked")
	requireDirLink(t, link, real)
	got, err := NormalizeEndpointPath(link)
	if err != nil {
		t.Fatal(err)
	}
	if want, err := NormalizeEndpointPath(real); err != nil || got != want {
		t.Fatalf("链接路径应解析到 realpath: %s != %s (err=%v)", got, want, err)
	}
}

func TestNormalizeEndpointPathMissing(t *testing.T) {
	if _, err := NormalizeEndpointPath(filepath.Join(t.TempDir(), "不存在")); !errors.Is(err, ErrEndpointUnreachable) {
		t.Fatalf("不存在的端点应返回 ErrEndpointUnreachable，got %v", err)
	}
}

func TestNormalizeEndpointPathFileRejected(t *testing.T) {
	// 端点必须是目录：指向普通文件的路径不是合法端点根
	p := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeEndpointPath(p); !errors.Is(err, ErrEndpointUnreachable) {
		t.Fatalf("文件路径应拒绝（got %v）", err)
	}
}

func TestResolverNestedNonexistentTail(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	// mods/ 存在而 .index/ 及目标不存在：逐级解析最近存在祖先
	got, err := r.Resolve("mods/.index/sodium.jar.pw.toml")
	if err != nil {
		t.Fatalf("嵌套不存在路径应可解析: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(got), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(got, []byte("meta"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveJunctionEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	requireDirLink(t, link, outside)
	r, err := NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	// 既有文件经链接逃逸
	if _, err := r.Resolve("linked/secret.txt"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("junction 内既有文件逃逸应拒绝（got %v）", err)
	}
	// 尚不存在的文件经链接逃逸（partial 解析同样必须 containment）
	if _, err := r.Resolve("linked/new.txt"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("junction 内新文件逃逸应拒绝（got %v）", err)
	}
	// root 内合法深层路径不受影响
	if err := os.MkdirAll(filepath.Join(root, "real", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve("real/deep/ok.txt"); err != nil {
		t.Fatalf("root 内路径应放行: %v", err)
	}
}

func TestNewResolverRootMissing(t *testing.T) {
	if _, err := NewResolver(filepath.Join(t.TempDir(), "缺")); !errors.Is(err, ErrEndpointUnreachable) {
		t.Fatalf("root 不可达应返回 ErrEndpointUnreachable，got %v", err)
	}
}

func TestFingerprintViaLinkEqualsReal(t *testing.T) {
	f := NewFingerprinter()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked")
	requireDirLink(t, link, real)
	a, err := f.Fingerprint(real)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Fingerprint(link)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("同一端点经链接访问指纹必须一致: %s vs %s", a, b)
	}
}

func TestFingerprintMissingRoot(t *testing.T) {
	f := NewFingerprinter()
	if _, err := f.Fingerprint(filepath.Join(t.TempDir(), "缺")); err == nil {
		t.Fatal("不可达端点指纹应报错")
	}
}
