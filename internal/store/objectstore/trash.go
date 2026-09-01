package objectstore

// trash.go 实现回收站（ADR-0007 §5，票 #64）：CAS 对象删除前的缓冲区。
//
// 两阶段删除的落盘侧：对象被标记 quarantined 后，zstd 压缩移入
// <root>/trash/sha256/<前缀>/<digest>.zst（文件 mtime 即 trash_days 时钟起点，
// digest 可从文件名复原），原对象文件删除。压缩完成前原文件仍在盘，任一步
// 崩溃下一轮重算重扫自然续上（GC 全程可重入，最坏是垃圾晚清一轮）。
//
// 复活（ADR-0007 §5「GC 误收的最后一道保险」）：7 天内可从 trash 解压回
// objects 并置回 ready（CLI 形态见 pgheadless -revive）；Put 幂等复活由
// CAS.Put 的 UPSERT+重物化天然承担，本文件不参与。
//
// zstd（klauspost/compress，纯 Go）只用于回收站；CAS 本体不压缩（ADR-0007 §9）。

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"

	"packgradle/internal/application/ports"
)

// trashDirName 是回收站目录名（数据根下，与 objects 平级）。
const trashDirName = "trash"

// 编译期断言：CAS 满足 GC 回收站端口（bootstrap 装配 ports.GCTrash）。
var _ ports.GCTrash = (*CAS)(nil)

// trashExt 是回收站压缩文件后缀。
const trashExt = ".zst"

// TrashRoot 返回回收站根目录 <root>/trash（root 取 objectsRoot 的上级——
// 布局 store.Layout.Root/ObjectsDir 的既有关系）。
func (c *CAS) TrashRoot() string {
	return filepath.Join(filepath.Dir(c.objectsRoot), trashDirName)
}

// Root 返回对象库根目录（objectsRoot；验收对账与测试的盘面定位出口）。
func (c *CAS) Root() string { return c.objectsRoot }

// trashPath 返回 digest 对应的回收站文件路径 <trash>/sha256/<前缀>/<digest>.zst。
func (c *CAS) trashPath(digest string) string {
	return filepath.Join(c.TrashRoot(), algorithm, digest[:2], digest+trashExt)
}

// MoveToTrash 把对象文件 zstd 压缩移入回收站（原文件删除）。幂等可重入：
//   - 对象文件与 trash 副本同时存在（上次压缩完成但原文件未删）→ 只删原文件；
//   - 对象文件缺失视为已完成（调用方据 DB 账目推进）。
//
// 写入经 .tmp 前缀 + rename 原子落位；返回 os.ErrNotExist 语义当对象文件缺失。
func (c *CAS) MoveToTrash(digest string) error {
	digest = strings.ToLower(digest)
	if err := validateDigest(digest); err != nil {
		return err
	}
	src := c.objectPath(digest)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("objectstore: 对象 %s 不在盘（可能已入回收站）: %w", digest, os.ErrNotExist)
		}
		return fmt.Errorf("objectstore: stat 对象 %s: %w", digest, err)
	}
	dst := c.trashPath(digest)
	if _, err := os.Stat(dst); err == nil {
		// 压缩已完成：上轮崩溃残留，直接清原文件即续上。
		return os.Remove(src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("objectstore: 创建回收站目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), tmpPrefix+"*")
	if err != nil {
		return fmt.Errorf("objectstore: 创建回收站临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	if err := compressFile(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("objectstore: 压缩对象 %s 入回收站失败: %w", digest, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("objectstore: 关闭回收站临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("objectstore: 回收站落位失败: %w", err)
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("objectstore: 删除已回收对象 %s 失败: %w", digest, err)
	}
	return nil
}

// compressFile 把 src 文件流式压缩写入 w。
func compressFile(w io.Writer, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return err
	}
	defer enc.Close()
	_, err = io.Copy(enc, f)
	if err != nil {
		return err
	}
	return enc.Close()
}

// RestoreFromTrash 把回收站副本解压回对象位置（字节级复原；内容寻址保证
// digest 即内容身份，解压产物无需二次校验——写入经 .tmp+rename 原子落位）。
// 对象文件已在盘（如 Put 幂等复活先行重物化）时幂等返回 nil。
func (c *CAS) RestoreFromTrash(digest string) error {
	digest = strings.ToLower(digest)
	if err := validateDigest(digest); err != nil {
		return err
	}
	src := c.trashPath(digest)
	dst := c.objectPath(digest)
	if _, err := os.Stat(dst); err == nil {
		return nil // 已复活（Put 重物化等），幂等
	}
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("objectstore: 打开回收站副本 %s 失败: %w", digest, err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("objectstore: 解压对象 %s 失败: %w", digest, err)
	}
	defer dec.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("objectstore: 创建对象分片目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), tmpPrefix+"*")
	if err != nil {
		return fmt.Errorf("objectstore: 创建复活临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, dec.IOReadCloser()); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("objectstore: 解压对象 %s 写盘失败: %w", digest, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("objectstore: 关闭复活临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("objectstore: 复活对象落位失败: %w", err)
	}
	return nil
}

// ListTrash 遍历回收站 <trash>/sha256/**/*.zst，返回全部条目（目录不存在
// 视为空）。非 .zst 文件与非法 digest 文件名跳过（外部残留不误报）。
// 满足 ports.GCTrash。
func (c *CAS) ListTrash() ([]ports.GCTrashEntry, error) {
	root := filepath.Join(c.TrashRoot(), algorithm)
	var out []ports.GCTrashEntry
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), trashExt) {
			return nil
		}
		digest := strings.TrimSuffix(d.Name(), trashExt)
		if err := validateDigest(digest); err != nil {
			return nil // 非法文件名：跳过，不做账目
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, ports.GCTrashEntry{
			Digest:     digest,
			Path:       path,
			ModifiedAt: info.ModTime(),
			SizeBytes:  info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("objectstore: 遍历回收站失败: %w", err)
	}
	return out, nil
}

// DeleteTrashEntry 物理删除单个回收站文件（超期清除，ADR-0007 §5 步骤 3；
// DB 隔离行的删除由调用方在文件删除成功后执行——先文件后行，崩溃残留的
// 孤行由孤儿清扫对账）。满足 ports.GCTrash。
func (c *CAS) DeleteTrashEntry(entry ports.GCTrashEntry) error {
	if err := os.Remove(entry.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("objectstore: 删除回收站条目 %s 失败: %w", entry.Digest, err)
	}
	return nil
}
