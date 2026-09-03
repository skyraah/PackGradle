// Package bootstrap 装配新架构（P1 只读核心）的完整栈：
// 用户数据目录 → SQLite（迁移门禁）→ 仓库 → 适配器 → 应用用例 → transport 服务。
// 这是唯一允许同时 import store/adapters/application 具体实现的位置（main.go 与 headless 工具共用）。
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"packgradle/internal/adapters/fsnotifywatch"
	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/packwiz"
	"packgradle/internal/adapters/prism"
	"packgradle/internal/application/ports"
	projectapp "packgradle/internal/application/project"
	runtimeapp "packgradle/internal/application/runtime"
	settingsapp "packgradle/internal/application/settings"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/application/watch"
	"packgradle/internal/core/ids"
	"packgradle/internal/download"
	"packgradle/internal/notify"
	"packgradle/internal/store"
	"packgradle/internal/store/objectstore"
	"packgradle/internal/store/sqlite"
	"packgradle/internal/transport"
)

// Stack 是装配结果。
type Stack struct {
	Layout  store.Layout
	DB      *sql.DB
	App     syncapp.Application
	Service *transport.SyncService
	// SyncApp 是具体应用类型（headless 工具消费非接口面：GC 触发/复活，
	// 票 #64——Application 接口保持 transport 契约面不膨胀）。
	SyncApp *syncapp.App
	// Watch 是监听引擎（ADR-0010，票 #92）：StartWatcher 启动常驻 goroutine，
	// Close 收敛。事件源构造失败时为 nil（监听面禁用，降级回手动）。
	Watch *watch.Engine
	// GCRepo / GCTrash / CAS 是验收对账与复活面的采集出口（票 #64；
	// headless 链经它们组装 core/gc.Audit 的四侧事实）。
	GCRepo  ports.GCRepository
	GCTrash ports.GCTrash
	CAS     *objectstore.CAS
	// ProjectService / RuntimeService 是端点管理用例出口（/sources、/runtimes 页）。
	ProjectService *transport.ProjectService
	RuntimeService *transport.RuntimeService
	// SettingsService 是设置/开关域出口（契约 06 §2；票 #57）：保留设置 +
	// 授权开关。仅 BuildWithRetention 装配（headless 工具无设置面）。
	Settings *transport.SettingsService
	// Notify 是系统通知 gate（票 #97，契约 07 §3.5）：事件源=自动链停靠
	// awaiting_confirmation（build 的 Chain 缝喂入）。GUI main 经
	// notify.AttachWails 装配 Windows 平台面后生效；headless 工具不装配，
	// gate 恒惰（全部入口 no-op）。
	Notify *notify.Gate
}

// StartGC 启动触发通道①（票 #64，ADR-0007 §3）：异步建 GC 任务（幂等单飞）。
// 产品入口（GUI main）与验收链显式调用；测试装配不自动触发。
func (s *Stack) StartGC() {
	if s.SyncApp != nil {
		s.SyncApp.StartGC()
	}
}

// StartWatcher 启动监听引擎常驻 goroutine（票 #92，ADR-0010 §4）：应用运行期
// 对全部健康 relation 常驻监听（窗口开闭无关）。引擎未装配（事件源构造失败，
// 见 build）时为 no-op——失败仅日志+降级回手动，不阻断启动；快速更新可用性
// 不受监听死活影响。产品入口（GUI main）显式调用；测试装配不自动启动。
func (s *Stack) StartWatcher() {
	if s.Watch != nil {
		s.Watch.Go()
	}
}

