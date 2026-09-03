package sync

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// 回滚确认用例（契约 06 §3.4；票 #60）：ConfirmRestorePlan 是 restore 计划的
// 唯一确认入口——确认即建 tasks(kind=restore, queued) + apply_runs(prepared)，
// 运行事实全在 apply_runs（ApplyRestore 不上 wire，Q1）；执行归 restore_apply.go
// 的引擎协程。幂等口径对齐 P2 ConfirmPlan（confirm.go），failed 终局可重入是
// restore 语义的增量（Q8：staging 下载失败终局可同 plan 重试）。

// ConfirmRestorePlan 确认 resolved 回滚计划并创建 restore 运行。单 RunInTx
// （ADR-0003 doctrine），确认记录/任务/run 三者同生共死；事件恒在提交后发布：
//
//  1. 读计划（err.plan.not_found，跨类计划同口径不泄露形状）→ 校验
//     status=resolved 且非 stale/expired（stale/expired 既有码，与 ConfirmPlan
//     同判：修订前进或绑定指纹失配）；
//  2. 幂等重入（契约 06 §3.4.2）：本 plan 存在活跃 restore 运行
//     （apply_runs.state ∉ 终局）→ 追加确认记录，返回既有 TaskDTO（双击/双窗口
//     安全）；存在未收口恢复（关系 recovery_required 或运行恢复态）→
//     err.recovery.in_progress；
//  3. failed 可重入（契约 06 §3.4.3，Q8）：同 plan 上一运行 state=failed
//     （staging 下载/物化失败终局，网络失败 ≠ 恢复门）→ 允许再次确认、建
//     新任务新运行；上一运行已 committed → err.plan.apply_not_reentrant
//     （引导重新 prepare）；
//  4. 首次确认：insertConfirmation（确认要求快照含恒非空的
//     restore_acknowledge）+ tasks(kind=restore, queued) + apply_runs(prepared)；
//  5. 提交后发布 task_updated 并启动引擎协程（startRestore）。
//
// 本计划无运行但同关系其他计划的运行仍活跃（非终局）时，按 ADR-0004 §6/§8
// 「同一 Relation 同时最多一个 Apply/Restore，Scan/Apply/Restore 互斥」拒绝
//（err.scan.already_running）。
func (a *App) ConfirmRestorePlan(ctx context.Context, input view.ConfirmRestorePlanInput) (view.TaskView, error) {
	now := a.deps.Now().UTC()
	var (
		created model.Task // 首次确认新建的任务（提交后发布事件并启动引擎）
		reused  model.Task // 幂等重入返回的既有任务
	)
	err := a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		p, err := repos.Plans.Get(ctx, input.PlanID)
		if err != nil {
			return errs.New(CodePlanNotFound, input.PlanID)
		}
		// 计划类别门禁（与 ConfirmPlan 的 apply 侧门禁对称）：本方法是 restore
		// 计划的确认入口；sync/initialize 计划不得经此建 restore 运行。
		if p.Kind != model.PlanRestore {
			return errs.New(CodePlanNotFound, input.PlanID)
		}
		if p.Status != model.PlanResolved {
			return errs.New(CodePlanStale, input.PlanID)
		}
		if expired(p.ExpiresAt, now) {
			return errs.New(CodePlanExpired, input.PlanID)
		}
		rel, err := repos.Relations.Get(ctx, p.RelationID)
		if err != nil {
			return errs.New(CodeRelationNotFound, p.RelationID)
		}
		if rel.Revision != p.RelationRevision {
			return errs.New(CodePlanStale, input.PlanID)
		}
		proj, err := repos.Endpoints.GetProject(ctx, rel.ProjectID)
		if err != nil {
			return err
		}
		rt, err := repos.Endpoints.GetRuntime(ctx, rel.RuntimeID)
		if err != nil {
			return err
		}
		// 重绑不递增修订号（ADR-0002 决议 2），旧计划失效由绑定指纹校验承担
		if p.ExpectedBindings.Project != proj.BindingFingerprint ||
			p.ExpectedBindings.Runtime != rt.BindingFingerprint {
			return errs.New(CodePlanStale, input.PlanID)
		}

		// 未收口恢复禁新回滚（ADR-0006 §8：恢复所需期间 restore 与 apply 同门禁）
		if rel.Health == model.HealthRecoveryRequired {
			return errs.New(CodeRecoveryInProgress)
		}
		planRun, found, err := repos.ApplyRuns.LatestByPlan(ctx, p.PlanID)
		if err != nil {
			return err
		}
		if found {
			switch {
			case planRun.State == model.ApplyRunRecoveryRequired:
				return errs.New(CodeRecoveryInProgress)
			case planRun.State == model.ApplyRunCommitted:
				return errs.New(CodePlanApplyNotReentrant, p.PlanID)
			case planRun.State == model.ApplyRunFailed:
				// failed 终局可重入（契约 06 Q8）：staging 相位下载/物化失败的
				// 网络面终局不是恢复门——重试 = 同 plan 重新确认，新建任务与
				// 运行（暂存锚上的用户补全字节跨运行延续）。
			default: // prepared/staged/applying/verifying：活跃 → 幂等重入
				if err := a.insertConfirmation(ctx, repos, p, now); err != nil {
					return err
				}
				t, err := repos.Tasks.Get(ctx, planRun.TaskID)
				if err != nil {
					return err
				}
				reused = t
				return nil
			}
		}
		relRun, found, err := repos.ApplyRuns.LatestByRelation(ctx, p.RelationID)
		if err != nil {
			return err
		}
		if found && !applyRunTerminal(relRun.State) {
			if relRun.State == model.ApplyRunRecoveryRequired {
				return errs.New(CodeRecoveryInProgress)
			}
			return errs.New(CodeRelationScanRunning, p.RelationID)
		}

		// 首次确认：确认记录 + 任务 + 运行同一事务（ConfirmPlan 语句序同构）
		nowStr := now.Format(time.RFC3339)
		if err := a.insertConfirmation(ctx, repos, p, now); err != nil {
			return err
		}
		t := model.Task{
			TaskID:      a.deps.IDs("task_"),
			RelationID:  p.RelationID,
			Kind:        model.TaskKindRestore,
			Status:      model.TaskStatusQueued,
			Phase:       "pending",
			MessageKey:  "msg.task.restore.queued",
			MessageArgs: []string{},
			PlanID:      p.PlanID,
			CanCancel:   false, // 引擎接管（startRestore）置 true，避免 queued 窗口半途态
			CreatedAt:   nowStr,
			UpdatedAt:   nowStr,
		}
		if err := repos.Tasks.Insert(ctx, t); err != nil {
			return err
		}
		if err := repos.ApplyRuns.Insert(ctx, model.ApplyRun{
			TaskID:           t.TaskID,
			RelationID:       p.RelationID,
			PlanID:           p.PlanID,
			PlanDigest:       p.PlanDigest,
			RelationRevision: p.RelationRevision,
			State:            model.ApplyRunPrepared,
			Preconditions:    aggregatePreconditions(p.Operations),
			RecoveryRefs:     json.RawMessage("[]"),
			OperationCount:   len(p.Operations),
			CreatedAt:        nowStr,
			UpdatedAt:        nowStr,
		}); err != nil {
			return err
		}
		created = t
		return nil
	})
	if err != nil {
		return view.TaskView{}, err
	}
	if created.TaskID != "" {
		// 事件发布恒在事务提交之后（ADR-0004 §6）；发布失败不影响已提交事实
		if err := a.pub.PublishTask(ctx, created); err != nil {
			slog.Warn("confirm_restore: 发布 task_updated 失败（任务已创建）", "task", created.TaskID, "err", err)
		}
		a.startRestore(created)
		return TaskView(created), nil
	}
	return TaskView(reused), nil
}
