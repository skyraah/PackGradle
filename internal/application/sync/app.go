// Package sync 实现 P1 只读核心应用用例：
// Relation 登记（Prepare/Create）、双端扫描（StartScan）、只读计划（PrepareSync/ResolvePlan/GetPlan）、
// 工作区投影与任务查询。不写 Project/Runtime 文件；Apply/Restore 属于 Phase 2/3。
package sync

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/task"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/syncstage"
)

// Application 是 P1 只读核心用例集（transport 依赖此接口而非具体实现）。
type Application interface {
	PrepareRelation(ctx context.Context, input model.PrepareRelationInput) (view.RelationPreparationView, error)
	CreateRelation(ctx context.Context, preparationID string) (view.RelationView, error)
	ListWorkspaces(ctx context.Context, page ports.PageRequest) (view.WorkspacePage, error)
	GetWorkspace(ctx context.Context, relationID string) (view.WorkspaceView, error)
	StartScan(ctx context.Context, relationID string) (view.TaskView, error)
	PrepareSync(ctx context.Context, input view.PrepareSyncInput) (view.SyncPlanView, error)
	ResolvePlan(ctx context.Context, input view.ResolvePlanInput) (view.SyncPlanView, error)
	GetPlan(ctx context.Context, planID string) (view.SyncPlanView, error)
	GetTask(ctx context.Context, taskID string) (view.TaskView, error)
	ListTasks(ctx context.Context, relationID string, active bool, page ports.PageRequest) (view.TaskPage, error)
	CancelTask(ctx context.Context, taskID string) error
	// GetSnapshotDiagnostics 查询快照持久化诊断（票 #17：mapping_collision 等在快照中可查）。
	GetSnapshotDiagnostics(ctx context.Context, relationID, snapshotID string) ([]model.Diagnostic, error)
	// GetHashCacheStats 查询 hash cache 命中统计（票 #17：命中计数/命中率可查询）。
	GetHashCacheStats(ctx context.Context) (view.HashCacheStatsView, error)
	// GetChanges 资源级变更浏览（契约 03 §2.2；票 #19）。
	GetChanges(ctx context.Context, input view.GetChangesInput) (view.ChangesPage, error)
	// GetMappingPolicy / UpdateMappingPolicy 映射策略读写（契约 03 §2.3；票 #20）。
	GetMappingPolicy(ctx context.Context, relationID string) (view.PolicyView, error)
	UpdateMappingPolicy(ctx context.Context, input view.UpdateMappingPolicyInput) (view.PolicyView, error)
	// PrepareRebind / ApplyRebind 重绑闭环（契约 03 §2.4；票 #22）：
	// 预检持久化 → Apply 单事务原位更新端点绑定（ADR-0003），恒 reinitialize。
	PrepareRebind(ctx context.Context, input view.PrepareRebindInput) (view.RebindPreparationView, error)
	ApplyRebind(ctx context.Context, preparationID string) (view.RelationView, error)
	// ConfirmPlan 计划确认并创建 Apply 运行（契约 05 §3.1；票 #36）：
	// token/任务/run 单事务同生共死，幂等重入返回既有任务。
	ConfirmPlan(ctx context.Context, input view.ConfirmPlanInput) (view.TaskView, error)
	// Apply 运行与历史读投影（契约 05 §2/§3.2/§3.3/§3.5；票 #39）。
	GetApplyRun(ctx context.Context, relationID string) (view.ApplyRunView, error)
	ListApplyOperations(ctx context.Context, input view.ListApplyOperationsInput) (view.ApplyOperationPage, error)
	ListCommits(ctx context.Context, relationID string, page ports.PageRequest) (view.CommitPage, error)
	GetCommit(ctx context.Context, relationID, commitID string) (view.CommitView, error)
}

var _ Application = (*App)(nil)

// AppDeps 是应用依赖（唯一允许注入具体实现的位置是构造调用方）。
type AppDeps struct {
	Endpoints     ports.EndpointRepository
	Relations     ports.RelationRepository
	Snapshots     ports.SnapshotRepository
	Baselines     ports.BaselineRepository
	Plans         ports.PlanRepository
	Tasks         ports.TaskRepository
	Mappings      ports.MappingRepository
	Preparations  ports.PreparationRepository
	HashCache     ports.HashCacheRepository
	Events        ports.TaskEventRepository
	// Apply 执行仓库（Phase 2，ADR-0004 事实模型，T01 落库；读投影票 #39 消费）。
	ApplyRuns ports.ApplyRunRepository
	Journal   ports.OperationJournalRepository
	Commits   ports.CommitRepository
	// Apply 引擎文件层依赖（T04）：CAS 承接 before-content 保全（objectstore.CAS
	// 满足 syncstage.ContentStore），StagingRoot 是按运行隔离的暂存根目录。
	CAS         syncstage.ContentStore
	StagingRoot string
	// Tx 是多步元数据写入的单事务边界（ADR-0003）；CreateRelation 走 RunInTx。
	Tx            ports.UnitOfWork
	Publisher     ports.EventPublisher // 事件出口（transport 桥），可为 nil
	ProjectScan   ports.ProjectScanner
	RuntimeScan   ports.RuntimeScanner
	Hasher        ports.FileHasher
	Fingerprinter ports.BindingFingerprinter
	EndpointPaths ports.EndpointNormalizer
	IDs           func(prefix string) string
	Now           func() time.Time
}

