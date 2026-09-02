package sync

// cleanup.go 实现惰性清理通道（ADR-0011 §2/§3，票 #89）：数据目录有界化的
// 任务事件与旧数据行面。
//
//   - 窗口常量（编译期，数值施工可调、数量级不变；零用户设置面——保留策略
//     五参数设置面 P3 已有，本通道不新增）；
//   - 触发时机 = 启动时（bootstrap 装配后同步调用 RunLazyCleanup）+ 任务
//     终态后（runner 终态钩子异步触发），无定时器；
//   - 判定 = 过期 ∧ 无存活引用（SQL 守卫内联，见 sqlite.CleanupRepository）：
//     task_events 保最近 10,000 条（stream_sequence 留尾，截断后从 MAX+1 续、
//     清全表从 1 重来——前端重启以首个事件建基线，皆不误判漏包）；
//     sync_plans expired/stale 历史行物理删、applied 行随其提交存亡；
//     preparations/rebind_preparations 过期/consumed 即删；
//     终态 tasks 保最近 200 条；apply_runs 永不删（墓碑计数分子，观测口径
//     基石——apply_runs.task_id 是主键，任务行随之结构上不可删）。

import (
	"context"
	"errors"
	"log"

	"packgradle/internal/core/model"
)

// 惰性清理窗口（编译期常量，ADR-0011 §2/§3）。
const (
	// TaskEventsKeep 是 task_events 条数窗口：保最近 10,000 条（按
	// stream_sequence 留尾）。
	TaskEventsKeep = 10000
	// TerminalTasksKeep 是终态任务行保底窗口：保最近 200 条。
	TerminalTasksKeep = 200
)

// RunLazyCleanup 执行一轮惰性清理（启动时 + 任务终态后同一通道）。
// 各步骤相互独立、逐步幂等可重入：单步失败记日志并继续其余步骤（机会主义
// 通道，下轮续清），全部失败经 errors.Join 返回。清理面未装配（deps.Cleanup
// 为 nil）时零操作——未接清理面的测试栈与既有链路零波及。
func (a *App) RunLazyCleanup(ctx context.Context) error {
	if a.deps.Cleanup == nil {
		return nil
	}
	a.cleanupMu.Lock()
	defer a.cleanupMu.Unlock()

	now := a.nowStr()
	var errs []error
	if n, err := a.deps.Cleanup.TruncateTaskEvents(ctx, TaskEventsKeep); err != nil {
		errs = append(errs, err)
	} else if n > 0 {
		log.Printf("cleanup: task_events 截断 %d 条（窗口 %d）", n, TaskEventsKeep)
	}
	if n, err := a.deps.Cleanup.DeleteExpiredPlans(ctx, now); err != nil {
		errs = append(errs, err)
	} else if n > 0 {
		log.Printf("cleanup: 历史计划行删除 %d 条", n)
	}
	if n, err := a.deps.Cleanup.DeleteExpiredPreparations(ctx, now); err != nil {
		errs = append(errs, err)
	} else if n > 0 {
		log.Printf("cleanup: 过期预检删除 %d 条", n)
	}
	if n, err := a.deps.Cleanup.PruneTerminalTasks(ctx, TerminalTasksKeep); err != nil {
		errs = append(errs, err)
	} else if n > 0 {
		log.Printf("cleanup: 终态任务修剪 %d 条（窗口 %d）", n, TerminalTasksKeep)
	}
	return errors.Join(errs...)
}

// lazyCleanupAfterTask 是任务终态钩子的落地（构造时装入 runner）：异步执行
// 一轮惰性清理，不阻塞终态响应；WithoutCancel 派生上下文——传入 ctx 已取消
// （取消收口路径）也不影响清理落库。清理失败只记日志，下轮触发续清。
func (a *App) lazyCleanupAfterTask(_ context.Context, t model.Task) {
	if a.deps.Cleanup == nil {
		return
	}
	go func() {
		if err := a.RunLazyCleanup(ctxWithoutCancel(context.Background())); err != nil {
			log.Printf("cleanup: 任务 %s 终态后清理失败（下轮续清）: %v", t.TaskID, err)
		}
	}()
}
