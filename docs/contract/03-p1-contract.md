# P1 契约补全执行规格（P1-CONTRACT）

> **状态：已决议（执行规格）。** 2026-08-28 经决策图票 #5 用户拍板定稿（五决策点决议见 §5）；执行会话按本规格施工，不再做隐式决策。
> 来源：决策图「PackGradle 重构全路线决策图（Phase 1-4）」票 #5；草案阶段存于 throwaway 分支 `prototype/p1-contract-draft`（历史存档），本文件为定稿。
>
> 依据：检视报告 P1-1/P1-2/P1-3、roadmap Step 6.3/6.4/7.5 与 §2.1 第 9/10 条、架构 §4.4/§9.3/§10.3/§10.4/§10.5、ADR-0002、`internal/transport/dto.go` 与 `internal/application/view/views.go` 现状。

## 0. 覆盖范围与硬约束

补检视报告 P1-1/P1-2/P1-3 指出的缺失契约：**WorkspaceFeatures、ActionAvailability、资源级 Changes/Diff 查询、Mapping 读写、Rebind（Prepare/Apply）、endpoint discovery/registration/health** 的 DTO 与用例签名（Go 应用层 + transport 层 + TS 形状）。

硬约束（本规格必须全部满足，逐条对照见 §4）：

1. `requested_exactness` 进入 Plan 模型 / SQLite / DTO 三处一致（P1-3）；
2. DTO 顶层带 `schema_version`，slice 一律归一为空数组 `[]`，错误用 `AppErrorDTO/ProblemDTO`（roadmap Step 6.4）；
3. 未实现能力不暴露为可用操作（roadmap §2.1 第 10 条）：`Features=false` 的动作不注册、不出现在 `availability` 中。

现有契约不变更：`dto.go` 既有 DTO 字段只增不删；`errs.AppError`（`code/args/detail`）是唯一调用级错误形态。

## 1. 用例签名总览

