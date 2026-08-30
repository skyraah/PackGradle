package syncstage

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"packgradle/internal/core/model"
)

// ---- 夹具 ----

// digestOf 计算字节内容的 sha256（hex）。
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// randomBytes 生成 n 字节密码学随机内容（含全字节值域，二进制往返夹具）。
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("生成随机内容失败: %v", err)
	}
	return b
}

// newStagingRoot 建立临时 staging 根目录。
func newStagingRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// openTestRun 在临时 staging 根下打开命名运行。
func openTestRun(t *testing.T, stagingRoot, taskID string) *Run {
	t.Helper()
	run, err := OpenRun(stagingRoot, taskID)
	if err != nil {
		t.Fatalf("OpenRun(%s) 失败: %v", taskID, err)
	}
	return run
}

// newEndpointRoot 建立临时端点 root 目录。
func newEndpointRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// writeEndpointFile 在端点 root 内写入文件，返回绝对路径。
func writeEndpointFile(t *testing.T, root, rel string, data []byte) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("建父目录失败: %v", err)
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		t.Fatalf("写夹具文件失败: %v", err)
	}
	return abs
}

// newActions 建立「运行 + 端点」的测试动作执行器。
func newActions(t *testing.T, taskID string) (*Actions, *Run, string) {
	t.Helper()
	run := openTestRun(t, newStagingRoot(t), taskID)
	root := newEndpointRoot(t)
	a, err := NewActions(run, root)
	if err != nil {
		t.Fatalf("NewActions 失败: %v", err)
	}
	return a, run, root
}

// mustIssueProof 签发证明，失败即致命。
func mustIssueProof(t *testing.T, run *Run, opID, relationID, targetRel, before, after string) OwnershipProof {
	t.Helper()
	p, err := run.IssueProof(relationID, opID, targetRel, before, after)
	if err != nil {
		t.Fatalf("IssueProof(%s) 失败: %v", opID, err)
	}
	return p
}

// readEndpointFile 读取端点内文件内容。
func readEndpointFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return data
}

// ---- digest 复核原语 ----

func TestHashFileAndVerifyDigest(t *testing.T) {
	root := newEndpointRoot(t)
	content := []byte("packgradle syncstage digest fixture")
	path := writeEndpointFile(t, root, "cfg/opts.ini", content)

	ref, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile 失败: %v", err)
	}
	if ref.Algorithm != "sha256" || ref.Digest != digestOf(content) || ref.Size != int64(len(content)) {
		t.Errorf("HashFile = %+v, 期望 digest=%s size=%d", ref, digestOf(content), len(content))
	}
	if err := VerifyFileDigest(path, ref.Digest); err != nil {
		t.Errorf("VerifyFileDigest 命中失败: %v", err)
	}
	other := digestOf([]byte("other content"))
	if err := VerifyFileDigest(path, other); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("VerifyFileDigest 不匹配应返回 ErrDigestMismatch, 实际 %v", err)
	}
}

// TestHashFileRejectsDirectory 目录不是可摘要目标。
func TestHashFileRejectsDirectory(t *testing.T) {
	root := newEndpointRoot(t)
	dir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := HashFile(dir); !errors.Is(err, ErrTargetNotFile) {
		t.Errorf("目录摘要应返回 ErrTargetNotFile, 实际 %v", err)
	}
}

// ---- writeFileAtomic：真实落盘 + 二进制往返 + 无临时残留 ----

func TestWriteFileAtomicBinaryRoundtrip(t *testing.T) {
	root := newEndpointRoot(t)
	dest := filepath.Join(root, "bin", "sodium-0.6.5.jar")
	content := randomBytes(t, 1024*1024+7) // >1MB，越过缓冲边界

	if err := writeFileAtomic(dest, bytes.NewReader(content)); err != nil {
		t.Fatalf("writeFileAtomic 失败: %v", err)
	}
	if got := readEndpointFile(t, dest); !bytes.Equal(got, content) {
		t.Errorf("二进制往返不一致: got %d bytes want %d bytes", len(got), len(content))
	}

	// 同名覆盖重写仍逐字节一致（os.Rename 的 Windows 覆盖语义）
	second := randomBytes(t, 4096)
	if err := writeFileAtomic(dest, bytes.NewReader(second)); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}
	if got := readEndpointFile(t, dest); !bytes.Equal(got, second) {
		t.Errorf("覆盖后内容不一致")
	}

	// 目录内无 .pgtmp-* 残留
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > 7 && e.Name()[:7] == ".pgtmp-" {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

// ---- 路径防线 ----

func TestResolveTargetRejectsEscape(t *testing.T) {
	root := newEndpointRoot(t)
	cases := []string{
		"../outside.txt",  // 相对逃逸
		"a/../../b.txt",   // 中段逃逸
		"/abs.txt",        // 根绝对路径
		"",                // 空路径
		".",               // 纯当前目录
		"C:/windows/evil", // 盘符（冒号一律拒绝）
		"mods\\..\\evil",  // 反斜杠夹带 ..
	}
	for _, rel := range cases {
		if _, _, err := resolveTarget(root, rel); !errors.Is(err, ErrPathEscape) {
			t.Errorf("resolveTarget(%q) 应返回 ErrPathEscape, 实际 %v", rel, err)
		}
	}
	// 逃逸目标不得在 root 外产生任何文件
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("root 外出现了文件: %v", err)
	}
}

func TestResolveTargetAcceptsCleanPaths(t *testing.T) {
	root := newEndpointRoot(t)
	abs, exists, err := resolveTarget(root, "mods/a/b.toml")
	if err != nil {
		t.Fatalf("干净路径被拒绝: %v", err)
	}
	if exists {
		t.Error("不存在的目标应 exists=false")
	}
	if abs != filepath.Join(root, "mods", "a", "b.toml") {
		t.Errorf("abs = %s", abs)
	}
	// 反斜杠输入归一为斜杠语义
	abs2, _, err := resolveTarget(root, `mods\a\b.toml`)
	if err != nil || abs2 != abs {
		t.Errorf("反斜杠归一失败: abs2=%s err=%v", abs2, err)
	}
}

func TestWithinRoot(t *testing.T) {
	root := `C:\Data\Root`
	cases := []struct {
		target string
		want   bool
	}{
		{`C:\Data\Root`, true},
		{`C:\Data\Root\mods\a.toml`, true},
		{`c:\data\root\MODS\a.toml`, true}, // 大小写不敏感
		{`C:\Data\Root2\a.toml`, false},    // 前缀碰撞
		{`C:\Data\Other\a.toml`, false},
		{`D:\Data\Root`, false},
	}
	for _, c := range cases {
		if got := withinRoot(root, c.target); got != c.want {
			t.Errorf("withinRoot(%q, %q) = %v, 期望 %v", root, c.target, got, c.want)
		}
	}
}

// zeroContentRef 便于断言「未返回引用」。
var zeroContentRef = model.ContentRef{}
