package sync

import (
	"context"
	"log"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// CodeScanInterrupted 标记进程重启/崩溃时被中断的非 Apply 任务（scan 等无
// journal 事实源的任务沿用 P1 口径收口）。
const CodeScanInterrupted = "err.scan.interrupted"

// CodeRecoveryNotRequired 是 AcknowledgeRecovery 于非 recovery_required 运行的
// 拆码（契约 05 §6，args {0}=task_id）。
const CodeRecoveryNotRequired = "err.recovery.not_required"

// RecoverInterruptedTasks 启动恢复入口（ADR-0004 §4 恢复协议，票 #38）：
//   - apply 任务 → journal 驱动的恢复管线（recoveryPipeline：probe 四路裁决，
//     自动路径收口或保持 recovery_required 等人工确认）；
//   - 其余任务（scan 等，无 journal 事实源）→ 沿 P1 占位口径标记中断。没有这步，
//     进程中断会留下永远 running 的僵尸任务，并因 StartScan 的「复用活动任务」
//     语义永久锁死该 Relation 的扫描。
//
// 幂等：恢复完成后运行必为终态（committed / recovery_required），重复调用
// 不再触碰文件系统——不重复补偿、不重复删除、不重复重做。
func (a *App) RecoverInterruptedTasks(ctx context.Context) error {
	actives, err := a.deps.Tasks.ListActiveAll(ctx)
	if err != nil {
		return err
	}
	for _, t := range actives {
		if t.Kind != model.TaskKindApply {
			interrupted := t
			a.runner.MarkFailed(ctx, interrupted, CodeScanInterrupted, "进程重启时任务仍在进行，已标记为中断", t.RelationID)
			log.Printf("recovery: 任务 %s（%s）标记为中断", t.TaskID, t.Kind)
			continue
		}
		a.recoverApplyTask(ctx, t)
	}
	return nil
}

// recoverApplyTask 处置单个遗留的 apply 任务。
func (a *App) recoverApplyTask(ctx context.Context, active model.Task) {
	run, err := a.deps.ApplyRuns.Get(ctx, active.TaskID)
	if err != nil {
		// 运行头缺失（理论不可达：ConfirmPlan 同事务建任务与运行头）——按 P1
		// 口径收口任务，不留僵尸。
		a.runner.MarkFailed(ctx, active, CodeScanInterrupted, "进程重启时任务仍在进行，已标记为中断", active.RelationID)
		log.Printf("recovery: 任务 %s 无运行头，按中断收口: %v", active.TaskID, err)
		return
	}
	rel, err := a.deps.Relations.Get(ctx, run.RelationID)
	if err != nil {
		log.Printf("recovery: 运行 %s 的关系 %s 不可读，跳过: %v", run.TaskID, run.RelationID, err)
		return
	}
	// 同 Relation 单 Apply（ADR-0004 §6）：与引擎/扫描共用同一把关系锁。
	gate := a.relationGate(rel.RelationID)
	gate.Lock()
	defer gate.Unlock()

	if applyRunTerminal(run.State) {
		a.reconcileTerminalRun(ctx, active, run)
		return
	}
	// 非终态运行：ADR-0004 §4 先将 Relation 标 recovery_required 并禁止新 Apply
	// （availability 推导 + ConfirmPlan 前置已在 T03/T04 挂钩，这里补落库事实），
	// 再进入 probe 四路裁决。
	if err := a.deps.Relations.UpdateHealth(ctx, rel.RelationID, model.HealthRecoveryRequired); err != nil {
		log.Printf("recovery: 关系 %s 标记恢复态失败: %v", rel.RelationID, err)
	}
	a.recoveryPipeline(ctx, active, run, rel)
}

// reconcileTerminalRun 补齐终态运行的簿记（崩溃可能截断引擎的 best-effort 落库）：
//   - committed：提交事务已成功而任务终态未落（提交事务与任务更新非原子）——
//     从提交事实重建任务成功投影（completeness 读提交行，不臆造）；
//   - recovery_required：T04 recoverApply 的 run 终态先于关系健康/任务终态落库，
//     崩溃窗口内可能缺后两笔——幂等补齐恢复门与任务终态。两种终态都不再裁决，
//     重复恢复不产生任何文件动作。
func (a *App) reconcileTerminalRun(ctx context.Context, active model.Task, run model.ApplyRun) {
	if run.State == model.ApplyRunCommitted {
		if active.Status != model.TaskStatusQueued && active.Status != model.TaskStatusRunning {
			return
		}
		outcome := model.TaskOutcomeExact
		if run.CommitID != "" {
			if c, err := a.deps.Commits.GetForRelation(ctx, run.CommitID, run.RelationID); err == nil &&
				c.Completeness == model.TaskOutcomePartial {
				outcome = model.TaskOutcomePartial
			}
		}
		active.Status = model.TaskStatusSucceeded
		active.Phase = "done"
		active.MessageKey = "msg.task.apply.succeeded"
		active.Completed = run.OperationCount
		active.Total = run.OperationCount
		active.CommitID = run.CommitID
		active.Outcome = outcome
		if _, err := a.runner.Update(ctx, active); err != nil {
			log.Printf("recovery: 任务 %s 成功终态重建失败: %v", active.TaskID, err)
			return
		}
		_ = a.pub.PublishRelationInvalidated(ctx, run.RelationID)
		log.Printf("recovery: 运行 %s 已 committed，任务成功投影重建完成", run.TaskID)
		return
	}
	// recovery_required 终态：恢复门与任务终态幂等补齐；不做任何裁决与文件动作。
	if err := a.deps.Relations.UpdateHealth(ctx, run.RelationID, model.HealthRecoveryRequired); err != nil {
		log.Printf("recovery: 关系 %s 标记恢复态失败: %v", run.RelationID, err)
	}
	if active.Status == model.TaskStatusQueued || active.Status == model.TaskStatusRunning {
		active.Status = model.TaskStatusRecoveryRequired
		active.MessageKey = "msg.task.apply.recovery_required"
		active.Problem = &model.Problem{Code: CodeRecoveryInProgress, Detail: "进程中断时运行已处于恢复态"}
		if _, err := a.runner.Update(ctx, active); err != nil {
			log.Printf("recovery: 任务 %s 恢复终态落库失败: %v", active.TaskID, err)
		}
	}
}

// AcknowledgeRecovery 人工确认恢复收口（契约 05 §3.4，单 RunInTx，ADR-0003
// doctrine；事件恒在提交后）：
//
//   - 前置：apply_runs.state=recovery_required，否则 err.recovery.not_required
//     （运行不存在同口径——该任务没有待确认的恢复）；
//   - 已 acknowledged → 幂等返回当前工作区投影，不报错、不再发布事件；
//   - 效果：acknowledged_at=now（仓储 COALESCE 保留首次）+ relation.health=healthy；
//     头基线不动、不建 SyncCommit（ADR-0004 §5：恢复路径不推进 Baseline）；
//   - 提交后发布 relation_invalidated（契约 05 §4 恢复收口发射点二，引导重扫）。
func (a *App) AcknowledgeRecovery(ctx context.Context, taskID string) (view.WorkspaceView, error) {
	var (
		relationID  string
		alreadyAckg bool
	)
	err := a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		run, err := repos.ApplyRuns.Get(ctx, taskID)
		if err != nil {
			return errs.New(CodeRecoveryNotRequired, taskID)
		}
		if run.State != model.ApplyRunRecoveryRequired {
			return errs.New(CodeRecoveryNotRequired, taskID)
		}
		relationID = run.RelationID
		if run.AcknowledgedAt != "" {
			alreadyAckg = true // 幂等重入：事实已落库，直接返回当前投影
			return nil
		}
		if err := repos.ApplyRuns.MarkAcknowledged(ctx, taskID, a.nowStr(), a.nowStr()); err != nil {
			return err
		}
		if err := repos.Relations.UpdateHealth(ctx, run.RelationID, model.HealthHealthy); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return view.WorkspaceView{}, err
	}
	if !alreadyAckg {
		// 事件只在 SQLite 事务提交后发布（ADR-0004 §6）；发布失败不影响已提交事实
		_ = a.pub.PublishRelationInvalidated(ctx, relationID)
		log.Printf("recovery: 运行 %s 恢复已人工确认，关系 %s 复位 healthy（头基线不动，引导重扫）", taskID, relationID)
		// 恢复处置收口=安全窗口复查事件（票 #64，ADR-0007 §3）：唤醒排队中的
		// GC 任务自动续排。
		a.kickGC()
	}
	return a.GetWorkspace(ctx, relationID)
}