| 用例 | application 层 | transport 层（Wails 方法） | 状态 |
| --- | --- | --- | --- |
| 工作区详情 | `GetWorkspace`（已有） | `GetWorkspace`（已有） | **扩展**：DTO 增补 features + availability |
| 工作区列表 | `ListWorkspaces`（已有） | `ListWorkspaces`（已有） | **扩展**：同上（同构 WorkspaceDTO） |
| 资源级 Changes | `GetChanges(ctx, GetChangesInput) (view.ChangesPage, error)` | `GetChanges(input GetChangesDTO) (ChangesPageDTO, error)` | **新增**（T09 执行落地，票 #19：`internal/application/sync/changes.go` + `/workspaces/:id/changes` 页） |
| Mapping 读 | `GetMappingPolicy(ctx, relationID) (view.PolicyView, error)` | `GetMappingPolicy(relationID string) (PolicyDTO, error)` | **新增**（T10 执行落地，票 #20：`internal/application/sync/mapping.go` + `/workspaces/:id/mappings` 页） |
| Mapping 写 | `UpdateMappingPolicy(ctx, UpdateMappingPolicyInput) (view.PolicyView, error)` | `UpdateMappingPolicy(input UpdateMappingPolicyDTO) (PolicyDTO, error)` | **新增**（T10 执行落地，票 #20：同上；乐观锁 + 编译校验 + 修订号同事务） |
| Rebind 预检 | `PrepareRebind(ctx, PrepareRebindInput) (view.RebindPreparationView, error)` | `PrepareRebind(input PrepareRebindDTO) (RebindPreparationDTO, error)` | **新增**（架构 §4.4 已定签名） |
| Rebind 执行 | `ApplyRebind(ctx, preparationID) (view.RelationView, error)` | `ApplyRebind(preparationID string) (RelationDTO, error)` | **新增**（架构 §4.4 已定签名） |
| Project 发现 | `DiscoverProjects(ctx, parentDir) ([]view.ProjectCandidateView, error)` | `ProjectService.DiscoverProjects(parentDir string) ([]ProjectCandidateDTO, error)` | **新增** |
| Project 登记 | `RegisterProject(ctx, RegisterEndpointInput) (view.EndpointView, error)` | `ProjectService.RegisterProject(input RegisterEndpointDTO) (EndpointDTO, error)` | **新增** |
| Project 健康 | `GetProjectHealth(ctx, endpointID) (view.EndpointHealthView, error)` | `ProjectService.GetProjectHealth(endpointID string) (EndpointHealthDTO, error)` | **新增** |
| Runtime 发现 | `DiscoverRuntimes(ctx) ([]view.RuntimeCandidateView, error)` | `RuntimeService.DiscoverRuntimes() ([]RuntimeCandidateDTO, error)` | **新增** |
| Runtime 登记 | `RegisterRuntime(ctx, RegisterEndpointInput) (view.EndpointView, error)` | `RuntimeService.RegisterRuntime(input RegisterEndpointDTO) (EndpointDTO, error)` | **新增** |
| Runtime 健康 | `GetRuntimeHealth(ctx, endpointID) (view.EndpointHealthView, error)` | `RuntimeService.GetRuntimeHealth(endpointID string) (EndpointHealthDTO, error)` | **新增** |
| 快照诊断查询 | `GetSnapshotDiagnostics(ctx, relationID, snapshotID) ([]model.Diagnostic, error)` | `GetSnapshotDiagnostics(relationID, snapshotID string) ([]DiagnosticDTO, error)` | **新增**（T07 执行增补，票 #17：mapping_collision 等诊断在快照中可查；跨 Relation 按 not found 处理） |
| hash cache 统计 | `GetHashCacheStats(ctx) (view.HashCacheStatsView, error)` | `GetHashCacheStats() (HashCacheStatsDTO, error)` | **新增**（T07 执行增补，票 #17：命中计数/命中率可查询，为 T14 性能基线供数；进程生命周期累计） |
| 只读计划与冲突解决 | `PrepareSync`/`ResolvePlan`/`GetPlan`（架构 §4.4 已定签名，此前已存在） | 同名 Wails 方法 | **T11 执行增补**（票 #21：`requested_exactness` 三处一致固化 + `/workspaces/:id/plans/:plan_id` 页；DTO 只增 `requested_exactness`，方法数不变） |

服务归属：Project/Runtime 端点用例按架构 §4.2 落在 `internal/application/project`、`internal/application/runtime` 两个新 app 包；`GetChanges`/Mapping/Rebind 留在 `internal/application/sync`。transport 侧对应 `ProjectService`、`RuntimeService` 两个新服务（与 `SyncService` 并列注册）。

## 2. DTO 规格

### 2.1 WorkspaceFeatures / ActionAvailability（内嵌 WorkspaceDTO）

```go
// WorkspaceFeaturesDTO 表达当前版本/平台实现的能力（架构 §10.4）。
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
	Action     string   `json:"action"` // scan|prepare_sync|apply_sync|prepare_restore|apply_restore|rebind
	Available  bool     `json:"available"`
	ReasonCode string   `json:"reason_code,omitempty"`
	ReasonArgs []string `json:"reason_args,omitempty"`
}

// WorkspaceDTO（增补两个字段，其余不变）：
type WorkspaceDTO struct {
	SchemaVersion         int                     `json:"schema_version"`
	Relation              RelationDTO             `json:"relation"`
	State                 WorkspaceStateDTO       `json:"state"`
	Features              WorkspaceFeaturesDTO    `json:"features"`
	Availability          []ActionAvailabilityDTO `json:"availability"`
	LatestProjectSnapshot *SnapshotSummaryDTO     `json:"latest_project_snapshot,omitempty"`
	LatestRuntimeSnapshot *SnapshotSummaryDTO     `json:"latest_runtime_snapshot,omitempty"`
}
```

