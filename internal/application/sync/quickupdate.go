package sync

// 统一快速更新用例（契约 07 §3.1；票 #86）：快速更新从纯前端编排（契约 06 §4）
// 下沉为后端单一用例，手动入口与 watcher 自动链（ADR-0010 §2，T05 承接）共用
// 同一条链、同一个免确认口径。链 = 扫描 → 无差异短路 no_diff（不建计划）→
// PrepareSync（requested_exactness 恒 exact、双端快照输入内部取最新）→
// ResolvePlan（空决议，默认推荐生效）→ 停靠判定 → ConfirmPlan 或停待确认。
// 阻塞到链收口再返回（对 wire 是一次 Promise）；链内失败 AppError 透传零新码。

import (
	"context"
	"log/slog"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/diff"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// QuickUpdate 收口三态（契约 07 §3.1：no_diff|apply_started|awaiting_confirmation）。
const (
	QuickUpdateNoDiff               = "no_diff"
	QuickUpdateApplyStarted         = "apply_started"
	QuickUpdateAwaitingConfirmation = "awaiting_confirmation"
)

// quPollInterval 是链内等待扫描收口的轮询间隔（任务持久化状态是事实源；
// 后端等待替代被退役的前端 500ms 轮询，间隔收紧到扫描分相粒度）。
const quPollInterval = 50 * time.Millisecond

// quickUpdateCall 是同 relation 进行中的一次链（单飞 join 的共享收口结果）。
type quickUpdateCall struct {
	done chan struct{} // 收口时关闭，释放全部等待者
	res  view.QuickUpdateResultView
	err  error
}

// QuickUpdate 统一快速更新入口：同 relation 链进行中时并发调用等待并返回同一
// 结果（契约 07 §3.1.5，双击/双窗口安全，对齐 ConfirmPlan 幂等先例）；链外
// 其他来源活跃任务照常互斥（err.scan.already_running 透传），不绕任务互斥守卫。
func (a *App) QuickUpdate(ctx context.Context, input view.QuickUpdateInput) (view.QuickUpdateResultView, error) {
	a.quMu.Lock()
	if c, ok := a.quInflight[input.RelationID]; ok {
		a.quMu.Unlock()
		select {
		case <-c.done:
			return c.res, c.err
		case <-ctx.Done():
			return view.QuickUpdateResultView{}, ctx.Err()
		}
	}
	c := &quickUpdateCall{done: make(chan struct{})}
	a.quInflight[input.RelationID] = c
	a.quMu.Unlock()
	defer func() {
		a.quMu.Lock()
		delete(a.quInflight, input.RelationID)
		a.quMu.Unlock()
		close(c.done)
	}()

	res, err := a.runQuickUpdateChain(ctx, input.RelationID)
	c.res, c.err = res, err
	return res, err
}

// runQuickUpdateChain 执行链本体（同 relation 同时最多一条，见 QuickUpdate 单飞）。
func (a *App) runQuickUpdateChain(ctx context.Context, relationID string) (view.QuickUpdateResultView, error) {
	// 任务互斥守卫（ADR-0010 §5：自动链不绕过）：其他来源的活跃任务照常互斥，
	// err.scan.already_running 透传（与 quick_update availability 推导同判）。
	active, _, err := a.deps.Tasks.ListByRelation(ctx, relationID, true, ports.PageRequest{Limit: 1})
	if err != nil {
		return view.QuickUpdateResultView{}, err
	}
	if len(active) > 0 {
		return view.QuickUpdateResultView{}, errs.New(CodeRelationScanRunning, relationID)
	}

	// ① 扫描：察觉上游变更（守卫已排除其他来源活跃任务，StartScan 恒新建任务）
	scan, err := a.StartScan(ctx, relationID)
	if err != nil {
		return view.QuickUpdateResultView{}, err
	}
	if err := a.waitScanTerminal(ctx, scan.TaskID); err != nil {
		return view.QuickUpdateResultView{}, err
	}

	// ② 扫描收口后取最新事实（修订/授权开关/双端最新快照，用例内部取）
	rel, err := a.deps.Relations.Get(ctx, relationID)
	if err != nil {
		return view.QuickUpdateResultView{}, errs.New(CodeRelationNotFound, relationID)
	}
	snapP, okP, err := a.deps.Snapshots.LatestByRelationSide(ctx, relationID, model.SideProject)
	if err != nil {
		return view.QuickUpdateResultView{}, err
	}
	snapR, okR, err := a.deps.Snapshots.LatestByRelationSide(ctx, relationID, model.SideRuntime)
	if err != nil {
		return view.QuickUpdateResultView{}, err
	}
	if !okP || !okR {
		// 扫描成功终态后双端快照必然在库；防御分支沿既有码透传，零新码。
		return view.QuickUpdateResultView{}, errs.NewDetail(CodeScanAdapterFailed, "扫描收口后缺少双端最新快照", relationID)
	}

	// ③ 无差异短路（契约 07 §3.1.2）：no_diff，不建计划（对契约 06 前端编排的
	// 补口——今天空计划会走完全链）。判定与 GetWorkspace 的 diff_state=clean 同口径。
	actionable, err := a.diffHasActionableChange(ctx, rel, snapP, snapR)
	if err != nil {
		return view.QuickUpdateResultView{}, err
	}
	if !actionable {
		return view.QuickUpdateResultView{RelationID: relationID, Outcome: QuickUpdateNoDiff}, nil
	}

	// ④ PrepareSync：requested_exactness 恒 exact（沿今天前端硬编码），不设入参
	draft, err := a.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             relationID,
		RelationRevision:       rel.Revision,
		InputProjectSnapshotID: snapP.SnapshotID,
		InputRuntimeSnapshotID: snapR.SnapshotID,
		RequestedExactness:     string(model.ExactnessExact),
	})
	if err != nil {
		return view.QuickUpdateResultView{}, err
	}

	// ⑤ 停靠判定（契约 06 §4 同口径唯一，同一纯函数两段求值）：
	// 段一（draft 面）：draft 含冲突（无决议输入）→ 计划停留既有流。冲突永不
	// 自动（ADR-0005 §4）：含冲突 draft 不进 ResolvePlan（决议面缺失必被拒），
	// 转计划页由用户决议。授权位在此段恒传 true——授权子句要求 requirements
	// 事实（resolved 面），draft 面尚不存在，先隔离冲突子句单独判定。
	if quickUpdateDock(len(draft.Conflicts) > 0, false, true) == QuickUpdateAwaitingConfirmation {
		a.publishDockedInvalidation(ctx, relationID)
		return view.QuickUpdateResultView{
			RelationID: relationID, Outcome: QuickUpdateAwaitingConfirmation, PlanID: draft.PlanID,
		}, nil
	}

	// 无冲突 draft → 空决议推进 resolved（默认推荐生效；决议面随 #87 merge
	// 扩展 take_merged 默认推荐，本用例零改动）
	resolved, err := a.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: []model.Resolution{}})
	if err != nil {
		return view.QuickUpdateResultView{}, err
	}

	// 段二（resolved 面）：requirements 空 ∧ 授权开启 → ConfirmPlan →
	// apply_started（任务中心移交）；requirements 非空（删除/不可恢复等损失面
	// 要求人工确认）或授权关闭 → 停待确认（计划停留既有流）。
	if quickUpdateDock(false, len(resolved.ConfirmationRequirements) > 0, rel.AuthorizedApply) == QuickUpdateApplyStarted {
		task, err := a.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
		if err != nil {
			return view.QuickUpdateResultView{}, err
		}
		return view.QuickUpdateResultView{
			RelationID: relationID, Outcome: QuickUpdateApplyStarted,
			PlanID: resolved.PlanID, ApplyTaskID: task.TaskID,
		}, nil
	}
	a.publishDockedInvalidation(ctx, relationID)
	return view.QuickUpdateResultView{
		RelationID: relationID, Outcome: QuickUpdateAwaitingConfirmation, PlanID: resolved.PlanID,
	}, nil
}

