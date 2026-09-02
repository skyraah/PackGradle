// Package ports 定义 application 层消费的外围接口。
// 适配器（internal/adapters/*）与存储（internal/store/*）实现这些接口；
// application 只面向接口编排，不 import 具体实现（装配处除外）。
package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"packgradle/internal/core/model"
)

// 仓库哨兵错误：store 实现统一返回（或用 errors.Is 可识别地包装）这些值，
// application 层据此映射 err.* 结构化错误码。
var (
	ErrNotFound           = errors.New("ports: 记录不存在")
	ErrDuplicate          = errors.New("ports: 唯一约束冲突")
	ErrSequenceConflict     = errors.New("ports: 序号冲突（乐观锁拒绝）")
	ErrPreparationExpired   = errors.New("ports: 预检已过期")
	ErrPreparationConsumed  = errors.New("ports: 预检已被消费")
	ErrRelationNotFound     = errors.New("ports: 关系不存在")
	// 完整性守卫哨兵（检视报告 P0-3）：repository 写入边界拒绝污染审计链的对象引用。
	ErrCrossRelation  = errors.New("ports: 引用对象属于另一 Relation")
	ErrSideMismatch   = errors.New("ports: 快照 side 与引用语义不符")
	ErrDigestMismatch = errors.New("ports: 持久化 digest 与重算值不一致")
	ErrParentMismatch = errors.New("ports: parent 对象属于另一 Relation")
	ErrPlanNotFound   = errors.New("ports: 被引用的计划不存在")

	// Phase 2 Apply（ADR-0004）仓库哨兵。
	ErrInvalidTransition    = errors.New("ports: 状态机不允许该迁移")
	ErrConfirmationConsumed = errors.New("ports: 确认令牌已被消费")
	ErrConfirmationExpired  = errors.New("ports: 确认令牌已过期")
)

// PageRequest 是列表分页参数（cursor page）。
type PageRequest struct {
	Cursor string
	Limit  int // <=0 取默认 50，上限 200
}

// DefaultPageLimit 与 MaxPageLimit 是分页边界。
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// NormalizeLimit 归一化分页大小。
func (p PageRequest) NormalizeLimit() int {
	if p.Limit <= 0 {
		return DefaultPageLimit
	}
	if p.Limit > MaxPageLimit {
		return MaxPageLimit
	}
	return p.Limit
}

// ---- 存储仓库 ----

// EndpointRepository 管理项目与运行时端点登记。
type EndpointRepository interface {
	CreateProject(ctx context.Context, p model.Project) error
	GetProject(ctx context.Context, id string) (model.Project, error)
	// FindProjectByRoot 按 binding fingerprint 查找已登记项目（幂等重登记）。
	FindProjectByRoot(ctx context.Context, fingerprint string) (model.Project, bool, error)
	// ListProjects 返回全部已登记项目（display_name 升序）。
	ListProjects(ctx context.Context) ([]model.Project, error)
	CreateRuntime(ctx context.Context, r model.Runtime) error
	GetRuntime(ctx context.Context, id string) (model.Runtime, error)
	// FindRuntimeByIdentity 按 adapter identity（如 Prism 实例目录名）查找。
	FindRuntimeByIdentity(ctx context.Context, adapter, adapterIdentity string) (model.Runtime, bool, error)
	// ListRuntimes 返回全部已登记运行实例（display_name 升序）。
	ListRuntimes(ctx context.Context) ([]model.Runtime, error)
	// UpdateProject 原位更新项目端点绑定（重绑 Apply，契约 03 §2.4）：
	// root_path/display_name/binding_fingerprint 随新位置更新，端点 ID 不变。
	// 不存在返回 ErrNotFound；新 root_path 与其他项目行冲突返回 ErrDuplicate。
	UpdateProject(ctx context.Context, p model.Project) error
	// UpdateRuntime 原位更新运行实例绑定（重绑 Apply）：root_path/adapter_identity/
	// display_name/binding_fingerprint 随新位置更新，端点 ID 不变。错误语义同 UpdateProject。
	UpdateRuntime(ctx context.Context, r model.Runtime) error
}