P1 固定值：`scan=true, sync_preview=true, conflict_inspection=true, conflict_resolution="choose_side"`；`sync_apply/history_view/restore_preview/restore_apply=false`；`materialization_modes=[]`。

Availability 推导规则（后端计算）：

| action | available 条件 | 不可用 reason |
| --- | --- | --- |
| `scan` | 该 relation 无活跃任务，且 health 非 `recovery_required` | `err.scan.already_running`（活跃任务）；`err.recovery.in_progress`（恢复任务占用） |
| `prepare_sync` | `scan_state == "ready"` 且无活跃任务 | `err.scan.incomplete`（未就绪）；`err.scan.already_running` |
| `rebind` | 无活跃任务（健康与否均可主动重绑，路径迁移是合法操作） | `err.scan.already_running`；`err.recovery.in_progress` |
| `apply_sync` / `prepare_restore` / `apply_restore` | 不出现（feature=false，不注册） | — |

### 2.2 资源级 Changes 查询（ChangesPageDTO）

```go
// GetChangesInput 是资源级 Diff 查询（读时计算：head baseline + 指定/最新快照对跑三方 Diff，
// 不存储投影）。快照对缺省取两侧最新；显式传入时必须同属该 relation 且为相对两侧。
type GetChangesInput struct {
	RelationID        string `json:"relation_id"`
	ProjectSnapshotID string `json:"project_snapshot_id,omitempty"`
	RuntimeSnapshotID string `json:"runtime_snapshot_id,omitempty"`
	Classification    string `json:"classification,omitempty"` // diff 分类单值筛选（见 ChangeDTO）
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
	Classification string             `json:"classification"` // 与 diff 包常量一致（§2.2 分类表）
	Base           *RepresentationDTO `json:"base,omitempty"`
	Project        *RepresentationDTO `json:"project,omitempty"`
	Runtime        *RepresentationDTO `json:"runtime,omitempty"`
	Conflicts      []ConflictDTO      `json:"conflicts"`
	Diagnostics    []DiagnosticDTO    `json:"diagnostics"`
}

// DiagnosticDTO 是逐资源诊断（映射冲突/未知格式/低置信度身份等），与快照诊断同构。
type DiagnosticDTO struct {
	Code   string   `json:"code"`
	Args   []string `json:"args"`
	Detail string   `json:"detail"`
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

type ChangesPageDTO struct {
	SchemaVersion int               `json:"schema_version"`
	Items         []ChangeDTO       `json:"items"`
	Summary       ChangesSummaryDTO `json:"summary"`
	NextCursor    string            `json:"next_cursor,omitempty"`
}
```

分类值（沿用 `internal/core/diff` 常量）与计数归组：

| classification | 含义 | summary 归组 |
| --- | --- | --- |
| `noop` | 三态无变化 | noop_count |
| `converged` | 双侧同值收敛 | converged_count |
| `adopt_equal` | 初始化等值采用 | adopt_equal_count |
| `init_choice` | 初始化待用户选择 | init_choice_count |
| `project_to_runtime` | 项目侧新增（runtime 无）/ 或内容变化 | 无 presence → create_count；双侧 present → modify_count |
| `runtime_to_project` | 同上反向 | 同上反向 |
| `remove_runtime_candidate` | 单侧删除候选 | delete_count |
| `remove_project_candidate` | 同上反向 | delete_count |
| `conflict_modify` / `conflict_delete_modify` | 冲突 | conflict_count |

实现注记：`ResourceDiff`（diff 包）只携带 presence 与语义摘要，三态 `RepresentationDTO` 需在实现时从快照资源表联取；排序固定按 `resource_id` 字节序；空结果 `items=[]`。

