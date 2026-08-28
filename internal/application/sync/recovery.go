package sync

import (
	"context"
	"log"
)

// CodeScanInterrupted 标记进程重启/崩溃时被中断的任务。
const CodeScanInterrupted = "err.scan.interrupted"

// RecoverInterruptedTasks 在启动时把遗留的 queued/running 任务标记为 failed。
// 没有这步，进程中断（强杀/断电）会留下永远 running 的僵尸任务，
// 并因 StartScan 的「复用活动任务」语义永久锁死该 Relation 的扫描。
func (a *App) RecoverInterruptedTasks(ctx context.Context) error {
	actives, err := a.deps.Tasks.ListActiveAll(ctx)
	if err != nil {
		return err
	}
	for _, t := range actives {
		interrupted := t
		a.runner.MarkFailed(ctx, interrupted, CodeScanInterrupted, "进程重启时任务仍在进行，已标记为中断", t.RelationID)
		log.Printf("sync: 启动恢复：任务 %s（%s/%s）标记为中断", t.TaskID, t.Kind, t.Status)
	}
	return nil
}
