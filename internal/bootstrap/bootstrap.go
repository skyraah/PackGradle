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
	projectapp "packgradle/internal/application/project"
	"packgradle/internal/application/ports"
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
	// ProjectService / RuntimeService 是端点管理用例出口（/sources、/runtimes 页）。
	ProjectService *transport.ProjectService
	RuntimeService *transport.RuntimeService
	// SettingsService 是设置/开关域出口（契约 06 §2；票 #57）：保留设置 +
	// 授权开关。仅 BuildWithRetention 装配（headless 工具无设置面）。
	Settings *transport.SettingsService
}

// Build 在指定用户数据根目录装配新栈。
// 迁移失败直接返回错误（调用方不得启动写操作——架构文档 §8.3）。
func Build(root string) (*Stack, error) {
	return build(root, nil)
}

// BuildWithRetention 同 Build，另接保留设置存取端口（config.toml [retention]
// 承载，appconfig.ConfigManager 实现）装配 SettingsService（契约 06 §2/§3.6；
// 票 #57）。GUI 主程序使用；headless 工具（无设置面）沿用 Build。
func BuildWithRetention(root string, retention ports.RetentionSettingsStore) (*Stack, error) {
	return build(root, retention)
}

// build 是装配主体：retention 非 nil 时额外装配 SettingsService。
func build(root string, retention ports.RetentionSettingsStore) (*Stack, error) {
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
	dlEngine, err := download.New(download.Options{})
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