T09 执行落地（票 #19）：读时计算不写库；显式快照 ID 经 `GetForRelation` 校验（跨关系/跨侧 → `err.changes.snapshot_pair_invalid`），缺省取两侧最新、该侧无快照 → `err.sync.snapshot_not_found`（args {0}=side）；`Base` 表示取基线 project 表示、缺失回退 runtime 表示（与 diff 冲突证据同序）；行内诊断取两侧快照中按 `resource_id` 命中的持久化诊断；分页 cursor 为上一页最后一条 `resource_id`，筛选条件由调用方跨页保持，summary 恒为全量计数；非法 classification/resource_kind 筛选值 → `err.sync.invalid_filter`。

### 2.3 Mapping 读写

```go
// GetMappingPolicy(relationID) → PolicyDTO（复用 dto.go 现有 PolicyDTO，顶层 schema_version）。
// UpdateMappingPolicyInput 是策略写输入（乐观锁：ExpectedRevision 必须等于当前 relation revision）。
type UpdateMappingPolicyInput struct {
	RelationID       string           `json:"relation_id"`
	ExpectedRevision int              `json:"expected_revision"`
	Rules            []MappingRuleDTO `json:"rules"`
}
// 成功 → PolicyDTO；同时递增 relation_revision（ADR-0002：policy 修改是唯一递增源）。
```

语义（按 ADR-0002 决议 5 注释）：`PolicyDTO.Revision`（策略集模板版本）与 `RelationDTO.Revision`（关系级策略代次）语义独立、互不驱动；两个数字都不进入用户可见文案或界面。写路径在 P1-POLICY 编译器落地前只做结构校验；编译器已落地（T04，`internal/application/policy`），`UpdateMappingPolicy` 实现时必须先过编译校验（`err.mapping.compile_failed`，§3）。

编译器落地实况（T04）：编译期校验方向、资源类型、prefix、include/exclude（root-relative glob 编译证明）与 root 边界，mod 语义规则恰好一条且前缀必须 mods；违规返回 `*RuleError` → `err.mapping.compile_failed`（args {0}=rule_id，字段与原因进 detail）。规则决议为「最具体前缀优先」，最长前缀并列无法唯一决议时产出 `diag.mapping.collision` 诊断（证据：并列规则 ID + 命中路径），该路径从观察剔除；诊断随快照持久化，并经 `SyncPlan.diagnostics` / `SyncPlanDTO.diagnostics` 透出（证据性数据，不参与 PlanDigest/SnapshotDigest）。

T10 执行落地（票 #20）：`view.PolicyView` = policy 本体（PolicyID/模板 Revision/Rules）+ `RelationRevision`（关系级策略代次），`PolicyDTO` 增补 `relation_revision`（只增不删，omitempty——预检投影恒 0）；`RelationRevision` 是 mappings 页乐观锁 `expected_revision` 的取值来源（ADR-0002 决议 2：policy 修改是唯一递增源；决议 3：两类修订号都不进入用户可见文案，`err.mapping.stale_revision` 的 locale 文案不插值 {0}/{1}）。写路径单 SQLite 事务（RunInTx）内依序：读关系（`err.relation.not_found`）→ 读当前策略（`err.mapping.not_found`，理论上不可达）→ 组装新策略（Rules 整体替换、策略集身份 PolicyID/模板 Revision 保持不变，ADR-0002 决议 5）→ 编译校验先行（失败即回滚，修订号不前进）→ 乐观锁校验（不等 → `err.mapping.stale_revision`）→ `SavePolicy`（UPSERT + 同事务递增 relations.revision，旧 Plan 立即 stale）；返回保存后投影（含新关系修订）。collision 证据经既有 `GetSnapshotDiagnostics`（票 #17）查询，mappings 页按两侧最新快照取诊断区渲染（哪两条规则并列、命中哪个路径；证据反映最近一次扫描的策略状态）。

### 2.4 Rebind（Prepare/Apply）

