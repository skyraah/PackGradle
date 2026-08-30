package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// OperationJournalRepository 是 ports.OperationJournalRepository 的 SQLite 实现
// （operation_journal 逐操作当前行 + operation_journal_events 追加历史，schema v5）。
//
// 追加历史防改写语义（ADR-0004 §2）：本仓库不提供任何改写/删除历史的方法；
// 当前操作行只能沿状态机推进，且每次推进先追加事件、再更新当前行（同事务原子）。
// 库层另有 RAISE(ABORT) 触发器兜底拒绝 UPDATE/DELETE（schema_v5.go）。
type OperationJournalRepository struct {
	db DBTX
}

var _ ports.OperationJournalRepository = (*OperationJournalRepository)(nil)

// NewOperationJournalRepository 创建共享 *sql.DB 的操作日志仓库。
func NewOperationJournalRepository(db *sql.DB) *OperationJournalRepository {
	return &OperationJournalRepository{db: db}
}

// journalColumns 是 operation_journal 表读取列清单（与 scanJournalOperation 对应）。
const journalColumns = `task_id, operation_id, ordinal, status, target_relative_path,
	before_digest, after_digest, temp_relative_path, recovery_ref_json,
	ownership_proof_json, operation_json, result_json`

// journalEventColumns 是 operation_journal_events 表读取列清单。
const journalEventColumns = `task_id, seq, operation_id, from_status, to_status, occurred_at, detail_json`

// scanJournalOperation 把一行 operation_journal 扫描为 model.JournalOperation。
func scanJournalOperation(scan func(...any) error) (model.JournalOperation, error) {
	var (
		op                                        model.JournalOperation
		before, after, temp                       sql.NullString
		recoveryRef, ownership, operation, result sql.NullString
	)
	if err := scan(&op.TaskID, &op.OperationID, &op.Ordinal, &op.Status, &op.TargetRelativePath,
		&before, &after, &temp, &recoveryRef, &ownership, &operation, &result); err != nil {
		return model.JournalOperation{}, err
	}
	op.BeforeDigest = before.String
	op.AfterDigest = after.String
	op.TempRelativePath = temp.String
	if recoveryRef.Valid && recoveryRef.String != "null" {
		op.RecoveryRef = json.RawMessage(recoveryRef.String)
	}
	if ownership.Valid && ownership.String != "null" {
		op.OwnershipProof = json.RawMessage(ownership.String)
	}
	if operation.Valid && operation.String != "null" {
		op.Operation = json.RawMessage(operation.String)
	}
	if result.Valid && result.String != "null" {
		op.Result = json.RawMessage(result.String)
	}
	return op, nil
}

// scanJournalEvent 把一行 operation_journal_events 扫描为 model.JournalEvent。
func scanJournalEvent(scan func(...any) error) (model.JournalEvent, error) {
	var (
		ev         model.JournalEvent
		detailJSON string
	)
	if err := scan(&ev.TaskID, &ev.Seq, &ev.OperationID, &ev.FromStatus, &ev.ToStatus,
		&ev.OccurredAt, &detailJSON); err != nil {
		return model.JournalEvent{}, err
	}
	if detailJSON != "" && detailJSON != "{}" && detailJSON != "null" {
		ev.Detail = json.RawMessage(detailJSON)
	}
	return ev, nil
}

