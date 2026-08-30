package sync

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// Phase 2 Apply 的计划确认用例（契约 05 §3.1；票 #36）。
// 本票后 Apply 任务存在但无 runner（runner 是 T04 引擎票）：ConfirmPlan 只落
// prepared 意图与 queued 任务，headless 断言止于 queued/prepared。

// Phase 2 新增错误码（契约 05 §6；文案由前端 locale 提供）。
const (
	// CodePlanExpired 是 resolved 计划过期后的调用级拆码（契约 05 §6：
	// 「expired 若施工时缺键随本票补」；P1 只以读取时投影 status=expired 呈现）。
	CodePlanExpired = "err.plan.expired"
	// CodePlanNoneReady 是 apply_sync availability 的「关系无可应用计划」原因码
	// （只出现在 ActionAvailabilityDTO.reason_code，不作为调用级错误抛出）。
	CodePlanNoneReady = "err.plan.none_ready"
	// CodePlanApplyNotReentrant 是同计划上一运行已 committed 后的重复确认
	// （args {0}=plan_id；引导重扫生成新计划）。
	CodePlanApplyNotReentrant = "err.plan.apply_not_reentrant"
)

// ConfirmPlan 确认 resolved 计划并创建 Apply 运行。单 RunInTx（ADR-0003
// doctrine），token/任务/run 三者同生共死、失败零残留；事件恒在提交后发布：
//
//  1. 读计划（err.plan.not_found）→ 校验 status=resolved 且非 stale/expired
//     （stale = 修订前进或端点重绑后绑定指纹失配，与 planViewWithStatus 同判）；
//  2. 幂等重入三分支（契约 05 §3.1 D4）：
//     本 plan 存在活跃 apply 运行（apply_runs.state ∉ 终局）→ 追加一条
//     plan_confirmations 记录（确认事件证据），任务/运行不新建，返回既有
//     TaskDTO——双击/双窗口安全；同 plan 上一运行已 committed →
//     err.plan.apply_not_reentrant（引导重扫生成新计划）；存在未收口恢复
//     （关系 health=recovery_required 或最近运行 recovery_required）→
//     err.recovery.in_progress；
//  3. 首次确认：confirmation_token 落 plan_confirmations + tasks(kind=apply,
//     status=queued) + apply_runs(state=prepared)——计划 digest、relation
//     revision、前置条件、恢复对象引用、操作数按 ADR-0004 §1 落列；
//  4. 提交后发布 task_updated（仅首次确认；重入未改变任务状态）。
//
// 本计划无运行但同关系其他计划的运行仍活跃（非终局）时，按 ADR-0004 §6
// 「同一 Relation 同时最多一个 Apply」拒绝（err.scan.already_running，与
// availability 活跃任务原因码一致）。
func (a *App) ConfirmPlan(ctx context.Context, input view.ConfirmPlanInput) (view.TaskView, error) {
	now := a.deps.Now().UTC()
	var (
		created model.Task // 首次确认新建的任务（提交后发布事件）
		reused  model.Task // 幂等重入返回的既有任务
	)
	err := a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		p, err := repos.Plans.Get(ctx, input.PlanID)
		if err != nil {
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

		// 未收口恢复禁新 Apply（ADR-0004 §4/§6）：关系已标记恢复，或最近运行处于恢复态
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
			default: // prepared/staged/applying/verifying：活跃 → 幂等重入（D4）
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

		// 首次确认：确认记录 + 任务 + 运行同一事务（契约 05 §3.1 语句序）
		nowStr := now.Format(time.RFC3339)
		if err := a.insertConfirmation(ctx, repos, p, now); err != nil {
			return err
		}
		t := model.Task{
			TaskID:      a.deps.IDs("task_"),
			RelationID:  p.RelationID,
			Kind:        model.TaskKindApply,
			Status:      model.TaskStatusQueued,
			Phase:       "pending",
			MessageKey:  "msg.task.apply.queued",
			MessageArgs: []string{},
			PlanID:      p.PlanID,
			CanCancel:   false, // 取消语义随 T04 runner 接管时收口，避免无 runner 半途态
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
			// prepared 意图尚无 staging 事实，恢复引用为空集；
			// 引擎进 staged 前落 CAS/staging 引用（ADR-0004 §3），仓储原样保存。
			RecoveryRefs:   json.RawMessage("[]"),
			OperationCount: len(p.Operations),
			CreatedAt:      nowStr,
			UpdatedAt:      nowStr,
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
			log.Printf("confirm: 发布 task_updated 失败（任务 %s 已创建）: %v", created.TaskID, err)
		}
		return TaskView(created), nil
	}
	return TaskView(reused), nil
}

// insertConfirmation 落一条计划确认记录。confirmation_token 是幂等键
// （plan_confirmations PK (plan_id, token)，每次确认生成新令牌）；有效期与
// 计划对齐，T04 引擎消费令牌时 MarkConsumed 的过期判定与计划失效语义一致。
// 确认项固化为计划确认要求快照（引擎定义形状的 JSON，仓储层原样保存）。
func (a *App) insertConfirmation(ctx context.Context, repos ports.Repos, p model.SyncPlan, confirmedAt time.Time) error {
	acks, err := json.Marshal(p.ConfirmationRequirements)
	if err != nil {
		return err
	}
	return repos.PlanConfirmations.Insert(ctx, model.PlanConfirmation{
		PlanID:            p.PlanID,
		PlanDigest:        p.PlanDigest,
		ConfirmationToken: a.deps.IDs("confirm_"),
		RelationRevision:  p.RelationRevision,
		Acknowledgements:  acks,
		ConfirmedAt:       confirmedAt.Format(time.RFC3339),
		ExpiresAt:         p.ExpiresAt,
	})
}

// applyRunTerminal 判定运行是否终局（committed/recovery_required，ADR-0004 §1：
// 两态在 ApplyRunTransitions 中无后继）。
func applyRunTerminal(state string) bool {
	return state == model.ApplyRunCommitted || state == model.ApplyRunRecoveryRequired
}

// aggregatePreconditions 汇总计划全部操作的前置条件为运行级集合（同资源同侧
// 去重，保持首次出现序）。ADR-0004 §1 要求 prepared 落列「全部前置条件」：
// 这里固化的是计划生成时的资源级预期（存在性/内容摘要）；快照对一致、baseline
// 未漂移由 T04 引擎在 staged 前复核、committed 事务内以完整复扫证明，不在此
// 重复建模——本集合是引擎复核的资源级权威清单。
func aggregatePreconditions(ops []model.PlannedOperation) []model.Precondition {
	seen := map[string]bool{}
	out := make([]model.Precondition, 0, len(ops))
	for _, op := range ops {
		for _, pre := range op.Preconditions {
			key := string(pre.ResourceID) + "|" + pre.Side
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, pre)
		}
	}
	return out
}