```go
// PrepareRebindInput 是重绑定预检输入（一次只重绑一侧）。
type PrepareRebindInput struct {
	RelationID string `json:"relation_id"`
	Side       string `json:"side"`      // project|runtime
	RootPath   string `json:"root_path"` // 新端点根路径
}

// RebindPreparationDTO 是 PrepareRebind 结果。
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

// ApplyRebind(preparationID) → RelationDTO
```

行为语义：

- 预检检查项（复用 `PreparationCheckDTO` 的 code/passed/severity 形态）：新路径存在且可 canonicalize/realpath；新端点 fingerprint 计算；与旧 fingerprint 对比（`fingerprint_changed`）；新端点是否已被其他 Relation 占用（重复 pair 阻止，`err.relation.duplicate_pair`）；legacy materialization 检查。
- **rebind 不递增 relation_revision**（ADR-0002 决议 2：唯一递增源是 policy 修改）；旧 plan 失效由 `expected_bindings` fingerprint 校验覆盖——GetPlan 读取时 binding 不匹配 → `status=stale`。
- `baseline_inheritance`：P1 恒为 `reinitialize`——ApplyRebind 后 `baseline_state="none"`、`diff_state="initialization_required"`，直到完整扫描证明新旧端点逻辑等价（等价证明与继承机制留 Phase 2）。字段保留 `inherit` 取值空间供 Phase 2。
- ApplyRebind：消费 preparation、更新端点与 fingerprint、`health="healthy"`、发布 `relation_invalidated` 事件。

### 2.5 端点发现 / 登记 / 健康

```go
// RegisterEndpointInput 是端点登记输入。
type RegisterEndpointInput struct {
	RootPath string `json:"root_path"` // project: pack.toml 所在目录；runtime: Prism 实例目录
}

// ProjectCandidateDTO 是 Project 发现候选（注册状态按 fingerprint 幂等判定）。
type ProjectCandidateDTO struct {
	DisplayName  string `json:"display_name"`
	RootPath     string `json:"root_path"`
	PackTomlPath string `json:"pack_toml_path"`
	Minecraft    string `json:"minecraft,omitempty"`
	Modloader    string `json:"modloader,omitempty"`
	Registered   bool   `json:"registered"`
	EndpointID   string `json:"endpoint_id,omitempty"`
}

// RuntimeCandidateDTO 是 Runtime 发现候选（注册状态按 adapter identity 幂等判定）。
type RuntimeCandidateDTO struct {
	InstanceID  string `json:"instance_id"`
	DisplayName string `json:"display_name"`
	GameDir     string `json:"game_dir"`
	Minecraft   string `json:"minecraft,omitempty"`
	Modloader   string `json:"modloader,omitempty"`
	Registered  bool   `json:"registered"`
	EndpointID  string `json:"endpoint_id,omitempty"`
}

// EndpointHealthDTO 是端点健康检查结果。
type EndpointHealthDTO struct {
	EndpointID         string `json:"endpoint_id"`
	Status             string `json:"status"` // ok|missing|identity_mismatch
	PathExists         bool   `json:"path_exists"`
	FingerprintMatches bool   `json:"fingerprint_matches"`
	CheckedAt          string `json:"checked_at"`
}
```

- `DiscoverProjects(parentDir)`：在指定目录（缺省为浏览器选择的父目录）内有限深度查找 `pack.toml`，返回候选列表。
- `DiscoverRuntimes()`：从 Prism 实例目录扫描实例。
- `RegisterProject/RegisterRuntime`：**幂等**——fingerprint（`FindProjectByRoot`）/ adapter identity（`FindRuntimeByIdentity`，ports 已存在）命中时返回既有 `EndpointDTO`，不重复登记。
- 健康检查只读，不改状态；`identity_mismatch` 提示用户重绑（relation 侧由 `relation_health` 承担，二者互补）。
- 与 `PrepareRelation` 的关系：**不扩展** `PrepareRelationInput`（继续收 raw path，按需在预检内登记端点）；独立登记服务供 `/sources`、`/runtimes` 端点管理页使用（决议 D5）。

