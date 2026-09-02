// Package bootstrap 装配新架构（P1 只读核心）的完整栈：
// 用户数据目录 → SQLite（迁移门禁）→ 仓库 → 适配器 → 应用用例 → transport 服务。
// 这是唯一允许同时 import store/adapters/application 具体实现的位置（main.go 与 headless 工具共用）。
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/packwiz"
	"packgradle/internal/adapters/prism"
	"packgradle/internal/application/ports"
	projectapp "packgradle/internal/application/project"
	runtimeapp "packgradle/internal/application/runtime"
	settingsapp "packgradle/internal/application/settings"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/core/ids"
	"packgradle/internal/download"
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
}

// StartGC 启动触发通道①（票 #64，ADR-0007 §3）：异步建 GC 任务（幂等单飞）。
// 产品入口（GUI main）与验收链显式调用；测试装配不自动触发。
func (s *Stack) StartGC() {
	if s.SyncApp != nil {
		s.SyncApp.StartGC()
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
		Tasks:         sqlite.NewTaskRepository(db),
		Mappings:      sqlite.NewMappingRepository(db),
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
	// SettingsService 保持未装配。
	var settingsSvc *transport.SettingsService
	if retention != nil {
		settingsApp, err := settingsapp.New(settingsapp.Deps{Retention: retention})
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("bootstrap: 装配设置用例: %w", err)
		}
		settingsSvc = transport.NewSettingsService(settingsApp, app)
	}
	return &Stack{
		Layout:         layout,
		DB:             db,
		App:            app,
		SyncApp:        app,
		GCRepo:         sqlite.NewGCRepository(db),
		GCTrash:        cas,
		CAS:            cas,
		Service:        transport.NewSyncService(app),
		ProjectService: transport.NewProjectService(projectSvc),
		RuntimeService: transport.NewRuntimeService(runtimeSvc),
		Settings:       settingsSvc,
	}, nil
}

// Close 释放底层资源。
func (s *Stack) Close() error {
	if s.DB != nil {
		return s.DB.Close()
	}
	return nil
}
