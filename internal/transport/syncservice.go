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

// GetSnapshotDiagnostics 返回快照持久化的诊断列表（diag.mapping.collision、
// diag.scan.* 等；票 #17：mapping_collision 等诊断在快照中可查）。
func (s *SyncService) GetSnapshotDiagnostics(relationID, snapshotID string) ([]DiagnosticDTO, error) {
	diags, err := s.app.GetSnapshotDiagnostics(context.Background(), relationID, snapshotID)
	if err != nil {
		return nil, err
	}
	return diagnosticDTOs(diags), nil
}

// GetHashCacheStats 返回 hash cache 命中统计（进程生命周期累计；
// 热扫描命中证明与 T14 性能基线供数口）。
func (s *SyncService) GetHashCacheStats() (HashCacheStatsDTO, error) {
	v, err := s.app.GetHashCacheStats(context.Background())
	if err != nil {
		return HashCacheStatsDTO{}, err
	}
	return HashCacheStatsDTO{
		SchemaVersion: model.CurrentSchemaVersion,
		Hits:          v.Hits, Misses: v.Misses, HitRatio: v.HitRatio,
	}, nil
}

// GetChanges 资源级变更查询（契约 03 §2.2；票 #19：读时计算三方 Diff，
// summary 全量计数不受筛选影响，items 按 resource_id 字节序分页）。
func (s *SyncService) GetChanges(input GetChangesDTO) (ChangesPageDTO, error) {
	v, err := s.app.GetChanges(context.Background(), view.GetChangesInput{
		RelationID:        input.RelationID,
		ProjectSnapshotID: input.ProjectSnapshotID,
		RuntimeSnapshotID: input.RuntimeSnapshotID,
		Classification:    input.Classification,
		ResourceKind:      input.ResourceKind,
		PathPrefix:        input.PathPrefix,
		Cursor:            input.Cursor,
		Limit:             input.Limit,
	})
	if err != nil {
		return ChangesPageDTO{}, err
	}
	return changesDTO(v), nil
}

// GetMappingPolicy 读取关系的当前映射策略（契约 03 §2.3；票 #20）。
func (s *SyncService) GetMappingPolicy(relationID string) (PolicyDTO, error) {
	v, err := s.app.GetMappingPolicy(context.Background(), relationID)
	if err != nil {
		return PolicyDTO{}, err
	}
	return policyViewDTO(v), nil
}

// UpdateMappingPolicy 保存映射策略修改：编译校验 + 乐观锁 + 修订号同事务递增
// （契约 03 §2.3；票 #20）。返回保存后的策略投影（含新关系修订）。
func (s *SyncService) UpdateMappingPolicy(input UpdateMappingPolicyDTO) (PolicyDTO, error) {
	rules := make([]model.MappingRule, 0, len(input.Rules))
	for _, r := range input.Rules {
		rules = append(rules, mappingRuleModel(r))
	}
	v, err := s.app.UpdateMappingPolicy(context.Background(), view.UpdateMappingPolicyInput{
		RelationID:       input.RelationID,
		ExpectedRevision: input.ExpectedRevision,
		Rules:            rules,
	})
	if err != nil {
		return PolicyDTO{}, err
	}
	return policyViewDTO(v), nil
}

// PrepareRebind 执行重绑预检（契约 03 §2.4；票 #22）。
func (s *SyncService) PrepareRebind(input PrepareRebindDTO) (RebindPreparationDTO, error) {
	v, err := s.app.PrepareRebind(context.Background(), view.PrepareRebindInput{
		RelationID: input.RelationID,
		Side:       input.Side,
		RootPath:   input.RootPath,
	})
	if err != nil {
		return RebindPreparationDTO{}, err
	}
	return rebindPreparationDTO(v), nil
}

// ApplyRebind 消费重绑预检并原位更新端点绑定（ADR-0003 单事务；恒 reinitialize）。
func (s *SyncService) ApplyRebind(preparationID string) (RelationDTO, error) {
	v, err := s.app.ApplyRebind(context.Background(), preparationID)
	if err != nil {
		return RelationDTO{}, err
	}
	return relationDTO(v), nil
}

func pageRequest(cursor string, limit int) ports.PageRequest {
	return ports.PageRequest{Cursor: cursor, Limit: limit}
}

// ---- Phase 2 Apply（契约 05；票 #36）----

// ConfirmPlan 确认 resolved 计划并创建 Apply 任务（契约 05 §3.1）。
// 幂等重入（D4）返回既有任务；任务此刻无 runner（T04），保持 queued。
func (s *SyncService) ConfirmPlan(input ConfirmPlanDTO) (TaskDTO, error) {
	v, err := s.app.ConfirmPlan(context.Background(), view.ConfirmPlanInput{PlanID: input.PlanID})
	if err != nil {
		return TaskDTO{}, err
	}
	return taskDTO(v), nil
}
