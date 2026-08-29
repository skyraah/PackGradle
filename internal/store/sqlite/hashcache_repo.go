package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"packgradle/internal/application/ports"
)

// HashCacheRepository 是 ports.HashCacheRepository 的 SQLite 实现（hash_cache 表）。
// 缓存只是性能优化、可随时丢弃（DeleteAll），不是事实来源（§5.1）。
type HashCacheRepository struct {
	db DBTX
}

var _ ports.HashCacheRepository = (*HashCacheRepository)(nil)

// NewHashCacheRepository 创建共享 *sql.DB 的 hash 缓存仓库。
func NewHashCacheRepository(db *sql.DB) *HashCacheRepository {
	return &HashCacheRepository{db: db}
}

// Lookup 精确匹配缓存键（五个字段全部一致才命中）；未命中返回 ("", false, nil)。
func (r *HashCacheRepository) Lookup(ctx context.Context, key ports.HashCacheKey) (string, bool, error) {
	var digest string
	err := r.db.QueryRowContext(ctx, `
SELECT digest FROM hash_cache
WHERE root_fingerprint=? AND relative_path=? AND size_bytes=? AND mtime_unix_nano=? AND file_key=?`,
		key.RootFingerprint, key.RelativePath, key.SizeBytes, key.MtimeUnixNano, key.FileKey).Scan(&digest)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("sqlite: 查询 hash 缓存: %w", err)
	}
	return digest, true, nil
}

// Save 批量 UPSERT 缓存条目（同键覆盖旧 digest 与 created_at），单事务写入。
// 独立使用时自开事务；处于 RunInTx 事务域内时加入外层事务。
func (r *HashCacheRepository) Save(ctx context.Context, entries []ports.HashCacheEntry) error {
	return beginOrJoin(ctx, r.db, "写入 hash 缓存", func(tx DBTX) error {
		stmt, err := tx.PrepareContext(ctx, `
INSERT INTO hash_cache(root_fingerprint, relative_path, size_bytes, mtime_unix_nano, file_key,
	algorithm, digest, created_at)
VALUES(?,?,?,?,?,'sha256',?,?)
ON CONFLICT(root_fingerprint, relative_path, size_bytes, mtime_unix_nano, file_key) DO UPDATE SET
	algorithm=excluded.algorithm, digest=excluded.digest, created_at=excluded.created_at`)
		if err != nil {
			return fmt.Errorf("sqlite: 写入 hash 缓存: %w", err)
		}
		defer stmt.Close()

		now := time.Now().UTC().Format(time.RFC3339)
		for _, e := range entries {
			if _, err := stmt.ExecContext(ctx,
				e.Key.RootFingerprint, e.Key.RelativePath, e.Key.SizeBytes,
				e.Key.MtimeUnixNano, e.Key.FileKey, e.Digest, now); err != nil {
				return fmt.Errorf("sqlite: 写入 hash 缓存 %s: %w", e.Key.RelativePath, err)
			}
		}
		return nil
	})
}

// DeleteAll 清空全部缓存（验收：删缓存后可从端点重建）。
func (r *HashCacheRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM hash_cache"); err != nil {
		return fmt.Errorf("sqlite: 清空 hash 缓存: %w", err)
	}
	return nil
}
