package transport

// 本文件定义 Wails 出口 DTO：顶层带 schema_version，字段 snake_case，
// slice 一律归一为空数组（由 convert 保证）。domain model 不直接暴露。

// PrepareRelationDTO 是 PrepareRelation 输入。
type PrepareRelationDTO struct {
	ProjectRoot        string   `json:"project_root"`
	RuntimeInstanceDir string   `json:"runtime_instance_dir"`
	PolicySet          string   `json:"policy_set"`
	Suggestions        []string `json:"suggestions,omitempty"` // 勾选的建议规则 ID（默认不勾选，确认前不写受管）
}

// EndpointDTO 是端点投影。
type EndpointDTO struct {
	ID                 string `json:"id"`
	Adapter            string `json:"adapter"`
	DisplayName        string `json:"display_name"`
	RootPath           string `json:"root_path"`
	AdapterIdentity    string `json:"adapter_identity,omitempty"`
	BindingFingerprint string `json:"binding_fingerprint"`
	// InstanceDir 仅 runtime 侧填充：实例目录（PrepareRelation 输入 root_path
	// 的取值来源），由游戏目录父目录派生；project 侧为空。
	InstanceDir string `json:"instance_dir,omitempty"`
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
	// RelationRevision 是关系级策略代次（ADR-0002 决议 5：与模板版本语义独立、
	// 互不驱动）。仅 Mapping 读写投影填充（mappings 页乐观锁 expected_revision
	// 的取值来源），预检投影恒 0；任何修订号不进入用户可见文案（决议 3）。
	RelationRevision int `json:"relation_revision,omitempty"`
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

// WorkspaceFeaturesDTO 表达当前版本/平台实现的能力（契约 03 §2.1；架构 §10.4）。
// feature=false 的动作不注册：不出现在 availability 中，前端不渲染入口。
type WorkspaceFeaturesDTO struct {
	Scan                 bool     `json:"scan"`
	SyncPreview          bool     `json:"sync_preview"`
	SyncApply            bool     `json:"sync_apply"`
	ConflictInspection   bool     `json:"conflict_inspection"`
	ConflictResolution   string   `json:"conflict_resolution"` // none|choose_side|merge
	HistoryView          bool     `json:"history_view"`
	RestorePreview       bool     `json:"restore_preview"`
	RestoreApply         bool     `json:"restore_apply"`
	MaterializationModes []string `json:"materialization_modes"` // P1 恒 []；Phase 2 起为 ["copy"]
}

// ActionAvailabilityDTO 是单动作可用性，由后端按当前状态推导（架构 §10.4）。
// 前端不得自行推断；不可用动作必须带原因码供 locale 渲染。
type ActionAvailabilityDTO struct {
	Action     string   `json:"action"` // scan|prepare_sync|apply_sync|quick_update|prepare_restore|apply_restore|rebind
	Available  bool     `json:"available"`
	ReasonCode string   `json:"reason_code,omitempty"`
	ReasonArgs []string `json:"reason_args,omitempty"`
}

// WorkspaceDTO 是工作区详情。
type WorkspaceDTO struct {
	SchemaVersion         int                     `json:"schema_version"`
	Relation              RelationDTO             `json:"relation"`
	State                 WorkspaceStateDTO       `json:"state"`
	Features              WorkspaceFeaturesDTO    `json:"features"`
	Availability          []ActionAvailabilityDTO `json:"availability"`
	LatestProjectSnapshot *SnapshotSummaryDTO     `json:"latest_project_snapshot,omitempty"`
	LatestRuntimeSnapshot *SnapshotSummaryDTO     `json:"latest_runtime_snapshot,omitempty"`
	// AuthorizedApply 是工作区授权开关投影（relations.authorized_apply，schema v6；
	// 契约 06 §3.6：只增不删，票 #57）。
	AuthorizedApply bool `json:"authorized_apply"`
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
	// Materialization 是物化模式（契约 06 §3.7，票 #63）：copy|download，由
	// 后端推导（有重取信息的 mod 写操作 → download，其余 → copy）；旧行空值
	// ＝copy 兼容。
	Materialization string `json:"materialization,omitempty"`
	// PreserveSkip 是「旧版本不留存」警示行标记（契约 06 §3.7；ADR-0007 §7，
	// 票 #64）：非 mod 单文件超过 preserve_max_bytes，不做 before 保全。
	// 只增不删，同「不可重取」警示先例（deletion_warn）。
	PreserveSkip bool `json:"preserve_skip,omitempty"`
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

// HashCacheStatsDTO 是 hash cache 命中统计（进程生命周期累计；
// 热扫描命中证明与 T14 性能基线供数）。
type HashCacheStatsDTO struct {
	SchemaVersion int     `json:"schema_version"`
	Hits          int64   `json:"hits"`
	Misses        int64   `json:"misses"`
	HitRatio      float64 `json:"hit_ratio"`
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
type SyncPlanDTO struct {
	SchemaVersion              int               `json:"schema_version"`
	PlanID                     string            `json:"plan_id"`
	RelationID                 string            `json:"relation_id"`
	Kind                       string            `json:"kind"`
	ResolvedFromPlanID         string            `json:"resolved_from_plan_id,omitempty"`
	BaseBaselineID             string            `json:"base_baseline_id,omitempty"`
	BaseBaselineDigest         string            `json:"base_baseline_digest,omitempty"`
	InputProjectSnapshotID     string            `json:"input_project_snapshot_id"`
	InputRuntimeSnapshotID     string            `json:"input_runtime_snapshot_id"`
	InputProjectSnapshotDigest string            `json:"input_project_snapshot_digest"`
	InputRuntimeSnapshotDigest string            `json:"input_runtime_snapshot_digest"`
	RelationRevision           int               `json:"relation_revision"`
	PolicyDigest               string            `json:"policy_digest"`
	ExpectedBindings           map[string]string `json:"expected_bindings"`
	// RequestedExactness 是请求确切度 exact|allow_partial（契约 03 §2.6；
	// 与 Plan 模型 / sync_plans 列三处一致，ResolvePlan 继承）。
	RequestedExactness       string                       `json:"requested_exactness"`
	PlanDigest               string                       `json:"plan_digest"`
	Status                   string                       `json:"status"`
	ExpiresAt                string                       `json:"expires_at"`
	Operations               []OperationDTO               `json:"operations"`
	Conflicts                []ConflictDTO                `json:"conflicts"`
	Resolutions              []ResolutionDTO              `json:"resolutions"`
	ConfirmationRequirements []ConfirmationRequirementDTO `json:"confirmation_requirements"`
	Summary                  PlanSummaryDTO               `json:"summary"`
	Diagnostics              []DiagnosticDTO              `json:"diagnostics"`
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

// UpdateMappingPolicyDTO 是映射策略写输入（契约 03 §2.3；票 #20）：rules 整体
// 替换，策略集身份保持不变；乐观锁——expected_revision 必须等于当前关系修订
// （PolicyDTO.relation_revision 的读值），不等返回 err.mapping.stale_revision。
type UpdateMappingPolicyDTO struct {
	RelationID       string           `json:"relation_id"`
	ExpectedRevision int              `json:"expected_revision"`
	Rules            []MappingRuleDTO `json:"rules"`
}

// PrepareRebindDTO 是重绑预检输入（契约 03 §2.4；一次只重绑一侧）。
type PrepareRebindDTO struct {
	RelationID string `json:"relation_id"`
	Side       string `json:"side"`      // project|runtime
	RootPath   string `json:"root_path"` // 新端点根路径（project: pack.toml 所在目录；runtime: Prism 实例目录）
}

// RebindPreparationDTO 是 PrepareRebind 结果（契约 03 §2.4）。new_endpoint 与
// old_endpoint 共享同一端点 ID：ApplyRebind 原位更新该端点行的绑定。
type RebindPreparationDTO struct {
	SchemaVersion        int                   `json:"schema_version"`
	PreparationID        string                `json:"preparation_id"`
	CreatedAt            string                `json:"created_at"`
	ExpiresAt            string                `json:"expires_at"`
	Side                 string                `json:"side"`
	Checks               []PreparationCheckDTO `json:"checks"`
	OldEndpoint          EndpointDTO           `json:"old_endpoint"`
	NewEndpoint          EndpointDTO           `json:"new_endpoint"`
	FingerprintChanged   bool                  `json:"fingerprint_changed"`
	BaselineInheritance  string                `json:"baseline_inheritance"` // inherit|reinitialize
	InvalidatedPlanCount int                   `json:"invalidated_plan_count"`
}

// GetChangesDTO 是资源级 Changes 查询输入（契约 03 §2.2；读时计算，快照对缺省
// 取两侧最新）。分页按 resource_id 字节序，cursor 为上一页最后一条 resource_id。
type GetChangesDTO struct {
	RelationID        string `json:"relation_id"`
	ProjectSnapshotID string `json:"project_snapshot_id,omitempty"`
	RuntimeSnapshotID string `json:"runtime_snapshot_id,omitempty"`
	Classification    string `json:"classification,omitempty"` // diff 分类单值筛选
	ResourceKind      string `json:"resource_kind,omitempty"`  // mod|text_file|binary_file
	PathPrefix        string `json:"path_prefix,omitempty"`    // root-relative 路径前缀
	Cursor            string `json:"cursor,omitempty"`
	Limit             int    `json:"limit"`
}

// ChangeDTO 是单资源三态 Diff 行。Base 在无基线时缺省。
type ChangeDTO struct {
	ResourceID     string             `json:"resource_id"`
	ResourceKind   string             `json:"resource_kind"`
	RelativePath   string             `json:"relative_path"`
	Classification string             `json:"classification"`
	Base           *RepresentationDTO `json:"base,omitempty"`
	Project        *RepresentationDTO `json:"project,omitempty"`
	Runtime        *RepresentationDTO `json:"runtime,omitempty"`
	Conflicts      []ConflictDTO      `json:"conflicts"`
	Diagnostics    []DiagnosticDTO    `json:"diagnostics"`
}

// ChangesSummaryDTO 是全量分组计数（不受筛选影响），供筛选条与页脚展示。
type ChangesSummaryDTO struct {
	Total           int `json:"total"`
	NoopCount       int `json:"noop_count"`
	ConvergedCount  int `json:"converged_count"`
	AdoptEqualCount int `json:"adopt_equal_count"`
	InitChoiceCount int `json:"init_choice_count"`
	CreateCount     int `json:"create_count"`
	ModifyCount     int `json:"modify_count"`
	DeleteCount     int `json:"delete_count"`
	ConflictCount   int `json:"conflict_count"`
}

// ChangesPageDTO 是资源级 Diff 分页。
type ChangesPageDTO struct {
	SchemaVersion int               `json:"schema_version"`
	Items         []ChangeDTO       `json:"items"`
	Summary       ChangesSummaryDTO `json:"summary"`
	NextCursor    string            `json:"next_cursor,omitempty"`
}

// ---- Phase 2 Apply（契约 05）----

// ConfirmPlanDTO 是计划确认输入（契约 05 §3.1）。成功返回 TaskDTO
// （kind=apply，status=queued，PlanID 字段回填）；幂等重入返回既有任务。
type ConfirmPlanDTO struct {
	PlanID string `json:"plan_id"`
}

// ---- Apply 运行与历史读 DTO（契约 05 §3 定稿，票 #39；schema_version/slice 归一沿契约 03 §0 硬约束）----

// ApplyRunDTO 是一次 Apply 的运行头投影（ADR-0004 §1 六阶段；契约 05 §3.2）。
type ApplyRunDTO struct {
	SchemaVersion  int    `json:"schema_version"`
	TaskID         string `json:"task_id"` // 即 run_id（apply_runs 主键）
	RelationID     string `json:"relation_id"`
	PlanID         string `json:"plan_id"`
	PlanDigest     string `json:"plan_digest"`
	State          string `json:"state"` // prepared|staged|applying|verifying|committed|recovery_required
	OperationCount int    `json:"operation_count"`
	StagingCleared bool   `json:"staging_cleared"`
	AcknowledgedAt string `json:"acknowledged_at,omitempty"` // 人工确认时间（recovery_required 收口后）
	CommitID       string `json:"commit_id,omitempty"`       // committed 后回填
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// ApplyOperationDTO 是单操作行投影。硬约束 4：不含 temp_relative_path / ownership_proof_json。
type ApplyOperationDTO struct {
	OperationID  string `json:"operation_id"`
	Ordinal      int    `json:"ordinal"`
	Status       string `json:"status"` // pending|running|applied|verified|failed|compensated（ADR-0004 §2 单调路径）
	ResourceID   string `json:"resource_id,omitempty"`
	RelativePath string `json:"relative_path,omitempty"` // root-relative，非临时路径
	ChangeKind   string `json:"change_kind,omitempty"`   // 与计划操作一致（create/modify/delete）
	ResultCode   string `json:"result_code,omitempty"`   // 终局摘要码（成功为空；失败/补偿带说明码）
}

// ApplyOperationPageDTO 是逐操作清单分页（ordinal 升序；cursor=上一页末条 operation_id）。
type ApplyOperationPageDTO struct {
	SchemaVersion int                 `json:"schema_version"`
	Items         []ApplyOperationDTO `json:"items"`
	NextCursor    string              `json:"next_cursor,omitempty"`
}

// CommitSummaryDTO 是历史列表行。
type CommitSummaryDTO struct {
	CommitID           string `json:"commit_id"`
	Kind               string `json:"kind"`         // initialize|sync|restore
	Completeness       string `json:"completeness"` // exact|partial
	RemainingChangeCnt int    `json:"remaining_change_count"`
	CreatedAt          string `json:"created_at"`
}

// CommitChangeDTO 是单资源变更行（源：commit_changes；before/after 为联表表示摘要，缺省 null）。
type CommitChangeDTO struct {
	ResourceID    string  `json:"resource_id"`
	ChangeKind    string  `json:"change_kind"`
	ProjectBefore *string `json:"project_before,omitempty"` // 表示摘要（联表），缺省 null
	ProjectAfter  *string `json:"project_after,omitempty"`
	RuntimeBefore *string `json:"runtime_before,omitempty"`
	RuntimeAfter  *string `json:"runtime_after,omitempty"`
}

// CommitDTO 是单提交详情（changes 全量，单 commit 不分页）。
type CommitDTO struct {
	SchemaVersion int               `json:"schema_version"`
	Summary       CommitSummaryDTO  `json:"summary"`
	PlanID        string            `json:"plan_id"`
	Changes       []CommitChangeDTO `json:"changes"`
	// Skipped 是本场剔出的取数失败清单（契约 06 §3.7/ADR-0008 §7，票 #63）：
	// 成功 N + 跳过 M（带 err.download.* 原因码）；旧行无该记录为空数组。
	Skipped []CommitSkippedDTO `json:"skipped"`
}

// CommitSkippedDTO 是跳过清单单行：资源 ID + 原因码（文案由前端 locale 提供）。
type CommitSkippedDTO struct {
	ResourceID string   `json:"resource_id"`
	ReasonCode string   `json:"reason_code"`
	ReasonArgs []string `json:"reason_args,omitempty"`
}

// CommitPageDTO 是历史列表分页（created_at DESC；cursor=上一页末条 commit_id）。
type CommitPageDTO struct {
	SchemaVersion int                `json:"schema_version"`
	Items         []CommitSummaryDTO `json:"items"`
	NextCursor    string             `json:"next_cursor,omitempty"`
	// PrunedBeforeCount 是墓碑计数（契约 06 §3.8，票 #64）；N=0 前端不渲染。
	PrunedBeforeCount int `json:"pruned_before_count"`
}

// ---- 设置域 DTO（契约 06 §3.6；票 #57）----

// RetentionSettingsDTO 是保留策略设置投影（config.toml [retention] 承载，
// ADR-0007 §2/§7/§8；K=3 硬保底固定不可调，不设键）。
type RetentionSettingsDTO struct {
	SchemaVersion         int   `json:"schema_version"`
	KeepCommits           int   `json:"keep_commits"`            // 默认 20，范围 5–200
	KeepDays              int   `json:"keep_days"`               // 默认 90，范围 7–365
	RelationCapacityBytes int64 `json:"relation_capacity_bytes"` // 默认 1 GiB，范围 128 MiB–20 GiB
	PreserveMaxBytes      int64 `json:"preserve_max_bytes"`      // 默认 32 MiB，范围 1 MiB–512 MiB；0＝不限
	TrashDays             int   `json:"trash_days"`              // 默认 7，范围 1–90
}

// UpdateRetentionSettingsDTO 是保留设置写输入：五键整体替换（设置页表单全量
// 提交）。单键范围校验，越界 → err.settings.retention_invalid（{0}=字段名），
// 整体拒绝（不落任何键）。
type UpdateRetentionSettingsDTO struct {
	KeepCommits           int   `json:"keep_commits"`
	KeepDays              int   `json:"keep_days"`
	RelationCapacityBytes int64 `json:"relation_capacity_bytes"`
	PreserveMaxBytes      int64 `json:"preserve_max_bytes"`
	TrashDays             int   `json:"trash_days"`
}

// ---- 回滚计划面 DTO（契约 06 §3；票 #59；独立族不复用 SyncPlanDTO，Q2）----

// RestorePrepareDTO 是准备回滚输入（Q4：目标 baseline 后端由 commit 推导，
// 不收 baseline id）。
type RestorePrepareDTO struct {
	RelationID string `json:"relation_id"`
	CommitID   string `json:"commit_id"` // 任意历史提交（含 restore 提交=重做）；head 合法（空差异计划）
}

// ResolveRestorePlanDTO 是回滚决议输入（ADR-0006 §3：无冲突决议面）。
type ResolveRestorePlanDTO struct {
	PlanID             string   `json:"plan_id"`
	RequestedExactness string   `json:"requested_exactness"` // exact|allow_partial（沿 P2 枚举；空值缺省 allow_partial）
	SkipResourceIDs    []string `json:"skip_resource_ids"`   // 逐资源 skip 决议，固化于 resolved plan
}

// ConfirmRestorePlanDTO 是回滚确认输入（契约 06 §3.4；票 #60）。成功返回
// TaskDTO（kind=restore，status=queued，PlanID 字段回填）；幂等重入返回既有
// 任务；failed 终局重入建新运行；committed 后 err.plan.apply_not_reentrant。
type ConfirmRestorePlanDTO struct {
	PlanID string `json:"plan_id"`
}

// StageUserObjectDTO 是用户对象补全输入：读字节→按 expected_digest 校验→暂存
//（暂存路径不透出，契约 06 §3.5）。
type StageUserObjectDTO struct {
	PlanID     string `json:"plan_id"`
	ResourceID string `json:"resource_id"`
	SourcePath string `json:"source_path"` // 本地绝对路径
}

// RestoreBlockedItemDTO 是 exact 阻塞清单行（draft 时点 exact_infeasible 证据，
// ADR-0006 §4）。
type RestoreBlockedItemDTO struct {
	ResourceID   string `json:"resource_id"`
	RelativePath string `json:"relative_path"`
	Marker       string `json:"marker"`
}

// RestorePlanItemDTO 是回滚计划单资源行。Marker 枚举
// restorable_from_cas|redownload_required|user_object_required|unrecoverable
//（delete 行不占四标记）；MarkerReason 仅 user_object_required 行
//（no_redownload_info|cf_unavailable|hash_format_unsupported）；Skipped/Staged
// 为读取时实时投影；Availability 仅 redownload_required 行（ok|unknown）；
// ExpectedDigest 仅 user_object_required 行（验收入库的目标摘要）。
type RestorePlanItemDTO struct {
	ResourceID     string `json:"resource_id"`
	RelativePath   string `json:"relative_path"`
	ChangeKind     string `json:"change_kind"` // create|modify|delete
	Marker         string `json:"marker"`      // delete 行为空串
	MarkerReason   string `json:"marker_reason,omitempty"`
	Skipped        bool   `json:"skipped"`
	Staged         bool   `json:"staged"`
	DeletionWarn   bool   `json:"deletion_warn,omitempty"`   // 手放 mod 删除＝「不可重取」警示（ADR-0006 §5）
	PreserveSkip   bool   `json:"preserve_skip,omitempty"`   // 「旧版本不留存」警示位（判定归票 #64）
	Availability   string `json:"availability,omitempty"`    // ok|unknown，仅 redownload_required 行
	NewerAvailable bool   `json:"newer_available,omitempty"` // 仅 ok 行；仅提示，版本决策归 packwiz
	ExpectedDigest string `json:"expected_digest,omitempty"` // user_object_required 行验收入库目标摘要
}

// RestorePlanDTO 是回滚计划投影（Status 沿 sync_plans CHECK 读取时投影；
// ExactFeasible 为实时就绪面，非 draft 静态标记）。
type RestorePlanDTO struct {
	SchemaVersion            int                          `json:"schema_version"`
	PlanID                   string                       `json:"plan_id"`
	RelationID               string                       `json:"relation_id"`
	TargetCommitID           string                       `json:"target_commit_id"`
	Status                   string                       `json:"status"` // draft|resolved|confirmed|applied|expired|stale
	ExactFeasible            bool                         `json:"exact_feasible"`
	BlockedBy                []RestoreBlockedItemDTO      `json:"blocked_by"`
	Items                    []RestorePlanItemDTO         `json:"items"`
	RequestedExactness       string                       `json:"requested_exactness,omitempty"` // resolved 后回填 exact|allow_partial
	ConfirmationRequirements []ConfirmationRequirementDTO `json:"confirmation_requirements"`     // 恒非空（restore_acknowledge）
	ExpiresAt                string                       `json:"expires_at"`
	CreatedAt                string                       `json:"created_at"`
}
