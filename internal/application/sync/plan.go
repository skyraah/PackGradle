package sync

import (
	"context"
	"errors"
	"strconv"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/task"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/core/plan"
	"packgradle/internal/errs"
)

// planTTL 是计划有效期；过期/修订变化均为读取时投影，不写库。
var planTTL = 15 * time.Minute

// CodeSyncInvalidExactness 是 requested_exactness 不在合法枚举（exact|allow_partial）。
// 契约 03 §3 增补（T11，票 #21）。
const CodeSyncInvalidExactness = "err.sync.invalid_exactness"

// PrepareSync 基于已完成的双端快照生成不可变 draft plan。
// 只接受已持久化的 snapshot ID 与 Relation revision，不隐式启动扫描。
// RequestedExactness 空值缺省 allow_partial（保守），固化进计划并随 ResolvePlan 继承。
func (a *App) PrepareSync(ctx context.Context, input view.PrepareSyncInput) (view.SyncPlanView, error) {
	ex := model.Exactness(input.RequestedExactness)
	if ex == "" {
		ex = model.ExactnessAllowPartial
	}
	if ex != model.ExactnessExact && ex != model.ExactnessAllowPartial {
		return view.SyncPlanView{}, errs.New(CodeSyncInvalidExactness, input.RequestedExactness)
	}
	input.RequestedExactness = string(ex)
	rel, err := a.deps.Relations.Get(ctx, input.RelationID)
	if err != nil {
		return view.SyncPlanView{}, errs.New(CodeRelationNotFound, input.RelationID)
	}
	if rel.Revision != input.RelationRevision {
		return view.SyncPlanView{}, errs.New(CodeSyncRevisionMismatch,
			input.RelationID, strconv.Itoa(rel.Revision), strconv.Itoa(input.RelationRevision))
	}
	snapP, err := a.deps.Snapshots.GetForRelation(ctx, input.InputProjectSnapshotID, input.RelationID, model.SideProject)
	if err != nil {
		return view.SyncPlanView{}, errs.New(CodeSyncSnapshotNotFound, input.InputProjectSnapshotID)
	}
	snapR, err := a.deps.Snapshots.GetForRelation(ctx, input.InputRuntimeSnapshotID, input.RelationID, model.SideRuntime)
	if err != nil {
		return view.SyncPlanView{}, errs.New(CodeSyncSnapshotNotFound, input.InputRuntimeSnapshotID)
	}

	pol, err := a.deps.Mappings.GetPolicy(ctx, input.RelationID)
	if err != nil {
		return view.SyncPlanView{}, err
	}
	polDigest, err := normalize.PolicyDigest(pol)
	if err != nil {
		return view.SyncPlanView{}, err
	}

	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		return view.SyncPlanView{}, err
	}
	rt, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		return view.SyncPlanView{}, err
	}

	kind := model.PlanSync
	var base *model.SyncBaseline
	var baseDigest string
	if rel.HeadBaselineID != "" {
		b, berr := a.deps.Baselines.Get(ctx, rel.HeadBaselineID)
		if berr != nil {
			return view.SyncPlanView{}, berr
		}
		base = &b
		baseDigest = b.BaselineDigest
	} else {
		kind = model.PlanInitialize
	}

	draft, err := plan.BuildDraft(plan.BuildInput{
		RelationID:         input.RelationID,
		RelationRevision:   rel.Revision,
		Policy:             pol,
		PolicyDigest:       polDigest,
		Kind:               kind,
		Base:               base,
		BaseBaselineDigest: baseDigest,
		Project:            snapP,
		Runtime:            snapR,
		ExpectedBindings:   model.ExpectedBindings{Project: proj.BindingFingerprint, Runtime: rt.BindingFingerprint},
		RequestedExactness: model.Exactness(input.RequestedExactness),
		ExpiresAt:          a.deps.Now().UTC().Add(planTTL),
	})
	if err != nil {
		return view.SyncPlanView{}, err
	}
	draft.PlanID = a.deps.IDs("plan_")
	draft.Status = model.PlanDraft
	if err := a.deps.Plans.Insert(ctx, draft); err != nil {
		return view.SyncPlanView{}, err
	}
	return a.planViewWithStatus(ctx, draft, rel)
}

