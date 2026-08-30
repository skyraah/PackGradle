package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// CommitRepository 是 ports.CommitRepository 的 SQLite 实现
// （sync_commits + commit_changes，schema v1 冻结表的 Phase 2 收口，契约 05 §7 D3）。
type CommitRepository struct {
	db DBTX
}

var _ ports.CommitRepository = (*CommitRepository)(nil)

// NewCommitRepository 创建共享 *sql.DB 的提交仓库。
func NewCommitRepository(db *sql.DB) *CommitRepository {
	return &CommitRepository{db: db}
}

// commitColumns 是 sync_commits 表读取列清单（与 scanCommit 对应）。
const commitColumns = `id, relation_id, parent_id, created_at, plan_id,
	verified_project_snapshot_id, verified_runtime_snapshot_id,
	previous_baseline_id, result_baseline_id, commit_kind, completeness,
	remaining_change_count, summary_json`

// scanCommit 把一行 sync_commits 扫描为 model.SyncCommit（不含 changes）。
func scanCommit(scan func(...any) error) (model.SyncCommit, error) {
	var (
		c                          model.SyncCommit
		parentID, previousBaseline sql.NullString
		summaryJSON                string
	)
	if err := scan(&c.CommitID, &c.RelationID, &parentID, &c.CreatedAt, &c.PlanID,
		&c.VerifiedProjectSnapshotID, &c.VerifiedRuntimeSnapshotID,
		&previousBaseline, &c.ResultBaselineID, &c.CommitKind, &c.Completeness,
		&c.RemainingChangeCount, &summaryJSON); err != nil {
		return model.SyncCommit{}, err
	}
	c.ParentCommitID = parentID.String
	c.PreviousBaselineID = previousBaseline.String
	if summaryJSON != "" && summaryJSON != "{}" && summaryJSON != "null" {
		c.Summary = json.RawMessage(summaryJSON)
	}
	return c, nil
}

// verifyCommitIntegrity 是 CommitRepository.Insert 的守卫（检视报告 P0-3 同款纪律）：
// 计划、verified 快照（side 相符）、前后基线、parent 提交必须存在且属于同一 Relation，
// 保证提交图的审计链不被装配错误污染。
func verifyCommitIntegrity(ctx context.Context, q guardQuerier, c model.SyncCommit) error {
	if err := requirePlanOfRelation(ctx, q, c.PlanID, c.RelationID); err != nil {
		return err
	}
	if _, err := requireSnapshotOfRelation(ctx, q, c.VerifiedProjectSnapshotID, c.RelationID, model.SideProject); err != nil {
		return err
	}
	if _, err := requireSnapshotOfRelation(ctx, q, c.VerifiedRuntimeSnapshotID, c.RelationID, model.SideRuntime); err != nil {
		return err
	}
	if c.PreviousBaselineID != "" {
		if _, err := requireBaselineOfRelation(ctx, q, c.PreviousBaselineID, c.RelationID); err != nil {
			return err
		}
	}
	if _, err := requireBaselineOfRelation(ctx, q, c.ResultBaselineID, c.RelationID); err != nil {
		return err
	}
	if c.ParentCommitID != "" {
		gotRelation, err := rowRelation(ctx, q, "sync_commits", c.ParentCommitID, ErrNotFound)
		if err != nil {
			return err
		}
		if gotRelation != c.RelationID {
			return fmt.Errorf("sqlite: parent 提交 %s 属于 Relation %s，期望 %s: %w",
				c.ParentCommitID, gotRelation, c.RelationID, ErrParentMismatch)
		}
	}
	return nil
}

// commitReferenceSentinel 把提交写入的外键违例翻译为可区分的哨兵错误：
// 按 relation → plan → snapshot → baseline → parent commit 的顺序定位坏引用。
func commitReferenceSentinel(ctx context.Context, q DBTX, c model.SyncCommit) error {
	if !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM relations WHERE id=?)", c.RelationID) {
		return ErrRelationNotFound
	}
	if !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM sync_plans WHERE id=?)", c.PlanID) {
		return ErrPlanNotFound
	}
	for _, ref := range []struct{ id string }{
		{c.VerifiedProjectSnapshotID}, {c.VerifiedRuntimeSnapshotID},
	} {
		if !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM observed_snapshots WHERE id=?)", ref.id) {
			return ErrNotFound
		}
	}
	for _, ref := range []struct{ id string }{
		{c.PreviousBaselineID}, {c.ResultBaselineID},
	} {
		if ref.id != "" && !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM sync_baselines WHERE id=?)", ref.id) {
			return ErrNotFound
		}
	}
	if c.ParentCommitID != "" && !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM sync_commits WHERE id=?)", c.ParentCommitID) {
		return ErrNotFound
	}
	return ErrRelationNotFound
}

