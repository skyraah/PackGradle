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
