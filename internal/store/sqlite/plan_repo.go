package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// PlanRepository 是 ports.PlanRepository 的 SQLite 实现
// （sync_plans 头表存全量 plan_json，conflicts 表另展开行供 SQL 查询）。
// 读取以 plan_json 反序列化为准，conflicts 行只是冗余索引。
type PlanRepository struct {
	db DBTX
}

var _ ports.PlanRepository = (*PlanRepository)(nil)

// NewPlanRepository 创建共享 *sql.DB 的计划仓库。
func NewPlanRepository(db *sql.DB) *PlanRepository {
	return &PlanRepository{db: db}
}

// Insert 在同一事务写 sync_plans 头表（全量 plan_json）并展开 conflicts 行。
// 独立使用时自开事务；处于 RunInTx 事务域内时加入外层事务。
func (r *PlanRepository) Insert(ctx context.Context, p model.SyncPlan) error {
	// 请求确切度空值按保守默认归一（列 CHECK 要求非空枚举；plan_json 与列保持一致）
	if p.RequestedExactness == "" {
		p.RequestedExactness = model.ExactnessAllowPartial
	}
	planJSON, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化计划 %s: %w", p.PlanID, err)
	}

	return beginOrJoin(ctx, r.db, "写入计划 "+p.PlanID, func(tx DBTX) error {
		// 完整性守卫（P0-3）：引用对象同 Relation、side 正确、digest 链一致，与写入同事务。
		if err := verifyPlanIntegrity(ctx, tx, p); err != nil {
			return fmt.Errorf("sqlite: 写入计划 %s 完整性校验失败: %w", p.PlanID, err)
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_plans(id, relation_id, kind, resolved_from_plan_id, base_baseline_id,
	input_project_snapshot_id, input_runtime_snapshot_id, relation_revision,
	plan_digest, requested_exactness, status, expires_at, normalization_version, plan_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.PlanID, p.RelationID, p.Kind, nullString(p.ResolvedFromPlanID), nullString(p.BaseBaselineID),
			p.InputProjectSnapshotID, p.InputRuntimeSnapshotID, p.RelationRevision,
			p.PlanDigest, p.RequestedExactness, p.Status, p.ExpiresAt, p.SchemaVersion, string(planJSON)); err != nil {
			if isForeignKeyViolation(err) {
				return fmt.Errorf("sqlite: 写入计划 %s: %w", p.PlanID, ErrRelationNotFound)
			}
			return fmt.Errorf("sqlite: 写入计划 %s: %w", p.PlanID, err)
		}

		insertConflict, err := tx.PrepareContext(ctx, `
INSERT INTO conflicts(plan_id, resource_id, conflict_kind, detail) VALUES(?,?,?,?)`)
		if err != nil {
			return fmt.Errorf("sqlite: 写入计划 %s 冲突: %w", p.PlanID, err)
		}
		defer insertConflict.Close()
		for _, c := range p.Conflicts {
			if _, err := insertConflict.ExecContext(ctx,
				p.PlanID, string(c.ResourceID), string(c.Kind), nullString(c.Detail)); err != nil {
				return fmt.Errorf("sqlite: 写入计划 %s 冲突 %s: %w", p.PlanID, c.ResourceID, err)
			}
		}
		return nil
	})
}

// Get 按 id 读取计划；plan_json 反序列化为权威数据；不存在返回 ErrNotFound。
func (r *PlanRepository) Get(ctx context.Context, id string) (model.SyncPlan, error) {
	var planJSON string
	err := r.db.QueryRowContext(ctx,
		"SELECT plan_json FROM sync_plans WHERE id=?", id).Scan(&planJSON)
	if err == sql.ErrNoRows {
		return model.SyncPlan{}, fmt.Errorf("sqlite: 读取计划 %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.SyncPlan{}, fmt.Errorf("sqlite: 读取计划 %s: %w", id, err)
	}
	var p model.SyncPlan
	if err := json.Unmarshal([]byte(planJSON), &p); err != nil {
		return model.SyncPlan{}, fmt.Errorf("sqlite: 解析计划 %s: %w", id, err)
	}
	// v3 迁移前的旧行 plan_json 无 requested_exactness：读取时以列的保守默认归一
	// （allow_partial），与「既有行以保守默认回填」的迁移语义一致，不回写库。
	if p.RequestedExactness == "" {
		p.RequestedExactness = model.ExactnessAllowPartial
	}
	return p, nil
}
