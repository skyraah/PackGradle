package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	// 注册 modernc 纯 Go SQLite 驱动（仅测试用内存/文件库）。
	_ "modernc.org/sqlite"
)

// objectsDDL 与 internal/store/sqlite v1 schema 的 objects 表定义一致（测试只需该表）。
const objectsDDL = `CREATE TABLE objects (algorithm TEXT NOT NULL, digest TEXT NOT NULL, size INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('staging','ready','quarantined')), created_at TEXT NOT NULL, PRIMARY KEY(algorithm, digest));`

// openTestCAS 建立临时对象库与仅含 objects 表的测试数据库。
func openTestCAS(t *testing.T) (*CAS, string) {
	t.Helper()
	dir := t.TempDir()

	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(objectsDDL); err != nil {
		t.Fatalf("建 objects 表失败: %v", err)
	}

	objectsRoot := filepath.Join(dir, "objects")
	cas, err := Open(objectsRoot, db)
	if err != nil {
		t.Fatalf("Open CAS 失败: %v", err)
	}
	return cas, objectsRoot
}

// countTmpFiles 统计 objectsRoot 下残留的 .tmp- 前缀文件数。
func countTmpFiles(t *testing.T, objectsRoot string) int {
	t.Helper()
	entries, err := os.ReadDir(objectsRoot)
	if err != nil {
		t.Fatalf("读取对象根目录失败: %v", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), tmpPrefix) {
			n++
		}
	}
	return n
}

func TestCASPutDedupAndLayout(t *testing.T) {
	cas, objectsRoot := openTestCAS(t)
	ctx := context.Background()
	content := []byte("packgradle cas fixture content")

	ref, err := cas.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	sum := sha256.Sum256(content)
	wantDigest := hex.EncodeToString(sum[:])
	if ref.Algorithm != "sha256" || ref.Digest != wantDigest || ref.Size != int64(len(content)) {
		t.Errorf("ContentRef = %+v, 期望 digest=%s size=%d", ref, wantDigest, len(content))
	}

	// 文件布局 sha256/<前2>/<hex>
	objPath := filepath.Join(objectsRoot, "sha256", wantDigest[:2], wantDigest)
	if _, err := os.Stat(objPath); err != nil {
		t.Fatalf("对象文件未落位 %s: %v", objPath, err)
	}

	// objects 行 ready
	var state string
	var size int64
	if err := cas.db.QueryRow("SELECT state, size FROM objects WHERE algorithm='sha256' AND digest=?", wantDigest).
		Scan(&state, &size); err != nil {
		t.Fatalf("objects 行缺失: %v", err)
	}
	if state != "ready" || size != int64(len(content)) {
		t.Errorf("objects 行 state=%q size=%d", state, size)
	}

	// 同内容二次 Put 去重：不报错、行数仍为 1、文件不变
	ref2, err := cas.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("二次 Put 失败: %v", err)
	}
	if ref2.Digest != ref.Digest {
		t.Errorf("二次 Put digest = %q, 期望 %q", ref2.Digest, ref.Digest)
	}
	var rows int
	if err := cas.db.QueryRow("SELECT COUNT(*) FROM objects WHERE digest=?", wantDigest).Scan(&rows); err != nil {
		t.Fatalf("统计 objects 失败: %v", err)
	}
	if rows != 1 {
		t.Errorf("去重后 objects 行数 = %d, 期望 1", rows)
	}

	// 不同内容 → 新对象
	other, err := cas.Put(ctx, bytes.NewReader([]byte("different")))
	if err != nil {
		t.Fatalf("Put 其它内容失败: %v", err)
	}
	if other.Digest == wantDigest {
		t.Error("不同内容不应得到相同 digest")
	}

	if n := countTmpFiles(t, objectsRoot); n != 0 {
		t.Errorf("残留临时文件 %d 个, 期望 0", n)
	}
}

func TestCASOpenAndHas(t *testing.T) {
	cas, _ := openTestCAS(t)
	ctx := context.Background()
	content := []byte("hello packgradle")

	ref, err := cas.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put 失败: %v", err)
	}

	ok, err := cas.Has(ctx, ref.Digest)
	if err != nil || !ok {
		t.Errorf("Has 已存对象 = (%v, %v), 期望 (true, nil)", ok, err)
	}
	if ok, _ := cas.Has(ctx, hex.EncodeToString(make([]byte, 32))); ok {
		t.Error("Has 未知 digest 不应命中")
	}

	rc, err := cas.Open(ctx, ref.Digest)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("读回对象失败: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("读回内容不一致: %q vs %q", got, content)
	}

	if _, err := cas.Open(ctx, hex.EncodeToString(make([]byte, 32))); err == nil {
		t.Error("Open 未知 digest 应报错")
	}
	if _, err := cas.Open(ctx, "bad-digest"); err == nil {
		t.Error("Open 非法 digest 应报错")
	}
}

// failingReader 先返回前 failAt 字节，之后报错。
type failingReader struct {
	data   []byte
	failAt int
	off    int
}

func (fr *failingReader) Read(p []byte) (int, error) {
	if fr.off >= fr.failAt {
		return 0, errors.New("simulated reader failure")
	}
	n := copy(p, fr.data[fr.off:fr.failAt])
	fr.off += n
	return n, nil
}

func TestCASPutReaderFailureRollsBack(t *testing.T) {
	cas, objectsRoot := openTestCAS(t)
	ctx := context.Background()
	content := bytes.Repeat([]byte("0123456789"), 100) // 1000 字节
	fr := &failingReader{data: content, failAt: 400}

	if _, err := cas.Put(ctx, fr); err == nil {
		t.Fatal("reader 中途失败时 Put 应返回错误")
	}

	// 无 ready 行（完整 digest 对应的内容从未登记）
	var rows int
	if err := cas.db.QueryRow("SELECT COUNT(*) FROM objects").Scan(&rows); err != nil {
		t.Fatalf("统计 objects 失败: %v", err)
	}
	if rows != 0 {
		t.Errorf("失败后 objects 行数 = %d, 期望 0", rows)
	}

	// 无残留 tmp 文件
	if n := countTmpFiles(t, objectsRoot); n != 0 {
		t.Errorf("失败后残留临时文件 %d 个, 期望 0", n)
	}
}
