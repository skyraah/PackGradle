// Package task 实现任务生命周期（持久化 Task + 事件发布）。
// Task 是长操作的事实源；事件只通知进度与失效。
package task

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// Publisher 发布事件：先经 TaskEventRepository 原子分配 stream_sequence 持久化，
// 再转发给外部 sink（transport 桥接 Wails Events；headless 测试用内存收集器）。
type Publisher struct {
	events ports.TaskEventRepository
	sink   ports.EventPublisher // 可为 nil（仅持久化）
	ids    func(prefix string) string
	now    func() time.Time
}

// NewPublisher 构造事件发布器。
func NewPublisher(events ports.TaskEventRepository, sink ports.EventPublisher, ids func(prefix string) string, now func() time.Time) *Publisher {
	return &Publisher{events: events, sink: sink, ids: ids, now: now}
}

// PublishTask 发布 task_updated 事件，payload 为 Task 快照 JSON。
func (p *Publisher) PublishTask(ctx context.Context, t model.Task) error {
	payload, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("task: 序列化任务快照: %w", err)
	}
	env := model.EventEnvelope{
		SchemaVersion: model.CurrentSchemaVersion,
		EventID:       p.ids("evt_"),
		EventType:     model.EventTaskUpdated,
		EmittedAt:     p.now().UTC().Format(time.RFC3339),
		RelationID:    t.RelationID,
		TaskID:        t.TaskID,
		Payload:       payload,
	}
	return p.publish(ctx, env)
}

// PublishRelationInvalidated 发布关系失效信号（Watcher/扫描完成后触发受控重查）。
func (p *Publisher) PublishRelationInvalidated(ctx context.Context, relationID string) error {
	env := model.EventEnvelope{
		SchemaVersion: model.CurrentSchemaVersion,
		EventID:       p.ids("evt_"),
		EventType:     model.EventRelationInvalidated,
		EmittedAt:     p.now().UTC().Format(time.RFC3339),
		RelationID:    relationID,
		Payload:       json.RawMessage(`{}`),
	}
	return p.publish(ctx, env)
}

func (p *Publisher) publish(ctx context.Context, env model.EventEnvelope) error {
	seq, err := p.events.Append(ctx, env)
	if err != nil {
		return fmt.Errorf("task: 持久化事件: %w", err)
	}
	env.StreamSequence = seq
	if p.sink != nil {
		if err := p.sink.Publish(ctx, env); err != nil {
			// sink 失败不回滚持久化事件（事件可由查询 API 恢复）
			return fmt.Errorf("task: 转发事件: %w", err)
		}
	}
	return nil
}
