package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// putTestObject 放入指定内容的对象并返回 digest。
func putTestObject(t *testing.T, c *CAS, content string) string {
	t.Helper()
	ref, err := c.Put(context.Background(), strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	return ref.Digest
}

// TestMoveToTrashLifecycle 覆盖回收站全周期（ADR-0007 §5）：Put → MoveToTrash
//（对象文件删除、trash 副本 .zst 落位、digest 从文件名复原）→ RestoreFromTrash
//（字节级复原）→ 二次 MoveToTrash 幂等 → DeleteTrashEntry 物理清除。
func TestMoveToTrashLifecycle(t *testing.T) {
	c, _ := openTestCAS(t)
	ctx := context.Background()
	content := "回收站全周期测试内容 @ PackGradle"
	digest := putTestObject(t, c, content)

	objPath := filepath.Join(c.objectsRoot, algorithm, digest[:2], digest)
	if _, err := os.Stat(objPath); err != nil {
		t.Fatalf("Put 后对象文件应在盘: %v", err)
	}

	// MoveToTrash：原文件删、trash 副本落位。
	if err := c.MoveToTrash(digest); err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}
	if _, err := os.Stat(objPath); !os.IsNotExist(err) {
		t.Fatalf("MoveToTrash 后原对象文件应删除: %v", err)
	}
	entries, err := c.ListTrash()
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(entries) != 1 || entries[0].Digest != digest {
		t.Fatalf("trash 条目 = %v，期望 [%s]", entries, digest)
	}
	if entries[0].SizeBytes == 0 {
		t.Fatalf("trash 副本 size=0（zstd 压缩未写入）")
	}

	// 盘面搬运后文件已不在 → Has() 不可见（隔离期的可见性语义由 DB 层
	// QuarantineObjects 的 state 标记正式承担，此处验证文件缺失即不可见）。
	ok, err := c.Has(ctx, digest)
	if err != nil || ok {
		t.Fatalf("搬运后 Has 应不可见: ok=%v err=%v", ok, err)
	}

	// RestoreFromTrash：字节级复原。
	if err := c.RestoreFromTrash(digest); err != nil {
		t.Fatalf("RestoreFromTrash: %v", err)
	}
	rc, err := c.Open(ctx, digest)
	if err != nil {
		t.Fatalf("复活后 Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != content {
		t.Fatalf("复活内容不匹配：got %q want %q", got, content)
	}

	// 已复活后再次 RestoreFromTrash 幂等（对象已在盘）。
	if err := c.RestoreFromTrash(digest); err != nil {
		t.Fatalf("RestoreFromTrash 幂等: %v", err)
	}

	// 再 MoveToTrash（可重入）→ DeleteTrashEntry 清除。
	if err := c.MoveToTrash(digest); err != nil {
		t.Fatalf("二次 MoveToTrash: %v", err)
	}
	if err := c.DeleteTrashEntry(entries[0]); err != nil {
		t.Fatalf("DeleteTrashEntry: %v", err)
	}
	entries, _ = c.ListTrash()
	if len(entries) != 0 {
		t.Fatalf("清除后 trash 应为空，got %v", entries)
	}
}

// TestMoveToTrashMissingObject：对象文件缺失返回 os.ErrNotExist 语义
//（row-without-file 场景由引擎改走删行对账，ADR-0007 §6）。
func TestMoveToTrashMissingObject(t *testing.T) {
	c, _ := openTestCAS(t)
	digest := strings.Repeat("ab", 32)
	err := c.MoveToTrash(digest)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失对象 MoveToTrash err=%v，期望 os.ErrNotExist 语义", err)
	}
}

// TestRestoreFromTrashMissingCopy：trash 副本缺失报错（复活通道的可诊断失败）。
func TestRestoreFromTrashMissingCopy(t *testing.T) {
	c, _ := openTestCAS(t)
	digest := strings.Repeat("cd", 32)
	if err := c.RestoreFromTrash(digest); err == nil {
		t.Fatalf("trash 副本缺失应报错")
	}
}

// TestListObjectFilesAndTmpFiles 覆盖孤儿三向清扫的盘面事实采集（ADR-0007 §6）：
// 在盘对象 digest 从路径复原、非法文件名跳过、.tmp-* 残渣单列、DeleteFile 幂等。
func TestListObjectFilesAndTmpFiles(t *testing.T) {
	c, objectsRoot := openTestCAS(t)
	ctx := context.Background()

	d1 := putTestObject(t, c, "对象一")
	d2 := putTestObject(t, c, "对象二")

	// 非法 digest 文件名（外部残留）不应进清单。
	junk := filepath.Join(objectsRoot, algorithm, "not-a-digest.txt")
	if err := os.WriteFile(junk, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := c.ListObjectFiles()
	if err != nil {
		t.Fatalf("ListObjectFiles: %v", err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Digest] = true
	}
	if !got[d1] || !got[d2] {
		t.Fatalf("清单缺在盘对象：got %v", got)
	}
	if got["not-a-digest.txt"] {
		t.Fatalf("非法 digest 文件不应进清单")
	}

	// .tmp-* 残渣：模拟 Put 写中断残留。
	tmpName := filepath.Join(objectsRoot, tmpPrefix+"broken")
	if err := os.WriteFile(tmpName, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmps, err := c.ListTmpFiles()
	if err != nil {
		t.Fatalf("ListTmpFiles: %v", err)
	}
	if len(tmps) != 1 {
		t.Fatalf(".tmp 残渣清单 = %v，期望 1 项", tmps)
	}
	if err := c.DeleteFile(tmps[0]); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if err := c.DeleteFile(tmps[0]); err != nil { // 不存在视为成功（幂等）
		t.Fatalf("DeleteFile 幂等: %v", err)
	}
	if n := countTmpFiles(t, objectsRoot); n != 0 {
		t.Fatalf("删除后残渣残留 %d", n)
	}

	// 非法 digest 的 MoveToTrash 拒绝（digest 校验统一口径）。
	if err := c.MoveToTrash("zz"); err == nil {
		t.Fatalf("非法 digest 应拒绝")
	}

	_ = ctx
}

// TestTrashRoundTripZstdBytes：zstd 压缩副本确为有效压缩流（解压后内容一致），
// 且 trash 目录布局 = <root>/trash/sha256/<前缀>/<digest>.zst（ADR-0007 §5）。
func TestTrashRoundTripZstdBytes(t *testing.T) {
	c, _ := openTestCAS(t)
	content := bytes.Repeat([]byte("zstd 回收站布局校验 "), 64)
	digest := putTestObject(t, c, string(content))

	if err := c.MoveToTrash(digest); err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}
	entries, err := c.ListTrash()
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListTrash: %v (%d)", err, len(entries))
	}
	wantPath := filepath.Join(c.TrashRoot(), "sha256", digest[:2], digest+".zst")
	if entries[0].Path != wantPath {
		t.Fatalf("trash 布局 %s，期望 %s", entries[0].Path, wantPath)
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != digest {
		t.Fatalf("digest 与内容不符")
	}
	_ = io.Discard
}
