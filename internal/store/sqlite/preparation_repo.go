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

// PreparationRepository 是 ports.PreparationRepository 的 SQLite 实现
// （preparations 表，Prepare/Apply 两段式创建 Relation 的中间状态）。
type PreparationRepository struct {
	db *sql.DB
}

var _ ports.PreparationRepository = (*PreparationRepository)(nil)

// NewPreparationRepository 创建共享 *sql.DB 的预检仓库。
func NewPreparationRepository(db *sql.DB) *PreparationRepository {
	return &PreparationRepository{db: db}
}

// Insert 写入一条预检记录（未消费，consumed_at 为 NULL）。
// 可空的 Project/Runtime 草稿经 marshalNullableJSON（task_repo.go）处理：
// 类型化 nil 指针存 SQL NULL，保证往返一致。
func (r *PreparationRepository) Insert(ctx context.Context, p model.RelationPreparation) error {
	inputJSON, err := json.Marshal(p.Input)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化预检 %s 输入: %w", p.PreparationID, err)
	}
	policyJSON, err := json.Marshal(p.Policy)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化预检 %s 策略: %w", p.PreparationID, err)
	}
	checksJSON, err := json.Marshal(p.Checks)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化预检 %s 检查项: %w", p.PreparationID, err)
	}
	projectJSON, err := marshalNullableJSON(p.Project)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化预检 %s 项目草稿: %w", p.PreparationID, err)
	}
	runtimeJSON, err := marshalNullableJSON(p.Runtime)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化预检 %s 运行时草稿: %w", p.PreparationID, err)
	}

	_, err = r.db.ExecContext(ctx, `
INSERT INTO preparations(preparation_id, created_at, expires_at, consumed_at,
	input_json, project_json, runtime_json, policy_json, checks_json)
VALUES(?,?,?,?,?,?,?,?,?)`,
		p.PreparationID, p.CreatedAt, p.ExpiresAt, nil,
		string(inputJSON), projectJSON, runtimeJSON, string(policyJSON), string(checksJSON))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("sqlite: 写入预检 %s: %w", p.PreparationID, ErrDuplicate)
		}
		return fmt.Errorf("sqlite: 写入预检 %s: %w", p.PreparationID, err)
	}
	return nil
}

// Get 按 id 读取预检；不存在返回 ErrNotFound。
func (r *PreparationRepository) Get(ctx context.Context, id string) (model.RelationPreparation, error) {
	var (
		p                                 model.RelationPreparation
		inputJSON, policyJSON, checksJSON string
		projectJSON, runtimeJSON          sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
SELECT preparation_id, created_at, expires_at,
	input_json, project_json, runtime_json, policy_json, checks_json
FROM preparations WHERE preparation_id=?`, id).
		Scan(&p.PreparationID, &p.CreatedAt, &p.ExpiresAt,
			&inputJSON, &projectJSON, &runtimeJSON, &policyJSON, &checksJSON)
	if err == sql.ErrNoRows {
		return model.RelationPreparation{}, fmt.Errorf("sqlite: 读取预检 %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.RelationPreparation{}, fmt.Errorf("sqlite: 读取预检 %s: %w", id, err)
	}
	p.SchemaVersion = model.CurrentSchemaVersion
	if err := json.Unmarshal([]byte(inputJSON), &p.Input); err != nil {
		return model.RelationPreparation{}, fmt.Errorf("sqlite: 解析预检 %s 输入: %w", id, err)
	}
	if err := json.Unmarshal([]byte(policyJSON), &p.Policy); err != nil {
		return model.RelationPreparation{}, fmt.Errorf("sqlite: 解析预检 %s 策略: %w", id, err)
	}
	if err := json.Unmarshal([]byte(checksJSON), &p.Checks); err != nil {
		return model.RelationPreparation{}, fmt.Errorf("sqlite: 解析预检 %s 检查项: %w", id, err)
	}
	if projectJSON.Valid && projectJSON.String != "null" {
		var proj model.Project
		if err := json.Unmarshal([]byte(projectJSON.String), &proj); err != nil {
			return model.RelationPreparation{}, fmt.Errorf("sqlite: 解析预检 %s 项目草稿: %w", id, err)
		}
		p.Project = &proj
	}
	if runtimeJSON.Valid && runtimeJSON.String != "null" {
		var rt model.Runtime
		if err := json.Unmarshal([]byte(runtimeJSON.String), &rt); err != nil {
			return model.RelationPreparation{}, fmt.Errorf("sqlite: 解析预检 %s 运行时草稿: %w", id, err)
		}
		p.Runtime = &rt
	}
	return p, nil
}

// MarkConsumed 消费预检：仅当未消费（consumed_at IS NULL）且未过期（expires_at > now）
// 时成功。影响行数为 0 时区分：记录不存在 → ErrNotFound；已消费或已过期 → ErrPreparationExpired。
// 时间统一为 UTC RFC3339 字符串比较（与写入格式一致）。
func (r *PreparationRepository) MarkConsumed(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE preparations SET consumed_at=?
WHERE preparation_id=? AND consumed_at IS NULL AND expires_at>?`, now, id, now)
	if err != nil {
		return fmt.Errorf("sqlite: 消费预检 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}

	var exists bool
	if err := r.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM preparations WHERE preparation_id=?)", id).Scan(&exists); err != nil {
		return fmt.Errorf("sqlite: 消费预检 %s: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("sqlite: 消费预检 %s: %w", id, ErrNotFound)
	}
	return fmt.Errorf("sqlite: 消费预检 %s: %w", id, ErrPreparationExpired)
}
