package main

// pgheadless -apply（P2-T07，票 #40；验收规格 §1.1/§6.1）：-resolve 链路
// （Scan → PrepareSync → GetPlan）之后的 A 口径 Apply 主链路——
//
//	ResolvePlan（apply 决议策略）→ ConfirmPlan → 轮询 GetTask 至终态
//	→ 断言 GetApplyRun committed + ListCommits 含新记录 + 计划投影 applied
//	→ 收口后 diff 归零。
//
// 任一步不符即返回 error（main 非零退出）。两遍幂等由 Taskfile 同一数据目录
// 两次执行承担（沿 -resolve 先例：第一遍冷启动建 Relation，第二遍复用）：
// 第二遍复扫全 noop → 0 操作同步计划收口，提交头 kind=sync/exact/remaining=0。
// 本文件独立成文：main.go 之后 T09（-metrics apply 分相）还要改，保持其最小。

import (
	"context"
	"fmt"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

const (
	// apply 轮询参数（启动时打印；验收规格要求轮询口径可见可复现）。
	applyPollInterval = 100 * time.Millisecond
	applyPollTimeout  = 2 * time.Minute
)

// runApplyChain 执行 -apply 链路并完成全部断言。draft 是主链路 GetPlan 产出
// 的 draft 计划（冲突证据齐备）；rel 为当前 Relation。
func runApplyChain(ctx context.Context, app syncapp.Application, rel view.RelationView, draft view.SyncPlanView) error {
	chainStart := time.Now()
	fmt.Printf("== -apply == 轮询间隔 %v / 超时 %v\n", applyPollInterval, applyPollTimeout)

	// 1) ResolvePlan：冲突固化为 resolved 计划（ConfirmPlan 只接受 resolved）。
	resolutions := applyResolutions(draft.Conflicts)
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		return fmt.Errorf("ResolvePlan: %w", err)
	}
	fmt.Printf("== ResolvePlan(apply) == kind=%s status=%s 操作数=%d 冲突=%d\n",
		resolved.Kind, resolved.Status, len(resolved.Operations), len(resolved.Conflicts))

	// 2) ConfirmPlan：确认令牌/queued 任务/apply_runs(prepared) 单事务同生共死；
	//    提交后引擎协程接管任务。
	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	if err != nil {
		return fmt.Errorf("ConfirmPlan: %w", err)
	}
	fmt.Printf("== ConfirmPlan == task=%s status=%s\n", tv.TaskID, tv.Status)

	// 3) 轮询 GetTask 至终态（事件不是事实源，以查询 API 为准）。
	final, err := waitApplyTask(ctx, app, tv.TaskID)
	if err != nil {
		return err
	}
	if final.Status != model.TaskStatusSucceeded {
		dumpApplyFailure(ctx, app, rel.RelationID, final)
		return fmt.Errorf("apply 任务终态 %s（期望 succeeded）", final.Status)
	}
	fmt.Printf("== GetTask == status=%s outcome=%s progress=%d/%d commit=%s\n",
		final.Status, final.Outcome, final.Completed, final.Total, final.CommitID)

	// 4) 运行头断言：committed + 与任务收口一致 + 逐操作 verified。
	run, err := app.GetApplyRun(ctx, rel.RelationID)
	if err != nil {
		return fmt.Errorf("GetApplyRun: %w", err)
	}
	if run.State != model.ApplyRunCommitted {
		return fmt.Errorf("GetApplyRun state=%s，期望 committed", run.State)
	}
	if run.TaskID != tv.TaskID || run.CommitID != final.CommitID || run.PlanID != resolved.PlanID {
		return fmt.Errorf("运行头与任务收口不一致: run(task=%s commit=%s plan=%s) task(commit=%s)",
			run.TaskID, run.CommitID, run.PlanID, final.CommitID)
	}
	ops, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: rel.RelationID, TaskID: tv.TaskID, Limit: ports.MaxPageLimit,
	})
	if err != nil {
		return fmt.Errorf("ListApplyOperations: %w", err)
	}
	if len(ops.Items) != run.OperationCount {
		return fmt.Errorf("操作清单 %d 项 ≠ 运行头 operation_count=%d", len(ops.Items), run.OperationCount)
	}
	for _, op := range ops.Items {
		if op.Status != model.OperationStatusVerified {
			return fmt.Errorf("操作 %s（%s）status=%s，期望 verified", op.OperationID, op.RelativePath, op.Status)
		}
	}

	// 5) 历史断言：ListCommits 含新记录（created_at DESC 新提交在前），
	//    kind 与计划一致；noop 遍（有基线的 0 操作同步计划，即第二遍复用
	//    Relation 重扫重 apply）追加提交头 exact/剩余 0/零变化行口径。
	//    noop 判定以「基线存在 + 零操作」为准——initialize 计划全 skip 时
	//    同样 0 操作但 remaining 可非零（skip 冲突计入剩余差异），不作 noop。
	commits, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 10})
	if err != nil {
		return fmt.Errorf("ListCommits: %w", err)
	}
	if len(commits.Items) == 0 || commits.Items[0].CommitID != final.CommitID {
		return fmt.Errorf("ListCommits 首条不含新提交 %s（共 %d 条）", final.CommitID, len(commits.Items))
	}
	head := commits.Items[0]
	if head.Kind != resolved.Kind {
		return fmt.Errorf("提交 kind=%s 与计划 kind=%s 不一致", head.Kind, resolved.Kind)
	}
	isNoopPass := len(resolved.Operations) == 0 && resolved.BaseBaselineID != ""
	if isNoopPass {
		if head.Completeness != model.TaskOutcomeExact || head.RemainingChangeCnt != 0 {
			return fmt.Errorf("noop 提交头 completeness=%s remaining=%d，期望 exact/0",
				head.Completeness, head.RemainingChangeCnt)
		}
		detail, err := app.GetCommit(ctx, rel.RelationID, head.CommitID)
		if err != nil {
			return fmt.Errorf("GetCommit: %w", err)
		}
		if len(detail.Changes) != 0 {
			return fmt.Errorf("noop 提交变化行 %d 条，期望 0", len(detail.Changes))
		}
	}

	// 6) 计划投影 applied（契约 05 §5：committed 运行读取时投影，不写库）。
	got, err := app.GetPlan(ctx, resolved.PlanID)
	if err != nil {
		return fmt.Errorf("GetPlan: %w", err)
	}
	if got.Status != string(model.PlanApplied) {
		return fmt.Errorf("计划投影 status=%s，期望 applied", got.Status)
	}

	// 7) 收口后 diff 归零：committed 以完整复扫一致为前提，基线与最新快照对
	//    （验证复扫产物）读时计算应无任何待同步/冲突计数。
	changes, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID})
	if err != nil {
		return fmt.Errorf("GetChanges: %w", err)
	}
	if s := changes.Summary; s.CreateCount != 0 || s.ModifyCount != 0 || s.DeleteCount != 0 ||
		s.ConflictCount != 0 || s.InitChoiceCount != 0 {
		return fmt.Errorf("收口后 diff 未归零: create=%d modify=%d delete=%d conflict=%d init_choice=%d",
			s.CreateCount, s.ModifyCount, s.DeleteCount, s.ConflictCount, s.InitChoiceCount)
	}

	// 可读摘要（阶段/操作数/耗时；stdout 直出，不落临时路径）。分相计时经
	// LastApplyTiming 类型断言读取（T04 暴露口，不入 transport 契约）。
	timing := lastApplyTiming(app)
	fmt.Printf("== apply 摘要 == commit=%s kind=%s completeness=%s 操作数=%d 剩余差异=%d\n"+
		"  分相耗时 staging=%dms applying=%dms verifying=%dms total=%dms\n"+
		"  链路耗时=%v\n",
		head.CommitID, head.Kind, head.Completeness, run.OperationCount, head.RemainingChangeCnt,
		timing.StagingMs, timing.ApplyingMs, timing.VerifyingMs, timing.TotalMs,
		time.Since(chainStart).Round(time.Millisecond))
	return nil
}

