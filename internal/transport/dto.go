package transport

// 本文件定义 Wails 出口 DTO：顶层带 schema_version，字段 snake_case，
// slice 一律归一为空数组（由 convert 保证）。domain model 不直接暴露。

// PrepareRelationDTO 是 PrepareRelation 输入。
type PrepareRelationDTO struct {
	ProjectRoot        string `json:"project_root"`
	RuntimeInstanceDir string `json:"runtime_instance_dir"`
	PolicySet          string `json:"policy_set"`
}

// EndpointDTO 是端点投影。
type EndpointDTO struct {
	ID                 string `json:"id"`
	Adapter            string `json:"adapter"`
	DisplayName        string `json:"display_name"`
	RootPath           string `json:"root_path"`
	AdapterIdentity    string `json:"adapter_identity,omitempty"`
	BindingFingerprint string `json:"binding_fingerprint"`
}

// PreparationCheckDTO 是预检单项。
type PreparationCheckDTO struct {
	Code     string   `json:"code"`
	Passed   bool     `json:"passed"`
	Severity string   `json:"severity"`
	Args     []string `json:"args"`
	Detail   string   `json:"detail"`
}

// RelationPreparationDTO 是 PrepareRelation 结果。
type RelationPreparationDTO struct {
	SchemaVersion int                   `json:"schema_version"`
	PreparationID string                `json:"preparation_id"`
	CreatedAt     string                `json:"created_at"`
	ExpiresAt     string                `json:"expires_at"`
	Checks        []PreparationCheckDTO `json:"checks"`
	Project       *EndpointDTO          `json:"project,omitempty"`
	Runtime       *EndpointDTO          `json:"runtime,omitempty"`
	Policy        PolicyDTO             `json:"policy"`
}

// PolicyDTO / MappingRuleDTO 是策略投影。
type PolicyDTO struct {
	SchemaVersion int              `json:"schema_version"`
	PolicyID      string           `json:"policy_id"`
	Revision      int              `json:"revision"`
	Rules         []MappingRuleDTO `json:"rules"`
}

type MappingRuleDTO struct {
	ID                 string   `json:"id"`
	ResourceKind       string   `json:"resource_kind"`
	ProjectPrefix      string   `json:"project_prefix"`
	RuntimePrefix      string   `json:"runtime_prefix"`
	Include            []string `json:"include"`
	Exclude            []string `json:"exclude"`
	Direction          string   `json:"direction"`
	Materialization    string   `json:"materialization"`
	MergePolicy        string   `json:"merge_policy"`
	RuntimeLocalPolicy string   `json:"runtime_local"`
}

// RelationDTO 是关系投影。
type RelationDTO struct {
	SchemaVersion int         `json:"schema_version"`
	RelationID    string      `json:"relation_id"`
	Project       EndpointDTO `json:"project"`
	Runtime       EndpointDTO `json:"runtime"`
	PolicySet     string      `json:"policy_set"`
	Revision      int         `json:"revision"`
	Health        string      `json:"health"`
	CreatedAt     string      `json:"created_at"`
}

// WorkspaceStateDTO 是工作区正交状态（架构 §10.5）。
type WorkspaceStateDTO struct {
	ScanState        string `json:"scan_state"`
	BaselineState    string `json:"baseline_state"`
	DiffState        string `json:"diff_state"`
	RelationHealth   string `json:"relation_health"`
	ActiveTaskID     string `json:"active_task_id,omitempty"`
	RelationRevision int    `json:"relation_revision"`
}

// SnapshotSummaryDTO 是快照摘要。
type SnapshotSummaryDTO struct {
	SnapshotID     string `json:"snapshot_id"`
	Side           string `json:"side"`
	CapturedAt     string `json:"captured_at"`
	SnapshotDigest string `json:"snapshot_digest"`
	ResourceCount  int    `json:"resource_count"`
}

// WorkspaceDTO 是工作区详情。
type WorkspaceDTO struct {
	SchemaVersion         int                 `json:"schema_version"`
	Relation              RelationDTO         `json:"relation"`
	State                 WorkspaceStateDTO   `json:"state"`
	LatestProjectSnapshot *SnapshotSummaryDTO `json:"latest_project_snapshot,omitempty"`
	LatestRuntimeSnapshot *SnapshotSummaryDTO `json:"latest_runtime_snapshot,omitempty"`
}

