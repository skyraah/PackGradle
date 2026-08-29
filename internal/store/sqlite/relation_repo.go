package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// RelationRepository 是 ports.RelationRepository 的 SQLite 实现（relations 表，聚合根）。
type RelationRepository struct {
	db DBTX
}

var _ ports.RelationRepository = (*RelationRepository)(nil)

// NewRelationRepository 创建共享 *sql.DB 的关系仓库。
func NewRelationRepository(db *sql.DB) *RelationRepository {
	return &RelationRepository{db: db}
}

// relationColumns 是 relations 表的读取列清单（与 scanRelation 对应）。
const relationColumns = `id, project_id, runtime_id, policy_set, revision, health, head_baseline_id, head_commit_id, created_at`

// scanRelation 把一行 relations 列扫描为 model.Relation。
func scanRelation(scan func(...any) error) (model.Relation, error) {
	var (
		rel          model.Relation
		headBaseline sql.NullString
		headCommit   sql.NullString
	)
	if err := scan(&rel.RelationID, &rel.ProjectID, &rel.RuntimeID, &rel.PolicySet,
		&rel.Revision, &rel.Health, &headBaseline, &headCommit, &rel.CreatedAt); err != nil {
		return model.Relation{}, err
	}
	rel.SchemaVersion = model.CurrentSchemaVersion
	rel.HeadBaselineID = headBaseline.String
	rel.HeadCommitID = headCommit.String
	return rel, nil
}

// Create 写入新关系；project/runtime 配对重复（UNIQUE(project_id, runtime_id)）返回 ErrDuplicate。
func (r *RelationRepository) Create(ctx context.Context, rel model.Relation) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO relations(id, project_id, runtime_id, policy_set, revision, health, head_baseline_id, head_commit_id, created_at)
VALUES(?,?,?,?,?,?,?,?,?)`,
		rel.RelationID, rel.ProjectID, rel.RuntimeID, rel.PolicySet, rel.Revision, rel.Health,
		nullString(rel.HeadBaselineID), nullString(rel.HeadCommitID), rel.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("sqlite: 创建 Relation %s: %w", rel.RelationID, ErrDuplicate)
		}
		return fmt.Errorf("sqlite: 创建 Relation %s: %w", rel.RelationID, err)
	}
	return nil
}

// Get 按 id 读取关系；不存在返回 ErrNotFound。
func (r *RelationRepository) Get(ctx context.Context, id string) (model.Relation, error) {
	rel, err := scanRelation(func(dest ...any) error {
		return r.db.QueryRowContext(ctx,
			"SELECT "+relationColumns+" FROM relations WHERE id=?", id).Scan(dest...)
	})
	if err == sql.ErrNoRows {
		return model.Relation{}, fmt.Errorf("sqlite: 读取 Relation %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.Relation{}, fmt.Errorf("sqlite: 读取 Relation %s: %w", id, err)
	}
	return rel, nil
}

// List 按 id 升序分页（cursor 为上一页最后一条 id），返回 items 与 nextCursor
// （nextCursor 为空表示没有更多数据）。
func (r *RelationRepository) List(ctx context.Context, page ports.PageRequest) ([]model.Relation, string, error) {
	limit := page.NormalizeLimit()
	query := "SELECT " + relationColumns + " FROM relations"
	args := make([]any, 0, 2)
	if page.Cursor != "" {
		query += " WHERE id>?"
		args = append(args, page.Cursor)
	}
	query += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit+1) // 多取一条判定是否有下一页

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlite: 列出 Relation: %w", err)
	}
	defer rows.Close()

	var items []model.Relation
	for rows.Next() {
		rel, err := scanRelation(rows.Scan)
		if err != nil {
			return nil, "", fmt.Errorf("sqlite: 列出 Relation: %w", err)
		}
		items = append(items, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("sqlite: 列出 Relation: %w", err)
	}

	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].RelationID
		items = items[:limit]
	}
	return items, nextCursor, nil
}

// UpdateHealth 更新关系健康状态；关系不存在返回 ErrNotFound。
func (r *RelationRepository) UpdateHealth(ctx context.Context, id string, health model.RelationHealth) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE relations SET health=? WHERE id=?", health, id)
	if err != nil {
		return fmt.Errorf("sqlite: 更新 Relation %s 健康: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("sqlite: 更新 Relation %s 健康: %w", id, ErrNotFound)
	}
	return nil
}

// IncrementRevision 原子递增关系修订号并返回新值；关系不存在返回 ErrNotFound。
func (r *RelationRepository) IncrementRevision(ctx context.Context, id string) (int, error) {
	var revision int
	err := r.db.QueryRowContext(ctx,
		"UPDATE relations SET revision=revision+1 WHERE id=? RETURNING revision", id).Scan(&revision)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("sqlite: 递增 Relation %s 修订号: %w", id, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("sqlite: 递增 Relation %s 修订号: %w", id, err)
	}
	return revision, nil
}

// PairExists 判断 project/runtime 配对是否已存在关系。
func (r *RelationRepository) PairExists(ctx context.Context, projectID, runtimeID string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM relations WHERE project_id=? AND runtime_id=?)",
		projectID, runtimeID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("sqlite: 检查 Relation 配对: %w", err)
	}
	return exists, nil
}

// nullString 把空字符串映射为 NULL（head 列等可空文本）。
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
