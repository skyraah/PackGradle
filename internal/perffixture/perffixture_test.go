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
	if want := mods*2 + textFiles + 3; res.Files != want { // metafile+jar 各 mods、text、index/pack/instance.cfg
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
		st, err := os.Stat(filepath.Join(gameDir, "mods", jarFileName(Options{}, i)))
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