// App 是 SyncApplication 的 P1 实现。
type App struct {
	deps   AppDeps
	runner *task.Runner
	pub    *task.Publisher

	scanMu sync.Mutex
	// startGate 保证同一 Relation 的 StartScan 创建段互斥（复用活动任务语义）。
	startGate sync.Map // relationID -> *sync.Mutex

	// hash cache 命中统计（进程生命周期累计；GetHashCacheStats 查询，
	// 为 T14 性能基线供数）。cachedHash 的每次 Lookup 归入 hit 或 miss。
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64

	// lastScanTiming 是最近一次完成的扫描分相耗时（LastScanTiming 查询，
	// 为 T14 pgheadless -metrics 供数；runScan 写入，互斥保护）。
	scanTimingMu   sync.Mutex
	lastScanTiming view.ScanTimingView

	// lastApplyTiming 是最近一次 Apply 运行的分相耗时（LastApplyTiming 查询，
	// 为 T09 pgheadless -metrics apply 度量供数；runApply 写入，互斥保护）。
	applyTimingMu   sync.Mutex
	lastApplyTiming view.ApplyTimingView
}

// LastScanTiming 返回最近一次完成的扫描分相耗时（进程生命周期内最后一次；
// 供 headless -metrics 读取，不入 Application 接口/transport 契约）。
func (a *App) LastScanTiming() view.ScanTimingView {
	a.scanTimingMu.Lock()
	defer a.scanTimingMu.Unlock()
	return a.lastScanTiming
}

// recordScanTiming 覆盖最近一次扫描的分相耗时。
func (a *App) recordScanTiming(timing view.ScanTimingView) {
	a.scanTimingMu.Lock()
	defer a.scanTimingMu.Unlock()
	a.lastScanTiming = timing
}

// LastApplyTiming 返回最近一次 Apply 运行的分相耗时（进程生命周期内最后一次；
// 供 headless -metrics apply 度量读取，不入 Application 接口/transport 契约，
// LastScanTiming 类型断言先例）。
func (a *App) LastApplyTiming() view.ApplyTimingView {
	a.applyTimingMu.Lock()
	defer a.applyTimingMu.Unlock()
	return a.lastApplyTiming
}

// recordApplyTiming 覆盖最近一次 Apply 运行的分相耗时。
func (a *App) recordApplyTiming(timing view.ApplyTimingView) {
	a.applyTimingMu.Lock()
	defer a.applyTimingMu.Unlock()
	a.lastApplyTiming = timing
}

// New 构造应用；依赖缺失返回错误。
func New(deps AppDeps) (*App, error) {
	required := []struct {
		name string
		ok   bool
	}{
		{"Endpoints", deps.Endpoints != nil},
		{"Relations", deps.Relations != nil},
		{"Snapshots", deps.Snapshots != nil},
		{"Baselines", deps.Baselines != nil},
		{"Plans", deps.Plans != nil},
		{"Tasks", deps.Tasks != nil},
		{"Mappings", deps.Mappings != nil},
		{"Preparations", deps.Preparations != nil},
		{"HashCache", deps.HashCache != nil},
		{"Events", deps.Events != nil},
		{"ApplyRuns", deps.ApplyRuns != nil},
		{"Journal", deps.Journal != nil},
		{"Commits", deps.Commits != nil},
		{"CAS", deps.CAS != nil},
		{"StagingRoot", deps.StagingRoot != ""},
		{"Tx", deps.Tx != nil},
		{"ProjectScan", deps.ProjectScan != nil},
		{"RuntimeScan", deps.RuntimeScan != nil},
		{"Hasher", deps.Hasher != nil},
		{"Fingerprinter", deps.Fingerprinter != nil},
		{"EndpointPaths", deps.EndpointPaths != nil},
		{"IDs", deps.IDs != nil},
		{"Now", deps.Now != nil},
	}
	for _, r := range required {
		if !r.ok {
			return nil, fmt.Errorf("sync: 缺少依赖 %s", r.name)
		}
	}
	pub := task.NewPublisher(deps.Events, deps.Publisher, deps.IDs, deps.Now)
	return &App{
		deps:   deps,
		pub:    pub,
		runner: task.NewRunner(deps.Tasks, pub, deps.IDs, deps.Now),
	}, nil
}

// runner 暴露给包内用例。
func (a *App) taskRunner() *task.Runner { return a.runner }

func (a *App) nowStr() string { return a.deps.Now().UTC().Format(time.RFC3339) }

func (a *App) relationGate(relationID string) *sync.Mutex {
	v, _ := a.startGate.LoadOrStore(relationID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// toProblem 把底层错误转为 model.Problem。
func toProblem(code string, err error, args ...string) *model.Problem {
	p := &model.Problem{Code: code, Args: args}
	if err != nil {
		p.Detail = err.Error()
	}
	return p
}

// ctxWithoutCancel 派生不随调用结束而取消的上下文（后台任务用）。
func ctxWithoutCancel(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}