// WorkspacePageDTO 是工作区分页。
type WorkspacePageDTO struct {
	SchemaVersion int            `json:"schema_version"`
	Items         []WorkspaceDTO `json:"items"`
	NextCursor    string         `json:"next_cursor,omitempty"`
}

// TaskDTO 是任务投影。
type TaskDTO struct {
	SchemaVersion int         `json:"schema_version"`
	TaskID        string      `json:"task_id"`
	RelationID    string      `json:"relation_id,omitempty"`
	Sequence      int         `json:"task_sequence"`
	Kind          string      `json:"kind"`
	Status        string      `json:"status"`
	Outcome       string      `json:"outcome,omitempty"`
	Phase         string      `json:"phase"`
	Completed     int         `json:"completed"`
	Total         int         `json:"total"`
	MessageKey    string      `json:"message_key"`
	MessageArgs   []string    `json:"message_args"`
	PlanID        string      `json:"plan_id,omitempty"`
	CommitID      string      `json:"commit_id,omitempty"`
	CanCancel     bool        `json:"can_cancel"`
	CreatedAt     string      `json:"created_at"`
	UpdatedAt     string      `json:"updated_at"`
	Problem       *ProblemDTO `json:"problem,omitempty"`
}

// ProblemDTO 是结构化错误（code/args/detail；文案由前端 locale 提供）。
type ProblemDTO struct {
	Code   string   `json:"code"`
	Args   []string `json:"args"`
	Detail string   `json:"detail"`
}

// TaskPageDTO 是任务分页。
type TaskPageDTO struct {
	SchemaVersion int       `json:"schema_version"`
	Items         []TaskDTO `json:"items"`
	NextCursor    string    `json:"next_cursor,omitempty"`
}

// PrepareSyncDTO 是 PrepareSync 输入。
type PrepareSyncDTO struct {
	RelationID             string `json:"relation_id"`
	RelationRevision       int    `json:"relation_revision"`
	InputProjectSnapshotID string `json:"input_project_snapshot_id"`
	InputRuntimeSnapshotID string `json:"input_runtime_snapshot_id"`
	RequestedExactness     string `json:"requested_exactness"`
}

// ResolutionDTO 是单个冲突选择。
type ResolutionDTO struct {
	ResourceID string `json:"resource_id"`
	Choice     string `json:"choice"`
}

// ResolvePlanDTO 是 ResolvePlan 输入。
type ResolvePlanDTO struct {
	PlanID      string          `json:"plan_id"`
	Resolutions []ResolutionDTO `json:"resolutions"`
}

// PreconditionDTO / OperationDTO 是计划操作投影。
type PreconditionDTO struct {
	ResourceID string         `json:"resource_id"`
	Side       string         `json:"side"`
	Expected   *ContentRefDTO `json:"expected,omitempty"`
	Existence  string         `json:"existence"`
}

type ContentRefDTO struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type OperationDTO struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	ResourceID    string            `json:"resource_id"`
	Preconditions []PreconditionDTO `json:"preconditions"`
	Reversible    bool              `json:"reversible"`
}

// RepresentationDTO 是冲突证据中的表示。
type RepresentationDTO struct {
	RelativePath string         `json:"relative_path"`
	Format       string         `json:"format"`
	Content      *ContentRefDTO `json:"content,omitempty"`
}

type ConflictDTO struct {
	ResourceID string             `json:"resource_id"`
	Kind       string             `json:"kind"`
	Base       *RepresentationDTO `json:"base,omitempty"`
	Project    *RepresentationDTO `json:"project,omitempty"`
	Runtime    *RepresentationDTO `json:"runtime,omitempty"`
	Detail     string             `json:"detail,omitempty"`
}

type ConfirmationRequirementDTO struct {
	Code          string `json:"code"`
	Severity      string `json:"severity"`
	ResourceCount int    `json:"resource_count"`
}

