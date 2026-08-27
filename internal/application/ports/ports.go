// Package ports 定义 application 层消费的外围接口。
// 适配器（internal/adapters/*）与存储（internal/store/*）实现这些接口；
// application 只面向接口编排，不 import 具体实现（装配处除外）。
package ports

import (
	"context"
	"errors"

	"packgradle/internal/core/model"
)

// 仓库哨兵错误：store 实现统一返回（或用 errors.Is 可识别地包装）这些值，
// application 层据此映射 err.* 结构化错误码。
var (
	ErrNotFound           = errors.New("ports: 记录不存在")
	ErrDuplicate          = errors.New("ports: 唯一约束冲突")
	ErrSequenceConflict   = errors.New("ports: 序号冲突（乐观锁拒绝）")
	ErrPreparationExpired = errors.New("ports: 预检已过期或已消费")
	ErrRelationNotFound   = errors.New("ports: 关系不存在")
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
	CreateRuntime(ctx context.Context, r model.Runtime) error
	GetRuntime(ctx context.Context, id string) (model.Runtime, error)
	// FindRuntimeByIdentity 按 adapter identity（如 Prism 实例目录名）查找。
	FindRuntimeByIdentity(ctx context.Context, adapter, adapterIdentity string) (model.Runtime, bool, error)
}

// RelationRepository 管理 Relation 聚合根。
type RelationRepository interface {
	Create(ctx context.Context, rel model.Relation) error
	Get(ctx context.Context, id string) (model.Relation, error)
	List(ctx context.Context, page PageRequest) ([]model.Relation, string, error) // items, nextCursor
	UpdateHealth(ctx context.Context, id string, health model.RelationHealth) error
	IncrementRevision(ctx context.Context, id string) (int, error)
	PairExists(ctx context.Context, projectID, runtimeID string) (bool, error)
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
}

// TaskRepository 持久化任务（长操作事实源）。
type TaskRepository interface {
	Insert(ctx context.Context, t model.Task) error
	// Update 以 Sequence 为乐观锁：必须大于库中当前值，否则返回冲突错误。
	Update(ctx context.Context, t model.Task) error
	Get(ctx context.Context, id string) (model.Task, error)
	ListByRelation(ctx context.Context, relationID string, active bool, page PageRequest) ([]model.Task, string, error)
	FindActiveByRelationAndKind(ctx context.Context, relationID, kind string) (model.Task, bool, error)
	// ListActiveAll 返回全部 queued/running 任务（启动恢复用）。
	ListActiveAll(ctx context.Context) ([]model.Task, error)
}

// MappingRepository 管理 Relation 的 MappingPolicy（修订与 relation.revision 同事务联动）。
type MappingRepository interface {
	GetPolicy(ctx context.Context, relationID string) (model.MappingPolicy, error)
	SavePolicy(ctx context.Context, relationID string, p model.MappingPolicy) error
}

// PreparationRepository 持久化 Relation 创建预检（Prepare/Apply 两段式）。
type PreparationRepository interface {
	Insert(ctx context.Context, p model.RelationPreparation) error
	Get(ctx context.Context, id string) (model.RelationPreparation, error)
	// MarkConsumed 消费预检；过期或已消费返回相应语义错误。
	MarkConsumed(ctx context.Context, id string) error
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
}

// BindingFingerprinter 计算端点绑定指纹（卷/文件 identity + 规范化路径）。
type BindingFingerprinter interface {
	Fingerprint(rootPath string) (string, error) // "sha256:<hex>"
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