// appendJournalEvent 追加一条历史事件，seq 在任务内取 MAX+1（「最后一个已持久化
// 意图」= 任务内 seq 最大的一行）。调用方须已处于事务内。
func appendJournalEvent(ctx context.Context, tx DBTX, ev model.JournalEvent) error {
	var seq int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(seq),0)+1 FROM operation_journal_events WHERE task_id=?", ev.TaskID).Scan(&seq); err != nil {
		return fmt.Errorf("sqlite: 分配操作历史序号: %w", err)
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO operation_journal_events(task_id, seq, operation_id, from_status, to_status, occurred_at, detail_json)
VALUES(?,?,?,?,?,?,?)`,
		ev.TaskID, seq, ev.OperationID, ev.FromStatus, ev.ToStatus, ev.OccurredAt,
		rawJSONLiteral(ev.Detail, "{}"))
	if err != nil {
		return fmt.Errorf("sqlite: 追加操作历史 %s/%s: %w", ev.TaskID, ev.OperationID, err)
	}
	return nil
}

// InsertBatch 在同一事务写入一整批操作行（初始持久化意图，ADR-0004 §2：先持久化
// 意图再执行文件动作）并为每行落初始历史事件（from_status 为空串）。缺省 status
// 为 pending；任一行失败则整批回滚。
func (r *OperationJournalRepository) InsertBatch(ctx context.Context, ops []model.JournalOperation, occurredAt string) error {
	if len(ops) == 0 {
		return nil
	}
	return beginOrJoin(ctx, r.db, "写入操作批", func(tx DBTX) error {
		for _, op := range ops {
			status := op.Status
			if status == "" {
				status = model.OperationStatusPending
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO operation_journal(task_id, operation_id, ordinal, status, target_relative_path,
	before_digest, after_digest, temp_relative_path, recovery_ref_json,
	ownership_proof_json, operation_json, result_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				op.TaskID, op.OperationID, op.Ordinal, status, op.TargetRelativePath,
				nullString(op.BeforeDigest), nullString(op.AfterDigest), nullString(op.TempRelativePath),
				nullableRaw(op.RecoveryRef), rawJSONLiteral(op.OwnershipProof, "{}"),
				rawJSONLiteral(op.Operation, "{}"), nullableRaw(op.Result)); err != nil {
				return fmt.Errorf("sqlite: 写入操作 %s/%s: %w", op.TaskID, op.OperationID, err)
			}
			if err := appendJournalEvent(ctx, tx, model.JournalEvent{
				TaskID:      op.TaskID,
				OperationID: op.OperationID,
				FromStatus:  "",
				ToStatus:    status,
				OccurredAt:  occurredAt,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// AdvanceStatus 推进单操作状态：同事务内先追加历史事件、再更新当前行
// （ADR-0004 §2：每次状态变更必须先持久化意图）。非法迁移返回 ErrInvalidTransition。
func (r *OperationJournalRepository) AdvanceStatus(ctx context.Context, taskID, operationID, toStatus, occurredAt string, detail json.RawMessage) error {
	return beginOrJoin(ctx, r.db, "推进操作状态", func(tx DBTX) error {
		var current string
		err := tx.QueryRowContext(ctx,
			"SELECT status FROM operation_journal WHERE task_id=? AND operation_id=?",
			taskID, operationID).Scan(&current)
		if err == sql.ErrNoRows {
			return fmt.Errorf("sqlite: 操作 %s/%s 不存在: %w", taskID, operationID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("sqlite: 读取操作 %s/%s 状态: %w", taskID, operationID, err)
		}
		if !model.OperationCanTransition(current, toStatus) {
			return fmt.Errorf("sqlite: 操作 %s/%s 不允许 %s→%s: %w",
				taskID, operationID, current, toStatus, ErrInvalidTransition)
		}
		if err := appendJournalEvent(ctx, tx, model.JournalEvent{
			TaskID:      taskID,
			OperationID: operationID,
			FromStatus:  current,
			ToStatus:    toStatus,
			OccurredAt:  occurredAt,
			Detail:      detail,
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE operation_journal SET status=? WHERE task_id=? AND operation_id=?",
			toStatus, taskID, operationID); err != nil {
			return fmt.Errorf("sqlite: 推进操作 %s/%s 至 %s: %w", taskID, operationID, toStatus, err)
		}
		return nil
	})
}

// GetOperation 按 (task_id, operation_id) 读取单操作当前投影；不存在返回 ErrNotFound。
func (r *OperationJournalRepository) GetOperation(ctx context.Context, taskID, operationID string) (model.JournalOperation, error) {
	op, err := scanJournalOperation(func(dest ...any) error {
		return r.db.QueryRowContext(ctx,
			"SELECT "+journalColumns+" FROM operation_journal WHERE task_id=? AND operation_id=?",
			taskID, operationID).Scan(dest...)
	})
	if err == sql.ErrNoRows {
		return model.JournalOperation{}, fmt.Errorf("sqlite: 读取操作 %s/%s: %w", taskID, operationID, ErrNotFound)
	}
	if err != nil {
		return model.JournalOperation{}, fmt.Errorf("sqlite: 读取操作 %s/%s: %w", taskID, operationID, err)
	}
	return op, nil
}

// ListByTask 按 ordinal 升序分页读取逐操作当前投影（cursor 为最后一条 ordinal）。
func (r *OperationJournalRepository) ListByTask(ctx context.Context, taskID string, page ports.PageRequest) ([]model.JournalOperation, string, error) {
	limit := page.NormalizeLimit()
	query := "SELECT " + journalColumns + " FROM operation_journal WHERE task_id=?"
	args := []any{taskID}
	if page.Cursor != "" {
		ord, err := strconv.Atoi(page.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("sqlite: 列出任务 %s 操作: 非法 cursor %q: %w", taskID, page.Cursor, err)
		}
		query += " AND ordinal>?"
		args = append(args, ord)
	}
	query += " ORDER BY ordinal ASC LIMIT ?"
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlite: 列出任务 %s 操作: %w", taskID, err)
	}
	defer rows.Close()

	var items []model.JournalOperation
	for rows.Next() {
		op, err := scanJournalOperation(rows.Scan)
		if err != nil {
			return nil, "", fmt.Errorf("sqlite: 列出任务 %s 操作: %w", taskID, err)
		}
		items = append(items, op)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("sqlite: 列出任务 %s 操作: %w", taskID, err)
	}

	nextCursor := ""
	if len(items) > limit {
		nextCursor = strconv.Itoa(items[limit-1].Ordinal)
		items = items[:limit]
	}
	return items, nextCursor, nil
}

// ListEvents 按序号升序返回任务的全部追加历史（审计与恢复解释）。
func (r *OperationJournalRepository) ListEvents(ctx context.Context, taskID string) ([]model.JournalEvent, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+journalEventColumns+" FROM operation_journal_events WHERE task_id=? ORDER BY seq ASC", taskID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出任务 %s 操作历史: %w", taskID, err)
	}
	defer rows.Close()
	var items []model.JournalEvent
	for rows.Next() {
		ev, err := scanJournalEvent(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 列出任务 %s 操作历史: %w", taskID, err)
		}
		items = append(items, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 列出任务 %s 操作历史: %w", taskID, err)
	}
	return items, nil
}

// LastEvent 回答「最后一个已持久化意图是什么」（任务内 seq 最大的一条）；
// 无历史返回 ok=false。
func (r *OperationJournalRepository) LastEvent(ctx context.Context, taskID string) (model.JournalEvent, bool, error) {
	ev, err := scanJournalEvent(func(dest ...any) error {
		return r.db.QueryRowContext(ctx,
			"SELECT "+journalEventColumns+" FROM operation_journal_events WHERE task_id=?"+
				" ORDER BY seq DESC LIMIT 1", taskID).Scan(dest...)
	})
	if err == sql.ErrNoRows {
		return model.JournalEvent{}, false, nil
	}
	if err != nil {
		return model.JournalEvent{}, false, fmt.Errorf("sqlite: 读取任务 %s 最后操作历史: %w", taskID, err)
	}
	return ev, true, nil
}

// MarkResult 记录单操作的终局结果摘要（result_json 列；失败带说明码，成功留空）。
// 只写当前行：不改状态、不追加历史（ADR-0004 §2——状态推进只走 AdvanceStatus，
// 本方法补齐 T06 投影 ResultCode 的数据源）。操作不存在返回 ErrNotFound。
func (r *OperationJournalRepository) MarkResult(ctx context.Context, taskID, operationID string, result json.RawMessage) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE operation_journal SET result_json=? WHERE task_id=? AND operation_id=?",
		nullableRaw(result), taskID, operationID)
	if err != nil {
		return fmt.Errorf("sqlite: 记录操作 %s/%s 结果: %w", taskID, operationID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("sqlite: 记录操作 %s/%s 结果: %w", taskID, operationID, ErrNotFound)
	}
	return nil
}
