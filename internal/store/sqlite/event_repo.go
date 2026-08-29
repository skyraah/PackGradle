package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// EventRepository 是 ports.TaskEventRepository 的 SQLite 实现（task_events 表）。
// StreamSequence 在事件流内单调递增（UNIQUE 约束兜底），用于发现连接期间漏包；
// 分配方式依赖 Open 的单连接设置（MaxOpenConns=1）下事务内的 MAX+1 读取。
type EventRepository struct {
	db DBTX
}

var _ ports.TaskEventRepository = (*EventRepository)(nil)

// NewEventRepository 创建共享 *sql.DB 的事件仓库。
func NewEventRepository(db *sql.DB) *EventRepository {
	return &EventRepository{db: db}
}

// Append 在单事务内读取 MAX(stream_sequence)+1 并写入事件，返回分配的序号。
// 独立使用时自开事务；处于 RunInTx 事务域内时加入外层事务。
func (r *EventRepository) Append(ctx context.Context, env model.EventEnvelope) (int64, error) {
	var next int64
	err := beginOrJoin(ctx, r.db, "写入事件 "+env.EventID, func(tx DBTX) error {
		if err := tx.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(stream_sequence),0)+1 FROM task_events").Scan(&next); err != nil {
			return fmt.Errorf("sqlite: 写入事件 %s 分配序号: %w", env.EventID, err)
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_events(event_id, stream_sequence, event_type, emitted_at, relation_id, task_id, payload_json)
VALUES(?,?,?,?,?,?,?)`,
			env.EventID, next, env.EventType, env.EmittedAt,
			nullString(env.RelationID), nullString(env.TaskID), string(env.Payload)); err != nil {
			return fmt.Errorf("sqlite: 写入事件 %s: %w", env.EventID, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return next, nil
}
