package task

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/ids"
	"packgradle/internal/core/model"
)

// ---- 内存假仓库 ----

type memTasks struct {
	mu   sync.Mutex
	rows map[string]model.Task
}

func newMemTasks() *memTasks { return &memTasks{rows: map[string]model.Task{}} }

func (m *memTasks) Insert(ctx context.Context, t model.Task) error {
	if err := ctx.Err(); err != nil {
		return err // 模拟 database/sql 在 ctx 取消后拒绝执行
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.rows[t.TaskID]; dup {
		return ports.ErrDuplicate
	}
	m.rows[t.TaskID] = t
	return nil
}

func (m *memTasks) Update(ctx context.Context, t model.Task) error {
	if err := ctx.Err(); err != nil {
		return err // 模拟 database/sql 在 ctx 取消后拒绝执行
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[t.TaskID]
	if !ok {
		return ports.ErrNotFound
	}
	if t.Sequence <= cur.Sequence {
		return ports.ErrSequenceConflict
	}
	m.rows[t.TaskID] = t
	return nil
}

func (m *memTasks) Get(_ context.Context, id string) (model.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.rows[id]; ok {
		return t, nil
	}
	return model.Task{}, ports.ErrNotFound
}

func (m *memTasks) ListByRelation(_ context.Context, relID string, active bool, page ports.PageRequest) ([]model.Task, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.Task
	for _, t := range m.rows {
		if t.RelationID != relID {
			continue
		}
		if active && t.Status != model.TaskStatusQueued && t.Status != model.TaskStatusRunning {
			continue
		}
		out = append(out, t)
	}
	return out, "", nil
}

func (m *memTasks) FindActiveByRelationAndKind(_ context.Context, relID, kind string) (model.Task, bool, error) {
	tasks, _, _ := m.ListByRelation(context.Background(), relID, true, ports.PageRequest{})
	for _, t := range tasks {
		if t.Kind == kind {
			return t, true, nil
		}
	}
	return model.Task{}, false, nil
}

func (m *memTasks) ListActiveAll(ctx context.Context) ([]model.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.Task
	for _, t := range m.rows {
		if t.Status == model.TaskStatusQueued || t.Status == model.TaskStatusRunning {
			out = append(out, t)
		}
	}
	return out, nil
}

type memEvents struct {
	mu    sync.Mutex
	seq   int64
	items []model.EventEnvelope
}

func (m *memEvents) Append(_ context.Context, env model.EventEnvelope) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	m.items = append(m.items, env)
	return m.seq, nil
}

type recordingSink struct {
	mu    sync.Mutex
	items []model.EventEnvelope
}

func (r *recordingSink) Publish(_ context.Context, env model.EventEnvelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, env)
	return nil
}

// ---- 测试 ----

func newFixture() (*Runner, *Publisher, *memTasks, *recordingSink) {
	repo := &memEvents{}
	sink := &recordingSink{}
	pub := NewPublisher(repo, sink, ids.New, time.Now)
	tasks := newMemTasks()
	runner := NewRunner(tasks, pub, ids.New, time.Now)
	return runner, pub, tasks, sink
}

func TestTaskLifecycleEvents(t *testing.T) {
	runner, _, tasks, sink := newFixture()
	ctx := context.Background()

	task, err := runner.Create(ctx, "rel_1", model.TaskKindScan, true)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = model.TaskStatusRunning
	task.Phase = "scan_project"
	if task, err = runner.Update(ctx, task); err != nil {
		t.Fatal(err)
	}
	if task.Sequence != 1 {
		t.Fatalf("sequence 应为 1: %d", task.Sequence)
	}
	task.Status = model.TaskStatusSucceeded
	task.Phase = "done"
	if _, err = runner.Update(ctx, task); err != nil {
		t.Fatal(err)
	}

	// 事件序列：queued/running/succeeded 共 3 条 task_updated，stream_sequence 单调
	if len(sink.items) != 3 {
		t.Fatalf("事件数: %d", len(sink.items))
	}
	prev := int64(0)
	for i, env := range sink.items {
		if env.EventType != model.EventTaskUpdated {
			t.Fatalf("事件 %d 类型: %s", i, env.EventType)
		}
		if env.StreamSequence <= prev {
			t.Fatalf("stream_sequence 非单调: %d <= %d", env.StreamSequence, prev)
		}
		prev = env.StreamSequence
	}
	// 持久化终态
	got, _ := tasks.Get(ctx, task.TaskID)
	if got.Status != model.TaskStatusSucceeded || got.Sequence != 2 {
		t.Fatalf("终态: %+v", got)
	}
}

func TestTaskFailureRecordsProblem(t *testing.T) {
	runner, _, tasks, _ := newFixture()
	ctx := context.Background()
	task, err := runner.Create(ctx, "rel_2", model.TaskKindScan, true)
	if err != nil {
		t.Fatal(err)
	}
	runner.MarkFailed(ctx, task, "err.scan.adapter_failed", "boom", "rel_2")
	got, err := tasks.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TaskStatusFailed || got.Problem == nil || got.Problem.Code != "err.scan.adapter_failed" {
		t.Fatalf("失败终态: %+v", got)
	}
}

func TestTaskCancel(t *testing.T) {
	runner, _, _, _ := newFixture()
	ctx := context.Background()
	task, _ := runner.Create(ctx, "rel_3", model.TaskKindScan, true)

	// 未注册句柄的 queued 任务：直接标 cancelled
	if err := runner.Cancel(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	// 再取消：已结束
	if err := runner.Cancel(ctx, task.TaskID); err != ErrAlreadyFinished {
		t.Fatalf("二次取消应报已结束: %v", err)
	}

	// 不可取消任务
	task2, _ := runner.Create(ctx, "rel_3", model.TaskKindApply, false)
	if err := runner.Cancel(ctx, task2.TaskID); err != ErrNotCancellable {
		t.Fatalf("不可取消任务: %v", err)
	}
	// 不存在
	if err := runner.Cancel(ctx, "task_missing"); err != ErrTaskNotFound {
		t.Fatalf("缺失任务: %v", err)
	}
}

func TestMarkCancelledWithCancelledContext(t *testing.T) {
	// 回归：终态落库不得依赖可取消 ctx——用户取消后 ctx 已失效，
	// 用它写 cancelled 会写库失败并留下永远 running 的僵尸任务。
	// 生产代码用 context.WithoutCancel 派生终态 ctx（见 sync.runScan）。
	runner, _, tasks, _ := newFixture()
	ctx := context.Background()
	task, err := runner.Create(ctx, "rel_c", model.TaskKindScan, true)
	if err != nil {
		t.Fatal(err)
	}
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	runner.MarkCancelled(cancelledCtx, task)
	got, err := tasks.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TaskStatusCancelled {
		t.Fatalf("取消终态未落库: %+v", got)
	}
}

func TestPublisherRelationInvalidated(t *testing.T) {
	_, pub, _, sink := newFixture()
	if err := pub.PublishRelationInvalidated(context.Background(), "rel_x"); err != nil {
		t.Fatal(err)
	}
	if len(sink.items) != 1 || sink.items[0].EventType != model.EventRelationInvalidated {
		t.Fatalf("事件: %+v", sink.items)
	}
	if fmt.Sprintf("%s", sink.items[0].Payload) != "{}" {
		t.Fatalf("payload: %s", sink.items[0].Payload)
	}
}
