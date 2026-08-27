package model

import "encoding/json"

// 事件类型（架构文档 §9.2）。事件只通知进度或状态失效，不是事实源。
const (
	EventTaskUpdated         = "task_updated"
	EventRelationInvalidated = "relation_invalidated"
	EventWatchFailed         = "watch_failed" // P1 仅保留常量，不接入 fsnotify
)

// EventEnvelope 是后端事件的统一信封。
// StreamSequence 在单次应用事件流内单调递增，用于发现连接期间漏包；
// 与 Task.Sequence（任务内序号）、Relation.Revision（关系修订号）不可混用。
type EventEnvelope struct {
	SchemaVersion  int             `json:"schema_version"`
	EventID        string          `json:"event_id"` // evt_ 前缀
	EventType      string          `json:"event_type"`
	StreamSequence int64           `json:"stream_sequence"`
	EmittedAt      string          `json:"emitted_at"`
	RelationID     string          `json:"relation_id,omitempty"`
	TaskID         string          `json:"task_id,omitempty"`
	Payload        json.RawMessage `json:"payload"`
}

// 任务类别与状态（架构文档 §9.2）。
const (
	TaskKindScan    = "scan"
	TaskKindApply   = "apply"
	TaskKindRestore = "restore"
	TaskKindGC      = "gc"

	TaskStatusQueued           = "queued"
	TaskStatusRunning          = "running"
	TaskStatusSucceeded        = "succeeded"
	TaskStatusFailed           = "failed"
	TaskStatusCancelled        = "cancelled"
	TaskStatusRecoveryRequired = "recovery_required"

	TaskOutcomeExact   = "exact"
	TaskOutcomePartial = "partial"
)

// Task 是后端持久化的长操作事实源（架构文档 §9.2）。
// Sequence 是任务内持久化单调递增序号，用作更新乐观锁，拒绝旧快照覆盖新状态。
type Task struct {
	TaskID      string   `json:"task_id"` // task_ 前缀
	RelationID  string   `json:"relation_id,omitempty"`
	Sequence    int      `json:"task_sequence"`
	Kind        string   `json:"kind"`
	Status      string   `json:"status"`
	Outcome     string   `json:"outcome,omitempty"`
	Phase       string   `json:"phase"`
	Completed   int      `json:"completed"`
	Total       int      `json:"total"`
	MessageKey  string   `json:"message_key"` // msg.task.*，文案由前端 locale 提供
	MessageArgs []string `json:"message_args"`
	PlanID      string   `json:"plan_id,omitempty"`
	CommitID    string   `json:"commit_id,omitempty"`
	CanCancel   bool     `json:"can_cancel"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	Problem     *Problem `json:"problem,omitempty"`
}

// Problem 形状与 internal/errs.AppError 一致（code/args/detail）；
// core 不依赖 errs 包，由 application/transport 负责互转。
type Problem struct {
	Code   string   `json:"code"`
	Args   []string `json:"args,omitempty"`
	Detail string   `json:"detail,omitempty"`
}
