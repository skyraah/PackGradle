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
	// InstanceDir 是运行实例的实例目录（登记输入 root_path 的取值来源）：
	// 由游戏目录 root_path 的父目录派生（登记不变量 gameDir=<实例>/minecraft）。
	// 仅 runtime 侧填充；project 侧为空。
	InstanceDir string `json:"instance_dir,omitempty"`
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
	// PendingPlanID 是最新一张待人工计划（契约 07 §3.2，票 #86）：status ∈
	// {draft, resolved} 且读取时投影非 stale/expired/applied（planViewWithStatus
	// 同判）的计划，按创建时间最新；无则空。系统通知去重依据与前端角标数据源。
	PendingPlanID string `json:"pending_plan_id,omitempty"`
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
	SchemaVersion         int                      `json:"schema_version"`
	Relation              RelationView             `json:"relation"`
	State                 WorkspaceStateView       `json:"state"`
	Features              WorkspaceFeaturesView    `json:"features"`
	Availability          []ActionAvailabilityView `json:"availability"`
	LatestProjectSnapshot *SnapshotSummaryView     `json:"latest_project_snapshot,omitempty"`
	LatestRuntimeSnapshot *SnapshotSummaryView     `json:"latest_runtime_snapshot,omitempty"`
	// AuthorizedApply 是工作区授权开关投影（relations.authorized_apply，schema v6；
	// 契约 06 §3.6：只增不删，票 #57）。
	AuthorizedApply bool `json:"authorized_apply"`
}