// RelationRepository 管理 Relation 聚合根。
type RelationRepository interface {
	Create(ctx context.Context, rel model.Relation) error
	Get(ctx context.Context, id string) (model.Relation, error)
	List(ctx context.Context, page PageRequest) ([]model.Relation, string, error) // items, nextCursor
	UpdateHealth(ctx context.Context, id string, health model.RelationHealth) error
	IncrementRevision(ctx context.Context, id string) (int, error)
	PairExists(ctx context.Context, projectID, runtimeID string) (bool, error)
	// UpdateHeadBaseline 设置/清除关系基线引用（空串清除）。重绑 Apply 用它重置基线
	// （契约 03 §2.4：P1 恒 reinitialize，不继承）；Phase 2 Apply 产生基线时复用写入。
	UpdateHeadBaseline(ctx context.Context, id, baselineID string) error
	// UpdateHeadCommit 设置关系头提交引用（Phase 2 Apply committed 收口写，
	// redesign §6.6 步骤 5：与新 Baseline/Commit/object refs 同一事务）。空串清除。
	UpdateHeadCommit(ctx context.Context, id, commitID string) error
	// UpdateAuthorizedApply 切换工作区授权开关（schema v6 列；契约 06 §3.6，票 #57）。
	// 只写列不做门禁（恢复期开关值保留，入口由 recovery 门禁挡）；关系不存在返回 ErrNotFound。
	UpdateAuthorizedApply(ctx context.Context, id string, enabled bool) error
}

// RetentionSettingsStore 是保留策略设置的存取端口（config.toml [retention] 承载，
// ADR-0007 §8；appconfig.ConfigManager 实现）。默认值/范围校验由实现方承担
// （加载层与写入层同款，契约 06 §3.6）：单键越界返回携带字段名的
// err.settings.retention_invalid 结构化错误。
type RetentionSettingsStore interface {
	// Retention 读取并归一保留设置：未写键取默认值，越界键整体拒绝。
	Retention() (model.RetentionSettings, error)
	// SetRetention 校验后整体替换五键并持久化，返回生效值。
	SetRetention(s model.RetentionSettings) (model.RetentionSettings, error)
}

// SnapshotRepository 持久化不可变观察快照。
type SnapshotRepository interface {
	// Insert 在同一事务写 observed_snapshots 与 resource_representations。
	Insert(ctx context.Context, s model.ObservedSnapshot) error
	Get(ctx context.Context, id string) (model.ObservedSnapshot, error)
	// GetForRelation 校验快照属于指定 Relation 且 side 匹配，否则返回 not found 语义错误。
	GetForRelation(ctx context.Context, id, relationID string, side model.Side) (model.ObservedSnapshot, error)
	LatestByRelationSide(ctx context.Context, relationID string, side model.Side) (model.ObservedSnapshot, bool, error)
}

// BaselineRepository 持久化同步基线（P1 仅供测试与建模，Apply 在 Phase 2 产生）。
type BaselineRepository interface {
	Insert(ctx context.Context, b model.SyncBaseline) error
	Get(ctx context.Context, id string) (model.SyncBaseline, error)
}

// PlanRepository 持久化不可变计划（draft/resolved 均为新行）。
type PlanRepository interface {
	Insert(ctx context.Context, p model.SyncPlan) error // 同事务展开 conflicts 行
	Get(ctx context.Context, id string) (model.SyncPlan, error)
	// CountByRelation 统计关系下仍可推进（draft/resolved）的计划数——重绑预检的
	// invalidated_plan_count 数据源（这些计划将因绑定指纹失配投影为 stale）。
	CountByRelation(ctx context.Context, relationID string) (int, error)
	// ListByRelation 返回该 Relation 的全部计划（id 升序 = 创建序，ULID 单调）。
	// apply_sync availability 计划面推导（存在可应用计划，契约 05 §1）的数据源。
	ListByRelation(ctx context.Context, relationID string) ([]model.SyncPlan, error)
}

// TaskRepository 持久化任务（长操作事实源）。
type TaskRepository interface {
	Insert(ctx context.Context, t model.Task) error
	// Update 以 Sequence 为乐观锁：必须大于库中当前值，否则返回冲突错误。
	Update(ctx context.Context, t model.Task) error
	Get(ctx context.Context, id string) (model.Task, error)
	ListByRelation(ctx context.Context, relationID string, active bool, page PageRequest) ([]model.Task, string, error)
	FindActiveByRelationAndKind(ctx context.Context, relationID, kind string) (model.Task, bool, error)
	// FindActiveByKind 查找全局（跨关系）指定类别的活跃任务（GC 全局单飞，
	// 票 #64：同一时刻至多一个 gc 任务排队/执行）。relation_id IS NULL 的
	// 任务也在查找范围。
	FindActiveByKind(ctx context.Context, kind string) (model.Task, bool, error)
	// ListActiveAll 返回全部 queued/running 任务（启动恢复用）。
	ListActiveAll(ctx context.Context) ([]model.Task, error)
}

