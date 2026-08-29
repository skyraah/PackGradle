// Package model 定义 PackGradle 新架构的全部领域类型。
// 字段命名与 json tag 与架构文档 §3.2/§6.5/§8.1 对齐；本包只依赖标准库。
package model

// CurrentSchemaVersion 是所有可持久化领域对象的当前 schema 版本。
const CurrentSchemaVersion = 1

// ResourceKind 是受管逻辑资源的类别（架构文档 §3.1）。
// directory_manifest 与 runtime_local 是派生诊断分类，不得伪装成可同步资源。
type ResourceKind string

const (
	ResourceMod        ResourceKind = "mod"
	ResourceTextFile   ResourceKind = "text_file"
	ResourceBinaryFile ResourceKind = "binary_file"
)

// ResourceID 是逻辑资源的稳定身份，由适配器的稳定 identity 生成，
// 例如 "mod:modrinth:AANobbMI"、"mod:curseforge:12345"、
// "mod:jar:sodium-0.6.5.jar"（runtime-only 低置信度）、"file:config/jei/jei-client.ini"。
type ResourceID string

// Side 是快照所属端。
type Side string

const (
	SideProject Side = "project"
	SideRuntime Side = "runtime"
)

// ContentRef 引用一份内容的指纹。MVP 固定 sha256（ADR-006）。
type ContentRef struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// Representation 是某逻辑资源在某一侧的表示。
// Format 取值例如 packwiz-mod-toml / jar / toml / ini / text / binary。
type Representation struct {
	RelativePath string            `json:"relative_path"`
	Format       string            `json:"format"`
	Content      *ContentRef       `json:"content,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// Representation.Metadata 的保留键。mod 语义摘要（normalize.SemanticDigest）
// 依赖这些键；扫描器必须按此填写。
const (
	MetaVersion           = "version"              // pw.toml 顶层 version / x-prismlauncher-version-number
	MetaSide              = "side"                 // 归一化后的 client/server/both
	MetaDeclaredHashAlgo  = "declared_hash_format" // pw.toml [download] hash-format（如 sha256/sha1/murmur2）
	MetaDeclaredHashValue = "declared_hash"        // pw.toml [download] hash
	MetaDisplayName       = "display_name"         // 展示名，永不进入 digest
	MetaFilename          = "filename"             // pw.toml filename 字段（对应 runtime jar 文件名），永不进入 digest；跨侧 hint 通道使用
)

// LogicalResource 是合并视图（两侧表示并入同一对象），供后续 MergeAdapter 使用；
// diff/plan 直接消费 ObservedSnapshot/Baseline，不依赖本类型。
type LogicalResource struct {
	ID       ResourceID      `json:"id"`
	Kind     ResourceKind    `json:"kind"`
	Project  *Representation `json:"project,omitempty"`
	Runtime  *Representation `json:"runtime,omitempty"`
	PolicyID string          `json:"policy_id"`
}

// Identity 描述资源身份的来源与置信度。
// Provider: "modrinth" | "curseforge" | "jar"（runtime-only 本地文件）| "path"（无 provider 的项目侧回退）| ""（文件类资源无身份概念）。
type Identity struct {
	Provider   string `json:"provider"`
	Key        string `json:"key"`
	Confidence string `json:"confidence"` // high | low | ""
}

// 置信度取值。
const (
	ConfidenceHigh = "high"
	ConfidenceLow  = "low"
)

// ResourceObservation 是扫描器输出的单条资源观察结果。
type ResourceObservation struct {
	ResourceID     ResourceID     `json:"resource_id"`
	Kind           ResourceKind   `json:"kind"`
	Identity       Identity       `json:"identity"`
	Representation Representation `json:"representation"`
	PolicyID       string         `json:"policy_id"` // 命中的 MappingRule.ID；诊断类为 ""
}

// Diagnostic 是扫描诊断（不影响 digest，随快照持久化）。
type Diagnostic struct {
	Severity     string     `json:"severity"` // info | warning | error
	Code         string     `json:"code"`     // diag.scan.* / diag.mapping.*
	Args         []string   `json:"args,omitempty"`
	Detail       string     `json:"detail,omitempty"`
	ResourceID   ResourceID `json:"resource_id,omitempty"`
	RelativePath string     `json:"relative_path,omitempty"`
}

// ScannerInfo 标识产生快照的扫描器实现。
type ScannerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ObservedSnapshot 是一次只读扫描得到的某一侧事实（架构文档 §8.1）。
// 不可变；可频繁生成、可因文件继续变化而过期。
type ObservedSnapshot struct {
	SchemaVersion        int                                `json:"schema_version"`
	SnapshotID           string                             `json:"snapshot_id"` // snap_ 前缀
	RelationID           string                             `json:"relation_id"`
	Side                 Side                               `json:"side"`
	CapturedAt           string                             `json:"captured_at"`
	BindingFingerprint   string                             `json:"binding_fingerprint"`
	SnapshotDigest       string                             `json:"snapshot_digest"` // "sha256:<hex>"，由 normalize.SnapshotDigest 计算
	NormalizationVersion int                                `json:"normalization_version"`
	PolicyDigest         string                             `json:"policy_digest"`
	Scanner              ScannerInfo                        `json:"scanner"`
	Resources            map[ResourceID]ResourceObservation `json:"resources"`
	Diagnostics          []Diagnostic                       `json:"diagnostics"`
}

// Recoverability 描述资源在回滚场景下的可恢复途径（Phase 3 消费，P1 先建模）。
type Recoverability string

const (
	RecoverabilityNone          Recoverability = "none"
	RecoverabilityCAS           Recoverability = "cas"
	RecoverabilityRedownload    Recoverability = "redownload"
	RecoverabilityUserObject    Recoverability = "user_object"
	RecoverabilityUnrecoverable Recoverability = "unrecoverable"
)

// BaselineResource 是基线中单资源的逻辑状态；absent 为显式 tombstone。
type BaselineResource struct {
	State                 string          `json:"state"` // present | absent
	LogicalDigest         string          `json:"logical_digest"`
	ProjectRepresentation *Representation `json:"project_representation,omitempty"`
	RuntimeRepresentation *Representation `json:"runtime_representation,omitempty"`
	Recoverability        Recoverability  `json:"recoverability"`
}

// SyncBaseline 是上次成功 Apply 并复扫验证后双方认可的逐资源逻辑状态。
// 只能由 Apply 后完整复扫验证成功时创建（P1 不产生，仅建模与持久化）。
type SyncBaseline struct {
	SchemaVersion        int                             `json:"schema_version"`
	BaselineID           string                          `json:"baseline_id"` // base_ 前缀
	RelationID           string                          `json:"relation_id"`
	ParentBaselineID     string                          `json:"parent_baseline_id,omitempty"`
	CreatedAt            string                          `json:"created_at"`
	BaselineDigest       string                          `json:"baseline_digest"` // normalize.BaselineDigest 计算
	NormalizationVersion int                             `json:"normalization_version"`
	Resources            map[ResourceID]BaselineResource `json:"resources"`
}

// Project 是一个已登记的 Packwiz 项目端点。
type Project struct {
	SchemaVersion      int    `json:"schema_version"`
	ProjectID          string `json:"project_id"` // prj_ 前缀
	Adapter            string `json:"adapter"`    // "packwiz"
	DisplayName        string `json:"display_name"`
	RootPath           string `json:"root_path"`
	BindingFingerprint string `json:"binding_fingerprint"`
	CreatedAt          string `json:"created_at"`
}

// Runtime 是一个已登记的可运行实例端点（MVP 适配器为 Prism）。
// RootPath 指向游戏目录（Prism/MMC 为 <实例目录>/minecraft）；
// AdapterIdentity 为实例目录名，参与端点身份。
type Runtime struct {
	SchemaVersion      int    `json:"schema_version"`
	RuntimeID          string `json:"runtime_id"` // run_ 前缀
	Adapter            string `json:"adapter"`    // "prism"
	DisplayName        string `json:"display_name"`
	RootPath           string `json:"root_path"`
	AdapterIdentity    string `json:"adapter_identity"`
	BindingFingerprint string `json:"binding_fingerprint"`
	CreatedAt          string `json:"created_at"`
}

// RelationHealth 是关系的健康状态。
type RelationHealth string

const (
	HealthHealthy          RelationHealth = "healthy"
	HealthEndpointMissing  RelationHealth = "endpoint_missing"
	HealthRebindRequired   RelationHealth = "rebind_required"
	HealthRecoveryRequired RelationHealth = "recovery_required"
)

// Relation 是一条本机 Project <-> Runtime 关系（聚合根）。
type Relation struct {
	SchemaVersion  int            `json:"schema_version"`
	RelationID     string         `json:"relation_id"` // rel_ 前缀
	ProjectID      string         `json:"project_id"`
	RuntimeID      string         `json:"runtime_id"`
	PolicySet      string         `json:"policy_set"`
	Revision       int            `json:"revision"`
	Health         RelationHealth `json:"health"`
	HeadBaselineID string         `json:"head_baseline_id,omitempty"`
	HeadCommitID   string         `json:"head_commit_id,omitempty"`
	CreatedAt      string         `json:"created_at"`
}

// PreparationCheck 是 Relation 创建预检的单项结果。
type PreparationCheck struct {
	Code     string   `json:"code"` // check.* 命名空间
	Passed   bool     `json:"passed"`
	Severity string   `json:"severity"` // blocking | warning
	Args     []string `json:"args,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// PrepareRelationInput 是 PrepareRelation 的用户输入。
type PrepareRelationInput struct {
	ProjectRoot        string   `json:"project_root"`          // pack.toml 所在目录
	RuntimeInstanceDir string   `json:"runtime_instance_dir"`  // Prism 实例目录（非游戏目录）
	PolicySet          string   `json:"policy_set"`            // 默认 "default-v1"
	Suggestions        []string `json:"suggestions,omitempty"` // 勾选的建议规则 ID（policy.Suggestions 子集，默认不勾选）
}

// RelationPreparation 是 Prepare 阶段持久化的预检结果；CreateRelation 只接受其 ID。
type RelationPreparation struct {
	SchemaVersion int                  `json:"schema_version"`
	PreparationID string               `json:"preparation_id"` // prep_ 前缀
	CreatedAt     string               `json:"created_at"`
	ExpiresAt     string               `json:"expires_at"`
	Input         PrepareRelationInput `json:"input"`
	Project       *Project             `json:"project,omitempty"` // 登记草稿（含 fingerprint）
	Runtime       *Runtime             `json:"runtime,omitempty"`
	Policy        MappingPolicy        `json:"policy"`
	Checks        []PreparationCheck   `json:"checks"`
}

// BaselineInheritance 是重绑的基线继承语义（契约 03 §2.4）。P1 恒 reinitialize；
// inherit（等价证明后继承基线）留 Phase 2。
const (
	BaselineInheritanceInherit      = "inherit"
	BaselineInheritanceReinitialize = "reinitialize"
)

// RebindPreparation 是 PrepareRebind 持久化的重绑预检结果（一次只重绑一侧）；
// ApplyRebind 只接受其 ID，端点行由 Apply 原位更新（NewProject/NewRuntime 携带
// 旧端点 ID，作为将被写入的绑定草稿）。
type RebindPreparation struct {
	SchemaVersion int                `json:"schema_version"`
	PreparationID string             `json:"preparation_id"` // prep_ 前缀
	RelationID    string             `json:"relation_id"`
	Side          Side               `json:"side"`
	CreatedAt     string             `json:"created_at"`
	ExpiresAt     string             `json:"expires_at"`
	// InputRootPath 是用户输入的原始路径（project: pack.toml 所在目录；runtime: Prism 实例目录）。
	InputRootPath string   `json:"input_root_path"`
	NewProject    *Project `json:"new_project,omitempty"` // side=project 时的绑定草稿（含 fingerprint）
	NewRuntime    *Runtime `json:"new_runtime,omitempty"` // side=runtime 时的绑定草稿（RootPath 为游戏目录）
	Checks        []PreparationCheck `json:"checks"`
	// FingerprintChanged 是新旧绑定指纹对比结果（重绑后旧 Plan 的 expected_bindings 失配）。
	FingerprintChanged bool `json:"fingerprint_changed"`
	// BaselineInheritance 表达基线继承语义，P1 恒 reinitialize（ApplyRebind 后
	// baseline_state="none"、diff_state="initialization_required"）。
	BaselineInheritance string `json:"baseline_inheritance"`
	// InvalidatedPlanCount 是预检时该关系下仍可推进（draft/resolved）的计划数，
	// 重绑后它们将因绑定指纹失配投影为 stale。
	InvalidatedPlanCount int `json:"invalidated_plan_count"`
}

// OperationKind 是计划中的操作类别（架构文档 §6.5）。
type OperationKind string

const (
	OpWriteRuntime  OperationKind = "write_runtime"
	OpWriteProject  OperationKind = "write_project"
	OpRemoveRuntime OperationKind = "remove_runtime"
	OpRemoveProject OperationKind = "remove_project"
	OpMaterialize   OperationKind = "materialize"
)

// Precondition 是 Apply 前必须仍成立的资源前置条件。
type Precondition struct {
	ResourceID ResourceID  `json:"resource_id"`
	Side       string      `json:"side"`
	Expected   *ContentRef `json:"expected,omitempty"`
	Existence  string      `json:"existence"` // present | absent
}

// PlannedOperation 是计划中的单个操作。ID 为确定性序号（op_0001 起）。
type PlannedOperation struct {
	ID            string         `json:"id"`
	Kind          OperationKind  `json:"kind"`
	ResourceID    ResourceID     `json:"resource_id"`
	Preconditions []Precondition `json:"preconditions"`
	Reversible    bool           `json:"reversible"`
	ObjectRefs    []ContentRef   `json:"object_refs,omitempty"`
}

// ConfirmationRequirement 由 resolved plan 的最终操作推导（架构文档 §6.5）。
type ConfirmationRequirement struct {
	Code          string `json:"code"` // overwrite | delete | write_project | unrecoverable | shared_materialization
	Severity      string `json:"severity"`
	ResourceCount int    `json:"resource_count"`
}

// ConflictKind 是冲突分类。
type ConflictKind string

const (
	ConflictModifyModify ConflictKind = "modify_modify"
	ConflictDeleteModify ConflictKind = "delete_modify"
	ConflictInitialize   ConflictKind = "initialize_choice"
	ConflictIdentity     ConflictKind = "identity_ambiguous"
	ConflictMapping      ConflictKind = "mapping_collision"
)

// Conflict 是计划中的未解决冲突。
type Conflict struct {
	ResourceID ResourceID      `json:"resource_id"`
	Kind       ConflictKind    `json:"kind"`
	Base       *Representation `json:"base,omitempty"`
	Project    *Representation `json:"project,omitempty"`
	Runtime    *Representation `json:"runtime,omitempty"`
	Detail     string          `json:"detail,omitempty"`
}

// ResolutionChoice 是用户对冲突的显式选择（P1 无自动合并）。
type ResolutionChoice string

const (
	ChoiceAdoptEqual            ResolutionChoice = "adopt_equal"
	ChoiceInitializeFromProject ResolutionChoice = "initialize_from_project"
	ChoiceInitializeFromRuntime ResolutionChoice = "initialize_from_runtime"
	ChoiceTakeProject           ResolutionChoice = "take_project"
	ChoiceTakeRuntime           ResolutionChoice = "take_runtime"
	ChoiceSkip                  ResolutionChoice = "skip"
	ChoiceManual                ResolutionChoice = "manual"
)

// Resolution 是单个冲突的用户选择。
type Resolution struct {
	ResourceID ResourceID       `json:"resource_id"`
	Choice     ResolutionChoice `json:"choice"`
}

// PlanKind / PlanStatus 是计划类别与状态。
type PlanKind string

const (
	PlanInitialize PlanKind = "initialize"
	PlanSync       PlanKind = "sync"
	PlanRestore    PlanKind = "restore"
)

type PlanStatus string

const (
	PlanDraft     PlanStatus = "draft"
	PlanResolved  PlanStatus = "resolved"
	PlanConfirmed PlanStatus = "confirmed"
	PlanApplied   PlanStatus = "applied"
	PlanExpired   PlanStatus = "expired"
	PlanStale     PlanStatus = "stale"
)

// Exactness 是请求确切度（requested_exactness）：PrepareSync 时的用户请求记录，
// 随计划不可变（ResolvePlan 从 draft 继承）。P1 只记录不消费（无 Apply）；
// Apply 的完成度判定在 Phase 2 消费该值。
type Exactness string

const (
	ExactnessExact        Exactness = "exact"
	ExactnessAllowPartial Exactness = "allow_partial"
)

// PlanSummary 是计划的影响摘要。
type PlanSummary struct {
	ResourceTotal   int `json:"resource_total"`
	AdoptEqualCount int `json:"adopt_equal_count"`
	CreateCount     int `json:"create_count"`
	ModifyCount     int `json:"modify_count"`
	DeleteCount     int `json:"delete_count"`
	ConflictCount   int `json:"conflict_count"`
}

// ExpectedBindings 是计划建立时锁定的两端 binding fingerprint。
type ExpectedBindings struct {
	Project string `json:"project"`
	Runtime string `json:"runtime"`
}

// SyncPlan 是无副作用的不可变计划（draft/resolved）。
// digest 覆盖字段见 normalize.PlanDigest；ID、status、expires_at 不参与 digest。
type SyncPlan struct {
	SchemaVersion              int              `json:"schema_version"`
	PlanID                     string           `json:"plan_id"` // plan_ 前缀
	RelationID                 string           `json:"relation_id"`
	Kind                       PlanKind         `json:"kind"`
	ResolvedFromPlanID         string           `json:"resolved_from_plan_id,omitempty"`
	BaseBaselineID             string           `json:"base_baseline_id,omitempty"`
	BaseBaselineDigest         string           `json:"base_baseline_digest,omitempty"`
	InputProjectSnapshotID     string           `json:"input_project_snapshot_id"`
	InputRuntimeSnapshotID     string           `json:"input_runtime_snapshot_id"`
	InputProjectSnapshotDigest string           `json:"input_project_snapshot_digest"`
	InputRuntimeSnapshotDigest string           `json:"input_runtime_snapshot_digest"`
	RelationRevision           int              `json:"relation_revision"`
	PolicyDigest               string           `json:"policy_digest"`
	ExpectedBindings           ExpectedBindings `json:"expected_bindings"`
	// RequestedExactness 是建立计划时的请求确切度（exact|allow_partial）。
	// 请求记录，不参与 PlanDigest（normalize.PlanDigest 排除清单）。
	RequestedExactness       Exactness                 `json:"requested_exactness"`
	PlanDigest               string                    `json:"plan_digest"`
	Status                   PlanStatus                `json:"status"`
	ExpiresAt                string                    `json:"expires_at"`
	Operations               []PlannedOperation        `json:"operations"`
	Conflicts                []Conflict                `json:"conflicts"`
	Resolutions              []Resolution              `json:"resolutions,omitempty"`
	ConfirmationRequirements []ConfirmationRequirement `json:"confirmation_requirements"`
	Summary                  PlanSummary               `json:"summary"`
	// Diagnostics 是输入快照携带的扫描/映射诊断（含 diag.mapping.collision 碰撞证据，
	// 检视报告 P0-5）。证据性数据，不参与 PlanDigest。
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// ChangeKind / Change 是资源相对 base 的状态变化（诊断与摘要用）。
type ChangeKind string

const (
	ChangeCreate     ChangeKind = "create"
	ChangeModify     ChangeKind = "modify"
	ChangeDelete     ChangeKind = "delete"
	ChangeAdoptEqual ChangeKind = "adopt_equal"
	ChangeConflict   ChangeKind = "conflict"
)

type Change struct {
	ResourceID ResourceID      `json:"resource_id"`
	Kind       ChangeKind      `json:"kind"`
	Side       Side            `json:"side"`
	Before     *Representation `json:"before,omitempty"`
	After      *Representation `json:"after,omitempty"`
}

// ScanReport 是扫描器（Project/Runtime Adapter）的单次输出。
type ScanReport struct {
	Observations []ResourceObservation `json:"observations"` // 必须按 ResourceID 字节序排序
	Diagnostics  []Diagnostic          `json:"diagnostics"`
}
