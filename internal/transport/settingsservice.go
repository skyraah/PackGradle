package transport

import (
	"context"

	settingsapp "packgradle/internal/application/settings"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
)

// SyncApplication 是 SettingsService 对 sync 域的依赖面：既有 syncapp.Application
// 全集 + GC 任务触发（票 #65 把票 #64 移交的 RequestGC transport 面接上 wire）。
// 独立组合而非扩 syncapp.Application 本体：「立即回收空间」是设置页的窄需求，
// 其他消费方不需要（非 transport 面的 GC 触发由 bootstrap.Stack.SyncApp 具体
// 型承担，口径同票 #64「Application 接口保持 transport 契约面不膨胀」）。
type SyncApplication interface {
	syncapp.Application
	// RequestGC 建 GC 任务（全局单飞幂等；契约 06 §9「立即回收空间」）。
	RequestGC(ctx context.Context) (view.TaskView, error)
}

// SettingsService 是设置/开关域用例的 Wails 出口（契约 06 §2/§3.6；票 #57）。
// 与 SyncService 分立注册：保留设置（config.toml [retention] 承载）与工作区
// 授权开关不与同步执行混装（契约 06 §2 服务归属 Q1/Q3）。票 #65 起增载
// RequestGC（设置页「立即回收空间」，消费票 #64 GC 任务面）——契约 06 §2
// 注明的「3 方法」增为 4（修订注记见契约 06 §13）。
type SettingsService struct {
	settings settingsapp.Application
	sync     SyncApplication
}

// NewSettingsService 构造服务（设置域 + 授权开关/GC 触发的 sync 域投影）。
func NewSettingsService(settings settingsapp.Application, sync SyncApplication) *SettingsService {
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

// RequestGC 建「立即回收空间」的 GC 任务（契约 06 §9，票 #65；消费票 #64 的
// GC 任务面）：全局单飞，已有活跃（queued/running）gc 任务时幂等返回既有任务；
// 安全窗口未开任务停 pending 排队（msg.task.gc.waiting 文案在任务中心可见，
// 开窗自动续排同一任务跑完），不拒绝。任务进度/终态由既有任务投影自动覆盖。
// 偏差注记：契约 06 §2 的「SettingsService 3 方法」因本方法增为 4——票 #64
// 报告已明确 RequestGC 的 transport 归属移交本票，规格计数未及回改。
func (s *SettingsService) RequestGC() (TaskDTO, error) {
	t, err := s.sync.RequestGC(context.Background())
	if err != nil {
		return TaskDTO{}, err
	}
	return taskDTO(t), nil
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
