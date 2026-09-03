// Package settings 实现设置域用例（契约 06 §3.6；票 #57）：保留策略五参数
// 的读写（config.toml [retention] 承载，ADR-0007 §8；范围校验在加载层与写入层
// 同款，由 appconfig 实现承担）。工作区授权开关（SetWorkspaceAuthorized）属
// relation 域用例，落 application/sync；transport SettingsService 汇聚两域出口，
// 本包不混装。
package settings

import (
	"context"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// Application 是设置域用例接口（transport SettingsService 依赖此接口）。
type Application interface {
	// GetRetentionSettings 读保留设置（未写键归一为默认值）。
	GetRetentionSettings(ctx context.Context) (view.RetentionSettingsView, error)
	// UpdateRetentionSettings 整体替换保留设置；单键越界整体拒绝
	// （err.settings.retention_invalid，{0}=字段名）。
	UpdateRetentionSettings(ctx context.Context, input view.UpdateRetentionSettingsInput) (view.RetentionSettingsView, error)
	// GetStorageStats 返回存储占用概览（ADR-0011 §8，票 #90；只读数据面，
	// 阈值与告警 UI 后置）。
	GetStorageStats(ctx context.Context) (view.StorageStatsView, error)
}

var _ Application = (*App)(nil)

// Deps 是应用依赖。
type Deps struct {
	// Retention 是保留设置存取端口（config.toml [retention]，appconfig 实现）。
	Retention ports.RetentionSettingsStore
	// Storage 是存储占用概览采集端口（ADR-0011 §8，票 #90；sqlite 仓库实现）。
	Storage ports.StorageStatsSource
}

// App 是设置域用例实现。
type App struct {
	deps Deps
}

// New 构造应用；依赖缺失返回错误。
func New(deps Deps) (*App, error) {
	if deps.Retention == nil {
		return nil, fmt.Errorf("settings: 缺少依赖 Retention")
	}
	if deps.Storage == nil {
		return nil, fmt.Errorf("settings: 缺少依赖 Storage")
	}
	return &App{deps: deps}, nil
}

// GetRetentionSettings 读保留设置并投影。
func (a *App) GetRetentionSettings(ctx context.Context) (view.RetentionSettingsView, error) {
	s, err := a.deps.Retention.Retention()
	if err != nil {
		return view.RetentionSettingsView{}, err
	}
	return retentionView(s), nil
}

// UpdateRetentionSettings 整体替换保留设置并投影生效值。
func (a *App) UpdateRetentionSettings(ctx context.Context, input view.UpdateRetentionSettingsInput) (view.RetentionSettingsView, error) {
	s, err := a.deps.Retention.SetRetention(model.RetentionSettings{
		KeepCommits:           input.KeepCommits,
		KeepDays:              input.KeepDays,
		RelationCapacityBytes: input.RelationCapacityBytes,
		PreserveMaxBytes:      input.PreserveMaxBytes,
		TrashDays:             input.TrashDays,
	})
	if err != nil {
		return view.RetentionSettingsView{}, err
	}
	return retentionView(s), nil
}

// GetStorageStats 返回存储占用概览（ADR-0011 §8 勘误兑现，票 #90）：
// cas_total_bytes + free_disk_bytes 为容量红线双指标承载；只读数据面，
// 阈值与告警 UI 后置，staging 侧指标待 #69 决议后补。
func (a *App) GetStorageStats(ctx context.Context) (view.StorageStatsView, error) {
	return a.deps.Storage.StorageStats(ctx)
}

// retentionView 投影保留设置（schema_version 随 DTO 顶层约定）。
func retentionView(s model.RetentionSettings) view.RetentionSettingsView {
	return view.RetentionSettingsView{
		SchemaVersion:         model.CurrentSchemaVersion,
		KeepCommits:           s.KeepCommits,
		KeepDays:              s.KeepDays,
		RelationCapacityBytes: s.RelationCapacityBytes,
		PreserveMaxBytes:      s.PreserveMaxBytes,
		TrashDays:             s.TrashDays,
	}
}