// MappingRepository 管理 Relation 的 MappingPolicy（修订与 relation.revision 同事务联动）。
// ADR-0002：创建时的初始 policy 写入不算修改，不递增 revision；
// 之后每次 SavePolicy（修改）在同一事务内递增 relations.revision。
type MappingRepository interface {
	GetPolicy(ctx context.Context, relationID string) (model.MappingPolicy, error)
	// CreatePolicy 写入创建时的初始 policy（INSERT，不递增 revision）；
	// 关系不存在返回 ErrNotFound，已有 policy 返回 ErrDuplicate。
	CreatePolicy(ctx context.Context, relationID string, p model.MappingPolicy) error
	// SavePolicy 保存策略修改（UPSERT）并在同一事务内递增 relations.revision，
	// 使旧 Plan 立即 stale（§8.3：映射修订与关系修订必须同事务联动）。
	SavePolicy(ctx context.Context, relationID string, p model.MappingPolicy) error
}

// PreparationRepository 持久化 Relation 创建预检（Prepare/Apply 两段式）。
type PreparationRepository interface {
	Insert(ctx context.Context, p model.RelationPreparation) error
	Get(ctx context.Context, id string) (model.RelationPreparation, error)
	// MarkConsumed 消费预检；过期或已消费返回相应语义错误。
	MarkConsumed(ctx context.Context, id string) error
	// InsertRebind 持久化重绑预检（rebind_preparations 表，Prepare/Apply 两段式）。
	InsertRebind(ctx context.Context, p model.RebindPreparation) error
	// GetRebind 按 id 读取重绑预检；不存在返回 ErrNotFound。
	GetRebind(ctx context.Context, id string) (model.RebindPreparation, error)
	// MarkRebindConsumed 消费重绑预检；过期或已消费返回相应语义错误（ADR-0003 决议 4 拆码）。
	MarkRebindConsumed(ctx context.Context, id string) error
}

// HashCacheKey 是可丢弃扫描缓存的键：仅 (root fingerprint, path, size, mtime, filekey)
// 全部一致时才允许复用 hash。缓存只是性能优化，不是事实来源。
type HashCacheKey struct {
	RootFingerprint string
	RelativePath    string // 规范化小写
	SizeBytes       int64
	MtimeUnixNano   int64
	FileKey         string // 平台文件标识，取不到为 ""
}

// HashCacheEntry 是一条缓存记录。
type HashCacheEntry struct {
	Key    HashCacheKey
	Digest string // 内容 digest（算法固定 sha256，MVP 不缓存其它算法）
}

// HashCacheRepository 是可丢弃扫描缓存（DeleteAll 支持验收：删缓存后可从端点重建）。
type HashCacheRepository interface {
	Lookup(ctx context.Context, key HashCacheKey) (string, bool, error)
	Save(ctx context.Context, entries []HashCacheEntry) error
	DeleteAll(ctx context.Context) error
}

// TaskEventRepository 持久化事件并原子分配 stream_sequence。
type TaskEventRepository interface {
	Append(ctx context.Context, env model.EventEnvelope) (int64, error) // 返回分配的 stream_sequence
}

// ---- Apply 执行仓库（Phase 2，ADR-0004 事实模型，schema v5）----

// ApplyRunRepository 持久化 Apply 运行头（apply_runs，一行 = 一次 Apply，
// task_id 即 run_id）。六阶段状态机沿 ADR-0004 §5：prepared→staged→applying→
// verifying→committed，失败入 recovery_required；终态不可再迁移。
type ApplyRunRepository interface {
	Insert(ctx context.Context, run model.ApplyRun) error
	Get(ctx context.Context, taskID string) (model.ApplyRun, error)
	// LatestByRelation 返回该 Relation 当前/最近一次运行（created_at 最新）。
	LatestByRelation(ctx context.Context, relationID string) (model.ApplyRun, bool, error)
	// LatestByPlan 返回该计划当前/最近一次运行（created_at 最新，task_id 决胜）；
	// 无运行返回 ok=false。ConfirmPlan 幂等重入三分支按「本计划的运行」判定
	// （契约 05 §3.1 D4：活跃重入 / committed 拆码 / recovery 拆码）。
	LatestByPlan(ctx context.Context, planID string) (model.ApplyRun, bool, error)
	// AdvanceState 沿六阶段状态机推进运行阶段；非法迁移返回 ErrInvalidTransition，
	// 运行不存在返回 ErrNotFound。
	AdvanceState(ctx context.Context, taskID, state, updatedAt string) error
	// SetRecoveryRefs 落运行级恢复对象引用（ADR-0004 §1/§3：CAS/staging 引用集合，
	// 引擎在 staged 前收集；JSON 形状由引擎定义，仓储原样保存）。
	SetRecoveryRefs(ctx context.Context, taskID string, refs json.RawMessage, updatedAt string) error
	// MarkStagingCleared 将 staging_cleared 记为事实（提交事务成功后清理，ADR-0004 §5）。
	MarkStagingCleared(ctx context.Context, taskID, updatedAt string) error
	// MarkAcknowledged 记录人工确认时间（恢复收口唯一出口，契约 05 §6）；
	// 幂等，保留首次确认时间。
	MarkAcknowledged(ctx context.Context, taskID, acknowledgedAt, updatedAt string) error
	// AttachCommit 在 committed 收口时回填提交引用；提交不存在返回 ErrNotFound。
	AttachCommit(ctx context.Context, taskID, commitID, updatedAt string) error
}