// Insert 在同一事务写 sync_commits 与 commit_changes（commit+changes 单事务原子，
// 契约 05 §7 零消费表收口第一步）。任一变化行失败则整体回滚。
func (r *CommitRepository) Insert(ctx context.Context, c model.SyncCommit) error {
	return beginOrJoin(ctx, r.db, "写入同步提交", func(tx DBTX) error {
		if err := verifyCommitIntegrity(ctx, tx, c); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_commits(id, relation_id, parent_id, created_at, plan_id,
	verified_project_snapshot_id, verified_runtime_snapshot_id,
	previous_baseline_id, result_baseline_id, commit_kind, completeness,
	remaining_change_count, summary_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.CommitID, c.RelationID, nullString(c.ParentCommitID), c.CreatedAt, c.PlanID,
			c.VerifiedProjectSnapshotID, c.VerifiedRuntimeSnapshotID,
			nullString(c.PreviousBaselineID), c.ResultBaselineID, c.CommitKind, c.Completeness,
			c.RemainingChangeCount, rawJSONLiteral(c.Summary, "{}")); err != nil {
			if isForeignKeyViolation(err) {
				return fmt.Errorf("sqlite: 写入提交 %s: %w", c.CommitID, commitReferenceSentinel(ctx, tx, c))
			}
			return fmt.Errorf("sqlite: 写入提交 %s: %w", c.CommitID, err)
		}
		for _, ch := range c.Changes {
			projectBefore, err := marshalNullableJSON(ch.ProjectBefore)
			if err != nil {
				return fmt.Errorf("sqlite: 序列化提交 %s 变化 %s: %w", c.CommitID, ch.ResourceID, err)
			}
			projectAfter, err := marshalNullableJSON(ch.ProjectAfter)
			if err != nil {
				return fmt.Errorf("sqlite: 序列化提交 %s 变化 %s: %w", c.CommitID, ch.ResourceID, err)
			}
			runtimeBefore, err := marshalNullableJSON(ch.RuntimeBefore)
			if err != nil {
				return fmt.Errorf("sqlite: 序列化提交 %s 变化 %s: %w", c.CommitID, ch.ResourceID, err)
			}
			runtimeAfter, err := marshalNullableJSON(ch.RuntimeAfter)
			if err != nil {
				return fmt.Errorf("sqlite: 序列化提交 %s 变化 %s: %w", c.CommitID, ch.ResourceID, err)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO commit_changes(commit_id, resource_id, change_kind,
	project_before, project_after, runtime_before, runtime_after)
VALUES(?,?,?,?,?,?,?)`,
				c.CommitID, ch.ResourceID, ch.ChangeKind,
				projectBefore, projectAfter, runtimeBefore, runtimeAfter); err != nil {
				return fmt.Errorf("sqlite: 写入提交 %s 变化 %s: %w", c.CommitID, ch.ResourceID, err)
			}
		}
		return nil
	})
}

// GetForRelation 读取单提交含逐资源 changes（联 resource_representations 取资源身份，
// 依 verified 快照定位两侧表示行）。不存在或属于其他 Relation 一律 ErrNotFound。
func (r *CommitRepository) GetForRelation(ctx context.Context, commitID, relationID string) (model.SyncCommit, error) {
	c, err := scanCommit(func(dest ...any) error {
		return r.db.QueryRowContext(ctx,
			"SELECT "+commitColumns+" FROM sync_commits WHERE id=? AND relation_id=?",
			commitID, relationID).Scan(dest...)
	})
	if err == sql.ErrNoRows {
		return model.SyncCommit{}, fmt.Errorf("sqlite: 读取提交 %s: %w", commitID, ErrNotFound)
	}
	if err != nil {
		return model.SyncCommit{}, fmt.Errorf("sqlite: 读取提交 %s: %w", commitID, err)
	}
	changes, err := r.loadChanges(ctx, c)
	if err != nil {
		return model.SyncCommit{}, err
	}
	c.Changes = changes
	return c, nil
}

// loadChanges 读取提交的全部变化行，LEFT JOIN resource_representations（verified
// 项目/运行时快照）取资源身份；两侧身份应一致，取 COALESCE 归一为一行。
func (r *CommitRepository) loadChanges(ctx context.Context, c model.SyncCommit) ([]model.CommitChange, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT cc.resource_id, cc.change_kind, cc.project_before, cc.project_after,
	cc.runtime_before, cc.runtime_after,
	COALESCE(rp_p.identity_provider, rp_r.identity_provider, ''),
	COALESCE(rp_p.identity_key, rp_r.identity_key, ''),
	COALESCE(rp_p.identity_confidence, rp_r.identity_confidence, '')
FROM commit_changes cc
LEFT JOIN resource_representations rp_p ON rp_p.snapshot_id=? AND rp_p.resource_id=cc.resource_id
LEFT JOIN resource_representations rp_r ON rp_r.snapshot_id=? AND rp_r.resource_id=cc.resource_id
WHERE cc.commit_id=?
ORDER BY cc.resource_id ASC`,
		c.VerifiedProjectSnapshotID, c.VerifiedRuntimeSnapshotID, c.CommitID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读取提交 %s 变化: %w", c.CommitID, err)
	}
	defer rows.Close()

	var items []model.CommitChange
	for rows.Next() {
		var (
			ch             model.CommitChange
			pb, pa, rb, ra sql.NullString
		)
		if err := rows.Scan(&ch.ResourceID, &ch.ChangeKind, &pb, &pa, &rb, &ra,
			&ch.Identity.Provider, &ch.Identity.Key, &ch.Identity.Confidence); err != nil {
			return nil, fmt.Errorf("sqlite: 读取提交 %s 变化: %w", c.CommitID, err)
		}
		for _, ref := range []struct {
			raw  sql.NullString
			dest **model.Representation
		}{{pb, &ch.ProjectBefore}, {pa, &ch.ProjectAfter}, {rb, &ch.RuntimeBefore}, {ra, &ch.RuntimeAfter}} {
			rep, err := nullableRepresentation(ref.raw)
			if err != nil {
				return nil, fmt.Errorf("sqlite: 解析提交 %s 变化 %s 表示: %w", c.CommitID, ch.ResourceID, err)
			}
			*ref.dest = rep
		}
		items = append(items, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 读取提交 %s 变化: %w", c.CommitID, err)
	}
	return items, nil
}

// nullableRepresentation 反序列化可空表示 JSON 列。
func nullableRepresentation(s sql.NullString) (*model.Representation, error) {
	if !s.Valid || s.String == "" || s.String == "null" {
		return nil, nil
	}
	var rep model.Representation
	if err := json.Unmarshal([]byte(s.String), &rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// ListByRelation 按 Relation 分页列出提交头（不含 changes；id 升序，cursor 为最后一条 id）。
func (r *CommitRepository) ListByRelation(ctx context.Context, relationID string, page ports.PageRequest) ([]model.SyncCommit, string, error) {
	limit := page.NormalizeLimit()
	query := "SELECT " + commitColumns + " FROM sync_commits WHERE relation_id=?"
	args := []any{relationID}
	if page.Cursor != "" {
		query += " AND id>?"
		args = append(args, page.Cursor)
	}
	query += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlite: 列出关系 %s 提交: %w", relationID, err)
	}
	defer rows.Close()

	var items []model.SyncCommit
	for rows.Next() {
		c, err := scanCommit(rows.Scan)
		if err != nil {
			return nil, "", fmt.Errorf("sqlite: 列出关系 %s 提交: %w", relationID, err)
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("sqlite: 列出关系 %s 提交: %w", relationID, err)
	}

	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].CommitID
		items = items[:limit]
	}
	return items, nextCursor, nil
}