// RelationIDs 返回全部关系 id（引用图对账的全局遍历底册，验收链用）。
func (s *Stack) RelationIDs(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM relations ORDER BY id ASC")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: 列关系 id: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Build 在指定用户数据根目录装配新栈。
// 迁移失败直接返回错误（调用方不得启动写操作——架构文档 §8.3）。
func Build(root string) (*Stack, error) {
	return build(root, nil, download.Options{})
}

// BuildWithRetention 同 Build，另接保留设置存取端口（config.toml [retention]
// 承载，appconfig.ConfigManager 实现）装配 SettingsService（契约 06 §2/§3.6；
// 票 #57）。GUI 主程序使用；headless 工具（无设置面）沿用 Build。
func BuildWithRetention(root string, retention ports.RetentionSettingsStore) (*Stack, error) {
	return build(root, retention, download.Options{})
}

// BuildWithDownloadOptions 同 BuildWithRetention，另注入下载引擎构造参数
//（票 #66 验收缝：pgheadless/pgrecovery 的 `-cdn <url>` 把引擎 BaseURL 指向
// 假 CDN 进程——同一 HTTP 栈、同一直链构造口径，零真网；其余 Options 字段
// 供验收链注入快退避等测试缝，dlTestStack 先例）。生产装配（GUI main）不经过
// 此入口，仍走零值 Options（生产 CDN 前缀 + 指数退避）。
func BuildWithDownloadOptions(root string, retention ports.RetentionSettingsStore, dl download.Options) (*Stack, error) {
	return build(root, retention, dl)
}

// build 是装配主体：retention 非 nil 时额外装配 SettingsService；dl 是下载
// 引擎构造参数（零值 = 生产默认）。
func build(root string, retention ports.RetentionSettingsStore, dl download.Options) (*Stack, error) {
	layout, err := store.EnsureLayout(root)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: 初始化用户数据目录: %w", err)
	}
	db, err := sqlite.Open(layout.DBPath)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: 打开 packgradle.db: %w", err)
	}
	if err := sqlite.Migrate(context.Background(), db, layout.Root); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: 数据库迁移失败（已中止，不启动写操作）: %w", err)
	}
	// CAS 在 P1 无写入方，但提前打开以验证对象库布局可用；
	// Phase 2 Apply 引擎复用同一 CAS 做 before-content 保全（票 #37）。
	cas, err := objectstore.Open(layout.ObjectsDir, db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: 初始化 CAS 对象库: %w", err)
	}

	hasher := filesystem.NewHasher()
	fingerprinter := filesystem.NewFingerprinter()
	endpoints := sqlite.NewEndpointRepository(db)
	// 监听引擎共用仓库（票 #92）：relation 集合/端点根路径/policy/任务终态
	// 的事实源与 sync 同一 DB。
	taskRepo := sqlite.NewTaskRepository(db)
	mappingRepo := sqlite.NewMappingRepository(db)
	// 下载物化引擎（ADR-0008，票 #58/#63）：并发度取全局 config [download]
	// concurrency 的生效值缺省（默认 6；headless 工具与 GUI 共用同一装配路径，
	// 显式配置的消费归 appconfig 加载层与 SettingsService 面）。零值即合法默认。
	// 同一引擎实例兼作 CF 探测引擎（契约 06 §5；票 #59 ProbeHead：直链构造与
	// HTTP 通道复用同一 Engine，探测预算为编译期常量）；构造无网络副作用。
	// dl 参数由 BuildWithDownloadOptions 注入（票 #66：-cdn 假 CDN BaseURL /
	// 验收快退避），Build 与 BuildWithRetention 传零值即生产行为不变。
	dlEngine, err := download.New(dl)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: 构造下载引擎: %w", err)
	}
	app, err := syncapp.New(syncapp.AppDeps{
		Endpoints:     endpoints,
		Relations:     sqlite.NewRelationRepository(db),
		Snapshots:     sqlite.NewSnapshotRepository(db),
		Baselines:     sqlite.NewBaselineRepository(db),
		Plans:         sqlite.NewPlanRepository(db),
		Tasks:         taskRepo,
		Mappings:      mappingRepo,
		Preparations:  sqlite.NewPreparationRepository(db),
		HashCache:     sqlite.NewHashCacheRepository(db),
		Events:        sqlite.NewEventRepository(db),
		ApplyRuns:     sqlite.NewApplyRunRepository(db),
		Journal:       sqlite.NewOperationJournalRepository(db),
		Commits:       sqlite.NewCommitRepository(db),
		CAS:           cas,
		StagingRoot:   layout.StagingDir,
		Downloads:     dlEngine,
		Probes:        dlEngine,
		// 保留策略设置（ADR-0007 §8，票 #64）：retention 非 nil 时供
		// PrepareSync 的 preserve_skip 阈值与 GC 引擎五键读取；nil（headless
		// Build）退默认值。
		Retention: retention,
		// GC 引擎存储面与回收站（票 #64）：GCRepository 走同一 DB，
		// GCTrash 即 CAS（回收站/孤儿清扫落盘侧）。
		GC:      sqlite.NewGCRepository(db),
		GCTrash: cas,
		// 惰性清理通道存储面（ADR-0011 §2/§3，票 #89）：task_events 条数窗口
		// + 旧数据行物理删除；启动触发在装配后执行（下方），任务终态触发由
		// runner 终态钩子承担。
		Cleanup: sqlite.NewCleanupRepository(db),
		Tx:            sqlite.NewUnitOfWork(db),
		Publisher:     transport.NewEventBridge(),
		ProjectScan:   packwiz.New(),
		RuntimeScan:   prism.New(),
		Hasher:        hasher,
		Fingerprinter: fingerprinter,
		EndpointPaths: filesystem.PathNormalizer{},
		IDs:           ids.New,
		Now:           defaultNow,
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: 装配应用: %w", err)
	}
	// 启动恢复：把进程中断遗留的 queued/running 任务标记为中断，
	// 否则僵尸任务会因 StartScan 的复用语义永久锁死对应 Relation。
	if err := app.RecoverInterruptedTasks(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: 启动任务恢复失败: %w", err)
	}
	// 启动时惰性清理（ADR-0011 §2/§3，票 #89）：task_events 条数窗口截断 +
	// 旧数据行物理删除（另一触发时机 = 任务终态后，runner 终态钩子承担）。
	// 机会主义通道：失败只记日志不阻断启动，下一轮触发续清。
	if err := app.RunLazyCleanup(context.Background()); err != nil {
		slog.Warn("bootstrap: 启动惰性清理失败（不阻断启动，下轮续清）", "err", err)
	}

	// 端点管理用例（/sources、/runtimes 页）：与 sync 共用同一数据库、
	// 同一端点仓库与指纹/规范化适配器；发现适配器按侧分立（packwiz/prism）。
	projectSvc, err := projectapp.New(projectapp.Deps{
		Endpoints:     endpoints,
		Paths:         filesystem.PathNormalizer{},
		Fingerprinter: fingerprinter,
		Discovery:     packwiz.New(),
		IDs:           ids.New,
		Now:           defaultNow,
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: 装配项目源端点用例: %w", err)
	}
	runtimeSvc, err := runtimeapp.New(runtimeapp.Deps{
		Endpoints:     endpoints,
		Paths:         filesystem.PathNormalizer{},
		Fingerprinter: fingerprinter,
		Discovery:     prism.NewDiscoverer(),
		IDs:           ids.New,
		Now:           defaultNow,
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: 装配运行实例端点用例: %w", err)
	}
	// 设置域用例（契约 06 §2/§3.6；票 #57）：保留设置端口为 nil（headless）时跳过，
	// SettingsService 保持未装配。存储占用概览采集端口（ADR-0011 §8，票 #90）
	// 走同一 DB 与数据根布局（GetStorageStats，SettingsService 第 5 方法）。
	var settingsSvc *transport.SettingsService
	if retention != nil {
		settingsApp, err := settingsapp.New(settingsapp.Deps{
			Retention: retention,
			Storage:   sqlite.NewStorageStatsRepository(db, layout),
		})
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("bootstrap: 装配设置用例: %w", err)
		}
		settingsSvc = transport.NewSettingsService(settingsApp, app)
	}

	// 监听引擎（ADR-0010，票 #92）：fsnotify 事件源（adapters 层，core 不
	// import fsnotify）+ 触发器状态机（application/watch 纯逻辑），自动链调
	// QuickUpdate 同一用例（不复制编排、不绕任务互斥）。事件源构造失败只记
	// 日志——监听面整体禁用降级回手动，不阻断启动（watch_status 投影空值）。
	// 启动归 StartWatcher（GUI main 显式调用）；Close 收敛 Stop。
	// 系统通知 gate（票 #97，契约 07 §3.5）在链缝前方构造：headless 不装配
	// 平台面（恒惰），GUI main 经 Stack.Notify 由 notify.AttachWails 接线。
	notifyG := notify.NewGate()
	var watchEng *watch.Engine
	wsrc, werr := fsnotifywatch.New()
	if werr != nil {
		slog.Warn("bootstrap: 监听事件源构造失败（监听面禁用，降级回手动）", "err", werr)
	} else {
		watchEng, werr = watch.New(watch.Deps{
			Relations: sqlite.NewRelationRepository(db),
			Endpoints: endpoints,
			Mappings:  mappingRepo,
			Tasks:     taskRepo,
			Source:    wsrc,
			NewSource: func() (ports.DirEventSource, error) { return fsnotifywatch.New() },
			// 自动链缝=统一快速更新用例（#86）：watcher 触发层（静默期/单飞/
			// 连败计数/暂停复位）在用例之外（ADR-0010 §5/§6，契约 07 §3.1.6）。
			Chain: func(ctx context.Context, relationID string) (string, string, error) {
				res, err := app.QuickUpdate(ctx, view.QuickUpdateInput{RelationID: relationID})
				if err != nil {
					return "", "", err
				}
				// 系统通知事件源（票 #97，契约 07 §3.5）：仅自动链停于
				// awaiting_confirmation 进通知判定（触发条件①）——手动入口经
				// transport SyncService.QuickUpdate（#92 的 paused 复位缝），
				// 不经本缝，人就在界面天然不弹。res.PlanID 即此刻
				// WorkspaceStateDTO.pending_plan_id 投影（刚停靠的计划是最新
				// 待人工计划）；三条件②③与降级判定在 gate 内完成。
				if res.Outcome == syncapp.QuickUpdateAwaitingConfirmation {
					notifyG.AutoChainDocked(relationID, res.PlanID)
				}
				return res.Outcome, res.ApplyTaskID, nil
			},
			PublishWatchFailed: func(ctx context.Context, relationID string) error {
				return app.PublishWatchFailed(ctx, relationID)
			},
			Now: defaultNow,
		})
		if werr != nil {
			slog.Warn("bootstrap: 监听引擎装配失败（监听面禁用，降级回手动）", "err", werr)
			_ = wsrc.Close()
			watchEng = nil
		} else {
			// 状态投影 + 动态挂卸 kick + 手动快速更新收口订阅（paused 复位
			// active 的接线点，契约 07 §3.2）。
			app.AttachWatch(watchEng, watchEng.Kick)
		}
	}
	svc := transport.NewSyncService(app)
	if watchEng != nil {
		svc.AttachQuickUpdateResult(watchEng.NotifyQuickUpdateResult)
	}

	return &Stack{
		Layout:         layout,
		DB:             db,
		App:            app,
		SyncApp:        app,
		GCRepo:         sqlite.NewGCRepository(db),
		GCTrash:        cas,
		CAS:            cas,
		Watch:          watchEng,
		Notify:         notifyG,
		Service:        svc,
		ProjectService: transport.NewProjectService(projectSvc),
		RuntimeService: transport.NewRuntimeService(runtimeSvc),
		Settings:       settingsSvc,
	}, nil
}

// Close 释放底层资源（监听引擎先停——事件源 goroutine 退出后再关 DB）。
func (s *Stack) Close() error {
	if s.Watch != nil {
		s.Watch.Stop()
	}
	if s.DB != nil {
		return s.DB.Close()
	}
	return nil
}