// OperationJournalRepository 管理逐操作当前行与追加历史（ADR-0004 §2 三层语义）。
// 历史表 operation_journal_events 只追加：本接口不提供任何改写/删除历史的方法，
// 当前操作行只按状态机推进（先持久化意图，再执行文件动作）。
type OperationJournalRepository interface {
	// InsertBatch 在同一事务写入一整批操作行（初始持久化意图，缺省 status=pending）
	// 并为每行落初始历史事件（occurredAt 为意图持久化时间）。
	InsertBatch(ctx context.Context, ops []model.JournalOperation, occurredAt string) error
	// AdvanceStatus 在同一事务内先追加历史事件、再推进当前行状态（先持久化意图）。
	// 非法迁移返回 ErrInvalidTransition；操作不存在返回 ErrNotFound。
	AdvanceStatus(ctx context.Context, taskID, operationID, toStatus, occurredAt string, detail json.RawMessage) error
	// GetOperation 读取单操作当前投影；不存在返回 ErrNotFound。
	GetOperation(ctx context.Context, taskID, operationID string) (model.JournalOperation, error)
	// ListByTask 按 ordinal 升序分页读取逐操作当前投影（cursor 为最后一条 ordinal）。
	ListByTask(ctx context.Context, taskID string, page PageRequest) ([]model.JournalOperation, string, error)
	// ListEvents 按序号升序返回任务的全部追加历史（审计与恢复解释）。
	ListEvents(ctx context.Context, taskID string) ([]model.JournalEvent, error)
	// LastEvent 回答「最后一个已持久化意图是什么」（任务内 seq 最大的一条；
	// 无历史返回 ok=false）。
	LastEvent(ctx context.Context, taskID string) (model.JournalEvent, bool, error)
	// MarkResult 记录单操作的终局结果摘要（result_json 列；失败带说明码，成功
	// 留空）。只写当前行，不改状态、不追加历史——状态推进仍走 AdvanceStatus。
	// 操作不存在返回 ErrNotFound。
	MarkResult(ctx context.Context, taskID, operationID string, result json.RawMessage) error
}

// CommitRepository 持久化 SyncCommit 提交图（sync_commits + commit_changes 收口，
// 契约 05 §7 D3）。
type CommitRepository interface {
	// Insert 在同一事务写 sync_commits 与 commit_changes（零消费表收口第一步）。
	// 跨 Relation 引用被守卫拒绝（ErrCrossRelation 系）。
	Insert(ctx context.Context, c model.SyncCommit) error
	// GetForRelation 读取单提交含逐资源 changes（联 resource_representations 取资源
	// 身份）；不存在或属于其他 Relation 一律 ErrNotFound（契约 05 err.commit.not_found 口径）。
	GetForRelation(ctx context.Context, commitID, relationID string) (model.SyncCommit, error)
	// ListByRelation 按 Relation 分页列出提交头（不含 changes；id 升序，cursor 为最后一条 id）。
	ListByRelation(ctx context.Context, relationID string, page PageRequest) ([]model.SyncCommit, string, error)
	// InsertObjectRefs 写 object_refs 引用行（owner = 提交，purpose 引擎定义）。
	// ADR-0004 §6：引用只指向已落盘就绪（state='ready'）的 CAS 对象——悬挂引用被
	// 外键拒绝。 redesign §6.6 步骤 5：与新 Baseline/Commit 同一事务写入。
	InsertObjectRefs(ctx context.Context, ownerType, ownerID string, refs []ObjectRefRow) error
}

// ObjectRefRow 是 object_refs 的一行（CAS 对象引用；形状照 §8.3 冻结 DDL）。
type ObjectRefRow struct {
	Algorithm string
	Digest    string
	Purpose   string
	Size      int64
}

// GCObjectRef 是 GC 决策/根集计算用的对象引用投影（object_refs 联 objects 取
// size；OwnerID 为引用提交 id，owner_type 恒 commit——GC 域无其他 owner）。
type GCObjectRef struct {
	OwnerID string
	Digest  string
	Size    int64
}

