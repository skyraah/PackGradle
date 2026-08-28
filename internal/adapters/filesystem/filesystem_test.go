package filesystem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHasherHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.toml")
	content := []byte("hello packgradle\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHasher()
	ref, facts, err := h.HashFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(content)
	if ref.Algorithm != "sha256" || ref.Digest != hex.EncodeToString(want[:]) {
		t.Fatalf("digest 不符: %+v", ref)
	}
	if ref.Size != int64(len(content)) || facts.SizeBytes != int64(len(content)) {
		t.Fatalf("size 不符: %+v %+v", ref, facts)
	}
	if _, _, err := h.HashFile(context.Background(), filepath.Join(dir, "不存在")); err == nil {
		t.Fatal("缺失文件应报错")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "sub", "out.txt")
	if err := WriteFileAtomic(dest, strings.NewReader("第一版")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(dest, strings.NewReader("第二版覆盖")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "第二版覆盖" {
		t.Fatalf("覆盖写入结果: %q err=%v", got, err)
	}
	if err := WriteFileAtomic(dest, failingReader{}); err == nil {
		t.Fatal("reader 失败应返回错误")
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pgtmp-") {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
}

func TestResolveWithin(t *testing.T) {
	root := t.TempDir()
	normal := filepath.Join(root, "config", "a.toml")
	if err := os.MkdirAll(filepath.Dir(normal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(normal, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWithin(root, "config/a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(got, normal) {
		t.Fatalf("解析结果 %s != %s", got, normal)
	}
	// 不存在的目标（计划写入新文件）也可解析
	if _, err := ResolveWithin(root, "config/new.toml"); err != nil {
		t.Fatalf("新文件路径应可解析: %v", err)
	}
	for _, bad := range []string{"..", "../escape", "/abs", `C:\abs`, "", "a/../../b"} {
		if _, err := ResolveWithin(root, bad); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("%q 应被拒绝（got err=%v）", bad, err)
		}
	}
}

func TestResolveWithinSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(outFile, link); err != nil {
		t.Skipf("本机无法创建 symlink: %v", err)
	}
	if _, err := ResolveWithin(root, "escape.txt"); !errors.Is(err, ErrPathEscape) {
		t.Fatal("指向 root 外的 symlink 应被拒绝")
	}
	// root 内合法 symlink 放行
	inDir := filepath.Join(root, "real")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inFile := filepath.Join(inDir, "in.txt")
	if err := os.WriteFile(inFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	okLink := filepath.Join(root, "ok.txt")
	if err := os.Symlink(inFile, okLink); err != nil {
		t.Skipf("本机无法创建 symlink: %v", err)
	}
	if _, err := ResolveWithin(root, "ok.txt"); err != nil {
		t.Fatalf("root 内 symlink 应放行: %v", err)
	}
}

func TestFingerprintStable(t *testing.T) {
	f := NewFingerprinter()
	dir := t.TempDir()
	a, err := f.Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("同 root 指纹不稳定: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("指纹格式: %s", a)
	}
	other, _ := f.Fingerprint(filepath.Join(dir, "sub"))
	if other == a {
		t.Fatal("不同路径应有不同指纹")
	}
	// 同一目录以不同写法（尾部分隔符）访问，指纹应一致
	c, _ := f.Fingerprint(dir + string(os.PathSeparator))
	if c != a {
		t.Fatalf("路径写法差异影响指纹: %s vs %s", c, a)
	}
}

var _ = bytes.MinRead // 保持 bytes 引用（错误注入测试使用）
