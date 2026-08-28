package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// MappingRepository 是 ports.MappingRepository 的 SQLite 实现（mappings 表）。
// 修订语义（ADR-0002）：CreatePolicy 写入创建时初始 policy 且不递增 revision；
// SavePolicy 在同一事务内 UPSERT mappings 并递增 relations.revision，
// 使旧 Plan 立即 stale（§8.3：映射修订与关系修订必须同事务联动）。
type MappingRepository struct {
	db *sql.DB
}

var _ ports.MappingRepository = (*MappingRepository)(nil)

// NewMappingRepository 创建共享 *sql.DB 的映射仓库。
func NewMappingRepository(db *sql.DB) *MappingRepository {
	return &MappingRepository{db: db}
}

// GetPolicy 读取关系的当前映射策略；尚未保存过返回 ErrNotFound。
func (r *MappingRepository) GetPolicy(ctx context.Context, relationID string) (model.MappingPolicy, error) {
	var policyJSON string
	err := r.db.QueryRowContext(ctx,
		"SELECT policy_json FROM mappings WHERE relation_id=?", relationID).Scan(&policyJSON)
	if err == sql.ErrNoRows {
		return model.MappingPolicy{}, fmt.Errorf("sqlite: 读取关系 %s 映射策略: %w", relationID, ErrNotFound)
	}
	if err != nil {
		return model.MappingPolicy{}, fmt.Errorf("sqlite: 读取关系 %s 映射策略: %w", relationID, err)
	}
	var p model.MappingPolicy
	if err := json.Unmarshal([]byte(policyJSON), &p); err != nil {
		return model.MappingPolicy{}, fmt.Errorf("sqlite: 解析关系 %s 映射策略: %w", relationID, err)
	}
	return p, nil
}

// CreatePolicy 写入创建时的初始 policy（INSERT，不递增 relations.revision，
// ADR-0002 决议 1/6：创建时初始写入不算修改，Relation 出生即 revision=1 且已带
// policy）。关系不存在返回 ErrNotFound；已有 policy 返回 ErrDuplicate。
func (r *MappingRepository) CreatePolicy(ctx context.Context, relationID string, p model.MappingPolicy) error {
	policyJSON, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化关系 %s 初始映射策略: %w", relationID, err)
	}
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO mappings(relation_id, policy_id, revision, policy_json, updated_at)
VALUES(?,?,?,?,?)`,
		relationID, p.PolicyID, p.Revision, string(policyJSON),
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("sqlite: 写入关系 %s 初始映射策略: %w", relationID, ErrNotFound)
		}
		if isUniqueViolation(err) {
			return fmt.Errorf("sqlite: 写入关系 %s 初始映射策略: %w", relationID, ErrDuplicate)
		}
		return fmt.Errorf("sqlite: 写入关系 %s 初始映射策略: %w", relationID, err)
	}
	return nil
}

// SavePolicy 单事务保存策略修改（UPSERT）并递增 relations.revision，
// 使旧 Plan 立即 stale（§8.3）。初始写入请走 CreatePolicy（不递增）。
// 关系不存在时 UPDATE 影响 0 行，整个事务回滚并返回 ErrNotFound。
func (r *MappingRepository) SavePolicy(ctx context.Context, relationID string, p model.MappingPolicy) error {
	policyJSON, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化关系 %s 映射策略: %w", relationID, err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: 保存关系 %s 映射策略开启事务: %w", relationID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO mappings(relation_id, policy_id, revision, policy_json, updated_at)
VALUES(?,?,?,?,?)
ON CONFLICT(relation_id) DO UPDATE SET
	policy_id=excluded.policy_id, revision=excluded.revision,
	policy_json=excluded.policy_json, updated_at=excluded.updated_at`,
		relationID, p.PolicyID, p.Revision, string(policyJSON),
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		if isForeignKeyViolation(err) {
			// mappings.relation_id 引用 relations(id)：FK 失败即关系不存在。
			return fmt.Errorf("sqlite: 保存关系 %s 映射策略: %w", relationID, ErrNotFound)
		}
		return fmt.Errorf("sqlite: 保存关系 %s 映射策略: %w", relationID, err)
	}

	res, err := tx.ExecContext(ctx,
		"UPDATE relations SET revision=revision+1 WHERE id=?", relationID)
	if err != nil {
		return fmt.Errorf("sqlite: 保存关系 %s 映射策略联动修订: %w", relationID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("sqlite: 保存关系 %s 映射策略联动修订: %w", relationID, ErrNotFound)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: 保存关系 %s 映射策略提交: %w", relationID, err)
	}
	return nil
}