// GCRepository 是 GC 引擎的存储面（票 #64）：修剪决策输入读取、级联删除执行、
// 对象账目与隔离态操作。隔离（quarantined）即回收账目（ADR-0007 §5），零新表。
type GCRepository interface {
	// ---- 修剪决策输入（per relation）----

	// RelationCommitsChain 返回关系全部存活提交（id 升序 = 链序 oldest-first，
	// ULID 单调保证创建序即链序）。
	RelationCommitsChain(ctx context.Context, relationID string) ([]model.SyncCommit, error)
	// RelationObjectRefs 返回关系全部存活提交的 object_refs 行（联 objects 取 size）。
	RelationObjectRefs(ctx context.Context, relationID string) ([]GCObjectRef, error)
	// RelationUsageBytes 返回关系存活提交引用对象的字节占用：SUM(objects.size)
	// over 去重 digest（ADR-0007 §2 关系占用口径；CAS 跨关系去重按去重记账）。
	RelationUsageBytes(ctx context.Context, relationID string) (int64, error)
	// ProtectedBaselineIDs 返回「屏障」基线集合：relations.head_baseline_id ∪
	// 活跃（draft/resolved）计划的 base_baseline_id——前者是头提交天然引用，
	// 后者是保护根集的计划引用通道（ADR-0007 §4；红线：活跃计划引用不回收）。
	ProtectedBaselineIDs(ctx context.Context, relationID string) ([]string, error)

	// ---- 修剪级联执行（单事务，FK 顺序：先重连、先提交后基线）----

	// ApplyPrune 在单事务内执行级联删除（core/gc.PruneDecision 的执行形态）：
	// 1) 重连——首个存活提交 parent_id/previous_baseline_id 置空、其结果基线
	//    parent_id 置空、被裁行自身 parent 引用置空、失效计划（非活跃）的
	//    base_baseline_id 置空、被裁提交对应 apply_runs.commit_id 置空
	//    （SQLite 立即外键要求先解除全部指向被裁行的引用）；
	// 2) 先删提交侧（commit_changes → object_refs → sync_commits），
	//    后删基线侧（baseline_resources → sync_baselines）——「先提交后基线」
	//    的 FK 顺序（ADR-0007 执行要点）。tasks/apply_runs 的 commit_id 列
	//    均有外键（tasks 自 schema v2 起 REFERENCES sync_commits），删行前
	//    置空；墓碑计数由 PrunedBeforeCount 读时推导承担。
	ApplyPrune(ctx context.Context, relationID string, prunedCommits, droppedBaselines []string,
		reconnectCommitID, reconnectBaselineID string) error
	// PrunedBeforeCount 返回按保留策略已清理的提交数（墓碑行数据源，契约 06
	// §3.8）：读时推导「committed 运行数 − 现存提交数」——每个 committed 运行
	// 恰产生一个提交（AttachCommit 1:1），删提交须置空 tasks/apply_runs 的
	// commit_id（schema v2 起 tasks.commit_id 有外键），两侧行均永不删除，
	// 差值即被裁数（ADR-0007「零新表零迁移」约束下的读时推导）。
	PrunedBeforeCount(ctx context.Context, relationID string) (int, error)

	// ---- 对象账目与隔离态（ADR-0007 §5 删除协议）----

	// ReadyDigests 返回全部 state='ready' 对象的 digest（GC 候选集底册）。
	ReadyDigests(ctx context.Context) ([]string, error)
	// BaselineDigestHits 返回存活基线（存活提交结果基线 ∪ 屏障基线）的
	// baseline_resources.logical_digest 中命中 objects 表的部分（去重）——
	// 保护根集 1 的基线通道（ADR-0007 §4）。
	BaselineDigestHits(ctx context.Context, relationIDs []string) ([]string, error)
	// PlanBaseDigestHits 返回活跃计划（单活跃口径，同 ProtectedBaselineIDs）
	// base 基线的 baseline_resources.logical_digest 中命中 objects 表的部分
	//（去重）——活跃计划引用通道的对象面：即使屏障失效导致 base 基线随提交
	// 被裁，其引用的对象在计划活跃期间也不回收（ADR-0007 §4 红线的对象账目）。
	PlanBaseDigestHits(ctx context.Context, relationIDs []string) ([]string, error)
	// UnresolvedRunRefs 返回活跃/未处置运行的恢复引用 digest（去重）：
	// apply_runs.state ∉ {committed}（活跃运行与未处置 recovery_required）的
	// recovery_refs_json 中 kind=cas 条目。解析在 Go 侧（JSON 形状引擎定义）。
	UnresolvedRunRefs(ctx context.Context) ([][]byte, error)
	// JournalCASRefs 返回运行日志恢复引用中的 cas digest 原始 JSON（去重前的
	// 全部行，Go 侧解析 kind=cas 条目）——恢复引用的 journal 通道。
	JournalCASRefs(ctx context.Context) ([][]byte, error)
	// QuarantineObjects 单事务把候选对象 ready→quarantined（WHERE state='ready'
	// 保可重入）；返回实际标记数。Has() 只认 ready：标记完成即对 restore/apply
	// 不可见（ADR-0007 §5 步骤 1）。
	QuarantineObjects(ctx context.Context, digests []string) (int64, error)
	// ListQuarantined 返回全部隔离对象（digest、size；入回收站与对账的账目侧）。
	ListQuarantined(ctx context.Context) ([]GCObjectRef, error)
	// RestoreObject 复活对象：quarantined→ready（人工复活 CLI 用；Put 幂等
	// 复活走 CAS.Put 的 UPSERT，不经此方法）。
	RestoreObject(ctx context.Context, digest string) error
	// PurgeQuarantinedRows 物理删除隔离行（超期清除随删，ADR-0007 §5 步骤 3；
	// row-without-file 对账删行同通道）：仅删零引用行（object_refs 外键兜底）。
	PurgeQuarantinedRows(ctx context.Context, digests []string) error
	// ObjectState 查询单对象状态（"" = 无行）。
	ObjectState(ctx context.Context, digest string) (string, error)
	// ReferencedMissingRows 返回被存活引用指向但文件缺失的 ready 行 digest
	//（row-without-file 且被引用：不删行——Has() 已按文件缺失返回不可见，
	// restore 走既有降级分支；返回供引擎记账）。
	ReferencedMissingRows(ctx context.Context, digests []string) ([]string, error)
	// HasUnresolvedRuns 回答安全窗口构成项：存在未收口运行（apply_runs.state
	// ∉ {'committed'}）——活跃 Apply/Restore run 与未处置 recovery_required
	// 都算（ADR-0007 §3 安全窗口＝无活跃 run ∧ 无 recovery_required）。
	HasUnresolvedRuns(ctx context.Context) (bool, error)

	// ---- 孤儿快照清扫（ADR-0011 §4，票 #89；挂 GC 清扫阶段）----

	// SnapshotRefFacts 采集孤儿快照判定的引用图事实（core/gc.OrphanSnapshots
	// 的输入侧）：全部快照 id、存活提交验证快照（verified_*_snapshot_id）、
	// 现存计划输入快照（input_*_snapshot_id）、各 relation 每端最新一份
	//（captured_at DESC, id DESC 取 1，与 LatestByRelationSide 同序）。
	SnapshotRefFacts(ctx context.Context) (SnapshotGCFacts, error)
	// DeleteSnapshots 单事务物理删除快照行并随行级联删资源表示行
	//（resource_representations PK 前缀即 snapshot_id，先子后父）。
	DeleteSnapshots(ctx context.Context, snapshotIDs []string) error
}

