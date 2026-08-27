package sync

import (
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// 本文件集中 domain -> view 的显式转换。

func relationView(rel model.Relation, proj model.Project, rt model.Runtime) view.RelationView {
	return view.RelationView{
		SchemaVersion: rel.SchemaVersion,
		RelationID:    rel.RelationID,
		Project: view.EndpointView{
			ID:                 proj.ProjectID,
			Adapter:            proj.Adapter,
			DisplayName:        proj.DisplayName,
			RootPath:           proj.RootPath,
			BindingFingerprint: proj.BindingFingerprint,
		},
		Runtime: view.EndpointView{
			ID:                 rt.RuntimeID,
			Adapter:            rt.Adapter,
			DisplayName:        rt.DisplayName,
			RootPath:           rt.RootPath,
			AdapterIdentity:    rt.AdapterIdentity,
			BindingFingerprint: rt.BindingFingerprint,
		},
		PolicySet: rel.PolicySet,
		Revision:  rel.Revision,
		Health:    string(rel.Health),
		CreatedAt: rel.CreatedAt,
	}
}

func preparationView(p model.RelationPreparation) view.RelationPreparationView {
	out := view.RelationPreparationView{
		SchemaVersion: p.SchemaVersion,
		PreparationID: p.PreparationID,
		CreatedAt:     p.CreatedAt,
		ExpiresAt:     p.ExpiresAt,
		Policy:        p.Policy,
		Checks:        make([]view.PreparationCheckView, 0, len(p.Checks)),
	}
	for _, c := range p.Checks {
		args := c.Args
		if args == nil {
			args = []string{}
		}
		out.Checks = append(out.Checks, view.PreparationCheckView{
			Code: c.Code, Passed: c.Passed, Severity: c.Severity, Args: args, Detail: c.Detail,
		})
	}
	if p.Project != nil {
		out.Project = &view.EndpointView{
			ID: p.Project.ProjectID, Adapter: p.Project.Adapter, DisplayName: p.Project.DisplayName,
			RootPath: p.Project.RootPath, BindingFingerprint: p.Project.BindingFingerprint,
		}
	}
	if p.Runtime != nil {
		out.Runtime = &view.EndpointView{
			ID: p.Runtime.RuntimeID, Adapter: p.Runtime.Adapter, DisplayName: p.Runtime.DisplayName,
			RootPath: p.Runtime.RootPath, AdapterIdentity: p.Runtime.AdapterIdentity,
			BindingFingerprint: p.Runtime.BindingFingerprint,
		}
	}
	return out
}

// TaskView 转换任务（MessageArgs/slice 归一为空数组而非 null）。
func TaskView(t model.Task) view.TaskView {
	args := t.MessageArgs
	if args == nil {
		args = []string{}
	}
	v := view.TaskView{
		TaskID: t.TaskID, RelationID: t.RelationID, Sequence: t.Sequence,
		Kind: t.Kind, Status: t.Status, Outcome: t.Outcome, Phase: t.Phase,
		Completed: t.Completed, Total: t.Total, MessageKey: t.MessageKey, MessageArgs: args,
		PlanID: t.PlanID, CommitID: t.CommitID, CanCancel: t.CanCancel,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	if t.Problem != nil {
		pa := t.Problem.Args
		if pa == nil {
			pa = []string{}
		}
		v.Problem = &view.ProblemView{Code: t.Problem.Code, Args: pa, Detail: t.Problem.Detail}
	}
	return v
}

// PlanView 转换计划（operations/conflicts 归一为空数组）。
func PlanView(p model.SyncPlan) view.SyncPlanView {
	ops := p.Operations
	if ops == nil {
		ops = []model.PlannedOperation{}
	}
	conflicts := p.Conflicts
	if conflicts == nil {
		conflicts = []model.Conflict{}
	}
	resolutions := p.Resolutions
	if resolutions == nil {
		resolutions = []model.Resolution{}
	}
	reqs := p.ConfirmationRequirements
	if reqs == nil {
		reqs = []model.ConfirmationRequirement{}
	}
	return view.SyncPlanView{
		SchemaVersion: p.SchemaVersion, PlanID: p.PlanID, RelationID: p.RelationID,
		Kind: string(p.Kind), ResolvedFromPlanID: p.ResolvedFromPlanID,
		BaseBaselineID: p.BaseBaselineID, BaseBaselineDigest: p.BaseBaselineDigest,
		InputProjectSnapshotID:     p.InputProjectSnapshotID,
		InputRuntimeSnapshotID:     p.InputRuntimeSnapshotID,
		InputProjectSnapshotDigest: p.InputProjectSnapshotDigest,
		InputRuntimeSnapshotDigest: p.InputRuntimeSnapshotDigest,
		RelationRevision:           p.RelationRevision, PolicyDigest: p.PolicyDigest,
		ExpectedBindings:         p.ExpectedBindings,
		PlanDigest:               p.PlanDigest,
		Status:                   string(p.Status),
		ExpiresAt:                p.ExpiresAt,
		Operations:               ops,
		Conflicts:                conflicts,
		Resolutions:              resolutions,
		ConfirmationRequirements: reqs,
		Summary:                  p.Summary,
	}
}

func snapshotSummary(s model.ObservedSnapshot) *view.SnapshotSummaryView {
	return &view.SnapshotSummaryView{
		SnapshotID: s.SnapshotID, Side: string(s.Side), CapturedAt: s.CapturedAt,
		SnapshotDigest: s.SnapshotDigest, ResourceCount: len(s.Resources),
	}
}
