package sync

import (
	"context"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/diff"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// ListWorkspaces 分页返回工作区列表（含正交状态投影）。
func (a *App) ListWorkspaces(ctx context.Context, page ports.PageRequest) (view.WorkspacePage, error) {
	rels, next, err := a.deps.Relations.List(ctx, page)
	if err != nil {
		return view.WorkspacePage{}, err
	}
	out := view.WorkspacePage{Items: make([]view.WorkspaceView, 0, len(rels)), NextCursor: next}
	for _, rel := range rels {
		w, err := a.GetWorkspace(ctx, rel.RelationID)
		if err != nil {
			return view.WorkspacePage{}, err
		}
		out.Items = append(out.Items, w)
	}
	return out, nil
}

// GetWorkspace 返回单个工作区的正交状态投影。
// scan/baseline/diff/relation_health 各自独立计算，不用差异数量推断 clean。
func (a *App) GetWorkspace(ctx context.Context, relationID string) (view.WorkspaceView, error) {
	rel, err := a.deps.Relations.Get(ctx, relationID)
	if err != nil {
		return view.WorkspaceView{}, errs.New(CodeRelationNotFound, relationID)
	}
	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		return view.WorkspaceView{}, err
	}
	rt, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		return view.WorkspaceView{}, err
	}

	snapP, okP, err := a.deps.Snapshots.LatestByRelationSide(ctx, relationID, model.SideProject)
	if err != nil {
		return view.WorkspaceView{}, err
	}
	snapR, okR, err := a.deps.Snapshots.LatestByRelationSide(ctx, relationID, model.SideRuntime)
	if err != nil {
		return view.WorkspaceView{}, err
	}

	activeTasks, _, err := a.deps.Tasks.ListByRelation(ctx, relationID, true, ports.PageRequest{Limit: ports.MaxPageLimit})
	if err != nil {
		return view.WorkspaceView{}, err
	}
	hasActiveTask := len(activeTasks) > 0
	var activeTaskID string
	var activeScanStatus string
	for _, t := range activeTasks {
		if activeTaskID == "" {
			activeTaskID = t.TaskID
		}
		if t.Kind == model.TaskKindScan {
			activeScanStatus = t.Status
			break
		}
	}

	// pending_plan_id（契约 07 §3.2，票 #86）：最新一张待人工计划的投影，
	// 系统通知去重依据与前端「有待确认计划」角标数据源。读取失败向上传播
	//（投影是读取面的一部分，不静默降级，与 planReadinessForRelation 同口径）。
	pending, err := a.pendingPlanIDFor(ctx, rel, proj, rt)
	if err != nil {
		return view.WorkspaceView{}, err
	}
	state := view.WorkspaceStateView{
		RelationHealth:   string(rel.Health),
		ActiveTaskID:     activeTaskID,
		RelationRevision: rel.Revision,
		PendingPlanID:    pending,
		// watch_status（契约 07 §3.2，票 #92）：监听引擎会话内存态，无监听
		// 面（headless/引擎未装配）投影空串=未挂载。
		WatchStatus: a.watchStatusFor(relationID),
	}

	// scan_state
	switch {
	case activeScanStatus == model.TaskStatusQueued:
		state.ScanState = "queued"
	case activeScanStatus == model.TaskStatusRunning:
		state.ScanState = "scanning"
	case okP && okR:
		state.ScanState = "ready"
	default:
		// 无完整快照：查最近一次 scan 任务是否失败
		tasks, _, err := a.deps.Tasks.ListByRelation(ctx, relationID, false, ports.PageRequest{Limit: 10})
		if err != nil {
			return view.WorkspaceView{}, err
		}
		state.ScanState = "never_scanned"
		for _, t := range tasks {
			if t.Kind == model.TaskKindScan && t.Status == model.TaskStatusFailed && !snapshotAfterTask(okP, snapP, t) {
				state.ScanState = "failed"
				break
			}
			if t.Kind == model.TaskKindScan {
				break // 最新一条 scan
			}
		}
	}

	// baseline_state
	switch {
	case rel.HeadBaselineID == "":
		state.BaselineState = "none"
	default:
		state.BaselineState = "ready"
		if base, err := a.deps.Baselines.Get(ctx, rel.HeadBaselineID); err == nil {
			baseAt, perr := time.Parse(time.RFC3339, base.CreatedAt)
			if perr == nil {
				if (okP && afterRFC3339(snapP.CapturedAt, baseAt)) || (okR && afterRFC3339(snapR.CapturedAt, baseAt)) {
					state.BaselineState = "stale"
				}
			}
		}
	}

	// diff_state
	switch {
	case !okP || !okR:
		state.DiffState = "unknown"
	case rel.HeadBaselineID == "":
		state.DiffState = "initialization_required"
	default:
		base, err := a.deps.Baselines.Get(ctx, rel.HeadBaselineID)
		if err != nil {
			return view.WorkspaceView{}, err
		}
		result, err := diff.ThreeWay(diff.Input{RelationID: relationID, Base: &base, Project: snapP, Runtime: snapR})
		if err != nil {
			return view.WorkspaceView{}, err
		}
		switch {
		case len(result.Conflicts) > 0:
			state.DiffState = "conflicted"
		default:
			state.DiffState = "clean"
			for _, d := range result.Diffs {
				if d.Classification != diff.ClassNoop && d.Classification != diff.ClassConverged {
					state.DiffState = "dirty"
					break
				}
			}
		}
	}

	// apply_sync（契约 05 §1）：在既有三动作之上注册，计划面按可应用计划推导；
	// prepare_restore（契约 06 §1；票 #59）：三条件推导（无活跃任务 ∧ 非
	// recovery_required ∧ scan ready），原因码已在 PrepareRestore 同码强制。
	face, err := a.planReadinessForRelation(ctx, rel, proj, rt)
	if err != nil {
		return view.WorkspaceView{}, err
	}
	// quick_update（契约 06 §1/§4，票 #62）：在 apply_sync 之上注册，授权开关 +
	// prepare_restore 同款三门禁（无活跃任务 ∧ 非 recovery_required ∧ scan ready）
	// 由后端推导，前端不得自行推断
	availability := append(deriveAvailability(string(rel.Health), state.ScanState, hasActiveTask),
		deriveApplySyncAvailability(string(rel.Health), state.ScanState, hasActiveTask, face),
		deriveQuickUpdateAvailability(string(rel.Health), state.ScanState, hasActiveTask, rel.AuthorizedApply),
		derivePrepareRestoreAvailability(string(rel.Health), state.ScanState, hasActiveTask),
		deriveApplyRestoreAvailability(string(rel.Health), state.ScanState, hasActiveTask))

	w := view.WorkspaceView{
		SchemaVersion:   model.CurrentSchemaVersion,
		Relation:        relationView(rel, proj, rt),
		State:           state,
		Features:        workspaceFeatures(),
		Availability:    availability,
		AuthorizedApply: rel.AuthorizedApply,
	}
	if okP {
		w.LatestProjectSnapshot = snapshotSummary(snapP)
	}
	if okR {
		w.LatestRuntimeSnapshot = snapshotSummary(snapR)
	}
	return w, nil
}

