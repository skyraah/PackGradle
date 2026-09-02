// Package sync 实现 P1 只读核心应用用例：
// Relation 登记（Prepare/Create）、双端扫描（StartScan）、只读计划（PrepareSync/ResolvePlan/GetPlan）、
// 工作区投影与任务查询。不写 Project/Runtime 文件；Apply/Restore 属于 Phase 2/3。
package sync

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/task"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/download"
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
	// AcknowledgeRecovery 人工确认恢复收口（契约 05 §3.4；票 #38）：
	// 前置 run=recovery_required，acknowledged_at 落库 + 关系复位 healthy，
	// 头基线不动、不建 Commit，发布 relation_invalidated 引导重扫。
	AcknowledgeRecovery(ctx context.Context, taskID string) (view.WorkspaceView, error)
	// SetWorkspaceAuthorized 切换工作区授权开关（契约 06 §3.6；票 #57）：
	// 写 relations.authorized_apply 列，返回更新后工作区投影。
	SetWorkspaceAuthorized(ctx context.Context, relationID string, enabled bool) (view.WorkspaceView, error)
	// ---- 回滚计划面（契约 06 §2/§3；票 #59）----
	// PrepareRestore 准备回滚：目标 baseline 后端推导，四标记判定 + CF 尽力探测，
	// draft 落 sync_plans(kind=restore) 沿既有计划机器。
	PrepareRestore(ctx context.Context, input view.PrepareRestoreInput) (view.RestorePlanView, error)
	// ResolveRestorePlan 回滚决议：仅 partial 逐资源 skip；exact 遇就绪面不满前置拒绝。
	ResolveRestorePlan(ctx context.Context, input view.ResolveRestorePlanInput) (view.RestorePlanView, error)
	// GetRestorePlan 回滚计划读伴随（对称 GetPlan；stale/expired 读取时投影）。
	GetRestorePlan(ctx context.Context, planID string) (view.RestorePlanView, error)
	// StageUserObject 用户对象补全：字节进 staging 绑 plan 不进 CAS，凭
	// expected_digest 验收，不改标记只改就绪面。
	StageUserObject(ctx context.Context, input view.StageUserObjectInput) (view.RestorePlanView, error)
	// ConfirmRestorePlan 回滚确认（契约 06 §3.4；票 #60）：确认即建 kind=restore
	// 任务与 apply_runs(prepared) 运行，引擎协程接管执行；幂等口径对齐
	// ConfirmPlan，failed 终局可重入，committed 后 err.plan.apply_not_reentrant。
	ConfirmRestorePlan(ctx context.Context, input view.ConfirmRestorePlanInput) (view.TaskView, error)
}

var _ Application = (*App)(nil)

// AppDeps 是应用依赖（唯一允许注入具体实现的位置是构造调用方）。
type AppDeps struct {
	Endpoints    ports.EndpointRepository
	Relations    ports.RelationRepository
	Snapshots    ports.SnapshotRepository
	Baselines    ports.BaselineRepository
	Plans        ports.PlanRepository
	Tasks        ports.TaskRepository
	Mappings     ports.MappingRepository
	Preparations ports.PreparationRepository
	HashCache    ports.HashCacheRepository
	Events       ports.TaskEventRepository
	// Apply 执行仓库（Phase 2，ADR-0004 事实模型，T01 落库；读投影票 #39 消费）。
	ApplyRuns ports.ApplyRunRepository
	Journal   ports.OperationJournalRepository
	Commits   ports.CommitRepository
	// Apply 引擎文件层依赖（T04）：CAS 承接 before-content 保全（objectstore.CAS
	// 满足 syncstage.ContentStore），StagingRoot 是按运行隔离的暂存根目录。
	CAS         CASStore
	StagingRoot string
	// Downloads 是下载物化引擎（ADR-0008，票 #58/#63）：download 行经其产
	// 「已过声明 hash 校验的字节」喂既有 StageContent。生产装配恒提供；
	// nil 时 download 行按取数失败剔除（不进恢复面），供未接下载面的夹具。
	Downloads *download.Engine
	// Probes 是 CF 探测引擎（internal/download.Engine 满足 RestoreProber；
	// 票 #59 PrepareRestore 尽力探测）。可为 nil（不探测，行内不标 availability）。
	Probes RestoreProber
	// Retention 是保留策略设置存取（config.toml [retention]，ADR-0007 §8，票 #64）。
	// 可选：nil（headless 无设置面）时用 model.DefaultRetention()——PrepareSync
	// 的 preserve_skip 阈值与 GC 引擎的五键读取都经 retentionSettings() 收口。
	Retention ports.RetentionSettingsStore
	// GC 是 GC 引擎的存储面（修剪决策输入/级联删除/对象账目与隔离态，票 #64）。
	// 可选：nil 时 GC 引擎不可用（RequestGC 报错、收口触发跳过），既有链路零波及。
	GC ports.GCRepository
	// GCTrash 是回收站与盘面对象操作（objectstore.CAS 实现，票 #64）。可选：
	// 与 GC 同生（bootstrap 成对装配）。
	GCTrash ports.GCTrash
	// Cleanup 是惰性清理通道存储面（task_events 条数窗口 + 旧数据行物理删除，
	// ADR-0011 §2/§3，票 #89）。可选：nil 时清理通道整体禁用（RunLazyCleanup
	// 零操作、任务终态钩子不装配），未接清理面的测试栈零波及。
	Cleanup ports.CleanupRepository
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

	// gcMu 串行化 GC 任务创建段（RequestGC 的单飞检查+创建非原子，进程内
	// 双保险；跨通道并发触发的后到请求复用首个任务）。gcKick 是安全窗口的
	// 唤醒通道（任务终态/恢复处置 kick，ADR-0007 §3；带缓冲单槽，无等待者
	// 时丢弃——轮询兜底）。New 初始化。
	gcMu   sync.Mutex
	gcKick chan struct{}

	// cleanupMu 串行化惰性清理通道（ADR-0011 §2/§3，票 #89：启动时 + 任务
	// 终态后两通道可能并发触发；各删除步骤幂等，互斥只为日志与测试确定性）。
	cleanupMu sync.Mutex
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
	app := &App{
		deps:   deps,
		pub:    pub,
		runner: task.NewRunner(deps.Tasks, pub, deps.IDs, deps.Now),
		gcKick: make(chan struct{}, 1),
	}
	// 任务终态钩子 = 惰性清理通道的任务终态触发（ADR-0011 §2/§3，票 #89）；
	// 清理面未装配时钩子内部零操作，装配调用保持无条件（runner 装配一次）。
	app.runner.SetTerminalHook(app.lazyCleanupAfterTask)
	return app, nil
}

// runner 暴露给包内用例。
func (a *App) taskRunner() *task.Runner { return a.runner }

func (a *App) nowStr() string { return a.deps.Now().UTC().Format(time.RFC3339) }

// retentionSettings 读取保留策略五键（ADR-0007 §8）：设置存取端口缺失
//（headless Build）或读取失败时退默认值——GC 是机会主义后台任务，设置面
// 故障不阻断产品主链路，引擎下轮再取。
func (a *App) retentionSettings() model.RetentionSettings {
	if a.deps.Retention == nil {
		return model.DefaultRetention()
	}
	s, err := a.deps.Retention.Retention()
	if err != nil {
		slog.Warn("gc: 读取保留设置失败（退默认值）", "err", err)
		return model.DefaultRetention()
	}
	return s
}

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