// DiagnosticDTO 是扫描/映射诊断投影（含 diag.mapping.collision 碰撞证据；
// code 的文案由前端 locale 提供）。
type DiagnosticDTO struct {
	Severity     string   `json:"severity"`
	Code         string   `json:"code"`
	Args         []string `json:"args"`
	Detail       string   `json:"detail,omitempty"`
	ResourceID   string   `json:"resource_id,omitempty"`
	RelativePath string   `json:"relative_path,omitempty"`
}

type PlanSummaryDTO struct {
	ResourceTotal   int `json:"resource_total"`
	AdoptEqualCount int `json:"adopt_equal_count"`
	CreateCount     int `json:"create_count"`
	ModifyCount     int `json:"modify_count"`
	DeleteCount     int `json:"delete_count"`
	ConflictCount   int `json:"conflict_count"`
}

// SyncPlanDTO 是计划投影（Status 反映读取时计算的 stale/expired）。
type SyncPlanDTO struct {	SchemaVersion              int                          `json:"schema_version"`
	PlanID                     string                       `json:"plan_id"`
	RelationID                 string                       `json:"relation_id"`
	Kind                       string                       `json:"kind"`
	ResolvedFromPlanID         string                       `json:"resolved_from_plan_id,omitempty"`
	BaseBaselineID             string                       `json:"base_baseline_id,omitempty"`
	BaseBaselineDigest         string                       `json:"base_baseline_digest,omitempty"`
	InputProjectSnapshotID     string                       `json:"input_project_snapshot_id"`
	InputRuntimeSnapshotID     string                       `json:"input_runtime_snapshot_id"`
	InputProjectSnapshotDigest string                       `json:"input_project_snapshot_digest"`
	InputRuntimeSnapshotDigest string                       `json:"input_runtime_snapshot_digest"`
	RelationRevision           int                          `json:"relation_revision"`
	PolicyDigest               string                       `json:"policy_digest"`
	ExpectedBindings           map[string]string            `json:"expected_bindings"`
	PlanDigest                 string                       `json:"plan_digest"`
	Status                     string                       `json:"status"`
	ExpiresAt                  string                       `json:"expires_at"`
	Operations                 []OperationDTO               `json:"operations"`
	Conflicts                  []ConflictDTO                `json:"conflicts"`
	Resolutions                []ResolutionDTO              `json:"resolutions"`
	ConfirmationRequirements   []ConfirmationRequirementDTO `json:"confirmation_requirements"`
	Summary                    PlanSummaryDTO               `json:"summary"`
	Diagnostics                []DiagnosticDTO              `json:"diagnostics"`
}

// RegisterEndpointDTO 是端点登记输入（契约 03 §2.5；project: pack.toml 所在目录，
// runtime: Prism 实例目录）。
type RegisterEndpointDTO struct {
	RootPath string `json:"root_path"`
}

// ProjectCandidateDTO 是项目源发现候选（registered 按 binding fingerprint 幂等判定）。
type ProjectCandidateDTO struct {
	DisplayName  string `json:"display_name"`
	RootPath     string `json:"root_path"`
	PackTomlPath string `json:"pack_toml_path"`
	Minecraft    string `json:"minecraft,omitempty"`
	Modloader    string `json:"modloader,omitempty"`
	Registered   bool   `json:"registered"`
	EndpointID   string `json:"endpoint_id,omitempty"`
}

// RuntimeCandidateDTO 是运行实例发现候选（registered 按 adapter identity 幂等判定）。
type RuntimeCandidateDTO struct {
	InstanceID  string `json:"instance_id"`
	InstanceDir string `json:"instance_dir"`
	DisplayName string `json:"display_name"`
	GameDir     string `json:"game_dir"`
	Minecraft   string `json:"minecraft,omitempty"`
	Modloader   string `json:"modloader,omitempty"`
	Registered  bool   `json:"registered"`
	EndpointID  string `json:"endpoint_id,omitempty"`
}

// EndpointHealthDTO 是端点健康检查结果（只读；status: ok|missing|identity_mismatch）。
type EndpointHealthDTO struct {
	EndpointID         string `json:"endpoint_id"`
	Status             string `json:"status"`
	PathExists         bool   `json:"path_exists"`
	FingerprintMatches bool   `json:"fingerprint_matches"`
	CheckedAt          string `json:"checked_at"`
}
