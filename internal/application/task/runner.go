package task

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// ErrTaskNotFound / ErrNotCancellable / ErrAlreadyFinished 是运行器哨兵错误。
var (
	ErrTaskNotFound    = errors.New("task: 任务不存在")
	ErrNotCancellable  = errors.New("task: 任务不可取消")
	ErrAlreadyFinished = errors.New("task: 任务已结束")
)

// Runner 管理任务生命周期：创建、更新（sequence 乐观锁 + 事件）、取消注册。
type Runner struct {
	repo ports.TaskRepository
	pub  *Publisher
	ids  func(prefix string) string
	now  func() time.Time

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// NewRunner 构造任务运行器。
func NewRunner(repo ports.TaskRepository, pub *Publisher, ids func(prefix string) string, now func() time.Time) *Runner {
	return &Runner{repo: repo, pub: pub, ids: ids, now: now, cancels: map[string]context.CancelFunc{}}
}

// Create 创建 queued 任务并发布首个事件。
func (r *Runner) Create(ctx context.Context, relationID, kind string, canCancel bool) (model.Task, error) {
	now := r.now().UTC().Format(time.RFC3339)
	t := model.Task{
		TaskID:      r.ids("task_"),
		RelationID:  relationID,
		Kind:        kind,
		Status:      model.TaskStatusQueued,
		Phase:       "pending",
		MessageKey:  "msg.task." + kind + ".queued",
		MessageArgs: []string{},
		CanCancel:   canCancel,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := r.repo.Insert(ctx, t); err != nil {
		return model.Task{}, fmt.Errorf("task: 创建任务: %w", err)
	}
	// 事件只是通知，不是事实源：发布失败不得阻止任务启动（否则留下无 worker 的僵尸任务）
	if err := r.pub.PublishTask(ctx, t); err != nil {
		log.Printf("task: 发布创建事件失败（任务 %s 仍继续）: %v", t.TaskID, err)
	}
	return t, nil
}

// Update 推进任务状态：递增 Sequence（乐观锁）、持久化并发布 task_updated。
// 事件发布失败只记日志：Task 持久化状态才是事实源。
func (r *Runner) Update(ctx context.Context, t model.Task) (model.Task, error) {
	t.Sequence++
	t.UpdatedAt = r.now().UTC().Format(time.RFC3339)
	if t.MessageArgs == nil {
		t.MessageArgs = []string{}
	}
	if err := r.repo.Update(ctx, t); err != nil {
		return t, fmt.Errorf("task: 更新任务 %s: %w", t.TaskID, err)
	}
	if err := r.pub.PublishTask(ctx, t); err != nil {
		log.Printf("task: 发布更新事件失败（任务 %s）: %v", t.TaskID, err)
	}
	return t, nil
}

// MarkFailed 记录失败（含结构化 Problem），状态 failed。
// 内部用 WithoutCancel 派生终态 ctx：终态落库必须可完成，
// 传入已取消的上下文（用户刚取消该任务）也不能阻止状态持久化。
func (r *Runner) MarkFailed(ctx context.Context, t model.Task, code, detail string, args ...string) {
	ctx = context.WithoutCancel(ctx)
	t.Status = model.TaskStatusFailed
	t.Problem = &model.Problem{Code: code, Args: args, Detail: detail}
	if _, err := r.Update(ctx, t); err != nil {
		log.Printf("task: 任务 %s 失败终态落库失败: %v", t.TaskID, err)
	}
}

// MarkCancelled 记录取消；终态落库同样不随传入 ctx 取消而失败。
func (r *Runner) MarkCancelled(ctx context.Context, t model.Task) {
	ctx = context.WithoutCancel(ctx)
	t.Status = model.TaskStatusCancelled
	if _, err := r.Update(ctx, t); err != nil {
		log.Printf("task: 任务 %s 取消终态落库失败: %v", t.TaskID, err)
	}
}

// RegisterCancel 注册任务的可取消句柄；任务结束后 UnregisterCancel 清理。
func (r *Runner) RegisterCancel(taskID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels[taskID] = cancel
}

// UnregisterCancel 清理取消句柄。
func (r *Runner) UnregisterCancel(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, taskID)
}

// Cancel 取消活动任务：触发已注册的取消句柄；queued 且尚未注册句柄时直接标 cancelled。
func (r *Runner) Cancel(ctx context.Context, taskID string) error {
	t, err := r.repo.Get(ctx, taskID)
	if err != nil {
		return ErrTaskNotFound
	}
	if !t.CanCancel {
		return ErrNotCancellable
	}
	switch t.Status {
	case model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
		return ErrAlreadyFinished
	}
	r.mu.Lock()
	cancel := r.cancels[taskID]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		return nil // 执行协程负责标记 cancelled
	}
	// queued 窗口期：直接落库 cancelled
	t.Status = model.TaskStatusCancelled
	_, err = r.Update(ctx, t)
	return err
}
