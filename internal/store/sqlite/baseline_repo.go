package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// BaselineRepository 是 ports.BaselineRepository 的 SQLite 实现
// （sync_baselines + baseline_resources 两表）。
// Baseline 保存版本化 canonical representation JSON，不引用扫描表中的可回收行（§8.3）。
type BaselineRepository struct {
	db *sql.DB
}

var _ ports.BaselineRepository = (*BaselineRepository)(nil)

// NewBaselineRepository 创建共享 *sql.DB 的基线仓库。
func NewBaselineRepository(db *sql.DB) *BaselineRepository {
	return &BaselineRepository{db: db}
}

// marshalRepresentation 把可空表示序列化为可空 SQL 值（nil 指针 → NULL）。
func marshalRepresentation(rep *model.Representation) (any, error) {
	if rep == nil {
		return nil, nil
	}
	data, err := json.Marshal(rep)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// Insert 在同一事务写 sync_baselines 头表与全部 baseline_resources 行。
func (r *BaselineRepository) Insert(ctx context.Context, b model.SyncBaseline) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: 写入基线 %s 开启事务: %w", b.BaselineID, err)
	}
	defer tx.Rollback()

	// 完整性守卫（P0-3）：parent 同 Relation、digest 重算一致，与写入同事务。
	if err := verifyBaselineIntegrity(ctx, tx, b); err != nil {
		return fmt.Errorf("sqlite: 写入基线 %s 完整性校验失败: %w", b.BaselineID, err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_baselines(id, relation_id, parent_id, created_at, baseline_digest, normalization_version)
VALUES(?,?,?,?,?,?)`,
		b.BaselineID, b.RelationID, nullString(b.ParentBaselineID), b.CreatedAt,
		b.BaselineDigest, b.NormalizationVersion); err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("sqlite: 写入基线 %s: %w", b.BaselineID, ErrRelationNotFound)
		}
		return fmt.Errorf("sqlite: 写入基线 %s: %w", b.BaselineID, err)
	}

	insertRes, err := tx.PrepareContext(ctx, `
INSERT INTO baseline_resources(baseline_id, resource_id, state, logical_digest,
	project_representation_json, runtime_representation_json, recoverability)
VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("sqlite: 写入基线 %s 资源: %w", b.BaselineID, err)
	}
	defer insertRes.Close()

	for id, res := range b.Resources {
		projectJSON, err := marshalRepresentation(res.ProjectRepresentation)
		if err != nil {
			return fmt.Errorf("sqlite: 序列化基线 %s 资源 %s: %w", b.BaselineID, id, err)
		}
		runtimeJSON, err := marshalRepresentation(res.RuntimeRepresentation)
		if err != nil {
			return fmt.Errorf("sqlite: 序列化基线 %s 资源 %s: %w", b.BaselineID, id, err)
		}
		if _, err := insertRes.ExecContext(ctx,
			b.BaselineID, string(id), res.State, res.LogicalDigest,
			projectJSON, runtimeJSON, string(res.Recoverability)); err != nil {
			return fmt.Errorf("sqlite: 写入基线 %s 资源 %s: %w", b.BaselineID, id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: 写入基线 %s 提交: %w", b.BaselineID, err)
	}
	return nil
}

// Get 按 id 读取完整基线（含 resources map）；不存在返回 ErrNotFound。
func (r *BaselineRepository) Get(ctx context.Context, id string) (model.SyncBaseline, error) {
	var (
		b        model.SyncBaseline
		parentID sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
SELECT id, relation_id, parent_id, created_at, baseline_digest, normalization_version
FROM sync_baselines WHERE id=?`, id).
		Scan(&b.BaselineID, &b.RelationID, &parentID, &b.CreatedAt,
			&b.BaselineDigest, &b.NormalizationVersion)
	if err == sql.ErrNoRows {
		return model.SyncBaseline{}, fmt.Errorf("sqlite: 读取基线 %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.SyncBaseline{}, fmt.Errorf("sqlite: 读取基线 %s: %w", id, err)
	}
	b.SchemaVersion = model.CurrentSchemaVersion
	b.ParentBaselineID = parentID.String

	rows, err := r.db.QueryContext(ctx, `
SELECT resource_id, state, logical_digest,
	project_representation_json, runtime_representation_json, recoverability
FROM baseline_resources WHERE baseline_id=? ORDER BY resource_id`, id)
	if err != nil {
		return model.SyncBaseline{}, fmt.Errorf("sqlite: 读取基线 %s 资源: %w", id, err)
	}
	defer rows.Close()

	b.Resources = make(map[model.ResourceID]model.BaselineResource)
	for rows.Next() {
		var (
			rid, state, digest, recoverability string
			projectJSON, runtimeJSON           sql.NullString
		)
		if err := rows.Scan(&rid, &state, &digest, &projectJSON, &runtimeJSON, &recoverability); err != nil {
			return model.SyncBaseline{}, fmt.Errorf("sqlite: 读取基线 %s 资源: %w", id, err)
		}
		res := model.BaselineResource{
			State:          state,
			LogicalDigest:  digest,
			Recoverability: model.Recoverability(recoverability),
		}
		if projectJSON.Valid {
			var rep model.Representation
			if err := json.Unmarshal([]byte(projectJSON.String), &rep); err != nil {
				return model.SyncBaseline{}, fmt.Errorf("sqlite: 解析基线 %s 资源 %s: %w", id, rid, err)
			}
			res.ProjectRepresentation = &rep
		}
		if runtimeJSON.Valid {
			var rep model.Representation
			if err := json.Unmarshal([]byte(runtimeJSON.String), &rep); err != nil {
				return model.SyncBaseline{}, fmt.Errorf("sqlite: 解析基线 %s 资源 %s: %w", id, rid, err)
			}
			res.RuntimeRepresentation = &rep
		}
		b.Resources[model.ResourceID(rid)] = res
	}
	if err := rows.Err(); err != nil {
		return model.SyncBaseline{}, fmt.Errorf("sqlite: 读取基线 %s 资源: %w", id, err)
	}
	return b, nil
}