// SnapshotGCFacts 是孤儿快照判定的引用图事实采集投影（GC 引擎清扫阶段采集，
// core/gc.OrphanSnapshots 判定；与 core/gc.SnapshotFacts 同形异型——决策包
// 不依赖 application 端口）。
type SnapshotGCFacts struct {
	All            []string // observed_snapshots 全部快照 id
	CommitVerified []string // 存活提交验证快照（verified_project/runtime_snapshot_id 并集）
	PlanInput      []string // 现存计划输入快照（input_project/runtime_snapshot_id 并集）
	Latest         []string // 各 relation 每端最新一份
}

// CleanupRepository 是惰性清理通道的存储面（ADR-0011 §2/§3，票 #89）：
// task_events 条数窗口截断 + 旧数据行物理删除（判定 = 过期 ∧ 无存活引用，
// SQL 内联守卫）。可选依赖：nil 时清理通道整体禁用（未装配清理面的测试栈
// 零波及）；触发时机（启动时 + 任务终态后）归 application 编排。
type CleanupRepository interface {
	// TruncateTaskEvents 保最近 keep 条（按 stream_sequence 留尾，ADR-0011 §2）。
	// 现存不足 keep 条时空转；截断后序号从 MAX+1 续（Append 既有硬约束，
	// 清全表则从 1 重来——前端重启以首个事件建基线，皆不误判漏包）。
	// 返回删除行数。
	TruncateTaskEvents(ctx context.Context, keep int) (int64, error)
	// DeleteExpiredPlans 物理删除过期/修订过时（读取时投影 expired/stale）的
	// 历史计划行：applied/confirmed 行随其运行与提交存亡（apply_runs.plan_id
	// NOT NULL + 运行行永不删 → 结构上永久保留；sync_commits.plan_id 命中的
	// 行同样保留）、存活子计划（resolved_from_plan_id）钉住的 draft 保留；
	// 随行删 conflicts 与 plan_confirmations。返回删除行数。
	DeleteExpiredPlans(ctx context.Context, now string) (int64, error)
	// DeleteExpiredPreparations 删除过期（expires_at ≤ now）或已消费
	//（consumed_at 非空）的预检行——创建预检（preparations）与重绑预检
	//（rebind_preparations）同一判定（两表均无被引用外键）。返回删除行数。
	DeleteExpiredPreparations(ctx context.Context, now string) (int64, error)
	// PruneTerminalTasks 终态任务行保最近 keep 条（created_at DESC, id DESC
	// 留尾，ADR-0011 §3）：被 apply_runs（PK 即 task_id，行永不删）或
	// operation_journal（append-only）引用的行结构上不可删、守卫跳过。
	// 返回删除行数。
	PruneTerminalTasks(ctx context.Context, keep int) (int64, error)
}

