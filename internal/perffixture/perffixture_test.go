package perffixture

// 确定性 fixture 契约（验收规格 §2.1）：同参数重放逐字节相同；构成与大小
// 分布符合规格；内容伪随机（无全零/全同文件）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// treeDigest 递归采集目录树指纹（rel slash 路径 → sha256）。
func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(content)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertTreeEqual(t *testing.T, a, b map[string]string) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("文件数不同: %d vs %d", len(a), len(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("内容不同: %s", k)
		}
	}
}

func TestGenerateDeterministicReplay(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	opts := Options{OutDir: dirA, Seed: 42, Mods: 6, TextFiles: 10}
	if _, err := Generate(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(context.Background(), Options{OutDir: dirB, Seed: 42, Mods: 6, TextFiles: 10}); err != nil {
		t.Fatal(err)
	}
	assertTreeEqual(t, treeDigest(t, dirA), treeDigest(t, dirB))

	// 换 seed → 内容必须不同（固定 seed 的另一面：非退化）
	dirC := t.TempDir()
	if _, err := Generate(context.Background(), Options{OutDir: dirC, Seed: 43, Mods: 6, TextFiles: 10}); err != nil {
		t.Fatal(err)
	}
	dA, dC := treeDigest(t, dirA), treeDigest(t, dirC)
	same := 0
	for k, v := range dA {
		if dC[k] == v {
			same++
		}
	}
	if same > len(dA)/2 {
		t.Fatalf("不同 seed 产出过多相同文件: %d/%d", same, len(dA))
	}
}

func TestGenerateLayoutAndSizes(t *testing.T) {
	dir := t.TempDir()
	mods, textFiles := 20, 50
	res, err := Generate(context.Background(), Options{OutDir: dir, Seed: 7, Mods: mods, TextFiles: textFiles})
	if err != nil {
		t.Fatal(err)
	}
	if want := mods*2 + textFiles + 7; res.Files != want { // metafile+jar 各 mods、text、index/pack/instance.cfg、双侧合并样本 ×2（票 #87）
		t.Fatalf("文件数 = %d, 期望 %d", res.Files, want)
	}
	if res.ModCount != mods || res.ManagedFiles != mods+textFiles {
		t.Fatalf("摘要不符: %+v", res)
	}
	gameDir := filepath.Join(dir, "instance", "minecraft")

	// 项目侧：pack.toml/index.toml/mods metafile 存在
	for _, p := range []string{
		filepath.Join(dir, "project", "pack.toml"),
		filepath.Join(dir, "project", "index.toml"),
		filepath.Join(dir, "project", "mods", metaFileName(0)),
		filepath.Join(dir, "instance", "instance.cfg"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("缺少 %s: %v", p, err)
		}
	}

	// JAR：数量、大小分布（i%10==0 → 5~20MB；其余 200KB~5MB）
	for i := 0; i < mods; i++ {
		st, err := os.Stat(filepath.Join(gameDir, "mods", jarFileName(i)))
		if err != nil {
			t.Fatal(err)
		}
		if i%10 == 0 {
			if st.Size() < 5<<20 || st.Size() >= 20<<20 {
				t.Fatalf("JAR %d 大小 %d 不在 5~20MB", i, st.Size())
			}
		} else if st.Size() < 200<<10 || st.Size() >= 5<<20 {
			t.Fatalf("JAR %d 大小 %d 不在 200KB~5MB", i, st.Size())
		}
	}

	// 受管文件：数量与大小（1KB~100KB）
	for i := 0; i < textFiles; i++ {
		st, err := os.Stat(filepath.Join(gameDir, filepath.FromSlash(textRelPath(i))))
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() < 1<<10 || st.Size() >= 100<<10 {
			t.Fatalf("受管文件 %d 大小 %d 不在 1KB~100KB", i, st.Size())
		}
	}
}

func TestGenerateContentNotDegenerate(t *testing.T) {
	dir := t.TempDir()
	// 覆盖文本（seed%10!=0）与二进制（seed%10==0）两类文件
	_, err := writeRandomFile(context.Background(), filepath.Join(dir, "text.bin"), fileSeed(7, 1), 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	_, err = writeRandomFile(context.Background(), filepath.Join(dir, "bin.bin"), fileSeed(7, 10), 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"text.bin", "bin.bin"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		distinct := map[byte]bool{}
		for _, c := range b {
			distinct[c] = true
		}
		if len(distinct) < 16 {
			t.Fatalf("%s 熵过低（%d 种字节值）——hash cache 行为会失真", name, len(distinct))
		}
		if name == "text.bin" && !bytes.ContainsRune(b, ' ') {
			t.Fatalf("%s 应为行式文本", name)
		}
	}
}

// TestGenerateMergeSamples（票 #87）：手工注释 toml 与二进制资源样本双侧
// 同字节落盘，样本含注释/键序/空行/缩进要素与两个注入锚点。
func TestGenerateMergeSamples(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(context.Background(), Options{OutDir: dir, Seed: 7, Mods: 2, TextFiles: 2}); err != nil {
		t.Fatal(err)
	}
	projSample := filepath.Join(dir, "project", filepath.FromSlash(HandmadeTomlRel))
	rtSample := filepath.Join(dir, "instance", "minecraft", filepath.FromSlash(HandmadeTomlRel))
	projBin := filepath.Join(dir, "project", filepath.FromSlash(BinarySampleRel))
	rtBin := filepath.Join(dir, "instance", "minecraft", filepath.FromSlash(BinarySampleRel))
	for _, p := range []string{projSample, rtSample, projBin, rtBin} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("缺少合并样本 %s: %v", p, err)
		}
	}
	// 双侧同字节：初次同步后进基线的前提。
	a, _ := os.ReadFile(projSample)
	b, _ := os.ReadFile(rtSample)
	if !bytes.Equal(a, b) || string(a) != HandmadeToml {
		t.Fatal("手工注释 toml 样本双侧应同字节且等于常量定义")
	}
	c, _ := os.ReadFile(projBin)
	d, _ := os.ReadFile(rtBin)
	if !bytes.Equal(c, d) {
		t.Fatal("二进制资源样本双侧应同字节")
	}
	if bytes.IndexByte(c, 0) < 0 {
		t.Fatal("二进制样本应含 NUL（真二进制而非文本）")
	}
	for _, want := range []string{"# 手工注释样本", "  render_distance", `[project_anchor]`, `[runtime_anchor]`,
		`project_marker = "untouched"`, `runtime_marker = "untouched"`} {
		if !strings.Contains(string(a), want) {
			t.Fatalf("样本缺要素 %q", want)
		}
	}
}