// WorkspaceFeaturesView 表达当前版本/平台实现的能力（契约 03 §2.1；架构 §10.4）。
// feature=false 的动作不注册：不出现在 availability 中，前端不渲染入口。
type WorkspaceFeaturesView struct {
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

// ActionAvailabilityView 是单动作可用性，由后端按当前状态推导（架构 §10.4）。
// 前端不得自行推断；不可用动作必须带原因码供 locale 渲染。
type ActionAvailabilityView struct {
	Action     string   `json:"action"` // scan|prepare_sync|apply_sync|prepare_restore|apply_restore|rebind
	Available  bool     `json:"available"`
	ReasonCode string   `json:"reason_code,omitempty"`
	ReasonArgs []string `json:"reason_args,omitempty"`
}

// HashCacheStatsView 是 hash cache 命中统计（进程生命周期累计，
// 为 T14 性能基线供数；热扫描命中证明）。
type HashCacheStatsView struct {
	Hits     int64   `json:"hits"`
	Misses   int64   `json:"misses"`
	HitRatio float64 `json:"hit_ratio"` // hits/(hits+misses)，无查询时为 0
}

// ScanTimingView 是最近一次扫描的分相耗时（T14 性能基线供数口；
// 只在具体 *syncapp.App 上暴露，不入 transport 契约）。
type ScanTimingView struct {
	RelationID    string `json:"relation_id"`
	ProjectScanMs int64  `json:"project_scan_ms"`
	RuntimeScanMs int64  `json:"runtime_scan_ms"`
	NormalizeMs   int64  `json:"normalize_ms"`
	PersistMs     int64  `json:"persist_ms"`
	TotalMs       int64  `json:"total_ms"`
}

// ApplyTimingView 是最近一次 Apply 运行的分相耗时（P2 验收规格 §3 apply 度量
// 供数口；T09 pgheadless -metrics 消费。只在具体 *syncapp.App 上暴露，
// 不入 transport 契约）。未走到的相为 0；失败路径记录已完成的相。
// Merge 分相（票 #93，P4 验收规格 §6：diff3/校验/写盘）只记录不设门槛，
// 无 write_merged 行的运行为 0。
type ApplyTimingView struct {
	RelationID     string `json:"relation_id"`
	OperationCount int    `json:"operation_count"`
	StagingMs      int64  `json:"staging_ms"`
	ApplyingMs     int64  `json:"applying_ms"`
	VerifyingMs    int64  `json:"verifying_ms"`
	TotalMs        int64  `json:"total_ms"`
	MergeDiff3MS   int64  `json:"merge_diff3_ms"`
	MergeValidateMS int64 `json:"merge_validate_ms"`
	MergeWriteMS   int64  `json:"merge_write_ms"`
	MergeOps       int    `json:"merge_ops"`
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
	RequestedExactness         string                          `json:"requested_exactness"` // exact|allow_partial（P1 记录不消费，随计划不可变）
	PlanDigest                 string                          `json:"plan_digest"`
	Status                     string                          `json:"status"`
	ExpiresAt                  string                          `json:"expires_at"`
	Operations                 []model.PlannedOperation        `json:"operations"`
	Conflicts                  []model.Conflict                `json:"conflicts"`
	Resolutions                []model.Resolution              `json:"resolutions,omitempty"`
	ConfirmationRequirements   []model.ConfirmationRequirement `json:"confirmation_requirements"`
	Summary                    model.PlanSummary               `json:"summary"`
	Diagnostics                []model.Diagnostic              `json:"diagnostics"`
}

// TaskPage 是任务分页。
type TaskPage struct {
	Items      []TaskView `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// RegisterEndpointInput 是端点登记输入（契约 03 §2.5；project: pack.toml 所在目录，runtime: Prism 实例目录）。
type RegisterEndpointInput struct {
	RootPath string `json:"root_path"`
}

// ProjectCandidateView 是项目源发现候选（registered 按 binding fingerprint 幂等判定）。
type ProjectCandidateView struct {
	DisplayName  string `json:"display_name"`
	RootPath     string `json:"root_path"`
	PackTomlPath string `json:"pack_toml_path"`
	Minecraft    string `json:"minecraft,omitempty"`
	Modloader    string `json:"modloader,omitempty"`
	Registered   bool   `json:"registered"`
	EndpointID   string `json:"endpoint_id,omitempty"`
}

// RuntimeCandidateView 是运行实例发现候选（registered 按 adapter identity 幂等判定）。
type RuntimeCandidateView struct {
	InstanceID  string `json:"instance_id"`
	InstanceDir string `json:"instance_dir"`
	DisplayName string `json:"display_name"`
	GameDir     string `json:"game_dir"`
	Minecraft   string `json:"minecraft,omitempty"`
	Modloader   string `json:"modloader,omitempty"`
	Registered  bool   `json:"registered"`
	EndpointID  string `json:"endpoint_id,omitempty"`
}

// EndpointHealthView 是端点健康检查结果（只读，不改状态）。
// status: ok|missing|identity_mismatch；identity_mismatch 提示用户重绑。
type EndpointHealthView struct {
	EndpointID         string `json:"endpoint_id"`
	Status             string `json:"status"`
	PathExists         bool   `json:"path_exists"`
	FingerprintMatches bool   `json:"fingerprint_matches"`
	CheckedAt          string `json:"checked_at"`
}

// GetChangesInput 是资源级 Diff 查询输入（契约 03 §2.2；读时计算：head baseline +
// 指定/最新快照对跑三方 Diff，不存储投影）。快照对缺省取两侧最新；显式传入时必须
// 同属该 relation 且为相对两侧。Classification/ResourceKind/PathPrefix 为可选筛选；
// 分页按 resource_id 字节序，cursor 为上一页最后一条 resource_id（筛选条件跨页不变）。
type GetChangesInput struct {
	RelationID        string `json:"relation_id"`
	ProjectSnapshotID string `json:"project_snapshot_id,omitempty"`
	RuntimeSnapshotID string `json:"runtime_snapshot_id,omitempty"`
	Classification    string `json:"classification,omitempty"` // diff 分类单值筛选
	ResourceKind      string `json:"resource_kind,omitempty"`  // mod|text_file|binary_file
	PathPrefix        string `json:"path_prefix,omitempty"`    // root-relative 路径前缀
	Cursor            string `json:"cursor,omitempty"`
	Limit             int    `json:"limit"`
}

// ChangeView 是单资源三态 Diff 行（契约 03 §2.2 ChangeDTO 的应用层投影；
// 表示/冲突/诊断复用 model 类型，transport 负责转 DTO）。Base 在无基线时缺省。
type ChangeView struct {
	ResourceID     string                `json:"resource_id"`
	ResourceKind   string                `json:"resource_kind"`
	RelativePath   string                `json:"relative_path"`
	Classification string                `json:"classification"`
	Base           *model.Representation `json:"base,omitempty"`
	Project        *model.Representation `json:"project,omitempty"`
	Runtime        *model.Representation `json:"runtime,omitempty"`
	Conflicts      []model.Conflict      `json:"conflicts"`
	Diagnostics    []model.Diagnostic    `json:"diagnostics"`
}

// ChangesSummary 是全量分组计数（不受筛选影响），供筛选条与页脚展示。
type ChangesSummary struct {
	Total            int `json:"total"`
	NoopCount        int `json:"noop_count"`
	ConvergedCount   int `json:"converged_count"`
	AdoptEqualCount  int `json:"adopt_equal_count"`
	InitChoiceCount  int `json:"init_choice_count"`
	CreateCount      int `json:"create_count"`
	ModifyCount      int `json:"modify_count"`
	DeleteCount      int `json:"delete_count"`
	ConflictCount    int `json:"conflict_count"`
	MergedCleanCount int `json:"merged_clean_count"` // 干净合并行数（ADR-0009 §4，票 #87；不并入 modify）
}

// ChangesPage 是资源级 Diff 分页。
type ChangesPage struct {
	SchemaVersion int            `json:"schema_version"`
	Items         []ChangeView   `json:"items"`
	Summary       ChangesSummary `json:"summary"`
	NextCursor    string         `json:"next_cursor,omitempty"`
}

// PolicyView 是映射策略读投影（契约 03 §2.3；票 #20）：policy 本体 + 关系级
// 策略代次。RelationRevision 是乐观锁 expected_revision 的取值来源（ADR-0002
// 决议 2：policy 修改是唯一递增源）；两类修订号都不进入用户可见文案（决议 3）。
type PolicyView struct {
	SchemaVersion    int                 `json:"schema_version"`
	PolicyID         string              `json:"policy_id"`
	PolicyRevision   int                 `json:"policy_revision"` // 策略集模板自身版本（ADR-0002 决议 5：与关系代次语义独立、互不驱动）
	Rules            []model.MappingRule `json:"rules"`
	RelationRevision int                 `json:"relation_revision"`
}

// UpdateMappingPolicyInput 是策略写输入（契约 03 §2.3）：Rules 整体替换，
// 策略集身份（PolicyID/模板 Revision）由当前策略保持不变；ExpectedRevision
// 必须等于当前关系修订（乐观锁，err.mapping.stale_revision）。
type UpdateMappingPolicyInput struct {
	RelationID       string              `json:"relation_id"`
	ExpectedRevision int                 `json:"expected_revision"`
	Rules            []model.MappingRule `json:"rules"`
}

// PrepareRebindInput 是重绑预检输入（契约 03 §2.4；一次只重绑一侧）。
type PrepareRebindInput struct {
	RelationID string `json:"relation_id"`
	Side       string `json:"side"`      // project|runtime
	RootPath   string `json:"root_path"` // 新端点根路径（project: pack.toml 所在目录；runtime: Prism 实例目录）
}

// ---- Apply 运行与历史读投影（契约 05 §2/§3.2/§3.3/§3.5；票 #39）----

// ApplyRunView 是一次 Apply 的运行头投影（ADR-0004 §1 六阶段；契约 05 §3.2）。
// TaskID 即 run_id（apply_runs 主键）。
type ApplyRunView struct {
	SchemaVersion  int    `json:"schema_version"`
	TaskID         string `json:"task_id"`
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

// ApplyOperationView 是单操作行投影（契约 05 §3.3）。硬约束 4（ADR-0004 §4）：
// 普通用户视图不暴露临时路径与 ownership proof——本投影只携带白名单字段。
type ApplyOperationView struct {
	OperationID  string `json:"operation_id"`
	Ordinal      int    `json:"ordinal"`
	Status       string `json:"status"` // pending|running|applied|verified|failed|compensated（ADR-0004 §2 单调路径）
	ResourceID   string `json:"resource_id,omitempty"`
	RelativePath string `json:"relative_path,omitempty"` // root-relative，非临时路径
	ChangeKind   string `json:"change_kind,omitempty"`   // 与计划操作一致（write_runtime/remove_project/...）
	ResultCode   string `json:"result_code,omitempty"`   // 终局摘要码（成功为空；失败/补偿带说明码）
}

// ApplyOperationPage 是逐操作清单分页（ordinal 升序；cursor=上一页末条 operation_id，
// 与 GetChanges 同协议）。
type ApplyOperationPage struct {
	SchemaVersion int                  `json:"schema_version"`
	Items         []ApplyOperationView `json:"items"`
	NextCursor    string               `json:"next_cursor,omitempty"`
}

// ListApplyOperationsInput 是逐操作清单查询输入（契约 05 §2）。
type ListApplyOperationsInput struct {
	RelationID string `json:"relation_id"`
	TaskID     string `json:"task_id"` // 即 run_id
	Cursor     string `json:"cursor,omitempty"`
	Limit      int    `json:"limit"`
}

// CommitSummaryView 是历史列表行（契约 05 §3.5）。
type CommitSummaryView struct {
	CommitID           string `json:"commit_id"`
	Kind               string `json:"kind"`         // initialize|sync|restore
	Completeness       string `json:"completeness"` // exact|partial
	RemainingChangeCnt int    `json:"remaining_change_count"`
	CreatedAt          string `json:"created_at"`
}

// CommitChangeView 是单资源变更行（源：commit_changes）。Project*/Runtime* 为
// 联表表示的单行展示摘要；缺省 nil（DTO omitempty 序列化为 null 缺省）。
type CommitChangeView struct {
	ResourceID    string  `json:"resource_id"`
	ChangeKind    string  `json:"change_kind"`
	ProjectBefore *string `json:"project_before,omitempty"`
	ProjectAfter  *string `json:"project_after,omitempty"`
	RuntimeBefore *string `json:"runtime_before,omitempty"`
	RuntimeAfter  *string `json:"runtime_after,omitempty"`
}

// CommitSkippedView 是本场剔出的取数失败清单单行（ADR-0008 §7，票 #63）：
// 资源 ID + 原因码（err.download.* / hash_format_unsupported /
// content_unavailable）+ 插值参数。
type CommitSkippedView struct {
	ResourceID string   `json:"resource_id"`
	ReasonCode string   `json:"reason_code"`
	ReasonArgs []string `json:"reason_args,omitempty"`
}

// CommitView 是单提交详情（changes 全量，单 commit 不分页；契约 05 §3.5）。
// Skipped 从提交头 summary JSON 解析（引擎定义形状；旧行无该记录为空切片）。
type CommitView struct {
	SchemaVersion int                 `json:"schema_version"`
	Summary       CommitSummaryView   `json:"summary"`
	PlanID        string              `json:"plan_id"`
	Changes       []CommitChangeView  `json:"changes"`
	Skipped       []CommitSkippedView `json:"skipped"`
}

// CommitPage 是历史列表分页（created_at DESC；cursor=上一页末条 commit_id）。
type CommitPage struct {
	SchemaVersion int                 `json:"schema_version"`
	Items         []CommitSummaryView `json:"items"`
	NextCursor    string              `json:"next_cursor,omitempty"`
	// PrunedBeforeCount 是墓碑计数（契约 06 §3.8，票 #64）：按保留策略已清理
	// 的提交数（读时推导：任务面 commit_id 悬挂计数）；前端列表尾渲染
	// 「更早 N 条提交已按保留策略清理」，N=0 不渲染。
	PrunedBeforeCount int `json:"pruned_before_count"`
}

// RebindPreparationView 是 PrepareRebind 结果（契约 03 §2.4）。NewEndpoint 与
// OldEndpoint 共享同一端点 ID：ApplyRebind 原位更新该端点行的绑定。
type RebindPreparationView struct {
	SchemaVersion        int                    `json:"schema_version"`
	PreparationID        string                 `json:"preparation_id"`
	CreatedAt            string                 `json:"created_at"`
	ExpiresAt            string                 `json:"expires_at"`
	Side                 string                 `json:"side"`
	Checks               []PreparationCheckView `json:"checks"`
	OldEndpoint          EndpointView           `json:"old_endpoint"`
	NewEndpoint          EndpointView           `json:"new_endpoint"`
	FingerprintChanged   bool                   `json:"fingerprint_changed"`
	BaselineInheritance  string                 `json:"baseline_inheritance"` // inherit|reinitialize
	InvalidatedPlanCount int                    `json:"invalidated_plan_count"`
}

// ConfirmPlanInput 是计划确认输入（契约 05 §3.1；票 #36）。成功返回
// TaskView（kind=apply，status=queued，PlanID 回填）；幂等重入返回既有任务。
type ConfirmPlanInput struct {
	PlanID string `json:"plan_id"`
}

// ---- 统一快速更新用例投影（契约 07 §3.1；票 #86）----

// QuickUpdateInput 是统一快速更新用例输入：只收 relation_id。requested_exactness
// 恒 exact 不设入参（沿今天前端硬编码），PrepareSync 输入（revision/双端快照）
// 由用例内部取最新。
type QuickUpdateInput struct {
	RelationID string `json:"relation_id"`
}

// QuickUpdateResultView 是一次快速更新链的收口结果（Q1：同步三态）。
// 阻塞到链收口再返回，对 wire 是一次 Promise。
type QuickUpdateResultView struct {
	RelationID  string `json:"relation_id"`
	Outcome     string `json:"outcome"` // no_diff|apply_started|awaiting_confirmation
	PlanID      string `json:"plan_id,omitempty"`       // apply_started/awaiting_confirmation 回填
	ApplyTaskID string `json:"apply_task_id,omitempty"` // 仅 apply_started 回填
}

// ---- 设置域投影（契约 06 §3.6；票 #57）----

// RetentionSettingsView 是保留策略设置投影（config.toml [retention] 承载，
// ADR-0007 §2/§7/§8）。
type RetentionSettingsView struct {
	SchemaVersion         int   `json:"schema_version"`
	KeepCommits           int   `json:"keep_commits"`            // 默认 20，范围 5–200
	KeepDays              int   `json:"keep_days"`               // 默认 90，范围 7–365
	RelationCapacityBytes int64 `json:"relation_capacity_bytes"` // 默认 1 GiB，范围 128 MiB–20 GiB
	PreserveMaxBytes      int64 `json:"preserve_max_bytes"`      // 默认 32 MiB，范围 1 MiB–512 MiB；0＝不限
	TrashDays             int   `json:"trash_days"`              // 默认 7，范围 1–90
}

// UpdateRetentionSettingsInput 是保留设置写输入：五键整体替换（设置页表单全量
// 提交），单键越界整体拒绝（err.settings.retention_invalid，{0}=字段名）。
type UpdateRetentionSettingsInput struct {
	KeepCommits           int   `json:"keep_commits"`
	KeepDays              int   `json:"keep_days"`
	RelationCapacityBytes int64 `json:"relation_capacity_bytes"`
	PreserveMaxBytes      int64 `json:"preserve_max_bytes"`
	TrashDays             int   `json:"trash_days"`
}

// ---- 回滚计划面投影（契约 06 §3.1/§3.2/§3.3/§3.5；票 #59）----

// PrepareRestoreInput 是准备回滚输入（Q4：目标 baseline 后端由 commit 推导，
// 不收 baseline id）。
type PrepareRestoreInput struct {
	RelationID string `json:"relation_id"`
	CommitID   string `json:"commit_id"` // 任意历史提交（含 restore 提交=重做）；head 合法（空差异计划）
}

// ResolveRestorePlanInput 是回滚决议输入（ADR-0006 §3：无冲突决议面）。
type ResolveRestorePlanInput struct {
	PlanID             string   `json:"plan_id"`
	RequestedExactness string   `json:"requested_exactness"` // exact|allow_partial（沿 P2 枚举；空值缺省 allow_partial）
	SkipResourceIDs    []string `json:"skip_resource_ids"`   // 逐资源 skip 决议，固化于 resolved plan
}

// StageUserObjectInput 是用户对象补全输入：读字节→按 expected_digest 校验→
// 暂存（暂存路径不透出）。
type StageUserObjectInput struct {
	PlanID     string `json:"plan_id"`
	ResourceID string `json:"resource_id"`
	SourcePath string `json:"source_path"` // 本地绝对路径
}

// ConfirmRestorePlanInput 是回滚确认输入（契约 06 §3.4；票 #60）。成功返回
// TaskView（kind=restore，status=queued，PlanID 回填）；幂等重入返回既有任务，
// failed 终局重入建新运行。
type ConfirmRestorePlanInput struct {
	PlanID string `json:"plan_id"`
}

// RestoreBlockedItemView 是 exact 阻塞清单行（draft 时点 exact_infeasible 证据，
// ADR-0006 §4）。
type RestoreBlockedItemView struct {
	ResourceID   string `json:"resource_id"`
	RelativePath string `json:"relative_path"`
	Marker       string `json:"marker"`
}

// RestorePlanItemView 是回滚计划单资源行：判定字段为 prepare 时点固化事实，
// Skipped/Staged 为读取时实时投影（skip 固化于 Resolutions，staged 由计划暂存
// 目录 digest 复核推导）。
type RestorePlanItemView struct {
	model.RestorePlanItem
	Skipped bool `json:"skipped"` // resolved 后 skip 决议投影（Q5）
	Staged  bool `json:"staged"`  // user_object_required 行补全就绪（§3.5）
}

// RestorePlanView 是回滚计划投影（Status 反映读取时计算的 stale/expired/applied；
// ExactFeasible 为实时就绪面，非 draft 静态标记）。
type RestorePlanView struct {
	SchemaVersion            int                             `json:"schema_version"`
	PlanID                   string                          `json:"plan_id"`
	RelationID               string                          `json:"relation_id"`
	TargetCommitID           string                          `json:"target_commit_id"`
	Status                   string                          `json:"status"` // draft|resolved|confirmed|applied|expired|stale（沿 sync_plans CHECK）
	ExactFeasible            bool                            `json:"exact_feasible"`
	BlockedBy                []RestoreBlockedItemView        `json:"blocked_by"`
	Items                    []RestorePlanItemView           `json:"items"`
	RequestedExactness       string                          `json:"requested_exactness,omitempty"` // resolved 后回填 exact|allow_partial
	ConfirmationRequirements []model.ConfirmationRequirement `json:"confirmation_requirements"`     // 恒非空（restore_acknowledge）
	ExpiresAt                string                          `json:"expires_at"`
	CreatedAt                string                          `json:"created_at"`
}

// StorageStatsView 是存储占用概览（ADR-0011 §8 勘误兑现，票 #90）：设置页
// 只读数据面；cas_total_bytes + free_disk_bytes 为容量红线双指标承载。
// staging 侧指标不占位（ADR-0011 §5 雾区，待 #69 决议后补）；阈值与告警 UI 后置。
type StorageStatsView struct {
	SchemaVersion   int   `json:"schema_version"`
	CasTotalBytes   int64 `json:"cas_total_bytes"`   // objects 表 ready 对象字节总量（含未引用，GC 账面口径）
	CasObjectCount  int64 `json:"cas_object_count"`  // ready 对象数
	CasTmpLeftovers int64 `json:"cas_tmp_leftovers"` // objectsRoot 根下 .tmp-* 写中断残留文件数
	TaskEventsCount int64 `json:"task_events_count"` // task_events 行数
	DBSizeBytes     int64 `json:"db_size_bytes"`     // packgradle.db 文件字节数（含 -wal）
	FreeDiskBytes   int64 `json:"free_disk_bytes"`   // 用户数据根所在卷剩余字节数
}