// GCTrashEntry 是回收站目录条目（digest 从文件名复原，文件 mtime 即
// trash_days 时钟起点，ADR-0007 §5）。
type GCTrashEntry struct {
	Digest     string
	Path       string
	ModifiedAt time.Time
	SizeBytes  int64
}

// GCObjectFile 是盘上一个对象文件（孤儿三向清扫的 file-without-row 对账用；
// digest 从路径文件名复原，ADR-0007 §6）。
type GCObjectFile struct {
	Digest string
	Path   string
}

// GCTrash 是回收站与盘面对象操作的端口（ADR-0007 §5/§6 落盘侧；objectstore.CAS
// 实现）。全部方法带路径参数/返回值，时间由文件 mtime 承载（无时钟注入点）。
// 幂等语义见各实现注释：GC 全程可重入，任一步崩溃下一轮重扫自然续上。
type GCTrash interface {
	// MoveToTrash 把对象文件 zstd 压缩移入回收站并删原文件（对象文件缺失返回
	// os.ErrNotExist 语义错误，row-without-file 场景由调用方改走删行对账）。
	MoveToTrash(digest string) error
	// RestoreFromTrash 把回收站副本解压回对象位置（人工复活；对象已在盘幂等）。
	RestoreFromTrash(digest string) error
	// ListTrash 遍历回收站全部条目（目录不存在视为空）。
	ListTrash() ([]GCTrashEntry, error)
	// DeleteTrashEntry 物理删除单个回收站文件（超期清除；DB 隔离行由调用方
	// 在文件删除成功后随删）。
	DeleteTrashEntry(entry GCTrashEntry) error
	// ListObjectFiles 遍历对象库全部在盘文件（digest 从路径复原；非法文件名跳过）。
	ListObjectFiles() ([]GCObjectFile, error)
	// ListTmpFiles 返回对象根目录下 .tmp-* 写中断残渣。
	ListTmpFiles() ([]string, error)
	// DeleteFile 删除盘上文件（不存在视为成功）。
	DeleteFile(path string) error
}

// PlanConfirmationRepository 持久化计划确认令牌（plan_confirmations 收口，
// 契约 05 §7 D4，ConfirmPlan 幂等重入）。
type PlanConfirmationRepository interface {
	Insert(ctx context.Context, c model.PlanConfirmation) error
	// ListByPlan 返回该计划的全部确认记录（confirmed_at 升序）。
	ListByPlan(ctx context.Context, planID string) ([]model.PlanConfirmation, error)
	// MarkConsumed 消费确认令牌：仅未消费且未过期时可消费；不存在返回 ErrNotFound、
	// 已消费返回 ErrConfirmationConsumed、已过期返回 ErrConfirmationExpired。
	MarkConsumed(ctx context.Context, planID, token string) error
}

// Repos 是单个事务域内可用的仓库集合（UnitOfWork.RunInTx 闭包参数），
// 字段与 AppDeps 的同名仓库一一对应，但全部绑定同一事务。
type Repos struct {
	Endpoints         EndpointRepository
	Relations         RelationRepository
	Snapshots         SnapshotRepository
	Baselines         BaselineRepository
	Plans             PlanRepository
	Tasks             TaskRepository
	Mappings          MappingRepository
	Preparations      PreparationRepository
	HashCache         HashCacheRepository
	Events            TaskEventRepository
	ApplyRuns         ApplyRunRepository
	Journal           OperationJournalRepository
	Commits           CommitRepository
	PlanConfirmations PlanConfirmationRepository
}

// UnitOfWork 是跨仓库的单事务边界（ADR-0003：多步元数据写入 doctrine）。
// 闭包内的全部写入随事务提交或回滚；事件发布一律在事务提交成功之后由
// 调用方执行（发布失败不影响提交，事件不是事实源）。
type UnitOfWork interface {
	RunInTx(ctx context.Context, fn func(repos Repos) error) error
}

// ---- 适配器 ----

// FileFacts 是文件的观察事实（供 hash cache 判定）。
type FileFacts struct {
	SizeBytes          int64
	ModifiedAtUnixNano int64
	FileKey            string // 平台文件标识（如同卷 file index），取不到为 ""
}

// FileHasher 流式计算文件内容指纹（大文件不得整读内存）。
type FileHasher interface {
	HashFile(ctx context.Context, absPath string) (model.ContentRef, FileFacts, error)
	// FileKey 返回平台文件标识（Windows 卷+file index / Unix dev+ino）；
	// 取不到为 ""。hash cache 键的 file identity 通道（检视 P1-5）。
	FileKey(absPath string) string
}

