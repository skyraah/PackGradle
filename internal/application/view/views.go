// Package view 定义应用用例的返回投影（transport 再转 DTO）。
package view

import "packgradle/internal/core/model"

// ProblemView 是结构化错误投影（code/args/detail；文案由前端 locale 提供）。
type ProblemView struct {
	Code   string   `json:"code"`
	Args   []string `json:"args"`
	Detail string   `json:"detail"`
}

// TaskView 是任务投影。
type TaskView struct {
	TaskID      string       `json:"task_id"`
	RelationID  string       `json:"relation_id,omitempty"`
	Sequence    int          `json:"task_sequence"`
	Kind        string       `json:"kind"`
	Status      string       `json:"status"`
	Outcome     string       `json:"outcome,omitempty"`
	Phase       string       `json:"phase"`
	Completed   int          `json:"completed"`
	Total       int          `json:"total"`
	MessageKey  string       `json:"message_key"`
	MessageArgs []string     `json:"message_args"`
	PlanID      string       `json:"plan_id,omitempty"`
	CommitID    string       `json:"commit_id,omitempty"`
	CanCancel   bool         `json:"can_cancel"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	Problem     *ProblemView `json:"problem,omitempty"`
}

// EndpointView 是端点投影。
type EndpointView struct {
	ID                 string `json:"id"`
	Adapter            string `json:"adapter"`
	DisplayName        string `json:"display_name"`
	RootPath           string `json:"root_path"`
	AdapterIdentity    string `json:"adapter_identity,omitempty"`
	BindingFingerprint string `json:"binding_fingerprint"`
}

// RelationView 是关系投影。
type RelationView struct {
	SchemaVersion int          `json:"schema_version"`
	RelationID    string       `json:"relation_id"`
	Project       EndpointView `json:"project"`
	Runtime       EndpointView `json:"runtime"`
	PolicySet     string       `json:"policy_set"`
	Revision      int          `json:"revision"`
	Health        string       `json:"health"`
	CreatedAt     string       `json:"created_at"`
}

// PreparationCheckView 是预检单项投影。
type PreparationCheckView struct {
	Code     string   `json:"code"`
	Passed   bool     `json:"passed"`
	Severity string   `json:"severity"`
	Args     []string `json:"args"`
	Detail   string   `json:"detail"`
}

// RelationPreparationView 是 PrepareRelation 结果。
type RelationPreparationView struct {
	SchemaVersion int                    `json:"schema_version"`
	PreparationID string                 `json:"preparation_id"`
	CreatedAt     string                 `json:"created_at"`
	ExpiresAt     string                 `json:"expires_at"`
	Checks        []PreparationCheckView `json:"checks"`
	Project       *EndpointView          `json:"project,omitempty"`
	Runtime       *EndpointView          `json:"runtime,omitempty"`
	Policy        model.MappingPolicy    `json:"policy"`
}

// WorkspaceStateView 是工作区正交状态（与架构文档 §10.5 WorkspaceStateDTO 对齐；
// 不得用差异数量推断 clean）。
type WorkspaceStateView struct {
	ScanState        string `json:"scan_state"`     // never_scanned|queued|scanning|ready|failed
	BaselineState    string `json:"baseline_state"` // none|ready|stale
	DiffState        string `json:"diff_state"`     // unknown|initialization_required|clean|dirty|conflicted
	RelationHealth   string `json:"relation_health"`
	ActiveTaskID     string `json:"active_task_id,omitempty"`
	RelationRevision int    `json:"relation_revision"`
}

// SnapshotSummaryView 是快照摘要。
type SnapshotSummaryView struct {
	SnapshotID     string `json:"snapshot_id"`
	Side           string `json:"side"`
	CapturedAt     string `json:"captured_at"`
	SnapshotDigest string `json:"snapshot_digest"`
	ResourceCount  int    `json:"resource_count"`
}

// WorkspaceView 是工作区详情。
type WorkspaceView struct {
	SchemaVersion         int                  `json:"schema_version"`
	Relation              RelationView         `json:"relation"`
	State                 WorkspaceStateView   `json:"state"`
	LatestProjectSnapshot *SnapshotSummaryView `json:"latest_project_snapshot,omitempty"`
	LatestRuntimeSnapshot *SnapshotSummaryView `json:"latest_runtime_snapshot,omitempty"`
}

// WorkspacePage 是工作区分页。
type WorkspacePage struct {
	Items      []WorkspaceView `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// PrepareSyncInput 是 PrepareSync 用例输入。
type PrepareSyncInput struct {
	RelationID             string `json:"relation_id"`
	RelationRevision       int    `json:"relation_revision"`
	InputProjectSnapshotID string `json:"input_project_snapshot_id"`
	InputRuntimeSnapshotID string `json:"input_runtime_snapshot_id"`
	RequestedExactness     string `json:"requested_exactness"` // exact|allow_partial（P1 记录不消费）
}

// ResolvePlanInput 是 ResolvePlan 用例输入。
type ResolvePlanInput struct {
	PlanID      string             `json:"plan_id"`
	Resolutions []model.Resolution `json:"resolutions"`
}

// SyncPlanView 是计划投影（Status 反映读取时计算的 stale/expired）。
type SyncPlanView struct {
	SchemaVersion              int                             `json:"schema_version"`
	PlanID                     string                          `json:"plan_id"`
	RelationID                 string                          `json:"relation_id"`
	Kind                       string                          `json:"kind"`
	ResolvedFromPlanID         string                          `json:"resolved_from_plan_id,omitempty"`
	BaseBaselineID             string                          `json:"base_baseline_id,omitempty"`
	BaseBaselineDigest         string                          `json:"base_baseline_digest,omitempty"`
	InputProjectSnapshotID     string                          `json:"input_project_snapshot_id"`
	InputRuntimeSnapshotID     string                          `json:"input_runtime_snapshot_id"`
	InputProjectSnapshotDigest string                          `json:"input_project_snapshot_digest"`
	InputRuntimeSnapshotDigest string                          `json:"input_runtime_snapshot_digest"`
	RelationRevision           int                             `json:"relation_revision"`
	PolicyDigest               string                          `json:"policy_digest"`
	ExpectedBindings           model.ExpectedBindings          `json:"expected_bindings"`
	PlanDigest                 string                          `json:"plan_digest"`
	Status                     string                          `json:"status"`
	ExpiresAt                  string                          `json:"expires_at"`
	Operations                 []model.PlannedOperation        `json:"operations"`
	Conflicts                  []model.Conflict                `json:"conflicts"`
	Resolutions                []model.Resolution              `json:"resolutions,omitempty"`
	ConfirmationRequirements   []model.ConfirmationRequirement `json:"confirmation_requirements"`
	Summary                    model.PlanSummary               `json:"summary"`
}

// TaskPage 是任务分页。
type TaskPage struct {
	Items      []TaskView `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}
