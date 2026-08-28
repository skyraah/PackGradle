// Package objectstore 实现全局内容寻址对象库（CAS，架构文档 §5.1/ADR-006）。
// MVP 固定 sha256：<objectsRoot>/sha256/<前2字符>/<hex>；
// SQLite 的 objects 表记录 (algorithm, digest) 的状态，文件先落盘再引用，
// 避免悬空引用（§14 数据完整性）。
package objectstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/core/model"
)

const (
	// algorithm 是 MVP 固定的内容指纹算法（ADR-006）。
	algorithm = "sha256"
	// tmpPrefix 是写入期间的临时文件前缀（位于 objectsRoot 根下）。
	tmpPrefix = ".tmp-"
	// stateReady 表示对象文件已完整落盘并完成登记。
	stateReady = "ready"
	// digestHexLen 是 sha256 十六进制长度。
	digestHexLen = 64
)

// CAS 是 SHA-256 内容寻址对象库；跨 Relation 全局去重。
type CAS struct {
	objectsRoot string
	db          *sql.DB
}

// Open 初始化 CAS，确保 <objectsRoot>/sha256 目录存在。
// db 必须是已迁移到 v1 的 packgradle.db（含 objects 表）。
func Open(objectsRoot string, db *sql.DB) (*CAS, error) {
	if db == nil {
		return nil, fmt.Errorf("objectstore: db 不能为 nil")
	}
	if err := os.MkdirAll(filepath.Join(objectsRoot, algorithm), 0o755); err != nil {
		return nil, fmt.Errorf("objectstore: 创建对象目录失败: %w", err)
	}
	return &CAS{objectsRoot: objectsRoot, db: db}, nil
}

// objectPath 返回 digest 对应的对象文件路径。
func (c *CAS) objectPath(digest string) string {
	return filepath.Join(c.objectsRoot, algorithm, digest[:2], digest)
}

// Put 流式写入对象：先写临时文件（同时计算 sha256），完成后 fsync 并 rename
// 到最终路径，再在事务中 UPSERT objects 行（state='ready'）。
// 同内容重复 Put 自动去重（同一文件、同一行）；reader 中途出错时清理临时文件，
// 不产生 ready 行。
func (c *CAS) Put(ctx context.Context, r io.Reader) (model.ContentRef, error) {
	tmp, err := os.CreateTemp(c.objectsRoot, tmpPrefix+"*")
	if err != nil {
		return model.ContentRef{}, fmt.Errorf("objectstore: 创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), r)
	if err != nil {
		cleanup()
		return model.ContentRef{}, fmt.Errorf("objectstore: 读取内容失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return model.ContentRef{}, fmt.Errorf("objectstore: 同步临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return model.ContentRef{}, fmt.Errorf("objectstore: 关闭临时文件失败: %w", err)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	finalPath := c.objectPath(digest)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		os.Remove(tmpPath)
		return model.ContentRef{}, fmt.Errorf("objectstore: 创建对象分片目录失败: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return model.ContentRef{}, fmt.Errorf("objectstore: 落位对象文件失败: %w", err)
	}

	// 文件已就位，登记 objects 行（UPSERT：同内容去重时刷新 state/size）。
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ContentRef{}, fmt.Errorf("objectstore: 登记对象开启事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO objects(algorithm, digest, size, state, created_at)
VALUES(?,?,?,?,?)
ON CONFLICT(algorithm, digest) DO UPDATE SET state=excluded.state, size=excluded.size`,
		algorithm, digest, size, stateReady, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return model.ContentRef{}, fmt.Errorf("objectstore: 登记对象失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.ContentRef{}, fmt.Errorf("objectstore: 登记对象提交失败: %w", err)
	}

	return model.ContentRef{Algorithm: algorithm, Digest: digest, Size: size}, nil
}

// validateDigest 校验 digest 形如 64 位小写/大写十六进制（统一小写比较）。
func validateDigest(digest string) error {
	if len(digest) != digestHexLen {
		return fmt.Errorf("objectstore: 非法 sha256 digest 长度 %d", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("objectstore: 非法 sha256 digest: %w", err)
	}
	return nil
}

// Has 判断对象可用：objects 行为 ready 且文件存在。
func (c *CAS) Has(ctx context.Context, digest string) (bool, error) {
	digest = strings.ToLower(digest)
	if err := validateDigest(digest); err != nil {
		return false, err
	}
	var state string
	err := c.db.QueryRowContext(ctx,
		"SELECT state FROM objects WHERE algorithm=? AND digest=?", algorithm, digest).Scan(&state)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("objectstore: 查询对象 %s 失败: %w", digest, err)
	}
	if state != stateReady {
		return false, nil
	}
	if _, err := os.Stat(c.objectPath(digest)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("objectstore: 检查对象文件 %s 失败: %w", digest, err)
	}
	return true, nil
}

// Open 打开对象内容流；digest 不存在或未 ready 时返回错误。
func (c *CAS) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	digest = strings.ToLower(digest)
	ok, err := c.Has(ctx, digest)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("objectstore: 对象 %s 不存在或未就绪", digest)
	}
	f, err := os.Open(c.objectPath(digest))
	if err != nil {
		return nil, fmt.Errorf("objectstore: 打开对象 %s 失败: %w", digest, err)
	}
	return f, nil
}
