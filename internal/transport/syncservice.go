package transport

import (
	"context"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/policy"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// SyncService 是新架构 P1 只读核心的 Wails 出口（与 legacy 三服务并存注册）。
// 不暴露 Apply/ConfirmPlan/Restore（Phase 2/3 能力，未实现即不出现在产品操作面）。
type SyncService struct {
	app syncapp.Application
}

// NewSyncService 构造服务。
func NewSyncService(app syncapp.Application) *SyncService { return &SyncService{app: app} }

// ServiceName 返回服务注册名（Wails v3 生命周期可选接口）。
func (s *SyncService) ServiceName() string { return "packgradle.core.SyncService" }

// PrepareRelation 执行端点登记预检。
func (s *SyncService) PrepareRelation(input PrepareRelationDTO) (RelationPreparationDTO, error) {
	v, err := s.app.PrepareRelation(context.Background(), model.PrepareRelationInput{
		ProjectRoot:        input.ProjectRoot,
		RuntimeInstanceDir: input.RuntimeInstanceDir,
		PolicySet:          input.PolicySet,
		Suggestions:        input.Suggestions,
	})
	if err != nil {
		return RelationPreparationDTO{}, err
	}
	return preparationDTO(v), nil
}

// ListPolicySuggestions 返回建议（默认不激活）的受管范围候选规则，
// 供 /workspaces/new 页勾选后并入初始 policy（/workspaces/new 建议流）。
func (s *SyncService) ListPolicySuggestions() ([]MappingRuleDTO, error) {
	rules := policy.Suggestions()
	out := make([]MappingRuleDTO, 0, len(rules))
	for _, r := range rules {
		out = append(out, mappingRuleDTO(r))
	}
	return out, nil
}

// CreateRelation 消费预检并创建 Relation。
func (s *SyncService) CreateRelation(preparationID string) (RelationDTO, error) {
	v, err := s.app.CreateRelation(context.Background(), preparationID)
	if err != nil {
		return RelationDTO{}, err
	}
	return relationDTO(v), nil
}

// ListWorkspaces 分页返回工作区。
func (s *SyncService) ListWorkspaces(cursor string, limit int) (WorkspacePageDTO, error) {
	page, err := s.app.ListWorkspaces(context.Background(), pageRequest(cursor, limit))
	if err != nil {
		return WorkspacePageDTO{}, err
	}
	items := make([]WorkspaceDTO, 0, len(page.Items))
	for _, w := range page.Items {
		items = append(items, workspaceDTO(w))
	}
	return WorkspacePageDTO{SchemaVersion: model.CurrentSchemaVersion, Items: items, NextCursor: page.NextCursor}, nil
}

// GetWorkspace 返回工作区详情。
func (s *SyncService) GetWorkspace(relationID string) (WorkspaceDTO, error) {
	v, err := s.app.GetWorkspace(context.Background(), relationID)
	if err != nil {
		return WorkspaceDTO{}, err
	}
	return workspaceDTO(v), nil
}

// StartScan 启动（或复用）扫描任务。
func (s *SyncService) StartScan(relationID string) (TaskDTO, error) {
	v, err := s.app.StartScan(context.Background(), relationID)
	if err != nil {
		return TaskDTO{}, err
	}
	return taskDTO(v), nil
}

// PrepareSync 生成不可变 draft plan。
func (s *SyncService) PrepareSync(input PrepareSyncDTO) (SyncPlanDTO, error) {
	v, err := s.app.PrepareSync(context.Background(), view.PrepareSyncInput{
		RelationID:             input.RelationID,
		RelationRevision:       input.RelationRevision,
		InputProjectSnapshotID: input.InputProjectSnapshotID,
		InputRuntimeSnapshotID: input.InputRuntimeSnapshotID,
		RequestedExactness:     input.RequestedExactness,
	})
	if err != nil {
		return SyncPlanDTO{}, err
	}
	return planDTO(v), nil
}

// ResolvePlan 将冲突选择固化为新 resolved plan。
func (s *SyncService) ResolvePlan(input ResolvePlanDTO) (SyncPlanDTO, error) {
	resolutions := make([]model.Resolution, 0, len(input.Resolutions))
	for _, r := range input.Resolutions {
		resolutions = append(resolutions, model.Resolution{
			ResourceID: model.ResourceID(r.ResourceID),
			Choice:     model.ResolutionChoice(r.Choice),
		})
	}
	v, err := s.app.ResolvePlan(context.Background(), view.ResolvePlanInput{
		PlanID: input.PlanID, Resolutions: resolutions,
	})
	if err != nil {
		return SyncPlanDTO{}, err
	}
	return planDTO(v), nil
}

// GetPlan 查询计划（stale/expired 为读取时投影）。
func (s *SyncService) GetPlan(planID string) (SyncPlanDTO, error) {
	v, err := s.app.GetPlan(context.Background(), planID)
	if err != nil {
		return SyncPlanDTO{}, err
	}
	return planDTO(v), nil
}

// GetTask 查询任务。
func (s *SyncService) GetTask(taskID string) (TaskDTO, error) {
	v, err := s.app.GetTask(context.Background(), taskID)
	if err != nil {
		return TaskDTO{}, err
	}
	return taskDTO(v), nil
}

// ListTasks 查询任务列表。
func (s *SyncService) ListTasks(relationID string, active bool, cursor string, limit int) (TaskPageDTO, error) {
	page, err := s.app.ListTasks(context.Background(), relationID, active, pageRequest(cursor, limit))
	if err != nil {
		return TaskPageDTO{}, err
	}
	items := make([]TaskDTO, 0, len(page.Items))
	for _, t := range page.Items {
		items = append(items, taskDTO(t))
	}
	return TaskPageDTO{SchemaVersion: model.CurrentSchemaVersion, Items: items, NextCursor: page.NextCursor}, nil
}

// CancelTask 取消任务。
func (s *SyncService) CancelTask(taskID string) error {
	return s.app.CancelTask(context.Background(), taskID)
}

func pageRequest(cursor string, limit int) ports.PageRequest {
	return ports.PageRequest{Cursor: cursor, Limit: limit}
}