// TestDualEditVariants（票 #87）：merge 变体两侧互不重叠改动；conflict 变体
// 双侧同段不同改动；project 侧文件缺失即报错。
func TestDualEditVariants(t *testing.T) {
	newFixture := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		if _, err := Generate(context.Background(), Options{OutDir: dir, Seed: 9, Mods: 1, TextFiles: 1}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("merge 变体互不重叠", func(t *testing.T) {
		dir := newFixture(t)
		if err := DualEdit(dir, "merge"); err != nil {
			t.Fatalf("DualEdit: %v", err)
		}
		proj, _ := os.ReadFile(filepath.Join(dir, "project", filepath.FromSlash(HandmadeTomlRel)))
		rt, _ := os.ReadFile(filepath.Join(dir, "instance", "minecraft", filepath.FromSlash(HandmadeTomlRel)))
		if !strings.Contains(string(proj), `project_marker = "edited-by-project"`) ||
			strings.Contains(string(proj), `runtime_marker = "edited-by-runtime"`) {
			t.Fatalf("project 侧应只改 project_anchor:\n%s", proj)
		}
		if !strings.Contains(string(rt), `runtime_marker = "edited-by-runtime"`) ||
			strings.Contains(string(rt), `project_marker = "edited-by-project"`) {
			t.Fatalf("runtime 侧应只改 runtime_anchor:\n%s", rt)
		}
	})
	t.Run("conflict 变体双侧同段不同改动", func(t *testing.T) {
		dir := newFixture(t)
		if err := DualEdit(dir, "conflict"); err != nil {
			t.Fatalf("DualEdit: %v", err)
		}
		proj, _ := os.ReadFile(filepath.Join(dir, "project", filepath.FromSlash(HandmadeTomlRel)))
		rt, _ := os.ReadFile(filepath.Join(dir, "instance", "minecraft", filepath.FromSlash(HandmadeTomlRel)))
		if !strings.Contains(string(proj), `project_marker = "project-side"`) {
			t.Fatalf("project 侧应改 project_anchor:\n%s", proj)
		}
		if !strings.Contains(string(rt), `project_marker = "runtime-side"`) {
			t.Fatalf("runtime 侧应同改 project_anchor:\n%s", rt)
		}
	})
	t.Run("未知变体报错", func(t *testing.T) {
		if err := DualEdit(newFixture(t), "bogus"); err == nil {
			t.Fatal("未知变体应报错")
		}
	})
	t.Run("project 侧文件缺失报错", func(t *testing.T) {
		dir := newFixture(t)
		// 模拟未同步：删除 project 侧副本
		if err := os.Remove(filepath.Join(dir, "project", filepath.FromSlash(HandmadeTomlRel))); err != nil {
			t.Fatal(err)
		}
		if err := DualEdit(dir, "merge"); err == nil {
			t.Fatal("project 侧文件缺失应报错")
		}
	})
}