// applyResolutions 为 -apply 的冲突生成固定决议：mod 资源一律 skip（T04 划线：
// P2 无下载器，mod copy 物化仅当目标已达声明 digest 或 CAS 恰有内容，验收
// fixture 的 jar 不满足——skip 即显式保留差异，verify 对 skip 豁免漂移断言）；
// 其余沿 -resolve 的 defaultResolutions（initialize 项目侧优先、modify 类
// take_project）。不可裁决冲突仍 fatal 带证据。
func applyResolutions(conflicts []model.Conflict) []model.Resolution {
	res := make([]model.Resolution, 0, len(conflicts))
	rest := make([]model.Conflict, 0, len(conflicts))
	for _, c := range conflicts {
		if normalize.KindOfResourceID(c.ResourceID) == model.ResourceMod {
			fmt.Printf("  [resolve] %s → skip（P2 划线：mod 无下载器，copy 物化不可用）\n", c.ResourceID)
			res = append(res, model.Resolution{ResourceID: c.ResourceID, Choice: model.ChoiceSkip})
			continue
		}
		rest = append(rest, c)
	}
	res = append(res, defaultResolutions(rest)...)
	return res
}

// waitApplyTask 轮询 GetTask 至任一终态（recovery_required/failed/cancelled 是
// 合法终态，交由调用方按非成功收口，不给假绿）。相位变化即打印一行进度。
func waitApplyTask(ctx context.Context, app syncapp.Application, taskID string) (view.TaskView, error) {
	deadline := time.Now().Add(applyPollTimeout)
	lastPhase := ""
	for {
		tv, err := app.GetTask(ctx, taskID)
		if err != nil {
			return view.TaskView{}, fmt.Errorf("GetTask: %w", err)
		}
		if tv.Phase != lastPhase {
			fmt.Printf("  [poll] status=%s phase=%s progress=%d/%d\n", tv.Status, tv.Phase, tv.Completed, tv.Total)
			lastPhase = tv.Phase
		}
		switch tv.Status {
		case model.TaskStatusSucceeded, model.TaskStatusFailed,
			model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
			return tv, nil
		}
		if time.Now().After(deadline) {
			return view.TaskView{}, fmt.Errorf("apply 任务超时未至终态（task=%s，超时 %v）", taskID, applyPollTimeout)
		}
		time.Sleep(applyPollInterval)
	}
}