### 2.6 SyncPlanDTO 增补 requested_exactness（P1-3）

```go
// SyncPlanDTO（增补一个字段，其余不变）：
type SyncPlanDTO struct {
	// ... 既有字段 ...
	RequestedExactness string `json:"requested_exactness"` // exact|allow_partial（新增）
}
```

三处一致（硬约束 1）：

| 层 | 变更 |
| --- | --- |
| Plan 模型（`internal/core/model`） | `SyncPlan` 增 `RequestedExactness string` |
| SQLite（`sync_plans`） | 增列 `requested_exactness TEXT NOT NULL DEFAULT 'allow_partial' CHECK (requested_exactness IN ('exact','allow_partial'))`；既有行以保守默认 `allow_partial` 回填 |
| DTO | 上表字段 |

`ResolvePlan` 从 draft plan 继承 exactness（不可变），与既有 digest 语义一起固化。`normalization_version` 与 `policy_digest` 的固化属 P1-PLAN 执行票（`policy_digest` 已存在于 DTO），不在本规格范围。

T11 执行落地（票 #21）：`model.SyncPlan.RequestedExactness`（json `requested_exactness`）+ `sync_plans` 列（v3 迁移 `ALTER TABLE … ADD COLUMN requested_exactness TEXT NOT NULL DEFAULT 'allow_partial' CHECK(...)`，既有行按保守默认回填）+ `SyncPlanDTO.RequestedExactness` 三处一致，并有契约测试（`sqlite_test.go` 列定义/CHECK/回填、`plan_test.go` 缺省与固化、`headless_test.go` 端到端链路）。取值 exact|allow_partial：空值缺省 `allow_partial`（保守），非法值 → `err.sync.invalid_exactness`（§3 增补）；exactness 是请求记录，不参与 PlanDigest（normalize.PlanDigest 排除清单），`ResolvePlan` 从 draft 继承。`/workspaces/:id/plans/:plan_id` 页（shadcn-vue，UX 原型 §7.5 P1）：只读操作/冲突/风险三页签，draft 计划提供 choose_side 冲突决议（`ResolvePlan` 产生全新不可变计划并导航到新 plan_id，旧计划只读不变），stale/expired 内容继续可读、推进控件隐藏并说明原因，页面无 Apply/History/Restore 入口；prepare_sync 入口在工作区列表行与 changes 页头部，由 availability 机制驱动（T07 推导表 + features.sync_preview）。

### 2.7 错误形态

沿用现有双路径，无新 Go 类型：

| 路径 | 形状 | 说明 |
| --- | --- | --- |
| 调用级（Promise rejection） | `errs.AppError` JSON `{code, args, detail}` | 不改码、不新增包装；前端 `api/` 适配层暴露同形 `AppError` TS 接口（复用 `utils/errors.ts` 的解析模式） |
| 逐项（数据字段） | `ProblemDTO`（已有） | 复用 |

## 3. 新增错误码清单

全部进入 `errs` 与 `frontend/src/locales/zh-CN.json`（P1-I18N 执行面落文案）：

