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
	db DBTX
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
// 时成功。影响行数为 0 时区分三种情况：记录不存在 → ErrNotFound；已消费 →
// ErrPreparationConsumed（引导刷新，关系可能已建成）；已过期 → ErrPreparationExpired
// （引导重新预检；ADR-0003 决议 4 拆码，已消费优先于已过期——消费必发生在过期前）。
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
	return consumeGuard(ctx, r.db, "preparations", "preparation_id", id)
}

// InsertRebind 写入一条重绑预检记录（未消费，consumed_at 为 NULL）。
// 绑定草稿按 side 二选一序列化进 new_endpoint_json（类型化 nil 指针存 SQL NULL）。
func (r *PreparationRepository) InsertRebind(ctx context.Context, p model.RebindPreparation) error {
	checksJSON, err := json.Marshal(p.Checks)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化重绑预检 %s 检查项: %w", p.PreparationID, err)
	}
	var draft any
	switch {
	case p.NewProject != nil:
		draft = p.NewProject
	case p.NewRuntime != nil:
		draft = p.NewRuntime
	}
	draftJSON, err := marshalNullableJSON(draft)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化重绑预检 %s 绑定草稿: %w", p.PreparationID, err)
	}
	fingerprintChanged := 0
	if p.FingerprintChanged {
		fingerprintChanged = 1
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO rebind_preparations(preparation_id, relation_id, side, created_at, expires_at,
	consumed_at, input_root_path, new_endpoint_json, fingerprint_changed,
	baseline_inheritance, invalidated_plan_count, checks_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.PreparationID, p.RelationID, p.Side, p.CreatedAt, p.ExpiresAt, nil,
		p.InputRootPath, draftJSON, fingerprintChanged,
		p.BaselineInheritance, p.InvalidatedPlanCount, string(checksJSON))
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("sqlite: 写入重绑预检 %s: %w", p.PreparationID, ErrRelationNotFound)
		}
		return fmt.Errorf("sqlite: 写入重绑预检 %s: %w", p.PreparationID, err)
	}
	return nil
}

// GetRebind 按 id 读取重绑预检；不存在返回 ErrNotFound。绑定草稿按 side 反序列化
// 为 Project 或 Runtime；无草稿（新端点不可达的失败预检）两字段保持 nil。
func (r *PreparationRepository) GetRebind(ctx context.Context, id string) (model.RebindPreparation, error) {
	var (
		p                                              model.RebindPreparation
		side, inputRoot, baselineInheritance           string
		fingerprintChanged, invalidatedPlanCount       int
		draftJSON                                      sql.NullString
		checksJSON                                     string
	)
	err := r.db.QueryRowContext(ctx, `
SELECT preparation_id, relation_id, side, created_at, expires_at,
	input_root_path, new_endpoint_json, fingerprint_changed,
	baseline_inheritance, invalidated_plan_count, checks_json
FROM rebind_preparations WHERE preparation_id=?`, id).
		Scan(&p.PreparationID, &p.RelationID, &side, &p.CreatedAt, &p.ExpiresAt,
			&inputRoot, &draftJSON, &fingerprintChanged,
			&baselineInheritance, &invalidatedPlanCount, &checksJSON)
	if err == sql.ErrNoRows {
		return model.RebindPreparation{}, fmt.Errorf("sqlite: 读取重绑预检 %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.RebindPreparation{}, fmt.Errorf("sqlite: 读取重绑预检 %s: %w", id, err)
	}
	p.SchemaVersion = model.CurrentSchemaVersion
	p.Side = model.Side(side)
	p.InputRootPath = inputRoot
	p.FingerprintChanged = fingerprintChanged == 1
	p.BaselineInheritance = baselineInheritance
	p.InvalidatedPlanCount = invalidatedPlanCount
	if err := json.Unmarshal([]byte(checksJSON), &p.Checks); err != nil {
		return model.RebindPreparation{}, fmt.Errorf("sqlite: 解析重绑预检 %s 检查项: %w", id, err)
	}
	if draftJSON.Valid && draftJSON.String != "null" {
		if p.Side == model.SideProject {
			var proj model.Project
			if err := json.Unmarshal([]byte(draftJSON.String), &proj); err != nil {
				return model.RebindPreparation{}, fmt.Errorf("sqlite: 解析重绑预检 %s 绑定草稿: %w", id, err)
			}
			p.NewProject = &proj
		} else {
			var rt model.Runtime
			if err := json.Unmarshal([]byte(draftJSON.String), &rt); err != nil {
				return model.RebindPreparation{}, fmt.Errorf("sqlite: 解析重绑预检 %s 绑定草稿: %w", id, err)
			}
			p.NewRuntime = &rt
		}
	}
	return p, nil
}

// MarkRebindConsumed 消费重绑预检，守卫语义与 MarkConsumed 一致（ADR-0003 决议 4 拆码）。
func (r *PreparationRepository) MarkRebindConsumed(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE rebind_preparations SET consumed_at=?
WHERE preparation_id=? AND consumed_at IS NULL AND expires_at>?`, now, id, now)
	if err != nil {
		return fmt.Errorf("sqlite: 消费重绑预检 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	return consumeGuard(ctx, r.db, "rebind_preparations", "preparation_id", id)
}

// consumeGuard 是两类预检共用的消费守卫收尾：更新影响 0 行时区分不存在/已消费/已过期
// （已消费优先——消费必发生在过期前；ADR-0003 决议 4）。
func consumeGuard(ctx context.Context, q DBTX, table, idCol, id string) error {
	var consumed bool
	if err := q.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM "+table+" WHERE "+idCol+"=? AND consumed_at IS NOT NULL)", id).
		Scan(&consumed); err != nil {
		return fmt.Errorf("sqlite: 消费预检 %s: %w", id, err)
	}
	if consumed {
		return fmt.Errorf("sqlite: 消费预检 %s: %w", id, ErrPreparationConsumed)
	}
	var exists bool
	if err := q.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM "+table+" WHERE "+idCol+"=?)", id).Scan(&exists); err != nil {
		return fmt.Errorf("sqlite: 消费预检 %s: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("sqlite: 消费预检 %s: %w", id, ErrNotFound)
	}
	return fmt.Errorf("sqlite: 消费预检 %s: %w", id, ErrPreparationExpired)
}
