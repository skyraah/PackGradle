package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// FileKey 返回非空平台文件标识（Windows 卷+file index / Unix dev+ino）；
// 同一文件两次调用一致，不同文件不同。FS 不支持身份时允许为空（降级 size+mtime）。
func TestFileKeyStableAndDistinct(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	ka := NewHasher().FileKey(a)
	kb := NewHasher().FileKey(b)
	if ka == "" {
		t.Skip("当前文件系统不提供文件身份（网络盘/FAT 等），FileKey 降级为空")
	}
	if ka != NewHasher().FileKey(a) {
		t.Errorf("同一文件 FileKey 应稳定: %s vs %s", ka, NewHasher().FileKey(a))
	}
	if ka == kb {
		t.Errorf("不同文件 FileKey 应不同: %s", ka)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("fixture 异常")
	}
	if got := NewHasher().FileKey(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("不可达文件 FileKey 应为空，得到 %s", got)
	}
}

// HashFile 填充 FileFacts.FileKey，且与 FileKey(path) 一致。
func TestHashFileFillsFileKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(p, []byte("hello packgradle"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, facts, err := NewHasher().HashFile(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Digest == "" || ref.Size != int64(len("hello packgradle")) {
		t.Errorf("ContentRef 不符: %+v", ref)
	}
	if facts.FileKey == "" {
		t.Skip("当前文件系统不提供文件身份，FileKey 降级为空")
	}
	if facts.FileKey != NewHasher().FileKey(p) {
		t.Errorf("HashFile FileKey %s 应与 FileKey(path) %s 一致", facts.FileKey, NewHasher().FileKey(p))
	}
}

// FileKey 对目录返回空（仅常规文件有身份语义；目录身份由 BindingFingerprinter 负责）。
func TestFileKeyDirEmpty(t *testing.T) {
	if got := NewHasher().FileKey(t.TempDir()); got != "" {
		t.Errorf("目录 FileKey 应为空，得到 %s", got)
	}
}