// SetWorkspaceAuthorized 切换工作区授权开关（契约 06 §3.6；票 #57）：写
// relations.authorized_apply 后返回更新后的工作区投影（WorkspaceDTO 开关值与
// 既有字段同源一致）。开关存储与判定解耦：恢复期开关值保留、入口由既有
// err.recovery.in_progress 门禁挡（CONTEXT.md 授权模式词条）；免确认编排
// （confirmation_requirements 为空 ∧ authorized）归前端快速更新编排，不在本用例。
func (a *App) SetWorkspaceAuthorized(ctx context.Context, relationID string, enabled bool) (view.WorkspaceView, error) {
	if err := a.deps.Relations.UpdateAuthorizedApply(ctx, relationID, enabled); err != nil {
		return view.WorkspaceView{}, errs.New(CodeRelationNotFound, relationID)
	}
	return a.GetWorkspace(ctx, relationID)
}

func snapshotAfterTask(ok bool, snap model.ObservedSnapshot, t model.Task) bool {
	if !ok {
		return false
	}
	at, err := time.Parse(time.RFC3339, snap.CapturedAt)
	if err != nil {
		return false
	}
	ut, err := time.Parse(time.RFC3339, t.UpdatedAt)
	if err != nil {
		return false
	}
	return at.After(ut)
}

func afterRFC3339(rfc3339 string, ref time.Time) bool {
	t, err := time.Parse(time.RFC3339, rfc3339)
	return err == nil && t.After(ref)
}
