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

// PlanConfirmationRepository 是 ports.PlanConfirmationRepository 的 SQLite 实现
// （plan_confirmations，schema v1 冻结表的 Phase 2 收口，契约 05 §7 D4；
// consumed_at 消费标记列为 schema v5 增补）。
type PlanConfirmationRepository struct {
	db DBTX
}

var _ ports.PlanConfirmationRepository = (*PlanConfirmationRepository)(nil)

// NewPlanConfirmationRepository 创建共享 *sql.DB 的计划确认仓库。
func NewPlanConfirmationRepository(db *sql.DB) *PlanConfirmationRepository {
	return &PlanConfirmationRepository{db: db}
}

// planConfirmationColumns 是 plan_confirmations 表读取列清单。
const planConfirmationColumns = `plan_id, plan_digest, confirmation_token, relation_revision,
	acknowledgements_json, confirmed_at, expires_at, consumed_at`

// scanPlanConfirmation 把一行 plan_confirmations 扫描为 model.PlanConfirmation。
func scanPlanConfirmation(scan func(...any) error) (model.PlanConfirmation, error) {
	var (
		c                model.PlanConfirmation
		acknowledgements string
		consumedAt       sql.NullString
	)
	if err := scan(&c.PlanID, &c.PlanDigest, &c.ConfirmationToken, &c.RelationRevision,
		&acknowledgements, &c.ConfirmedAt, &c.ExpiresAt, &consumedAt); err != nil {
		return model.PlanConfirmation{}, err
	}
	c.ConsumedAt = consumedAt.String
	if acknowledgements != "" && acknowledgements != "[]" && acknowledgements != "null" {
		c.Acknowledgements = json.RawMessage(acknowledgements)
	}
	return c, nil
}

// Insert 写入一条计划确认（未消费，consumed_at 为 NULL）；同 (plan, token) 重复
// 返回 ErrDuplicate，计划不存在返回 ErrPlanNotFound。
func (r *PlanConfirmationRepository) Insert(ctx context.Context, c model.PlanConfirmation) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO plan_confirmations(plan_id, plan_digest, confirmation_token, relation_revision,
	acknowledgements_json, confirmed_at, expires_at, consumed_at)
VALUES(?,?,?,?,?,?,?,NULL)`,
		c.PlanID, c.PlanDigest, c.ConfirmationToken, c.RelationRevision,
		rawJSONLiteral(c.Acknowledgements, "[]"), c.ConfirmedAt, c.ExpiresAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("sqlite: 写入计划确认 %s/%s: %w", c.PlanID, c.ConfirmationToken, ErrDuplicate)
		}
		if isForeignKeyViolation(err) {
			return fmt.Errorf("sqlite: 写入计划确认 %s: 计划不存在: %w", c.PlanID, ErrPlanNotFound)
		}
		return fmt.Errorf("sqlite: 写入计划确认 %s/%s: %w", c.PlanID, c.ConfirmationToken, err)
	}
	return nil
}

// ListByPlan 返回该计划的全部确认记录（confirmed_at 升序，token 决胜）。
func (r *PlanConfirmationRepository) ListByPlan(ctx context.Context, planID string) ([]model.PlanConfirmation, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+planConfirmationColumns+" FROM plan_confirmations WHERE plan_id=?"+
			" ORDER BY confirmed_at ASC, confirmation_token ASC", planID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出计划 %s 确认: %w", planID, err)
	}
	defer rows.Close()
	var items []model.PlanConfirmation
	for rows.Next() {
		c, err := scanPlanConfirmation(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 列出计划 %s 确认: %w", planID, err)
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 列出计划 %s 确认: %w", planID, err)
	}
	return items, nil
}

// MarkConsumed 消费确认令牌：仅未消费（consumed_at IS NULL）且未过期时成功。
// 影响行数为 0 时区分：不存在 → ErrNotFound；已消费 → ErrConfirmationConsumed；
// 已过期 → ErrConfirmationExpired（已消费优先——消费必发生在过期前，
// 与预检消费守卫同款语义）。时间统一为 UTC RFC3339 字符串比较。
func (r *PlanConfirmationRepository) MarkConsumed(ctx context.Context, planID, token string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE plan_confirmations SET consumed_at=?
WHERE plan_id=? AND confirmation_token=? AND consumed_at IS NULL AND expires_at>?`,
		now, planID, token, now)
	if err != nil {
		return fmt.Errorf("sqlite: 消费计划确认 %s/%s: %w", planID, token, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	var consumed bool
	if err := r.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM plan_confirmations WHERE plan_id=? AND confirmation_token=? AND consumed_at IS NOT NULL)",
		planID, token).Scan(&consumed); err != nil {
		return fmt.Errorf("sqlite: 消费计划确认 %s/%s: %w", planID, token, err)
	}
	if consumed {
		return fmt.Errorf("sqlite: 消费计划确认 %s/%s: %w", planID, token, ErrConfirmationConsumed)
	}
	var exists bool
	if err := r.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM plan_confirmations WHERE plan_id=? AND confirmation_token=?)",
		planID, token).Scan(&exists); err != nil {
		return fmt.Errorf("sqlite: 消费计划确认 %s/%s: %w", planID, token, err)
	}
	if !exists {
		return fmt.Errorf("sqlite: 消费计划确认 %s/%s: %w", planID, token, ErrNotFound)
	}
	return fmt.Errorf("sqlite: 消费计划确认 %s/%s: %w", planID, token, ErrConfirmationExpired)
}
