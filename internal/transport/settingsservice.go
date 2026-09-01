package transport

import (
	"context"

	settingsapp "packgradle/internal/application/settings"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
)

// SettingsService 是设置/开关域用例的 Wails 出口（契约 06 §2/§3.6；票 #57）。
// 与 SyncService 分立注册：保留设置（config.toml [retention] 承载）与工作区
// 授权开关不与同步执行混装（契约 06 §2 服务归属 Q1/Q3）。回滚/下载/GC 等
// 后续域能力另票点亮，本服务只承载三方法。
type SettingsService struct {
	settings settingsapp.Application
	sync     syncapp.Application
}

// NewSettingsService 构造服务（设置域 + 授权开关的 relation 域投影）。
func NewSettingsService(settings settingsapp.Application, sync syncapp.Application) *SettingsService {
	return &SettingsService{settings: settings, sync: sync}
}

// ServiceName 返回服务注册名（Wails v3 生命周期可选接口）。
func (s *SettingsService) ServiceName() string { return "packgradle.core.SettingsService" }

// GetRetentionSettings 返回保留策略设置（未配置键归一为默认值；ADR-0007 §8）。
func (s *SettingsService) GetRetentionSettings() (RetentionSettingsDTO, error) {
	v, err := s.settings.GetRetentionSettings(context.Background())
	if err != nil {
		return RetentionSettingsDTO{}, err
	}
	return retentionSettingsDTO(v), nil
}

// UpdateRetentionSettings 整体替换保留策略设置：单键范围校验，越界 →
// err.settings.retention_invalid（{0}=字段名），整体拒绝（契约 06 §3.6）。
// 返回写入后的设置投影。
func (s *SettingsService) UpdateRetentionSettings(input UpdateRetentionSettingsDTO) (RetentionSettingsDTO, error) {
	v, err := s.settings.UpdateRetentionSettings(context.Background(), settingsUpdateInput(input))
	if err != nil {
		return RetentionSettingsDTO{}, err
	}
	return retentionSettingsDTO(v), nil
}

// SetWorkspaceAuthorized 切换工作区授权开关（relations.authorized_apply，schema v6）
// 并返回更新后的 WorkspaceDTO（投影一致，契约 06 §3.6）。恢复期开关值保留，
// 入口由既有 err.recovery.in_progress 门禁挡。
func (s *SettingsService) SetWorkspaceAuthorized(relationID string, enabled bool) (WorkspaceDTO, error) {
	v, err := s.sync.SetWorkspaceAuthorized(context.Background(), relationID, enabled)
	if err != nil {
		return WorkspaceDTO{}, err
	}
	return workspaceDTO(v), nil
}

// settingsUpdateInput 把 DTO 还原为应用层写输入。
func settingsUpdateInput(input UpdateRetentionSettingsDTO) view.UpdateRetentionSettingsInput {
	return view.UpdateRetentionSettingsInput{
		KeepCommits:           input.KeepCommits,
		KeepDays:              input.KeepDays,
		RelationCapacityBytes: input.RelationCapacityBytes,
		PreserveMaxBytes:      input.PreserveMaxBytes,
		TrashDays:             input.TrashDays,
	}
}