// ResolvePlan 将冲突选择固化为新的不可变 resolved plan（旧 plan 不修改）。
func (a *App) ResolvePlan(ctx context.Context, input view.ResolvePlanInput) (view.SyncPlanView, error) {
	draft, err := a.deps.Plans.Get(ctx, input.PlanID)
	if err != nil {
		return view.SyncPlanView{}, errs.New(CodePlanNotFound, input.PlanID)
	}
	// 计划类别门禁（票 #59）：本方法是 sync/initialize 计划的决议入口；restore
	// 计划归 ResolveRestorePlan。跨类计划按 not_found 同一口径，不泄露形状。
	if draft.Kind != model.PlanSync && draft.Kind != model.PlanInitialize {
		return view.SyncPlanView{}, errs.New(CodePlanNotFound, input.PlanID)
	}
	if draft.Status != model.PlanDraft {
		return view.SyncPlanView{}, errs.New(CodePlanStale, input.PlanID)
	}
	if expired(draft.ExpiresAt, a.deps.Now().UTC()) {
		return view.SyncPlanView{}, errs.New(CodePlanStale, input.PlanID)
	}
	rel, err := a.deps.Relations.Get(ctx, draft.RelationID)
	if err != nil {
		return view.SyncPlanView{}, errs.New(CodeRelationNotFound, draft.RelationID)
	}
	if rel.Revision != draft.RelationRevision {
		return view.SyncPlanView{}, errs.New(CodePlanStale, input.PlanID)
	}
	snapP, err := a.deps.Snapshots.GetForRelation(ctx, draft.InputProjectSnapshotID, draft.RelationID, model.SideProject)
	if err != nil {
		return view.SyncPlanView{}, errs.New(CodeSyncSnapshotNotFound, draft.InputProjectSnapshotID)
	}
	snapR, err := a.deps.Snapshots.GetForRelation(ctx, draft.InputRuntimeSnapshotID, draft.RelationID, model.SideRuntime)
	if err != nil {
		return view.SyncPlanView{}, errs.New(CodeSyncSnapshotNotFound, draft.InputRuntimeSnapshotID)
	}

	resolved, err := plan.Resolve(draft, snapP, snapR, input.Resolutions)
	if err != nil {
		if errors.Is(err, plan.ErrResolutionIncomplete) || errors.Is(err, plan.ErrResolutionUnknown) ||
			errors.Is(err, plan.ErrResolutionInvalidChoice) {
			return view.SyncPlanView{}, errs.NewDetail(CodePlanResolutionInvalid, err.Error(), input.PlanID)
		}
		return view.SyncPlanView{}, err
	}
	resolved.PlanID = a.deps.IDs("plan_")
	resolved.Status = model.PlanResolved
	resolved.ExpiresAt = a.deps.Now().UTC().Add(planTTL).Format(time.RFC3339)
	if err := a.deps.Plans.Insert(ctx, resolved); err != nil {
		return view.SyncPlanView{}, err
	}
	return a.planViewWithStatus(ctx, resolved, rel)
}

// GetPlan 查询计划；stale/expired 为读取时投影，不写库。
func (a *App) GetPlan(ctx context.Context, planID string) (view.SyncPlanView, error) {
	p, err := a.deps.Plans.Get(ctx, planID)
	if err != nil {
		return view.SyncPlanView{}, errs.New(CodePlanNotFound, planID)
	}
	rel, err := a.deps.Relations.Get(ctx, p.RelationID)
	if err != nil {
		// 关系被删除的场景：仍可返回计划本体，状态按过期处理
		return PlanView(p), nil
	}
	return a.planViewWithStatus(ctx, p, rel)
}

func (a *App) planViewWithStatus(ctx context.Context, p model.SyncPlan, rel model.Relation) (view.SyncPlanView, error) {
	effective := p.Status
	if p.Status == model.PlanResolved {
		// committed 后计划投影 status=applied（契约 05 §5）：读取时推导不写库。
		// committed 是运行终态且 ConfirmPlan 拒绝已应用计划重入，最新运行即判定。
		if run, found, err := a.deps.ApplyRuns.LatestByPlan(ctx, p.PlanID); err == nil && found &&
			run.State == model.ApplyRunCommitted {
			v := PlanView(p)
			v.Status = string(model.PlanApplied)
			return v, nil
		}
	}
	if (p.Status == model.PlanDraft || p.Status == model.PlanResolved) && expired(p.ExpiresAt, a.deps.Now().UTC()) {
		effective = model.PlanExpired
	} else if p.Status == model.PlanDraft || p.Status == model.PlanResolved {
		switch {
		case rel.Revision != p.RelationRevision:
			effective = model.PlanStale
		case a.bindingsMismatch(ctx, p, rel):
			// 重绑不递增修订号（ADR-0002 决议 2），旧计划失效由绑定指纹校验承担
			// （契约 03 §2.4：GetPlan 读取时 binding 不匹配 → status=stale）
			effective = model.PlanStale
		}
	}
	v := PlanView(p)
	v.Status = string(effective)
	return v, nil
}

// bindingsMismatch 判断计划锁定的两端绑定指纹与当前端点登记是否失配。
// 端点读取失败不作 stale 判定（关系常态失效由 revision 校验覆盖，端点行不可删除）。
func (a *App) bindingsMismatch(ctx context.Context, p model.SyncPlan, rel model.Relation) bool {
	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		return false
	}
	rt, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		return false
	}
	return p.ExpectedBindings.Project != proj.BindingFingerprint ||
		p.ExpectedBindings.Runtime != rt.BindingFingerprint
}

func expired(rfc3339 string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, rfc3339)
	return err != nil || !now.Before(t)
}

// GetTask 查询任务。
func (a *App) GetTask(ctx context.Context, taskID string) (view.TaskView, error) {
	t, err := a.deps.Tasks.Get(ctx, taskID)
	if err != nil {
		return view.TaskView{}, errs.New(CodeTaskNotFound, taskID)
	}
	return TaskView(t), nil
}

// ListTasks 查询任务列表（active=true 仅活动任务）。
func (a *App) ListTasks(ctx context.Context, relationID string, active bool, page ports.PageRequest) (view.TaskPage, error) {
	items, next, err := a.deps.Tasks.ListByRelation(ctx, relationID, active, page)
	if err != nil {
		return view.TaskPage{}, err
	}
	out := view.TaskPage{Items: make([]view.TaskView, 0, len(items)), NextCursor: next}
	for _, t := range items {
		out.Items = append(out.Items, TaskView(t))
	}
	return out, nil
}

// CancelTask 取消可取消的活动任务。
func (a *App) CancelTask(ctx context.Context, taskID string) error {
	err := a.runner.Cancel(ctx, taskID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ports.ErrNotFound):
		return errs.New(CodeTaskNotFound, taskID)
	case errors.Is(err, task.ErrTaskNotFound):
		return errs.New(CodeTaskNotFound, taskID)
	case errors.Is(err, task.ErrNotCancellable):
		return errs.New(CodeTaskNotCancellable, taskID)
	default:
		return err
	}
}
