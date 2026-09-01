package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// TaskRepository 是 ports.TaskRepository 的 SQLite 实现（tasks 表）。
// Sequence 是任务内持久化单调递增序号，Update 以其为乐观锁。
type TaskRepository struct {
	db DBTX
}

var _ ports.TaskRepository = (*TaskRepository)(nil)

// NewTaskRepository 创建共享 *sql.DB 的任务仓库。
func NewTaskRepository(db *sql.DB) *TaskRepository {
	return &TaskRepository{db: db}
}

// marshalMessageArgs 序列化 MessageArgs；nil 列归一化为 "[]"（与列默认值一致）。
func marshalMessageArgs(args []string) (string, error) {
	if args == nil {
		return "[]", nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// marshalNullableJSON 序列化可空对象；nil（含类型化 nil 指针）存 SQL NULL。
func marshalNullableJSON(v any) (any, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || (rv.Kind() == reflect.Pointer && rv.IsNil()) {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// Insert 新建任务（初始 sequence 由调用方置 0）。relation 不存在返回 ErrRelationNotFound。
func (r *TaskRepository) Insert(ctx context.Context, t model.Task) error {
	argsJSON, err := marshalMessageArgs(t.MessageArgs)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化任务 %s 参数: %w", t.TaskID, err)
	}
	problemJSON, err := marshalNullableJSON(t.Problem)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化任务 %s 问题: %w", t.TaskID, err)
	}

	// 完整性守卫（P0-3）：plan/commit 引用必须存在且属于任务自己的 Relation。
	if err := verifyTaskIntegrity(ctx, r.db, t); err != nil {
		return fmt.Errorf("sqlite: 写入任务 %s 完整性校验失败: %w", t.TaskID, err)
	}

	_, err = r.db.ExecContext(ctx, `
INSERT INTO tasks(id, relation_id, kind, status, phase, sequence, outcome, can_cancel,
	completed, total, message_key, message_args_json, plan_id, commit_id, created_at, updated_at, problem_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.TaskID, nullString(t.RelationID), t.Kind, t.Status, t.Phase, t.Sequence,
		nullString(t.Outcome), boolToInt(t.CanCancel), t.Completed, t.Total,
		t.MessageKey, argsJSON, nullString(t.PlanID), nullString(t.CommitID),
		t.CreatedAt, t.UpdatedAt, problemJSON)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("sqlite: 写入任务 %s: %w", t.TaskID, taskReferenceSentinel(ctx, r.db, t))
		}
		return fmt.Errorf("sqlite: 写入任务 %s: %w", t.TaskID, err)
	}
	return nil
}

// Update 以 Sequence 为乐观锁：库中当前 sequence 必须小于 t.Sequence
// （即新值必须大于库中当前值），否则返回 ErrSequenceConflict，拒绝旧快照覆盖新状态。
func (r *TaskRepository) Update(ctx context.Context, t model.Task) error {
	argsJSON, err := marshalMessageArgs(t.MessageArgs)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化任务 %s 参数: %w", t.TaskID, err)
	}
	problemJSON, err := marshalNullableJSON(t.Problem)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化任务 %s 问题: %w", t.TaskID, err)
	}

	// 完整性守卫（P0-3）：plan/commit 引用必须存在且属于任务自己的 Relation。
	if err := verifyTaskIntegrity(ctx, r.db, t); err != nil {
		return fmt.Errorf("sqlite: 更新任务 %s 完整性校验失败: %w", t.TaskID, err)
	}

	res, err := r.db.ExecContext(ctx, `
UPDATE tasks SET status=?, outcome=?, phase=?, sequence=?, can_cancel=?,
	completed=?, total=?, message_key=?, message_args_json=?, plan_id=?, commit_id=?,
	updated_at=?, problem_json=?
WHERE id=? AND sequence<?`,
		t.Status, nullString(t.Outcome), t.Phase, t.Sequence, boolToInt(t.CanCancel),
		t.Completed, t.Total, t.MessageKey, argsJSON, nullString(t.PlanID), nullString(t.CommitID),
		t.UpdatedAt, problemJSON, t.TaskID, t.Sequence)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("sqlite: 更新任务 %s: %w", t.TaskID, taskReferenceSentinel(ctx, r.db, t))
		}
		return fmt.Errorf("sqlite: 更新任务 %s: %w", t.TaskID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("sqlite: 更新任务 %s (sequence=%d): %w", t.TaskID, t.Sequence, ErrSequenceConflict)
	}
	return nil
}

// taskColumns 是 tasks 表读取列清单（与 scanTask 对应）。
const taskColumns = `id, relation_id, kind, status, phase, sequence, outcome, can_cancel,
	completed, total, message_key, message_args_json, plan_id, commit_id, created_at, updated_at, problem_json`

// scanTask 把一行 tasks 扫描为 model.Task。
func scanTask(scan func(...any) error) (model.Task, error) {
	var (
		t                               model.Task
		relationID, outcome, planID     sql.NullString
		commitID, argsJSON, problemJSON sql.NullString
		canCancel                       int
	)
	if err := scan(&t.TaskID, &relationID, &t.Kind, &t.Status, &t.Phase, &t.Sequence,
		&outcome, &canCancel, &t.Completed, &t.Total, &t.MessageKey,
		&argsJSON, &planID, &commitID, &t.CreatedAt, &t.UpdatedAt, &problemJSON); err != nil {
		return model.Task{}, err
	}
	t.CanCancel = canCancel != 0
	t.RelationID = relationID.String
	t.Outcome = outcome.String
	t.PlanID = planID.String
	t.CommitID = commitID.String
	if argsJSON.Valid && argsJSON.String != "[]" && argsJSON.String != "null" {
		if err := json.Unmarshal([]byte(argsJSON.String), &t.MessageArgs); err != nil {
			return model.Task{}, fmt.Errorf("sqlite: 解析任务 %s 参数: %w", t.TaskID, err)
		}
	}
	if problemJSON.Valid && problemJSON.String != "null" {
		var problem model.Problem
		if err := json.Unmarshal([]byte(problemJSON.String), &problem); err != nil {
			return model.Task{}, fmt.Errorf("sqlite: 解析任务 %s 问题: %w", t.TaskID, err)
		}
		t.Problem = &problem
	}
	return t, nil
}

// Get 按 id 读取任务；不存在返回 ErrNotFound。
func (r *TaskRepository) Get(ctx context.Context, id string) (model.Task, error) {
	t, err := scanTask(func(dest ...any) error {
		return r.db.QueryRowContext(ctx,
			"SELECT "+taskColumns+" FROM tasks WHERE id=?", id).Scan(dest...)
	})
	if err == sql.ErrNoRows {
		return model.Task{}, fmt.Errorf("sqlite: 读取任务 %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return model.Task{}, fmt.Errorf("sqlite: 读取任务 %s: %w", id, err)
	}
	return t, nil
}

// ListByRelation 按关系列出任务（id 升序分页，cursor 为最后一条 id）。
// active=true 时只返回 status IN ('queued','running') 的活跃任务。
func (r *TaskRepository) ListByRelation(ctx context.Context, relationID string, active bool, page ports.PageRequest) ([]model.Task, string, error) {
	limit := page.NormalizeLimit()
	query := "SELECT " + taskColumns + " FROM tasks WHERE relation_id=?"
	args := []any{relationID}
	if active {
		query += " AND status IN ('queued','running')"
	}
	if page.Cursor != "" {
		query += " AND id>?"
		args = append(args, page.Cursor)
	}
	query += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlite: 列出关系 %s 任务: %w", relationID, err)
	}
	defer rows.Close()

	var items []model.Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, "", fmt.Errorf("sqlite: 列出关系 %s 任务: %w", relationID, err)
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("sqlite: 列出关系 %s 任务: %w", relationID, err)
	}

	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].TaskID
		items = items[:limit]
	}
	return items, nextCursor, nil
}

// FindActiveByRelationAndKind 查找关系下指定类别的最新活跃任务
// （status IN ('queued','running')，created_at DESC + id DESC 取 1）。
func (r *TaskRepository) FindActiveByRelationAndKind(ctx context.Context, relationID, kind string) (model.Task, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `
SELECT id FROM tasks WHERE relation_id=? AND kind=? AND status IN ('queued','running')
ORDER BY created_at DESC, id DESC LIMIT 1`, relationID, kind).Scan(&id)
	if err == sql.ErrNoRows {
		return model.Task{}, false, nil
	}
	if err != nil {
		return model.Task{}, false, fmt.Errorf("sqlite: 查找关系 %s 活跃 %s 任务: %w", relationID, kind, err)
	}
	t, err := r.Get(ctx, id)
	if err != nil {
		return model.Task{}, false, err
	}
	return t, true, nil
}

// FindActiveByKind 查找全局（跨关系，含 relation_id IS NULL）指定类别的活跃
// 任务（status IN ('queued','running')，created_at DESC + id DESC 取 1）。
// GC 全局单飞的 DB 侧守卫（票 #64）。
func (r *TaskRepository) FindActiveByKind(ctx context.Context, kind string) (model.Task, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `
SELECT id FROM tasks WHERE kind=? AND status IN ('queued','running')
ORDER BY created_at DESC, id DESC LIMIT 1`, kind).Scan(&id)
	if err == sql.ErrNoRows {
		return model.Task{}, false, nil
	}
	if err != nil {
		return model.Task{}, false, fmt.Errorf("sqlite: 查找全局活跃 %s 任务: %w", kind, err)
	}
	t, err := r.Get(ctx, id)
	if err != nil {
		return model.Task{}, false, err
	}
	return t, true, nil
}

// ListActiveAll 返回全部 queued/running 任务（启动恢复用，id 升序）。
func (r *TaskRepository) ListActiveAll(ctx context.Context) ([]model.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE status IN ('queued','running') ORDER BY id ASC")
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出活跃任务: %w", err)
	}
	defer rows.Close()
	var items []model.Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 列出活跃任务: %w", err)
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 列出活跃任务: %w", err)
	}
	return items, nil
}

// boolToInt 把布尔值映射为 SQLite 的 0/1 存储。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