// dumpApplyFailure 打印失败收口证据：运行头状态 + 逐操作结果码摘要。
// 只用白名单投影（无临时路径/ownership proof——契约 05 硬约束 4），stdout
// 直出不落临时文件。
func dumpApplyFailure(ctx context.Context, app syncapp.Application, relationID string, final view.TaskView) {
	fmt.Printf("== apply 失败收口 == task=%s status=%s phase=%s problem=%s\n",
		final.TaskID, final.Status, final.Phase, problemText(final.Problem))
	run, err := app.GetApplyRun(ctx, relationID)
	if err != nil {
		fmt.Printf("  GetApplyRun 失败: %v\n", err)
		return
	}
	fmt.Printf("  run state=%s operation_count=%d staging_cleared=%v commit=%q acknowledged_at=%q\n",
		run.State, run.OperationCount, run.StagingCleared, run.CommitID, run.AcknowledgedAt)
	ops, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: relationID, TaskID: final.TaskID, Limit: ports.MaxPageLimit,
	})
	if err != nil {
		fmt.Printf("  ListApplyOperations 失败: %v\n", err)
		return
	}
	for _, op := range ops.Items {
		fmt.Printf("  op %03d change=%s status=%-10s result=%s resource=%s\n",
			op.Ordinal, op.ChangeKind, op.Status, op.ResultCode, op.ResourceID)
	}
}

// problemText 把任务 Problem 压成单行（code + args；detail 只在非空时附带）。
func problemText(p *view.ProblemView) string {
	if p == nil {
		return "-"
	}
	s := p.Code
	if len(p.Args) > 0 {
		s += fmt.Sprintf("%v", p.Args)
	}
	if p.Detail != "" {
		s += " detail=" + p.Detail
	}
	return s
}

// lastApplyTiming 经非导出能力读取 Apply 分相计时（LastScanTiming 类型断言
// 先例；不入 transport 契约）。
func lastApplyTiming(app syncapp.Application) view.ApplyTimingView {
	type timingSource interface{ LastApplyTiming() view.ApplyTimingView }
	ts, ok := app.(timingSource)
	if !ok {
		return view.ApplyTimingView{}
	}
	return ts.LastApplyTiming()
}