// BindingFingerprinter 计算端点绑定指纹（卷/文件 identity + 规范化路径）。
type BindingFingerprinter interface {
	Fingerprint(rootPath string) (string, error) // "sha256:<hex>"
}

// EndpointNormalizer 是端点路径规范化管线的强制入口（检视 P0-4）：
// 相对输入绝对化 → realpath（symlink/junction/reparse 全解析）→ 目录存在性校验。
// 端点登记、绑定指纹与包含关系判定一律以返回的 canonical 路径为准。
type EndpointNormalizer interface {
	NormalizeEndpointPath(rootPath string) (string, error)
}

// ScanHint 是 application 传给 Runtime 扫描器的跨侧身份提示：
// key 为 pw.toml filename 字段的小写值，value 为对应 ResourceID。
// 这是唯一的跨侧身份匹配通道；core/diff 不做路径→身份推断。
type ScanHint struct {
	FilenameToResourceID map[string]string
}

// ScanOptions 是扫描输入。
type ScanOptions struct {
	Policy model.MappingPolicy
	Hint   ScanHint // 仅 Runtime 侧使用
	// HashFile 由 application 注入（带 hash cache 闭包）；nil 时扫描器必须自行报错或跳过内容指纹。
	HashFile func(ctx context.Context, absPath string) (model.ContentRef, FileFacts, error)
}

// ProjectScanner 扫描 Packwiz 项目端点。
type ProjectScanner interface {
	Name() string
	Version() string
	Scan(ctx context.Context, root string, opts ScanOptions) (model.ScanReport, error)
}

// RuntimeScanner 扫描运行时端点（Prism 实例游戏目录）。
type RuntimeScanner interface {
	Name() string
	Version() string
	Scan(ctx context.Context, root string, opts ScanOptions) (model.ScanReport, error)
}

// EventPublisher 发布事件（transport 桥接到 Wails；headless 测试用内存实现）。
type EventPublisher interface {
	Publish(ctx context.Context, env model.EventEnvelope) error
}

// ---- 端点发现 ----

// ProjectCandidate 是项目源发现候选（adapter 层产出；登记状态由 application 判定）。
type ProjectCandidate struct {
	DisplayName  string // pack.toml 的 name，缺失回退目录名
	RootPath     string // pack.toml 所在目录（绝对路径，未规范化）
	PackTomlPath string // pack.toml 完整路径
	Minecraft    string // pack.toml [versions].minecraft，缺失为空
	Modloader    string // [versions] 中的加载器键名（fabric/forge/quilt/neoforge），缺失为空
}

// RuntimeCandidate 是运行实例发现候选（adapter 层产出；登记状态由 application 判定）。
type RuntimeCandidate struct {
	InstanceID  string // 实例目录名（Prism 实例的事实 ID）
	InstanceDir string // 实例目录完整路径（登记输入 root_path 的取值来源）
	DisplayName string // instance.cfg 的 name，缺失回退目录名
	GameDir     string // <实例>/minecraft
	Minecraft   string // mmc-pack.json net.minecraft 组件版本，缺失为空
	Modloader   string // mmc-pack.json 加载器组件短名（fabric/forge/quilt/neoforge），缺失为空
}

// ProjectDiscovery 发现磁盘上的 Packwiz 项目源（/sources 页发现入口）。
type ProjectDiscovery interface {
	// DiscoverProjects 在 parentDir 内有限深度查找含 pack.toml 的项目根目录。
	DiscoverProjects(ctx context.Context, parentDir string) ([]ProjectCandidate, error)
}

// RuntimeDiscovery 定位 Prism 实例目录并枚举运行实例候选（/runtimes 页发现入口）。
type RuntimeDiscovery interface {
	// DiscoverRuntimes 从 Prism 实例目录扫描实例（实例根目录不可定位返回错误）。
	DiscoverRuntimes(ctx context.Context) ([]RuntimeCandidate, error)
}

// InstancesDirError 是 RuntimeDiscovery 定位/读取实例根目录失败的结构化错误：
// DataDir 为尝试定位的 Prism 数据目录（application 映射 err.endpoint.instances_dir_not_found
// 的 args {0}=path），Err 为底层原因（Unwrap 可达）。
type InstancesDirError struct {
	DataDir string
	Err     error
}

func (e *InstancesDirError) Error() string {
	if e.Err != nil {
		return "ports: Prism 实例根目录不可定位: " + e.DataDir + ": " + e.Err.Error()
	}
	return "ports: Prism 实例根目录不可定位: " + e.DataDir
}

func (e *InstancesDirError) Unwrap() error { return e.Err }