| code | args | 场景 |
| --- | --- | --- |
| `err.endpoint.not_found` | {0}=endpoint_id | 端点不存在 |
| `err.endpoint.invalid_path` | {0}=path | 路径无法解析/非目录 |
| `err.endpoint.missing` | {0}=path | 健康检查：路径不存在 |
| `err.endpoint.identity_mismatch` | {0}=endpoint_id | 健康检查：fingerprint 不匹配（提示重绑） |
| `err.endpoint.discovery_failed` | {0}=parent_dir | Project 发现失败 |
| `err.endpoint.instances_dir_not_found` | {0}=path | Prism 实例目录不可定位 |
| `err.mapping.not_found` | {0}=relation_id | 关系无策略（理论上不可达） |
| `err.mapping.stale_revision` | {0}=expected, {1}=actual | 乐观锁冲突 |
| `err.mapping.compile_failed` | {0}=rule_id | 规则编译失败（T04 已生效：字段与原因进 detail） |
| `diag.mapping.collision` | {0}=rule_a, {1}=rule_b（证据另含命中路径 relative_path） | 扫描期多规则无法唯一决议，路径从观察剔除（诊断，非 err.*；随 Snapshot/Plan/DTO 透出，见 §2.3） |
| `err.relation.rebind_prep_not_found` | {0}=preparation_id | 重绑预检不存在 |
| `err.relation.rebind_prep_expired` | {0}=preparation_id | 重绑预检过期 |
| `err.relation.rebind_invalid_side` | {0}=side | side 非 project/runtime |
| `err.relation.prep_expired` | {0}=preparation_id | 创建预检已过期（引导重新预检；ADR-0003 决议 4） |
| `err.relation.prep_consumed` | {0}=preparation_id | 创建预检已被消费（引导刷新，关系可能已建成——双击/双窗口；ADR-0003 决议 4） |
| `err.changes.snapshot_pair_invalid` | — | 快照对不属同 relation 或非同侧（T09，票 #19；detail 携带存储层原因） |
| `err.sync.invalid_filter` | {0}=筛选字段, {1}=筛选值 | GetChanges 筛选值不在合法枚举（classification / resource_kind）（T09，票 #19） |
| `err.sync.invalid_exactness` | {0}=值 | requested_exactness 不在合法枚举 exact|allow_partial（T11，票 #21） |
| `err.recovery.in_progress` | — | 恢复任务占用（availability reason） |
| `err.scan.incomplete` | — | scan_state 非 ready（availability reason） |
| `diag.scan.ignored` | {0}=path | 包内追踪但不在受管范围（index.toml 非 mods/ metafile 条目），从观察剔除（T07，票 #17） |
| `diag.scan.unsupported` | {0}=path | runtime mods 目录中无法按 mod 观察的非 .jar 常规文件（T07，票 #17） |
| `diag.scan.runtime_local` | {0}=path, {1}=resource_id | runtime 本地内容（项目包未包含），以低置信度本地身份观察（T07，票 #17） |

## 4. 硬约束落实对照

| 硬约束 | 落实位置 |
| --- | --- |
| 1. `requested_exactness` 三处一致 | §2.6：Plan 模型字段 + `sync_plans` 列 + `SyncPlanDTO` 字段（`PrepareSync` 已透传，`ResolvePlan` 继承） |
| 2. 顶层 `schema_version` / `[]` / `AppErrorDTO·ProblemDTO` | 全部新 DTO 顶层带 `schema_version`；slice 由 transport 转换层归一 `[]`；错误形态 §2.7 沿用 |
| 3. 未实现能力不暴露 | §2.1：feature=false 的动作不注册、availability 不含未实现动作；`materialization_modes=[]`；P1 无 Apply/Restore 入口 |

## 5. 已决议决策点（2026-08-28）

正文 §2 各节即按下表决议书写：

| 决策 | 决议 | 理由 |
| --- | --- | --- |
| D1 Features/Availability 挂载点 | 内嵌 `WorkspaceDTO` | 列表与详情同构；架构 §10.3 缓存键 `workspaceDetail` 一体缓存 features 与 availability |
| D2 资源级 Changes 形态 | 独立分页 `GetChanges` | 避免列表页全量拉取；支持按分类/路径筛选 |
| D3 rebind 的 baseline 继承 | P1 恒 `reinitialize` | 等价证明需完整扫描，P1 不承担；`inherit` 留 Phase 2 |
| D4 端点用例服务归属 | `ProjectService` + `RuntimeService` 两服务 | 对齐架构 §4.2 目录；两者发现语义不同 |
| D5 PrepareRelation 输入扩展 | 不扩展 `endpoint_id` | P1 无消费方，避免双通道输入漂移；前端接入时再议 |
