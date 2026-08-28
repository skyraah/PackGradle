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
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/core/ids"
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
}

// Build 在指定用户数据根目录装配新栈。
// 迁移失败直接返回错误（调用方不得启动写操作——架构文档 §8.3）。
func Build(root string) (*Stack, error) {
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
	// CAS 在 P1 无写入方，但提前打开以验证对象库布局可用
	if _, err := objectstore.Open(layout.ObjectsDir, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: 初始化 CAS 对象库: %w", err)
	}

	hasher := filesystem.NewHasher()
	fingerprinter := filesystem.NewFingerprinter()
	app, err := syncapp.New(syncapp.AppDeps{
		Endpoints:     sqlite.NewEndpointRepository(db),
		Relations:     sqlite.NewRelationRepository(db),
		Snapshots:     sqlite.NewSnapshotRepository(db),
		Baselines:     sqlite.NewBaselineRepository(db),
		Plans:         sqlite.NewPlanRepository(db),
		Tasks:         sqlite.NewTaskRepository(db),
		Mappings:      sqlite.NewMappingRepository(db),
		Preparations:  sqlite.NewPreparationRepository(db),
		HashCache:     sqlite.NewHashCacheRepository(db),
		Events:        sqlite.NewEventRepository(db),
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
	return &Stack{
		Layout:  layout,
		DB:      db,
		App:     app,
		Service: transport.NewSyncService(app),
	}, nil
}

// Close 释放底层资源。
func (s *Stack) Close() error {
	if s.DB != nil {
		return s.DB.Close()
	}
	return nil
}
