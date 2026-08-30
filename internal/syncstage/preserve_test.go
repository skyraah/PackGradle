package syncstage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"packgradle/internal/core/model"
	"packgradle/internal/store/objectstore"

	// 注册 modernc 纯 Go SQLite 驱动（仅测试用：真实 CAS 落盘验证）。
	_ "modernc.org/sqlite"
)

// objectsDDL 与 internal/store/sqlite v1 schema 的 objects 表定义一致
// （沿 objectstore 自测先例，测试只需该表）。
const objectsDDL = `CREATE TABLE objects (algorithm TEXT NOT NULL, digest TEXT NOT NULL, size INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('staging','ready','quarantined')), created_at TEXT NOT NULL, PRIMARY KEY(algorithm, digest));`

// openTestCAS 建立临时对象库与仅含 objects 表的测试数据库（真实落盘）。
func openTestCAS(t *testing.T) (*objectstore.CAS, string) {
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
	cas, err := objectstore.Open(objectsRoot, db)
	if err != nil {
		t.Fatalf("Open CAS 失败: %v", err)
	}
	return cas, objectsRoot
}

// corruptingStore 在 Put 后按 digest 返回被破坏的内容流（模拟落盘损坏/复核失配）。
type corruptingStore struct {
	inner *objectstore.CAS
}

func (s *corruptingStore) Put(ctx context.Context, r io.Reader) (model.ContentRef, error) {
	return s.inner.Put(ctx, r)
}

func (s *corruptingStore) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	rc, err := s.inner.Open(ctx, digest)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		data[0] ^= 0xFF // 翻转首字节
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// lyingStore 的 Put 返回与内容不符的 digest（模拟 CAS 写入侧虚报）。
type lyingStore struct{}

func (lyingStore) Put(ctx context.Context, r io.Reader) (model.ContentRef, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return model.ContentRef{}, err
	}
	_ = data
	return model.ContentRef{Algorithm: "sha256", Digest: digestOf([]byte("claimed")), Size: 7}, nil
}

func (lyingStore) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	return nil, errors.New("lying store: 无对象可开")
}

// ---- RequiresCASBackup 策略矩阵 ----

func TestRequiresCASBackupMatrix(t *testing.T) {
	cases := []struct {
		rec  model.Recoverability
		want bool
	}{
		{model.RecoverabilityCAS, true},
		{model.RecoverabilityUserObject, true},
		{model.RecoverabilityUnrecoverable, true},
		{model.RecoverabilityNone, false},
		{model.RecoverabilityRedownload, false},
		{model.Recoverability("unknown_policy"), true}, // 未知值 fail-safe
		{model.Recoverability(""), true},
	}
	for _, c := range cases {
		if got := RequiresCASBackup(c.rec); got != c.want {
			t.Errorf("RequiresCASBackup(%q) = %v, 期望 %v", c.rec, got, c.want)
		}
	}
}

// ---- PreserveBeforeContent：真实 CAS 落盘 + 独立复核 ----

func TestPreserveBeforeContentRealCASBinaryRoundtrip(t *testing.T) {
	cas, objectsRoot := openTestCAS(t)
	ctx := context.Background()
	root := newEndpointRoot(t)
	content := randomBytes(t, 512*1024)
	path := writeEndpointFile(t, root, "mods/jei.jar", content)

	ref, preserved, err := PreserveBeforeContent(ctx, cas, path, model.RecoverabilityUnrecoverable)
	if err != nil {
		t.Fatalf("PreserveBeforeContent 失败: %v", err)
	}
	if !preserved {
		t.Fatal("unrecoverable 内容必须保全")
	}
	if ref.Algorithm != "sha256" || ref.Digest != digestOf(content) || ref.Size != int64(len(content)) {
		t.Fatalf("引用 = %+v, 期望 digest=%s size=%d", ref, digestOf(content), len(content))
	}

	// 对象文件真实落盘且字节一致（二进制往返）
	objPath := filepath.Join(objectsRoot, "sha256", ref.Digest[:2], ref.Digest)
	if got := readEndpointFile(t, objPath); !bytes.Equal(got, content) {
		t.Error("CAS 对象内容与源不一致")
	}

	// 同内容幂等：重复保全得到同一引用
	ref2, _, err := PreserveBeforeContent(ctx, cas, path, model.RecoverabilityUserObject)
	if err != nil || ref2.Digest != ref.Digest {
		t.Errorf("重复保全 = (%+v, %v), 期望同 digest", ref2, err)
	}
}

func TestPreserveBeforeContentPolicyExempt(t *testing.T) {
	cas, objectsRoot := openTestCAS(t)
	root := newEndpointRoot(t)
	path := writeEndpointFile(t, root, "mods/dl.jar", []byte("redownloadable"))

	ref, preserved, err := PreserveBeforeContent(context.Background(), cas, path, model.RecoverabilityRedownload)
	if err != nil || preserved || ref != zeroContentRef {
		t.Errorf("策略豁免应返回未保全零引用, 实际 (%+v, %v, %v)", ref, preserved, err)
	}
	// 无对象被写入
	if _, err := os.Stat(filepath.Join(objectsRoot, "sha256")); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("对象分片目录不应存在: %v", err)
		}
	}
}

func TestPreserveBeforeContentMissingTarget(t *testing.T) {
	cas, _ := openTestCAS(t)
	root := newEndpointRoot(t)
	missing := filepath.Join(root, "gone.ini")

	_, preserved, err := PreserveBeforeContent(context.Background(), cas, missing, model.RecoverabilityCAS)
	if err != nil || preserved {
		t.Errorf("目标缺失应视为无需保全, 实际 (%v, %v)", preserved, err)
	}
}

func TestPreserveBeforeContentNotPlainFile(t *testing.T) {
	cas, _ := openTestCAS(t)
	root := newEndpointRoot(t)
	dir := filepath.Join(root, "adir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PreserveBeforeContent(context.Background(), cas, dir, model.RecoverabilityCAS); !errors.Is(err, ErrTargetNotFile) {
		t.Errorf("目录应拒绝保全, 实际 %v", err)
	}
}

// TestPreserveBeforeContentVerifyFailureNoRef 复核失败即错、不返回引用：
// CAS 返回的 digest 只是写入侧宣称值，落盘对象必须独立复核。
func TestPreserveBeforeContentVerifyFailureNoRef(t *testing.T) {
	root := newEndpointRoot(t)
	path := writeEndpointFile(t, root, "mods/x.jar", []byte("trustworthy content"))

	// 落盘对象读回被破坏 → 复核失配
	corrupt := &corruptingStore{inner: func() *objectstore.CAS {
		c, _ := openTestCAS(t)
		return c
	}()}
	ref, preserved, err := PreserveBeforeContent(context.Background(), corrupt, path, model.RecoverabilityCAS)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("复核失配应返回 ErrDigestMismatch, 实际 %v", err)
	}
	if preserved || ref != zeroContentRef {
		t.Errorf("复核失败不得返回引用, 实际 (%+v, %v)", ref, preserved)
	}

	// 写入侧虚报 digest（对象根本不可开）→ 同样失败且无引用
	lying := lyingStore{}
	ref2, preserved2, err := PreserveBeforeContent(context.Background(), lying, path, model.RecoverabilityCAS)
	if err == nil {
		t.Fatal("虚报 digest 应失败")
	}
	if preserved2 || ref2 != zeroContentRef {
		t.Errorf("失败路径不得返回引用, 实际 (%+v, %v)", ref2, preserved2)
	}
}