// quickUpdateDock 是停靠判定的纯函数边界（判定函数按测试决议做表驱动全格覆盖）。
// 契约 06 §4 同口径唯一：draft 含冲突（无决议输入）∨ resolved
// confirmation_requirements 非空 ∨ 授权关闭 → awaiting_confirmation（计划停留
// 既有流）；requirements 空 ∧ 授权开启 → apply_started（ConfirmPlan 免确认直达）。
func quickUpdateDock(draftHasConflicts, requirementsNonEmpty, authorized bool) string {
	switch {
	case draftHasConflicts, requirementsNonEmpty, !authorized:
		return QuickUpdateAwaitingConfirmation
	default:
		return QuickUpdateApplyStarted
	}
}

// waitScanTerminal 阻塞等待扫描任务收口（契约 07 §3.1.1：阻塞到链收口再返回；
// 与被退役的前端轮询等价）。任务持久化状态是事实源；失败/取消透传任务自带
// Problem（err.scan.* 零新码），ctx 取消即中止等待。
func (a *App) waitScanTerminal(ctx context.Context, taskID string) error {
	for {
		t, err := a.deps.Tasks.Get(ctx, taskID)
		if err != nil {
			return err
		}
		switch t.Status {
		case model.TaskStatusSucceeded:
			return nil
		case model.TaskStatusFailed, model.TaskStatusRecoveryRequired:
			if t.Problem != nil {
				return &errs.AppError{Code: t.Problem.Code, Args: t.Problem.Args, Detail: t.Problem.Detail}
			}
			return errs.New(CodeScanAdapterFailed, taskID)
		case model.TaskStatusCancelled:
			return errs.NewDetail(CodeScanAdapterFailed, "扫描任务已取消", taskID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(quPollInterval):
		}
	}
}

// diffHasActionableChange 判定双端最新快照相对基线是否存在可行动差异（无差异
// 短路的判定边界）：与 GetWorkspace 的 diff_state=clean 同判——无冲突且全部
// diff 为 noop/converged 即无差异（adopt_equal 属初始化语境的可行动差异，走
// 计划链完成初始化）；direction=ignore 的资源与计划构建同口径剔除（票 #100，
// ADR-0013 §3），否则忽略资源恒 actionable、快速更新永不短路。
func (a *App) diffHasActionableChange(ctx context.Context, rel model.Relation, snapP, snapR model.ObservedSnapshot) (bool, error) {
	var base *model.SyncBaseline
	if rel.HeadBaselineID != "" {
		b, err := a.deps.Baselines.Get(ctx, rel.HeadBaselineID)
		if err != nil {
			return false, err
		}
		base = &b
	}
	result, err := diff.ThreeWay(diff.Input{RelationID: rel.RelationID, Base: base, Project: snapP, Runtime: snapR})
	if err != nil {
		return false, err
	}
	policySet, err := a.deps.Mappings.GetPolicy(ctx, rel.RelationID)
	if err != nil {
		return false, err
	}
	ignored := ignoreDirectionFilter(policySet, snapP, snapR)
	for _, c := range result.Conflicts {
		if !ignored(c.ResourceID) {
			return true, nil
		}
	}
	for _, d := range result.Diffs {
		if ignored(d.ResourceID) {
			continue
		}
		if d.Classification != diff.ClassNoop && d.Classification != diff.ClassConverged {
			return true, nil
		}
	}
	return false, nil
}

// publishDockedInvalidation 在用例停于 awaiting_confirmation 之后补发
// relation_invalidated（契约 07 §4 新发射点，Q2 竞态收口）：扫描提交的既有发射
// 在链中段，前端重查会扑空（计划尚未生成），此发收口时序；手动/自动入口都经
// 本用例停靠，故单一发射点覆盖两侧。发布失败只记日志（事件是通知不是事实源），
// 收口结果照常返回。
func (a *App) publishDockedInvalidation(ctx context.Context, relationID string) {
	if err := a.pub.PublishRelationInvalidated(ctx, relationID); err != nil {
		slog.Warn("quickupdate: 发布 relation_invalidated 失败（relation 已停靠待确认）", "relation", relationID, "err", err)
	}
}

// pendingPlanIDFor 投影最新一张待人工计划（契约 07 §3.2）：status ∈ {draft,
// resolved}（新状态行），读取时投影 stale/expired（planViewWithStatus 同判）、
// applied（契约 05 §5：committed 后不再待人工）与已被决议推进的祖先 draft
//（resolved_from 指向，决议后不再是待人工面）排除，按创建时间取最新
//（ListByRelation id 升序 = ULID 单调创建序）；无则空。系统通知去重依据与
// 前端「有待确认计划」角标数据源。
func (a *App) pendingPlanIDFor(ctx context.Context, rel model.Relation, proj model.Project, rt model.Runtime) (string, error) {
	plans, err := a.deps.Plans.ListByRelation(ctx, rel.RelationID)
	if err != nil {
		return "", err
	}
	resolvedFrom := make(map[string]bool, len(plans))
	for i := range plans {
		if from := plans[i].ResolvedFromPlanID; from != "" {
			resolvedFrom[from] = true
		}
	}
	now := a.deps.Now().UTC()
	for i := len(plans) - 1; i >= 0; i-- {
		p := plans[i]
		if p.Status != model.PlanDraft && p.Status != model.PlanResolved {
			continue
		}
		if expired(p.ExpiresAt, now) {
			continue
		}
		if p.RelationRevision != rel.Revision {
			continue
		}
		// 重绑不递增修订号（ADR-0002 决议 2），失效由绑定指纹校验承担
		if p.ExpectedBindings.Project != proj.BindingFingerprint ||
			p.ExpectedBindings.Runtime != rt.BindingFingerprint {
			continue
		}
		if resolvedFrom[p.PlanID] {
			continue
		}
		if p.Status == model.PlanResolved {
			if run, found, rerr := a.deps.ApplyRuns.LatestByPlan(ctx, p.PlanID); rerr == nil && found &&
				run.State == model.ApplyRunCommitted {
				continue
			}
		}
		return p.PlanID, nil
	}
	return "", nil
}
