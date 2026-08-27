package transport

import (
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// 本文件集中 view -> DTO 的显式转换；slice 归一为空数组。

func strs(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func contentRefDTO(c *model.ContentRef) *ContentRefDTO {
	if c == nil {
		return nil
	}
	return &ContentRefDTO{Algorithm: c.Algorithm, Digest: c.Digest, Size: c.Size}
}

func endpointDTO(e view.EndpointView) EndpointDTO {
	return EndpointDTO{
		ID: e.ID, Adapter: e.Adapter, DisplayName: e.DisplayName, RootPath: e.RootPath,
		AdapterIdentity: e.AdapterIdentity, BindingFingerprint: e.BindingFingerprint,
	}
}

func policyDTO(p model.MappingPolicy) PolicyDTO {
	rules := make([]MappingRuleDTO, 0, len(p.Rules))
	for _, r := range p.Rules {
		rules = append(rules, MappingRuleDTO{
			ID: r.ID, ResourceKind: r.ResourceKind,
			ProjectPrefix: r.ProjectPrefix, RuntimePrefix: r.RuntimePrefix,
			Include: strs(r.Include), Exclude: strs(r.Exclude),
			Direction: r.Direction, Materialization: r.Materialization,
			MergePolicy: r.MergePolicy, RuntimeLocalPolicy: r.RuntimeLocalPolicy,
		})
	}
	return PolicyDTO{SchemaVersion: p.SchemaVersion, PolicyID: p.PolicyID, Revision: p.Revision, Rules: rules}
}

func preparationDTO(v view.RelationPreparationView) RelationPreparationDTO {
	checks := make([]PreparationCheckDTO, 0, len(v.Checks))
	for _, c := range v.Checks {
		checks = append(checks, PreparationCheckDTO{
			Code: c.Code, Passed: c.Passed, Severity: c.Severity, Args: strs(c.Args), Detail: c.Detail,
		})
	}
	out := RelationPreparationDTO{
		SchemaVersion: v.SchemaVersion, PreparationID: v.PreparationID,
		CreatedAt: v.CreatedAt, ExpiresAt: v.ExpiresAt,
		Checks: checks, Policy: policyDTO(v.Policy),
	}
	if v.Project != nil {
		e := endpointDTO(*v.Project)
		out.Project = &e
	}
	if v.Runtime != nil {
		e := endpointDTO(*v.Runtime)
		out.Runtime = &e
	}
	return out
}

func relationDTO(v view.RelationView) RelationDTO {
	return RelationDTO{
		SchemaVersion: v.SchemaVersion, RelationID: v.RelationID,
		Project: endpointDTO(v.Project), Runtime: endpointDTO(v.Runtime),
		PolicySet: v.PolicySet, Revision: v.Revision, Health: v.Health, CreatedAt: v.CreatedAt,
	}
}

func workspaceDTO(v view.WorkspaceView) WorkspaceDTO {
	out := WorkspaceDTO{
		SchemaVersion: v.SchemaVersion,
		Relation:      relationDTO(v.Relation),
		State: WorkspaceStateDTO{
			ScanState: v.State.ScanState, BaselineState: v.State.BaselineState,
			DiffState: v.State.DiffState, RelationHealth: v.State.RelationHealth,
			ActiveTaskID: v.State.ActiveTaskID, RelationRevision: v.State.RelationRevision,
		},
	}
	if v.LatestProjectSnapshot != nil {
		out.LatestProjectSnapshot = snapshotSummaryDTO(*v.LatestProjectSnapshot)
	}
	if v.LatestRuntimeSnapshot != nil {
		out.LatestRuntimeSnapshot = snapshotSummaryDTO(*v.LatestRuntimeSnapshot)
	}
	return out
}

func snapshotSummaryDTO(s view.SnapshotSummaryView) *SnapshotSummaryDTO {
	return &SnapshotSummaryDTO{
		SnapshotID: s.SnapshotID, Side: s.Side, CapturedAt: s.CapturedAt,
		SnapshotDigest: s.SnapshotDigest, ResourceCount: s.ResourceCount,
	}
}

func taskDTO(v view.TaskView) TaskDTO {
	out := TaskDTO{
		SchemaVersion: model.CurrentSchemaVersion,
		TaskID:        v.TaskID, RelationID: v.RelationID, Sequence: v.Sequence,
		Kind: v.Kind, Status: v.Status, Outcome: v.Outcome, Phase: v.Phase,
		Completed: v.Completed, Total: v.Total, MessageKey: v.MessageKey, MessageArgs: strs(v.MessageArgs),
		PlanID: v.PlanID, CommitID: v.CommitID, CanCancel: v.CanCancel,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
	if v.Problem != nil {
		out.Problem = &ProblemDTO{Code: v.Problem.Code, Args: strs(v.Problem.Args), Detail: v.Problem.Detail}
	}
	return out
}

func representationDTO(r *model.Representation) *RepresentationDTO {
	if r == nil {
		return nil
	}
	return &RepresentationDTO{
		RelativePath: r.RelativePath, Format: r.Format, Content: contentRefDTO(r.Content),
	}
}

func planDTO(v view.SyncPlanView) SyncPlanDTO {
	ops := make([]OperationDTO, 0, len(v.Operations))
	for _, op := range v.Operations {
		preconds := make([]PreconditionDTO, 0, len(op.Preconditions))
		for _, pc := range op.Preconditions {
			preconds = append(preconds, PreconditionDTO{
				ResourceID: string(pc.ResourceID), Side: pc.Side,
				Expected: contentRefDTO(pc.Expected), Existence: pc.Existence,
			})
		}
		ops = append(ops, OperationDTO{
			ID: op.ID, Kind: string(op.Kind), ResourceID: string(op.ResourceID),
			Preconditions: preconds, Reversible: op.Reversible,
		})
	}
	conflicts := make([]ConflictDTO, 0, len(v.Conflicts))
	for _, c := range v.Conflicts {
		conflicts = append(conflicts, ConflictDTO{
			ResourceID: string(c.ResourceID), Kind: string(c.Kind),
			Base: representationDTO(c.Base), Project: representationDTO(c.Project), Runtime: representationDTO(c.Runtime),
			Detail: c.Detail,
		})
	}
	resolutions := make([]ResolutionDTO, 0, len(v.Resolutions))
	for _, r := range v.Resolutions {
		resolutions = append(resolutions, ResolutionDTO{ResourceID: string(r.ResourceID), Choice: string(r.Choice)})
	}
	reqs := make([]ConfirmationRequirementDTO, 0, len(v.ConfirmationRequirements))
	for _, r := range v.ConfirmationRequirements {
		reqs = append(reqs, ConfirmationRequirementDTO{Code: r.Code, Severity: r.Severity, ResourceCount: r.ResourceCount})
	}
	return SyncPlanDTO{
		SchemaVersion: v.SchemaVersion, PlanID: v.PlanID, RelationID: v.RelationID,
		Kind: v.Kind, ResolvedFromPlanID: v.ResolvedFromPlanID,
		BaseBaselineID: v.BaseBaselineID, BaseBaselineDigest: v.BaseBaselineDigest,
		InputProjectSnapshotID:     v.InputProjectSnapshotID,
		InputRuntimeSnapshotID:     v.InputRuntimeSnapshotID,
		InputProjectSnapshotDigest: v.InputProjectSnapshotDigest,
		InputRuntimeSnapshotDigest: v.InputRuntimeSnapshotDigest,
		RelationRevision:           v.RelationRevision, PolicyDigest: v.PolicyDigest,
		ExpectedBindings: map[string]string{"project": v.ExpectedBindings.Project, "runtime": v.ExpectedBindings.Runtime},
		PlanDigest:       v.PlanDigest, Status: v.Status, ExpiresAt: v.ExpiresAt,
		Operations: ops, Conflicts: conflicts, Resolutions: resolutions,
		ConfirmationRequirements: reqs,
		Summary: PlanSummaryDTO{
			ResourceTotal: v.Summary.ResourceTotal, AdoptEqualCount: v.Summary.AdoptEqualCount,
			CreateCount: v.Summary.CreateCount, ModifyCount: v.Summary.ModifyCount,
			DeleteCount: v.Summary.DeleteCount, ConflictCount: v.Summary.ConflictCount,
		},
	}
}
