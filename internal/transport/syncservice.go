package transport

import (
	"context"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/policy"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// SyncService 是 relation 域用例的 Wails 出口（与 legacy 三服务并存注册）。
// 写路径（ConfirmPlan/Apply 执行/Restore）按 Phase 2 票面逐票点亮；读投影
// （Apply 运行头/逐操作/历史提交）自票 #39 起挂载。
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

// GetApplyRun 返回该工作区当前/最近一次 Apply 运行头投影（契约 05 §3.2；票 #39）。
// 关系无任何运行记录 → err.apply.no_run。
func (s *SyncService) GetApplyRun(relationID string) (ApplyRunDTO, error) {
	v, err := s.app.GetApplyRun(context.Background(), relationID)
	if err != nil {
		return ApplyRunDTO{}, err
	}
	return applyRunDTO(v), nil
}

// ListApplyOperations 逐操作清单分页（契约 05 §3.3；票 #39）：ordinal 升序，
// cursor 为上一页末条 operation_id（GetChanges 同协议）；task 不存在或跨关系
// → err.apply.run_not_found。DTO 为白名单投影，绝不含临时路径与 ownership proof
// （契约 05 §0 硬约束 4）。
func (s *SyncService) ListApplyOperations(relationID, taskID, cursor string, limit int) (ApplyOperationPageDTO, error) {
	v, err := s.app.ListApplyOperations(context.Background(), view.ListApplyOperationsInput{
		RelationID: relationID, TaskID: taskID, Cursor: cursor, Limit: limit,
	})
	if err != nil {
		return ApplyOperationPageDTO{}, err
	}
	return applyOperationPageDTO(v), nil
}

// ListCommits 历史提交列表，created_at DESC 分页（契约 05 §3.5；票 #39）；
// cursor 为上一页末条 commit_id。
func (s *SyncService) ListCommits(relationID, cursor string, limit int) (CommitPageDTO, error) {
	v, err := s.app.ListCommits(context.Background(), relationID, pageRequest(cursor, limit))
	if err != nil {
		return CommitPageDTO{}, err
	}
	return commitPageDTO(v), nil
}

// GetCommit 单提交详情，changes 全量（契约 05 §3.5；票 #39）；
// 记录不存在或跨关系 → err.commit.not_found。
func (s *SyncService) GetCommit(relationID, commitID string) (CommitDTO, error) {
	v, err := s.app.GetCommit(context.Background(), relationID, commitID)
	if err != nil {
		return CommitDTO{}, err
	}
	return commitDTO(v), nil
}

// AcknowledgeRecovery 人工确认恢复收口（契约 05 §3.4；票 #38）：前置
// run=recovery_required（否则 err.recovery.not_required），效果 acknowledged_at
// 落库 + 关系复位 healthy（头基线不动、不建 SyncCommit），发布
// relation_invalidated 引导重扫；返回确认后的工作区投影。已确认重入幂等返回。
func (s *SyncService) AcknowledgeRecovery(taskID string) (WorkspaceDTO, error) {
	v, err := s.app.AcknowledgeRecovery(context.Background(), taskID)
	if err != nil {
		return WorkspaceDTO{}, err
	}
	return workspaceDTO(v), nil
}

// ---- 回滚计划面（契约 06 §2/§3；票 #59）----

// PrepareRestore 准备回滚：只收 relation_id + commit_id，目标 baseline 后端推导；
// 四标记判定 + CF 尽力探测，draft 落 sync_plans(kind=restore)（契约 06 §3.1）。
// 成功 → RestorePlanDTO（status=draft）。
func (s *SyncService) PrepareRestore(input RestorePrepareDTO) (RestorePlanDTO, error) {
	v, err := s.app.PrepareRestore(context.Background(), view.PrepareRestoreInput{
		RelationID: input.RelationID,
		CommitID:   input.CommitID,
	})
	if err != nil {
		return RestorePlanDTO{}, err
	}
	return restorePlanDTO(v), nil
}

// ResolveRestorePlan 固化回滚决议（契约 06 §3.3）：仅 partial 逐资源 skip，
// exact 遇就绪面不满前置拒绝（err.restore.exact_infeasible）。
func (s *SyncService) ResolveRestorePlan(input ResolveRestorePlanDTO) (RestorePlanDTO, error) {
	v, err := s.app.ResolveRestorePlan(context.Background(), view.ResolveRestorePlanInput{
		PlanID:             input.PlanID,
		RequestedExactness: input.RequestedExactness,
		SkipResourceIDs:    input.SkipResourceIDs,
	})
	if err != nil {
		return RestorePlanDTO{}, err
	}
	return restorePlanDTO(v), nil
}

// GetRestorePlan 查询回滚计划（对称 GetPlan 的读伴随，契约 06 §2；
// stale/expired 为读取时投影）。
func (s *SyncService) GetRestorePlan(planID string) (RestorePlanDTO, error) {
	v, err := s.app.GetRestorePlan(context.Background(), planID)
	if err != nil {
		return RestorePlanDTO{}, err
	}
	return restorePlanDTO(v), nil
}

// StageUserObject 用户对象补全（契约 06 §3.5）：draft/resolved 均可补全；
// 按 expected_digest 验收不符 → err.userobject.hash_mismatch（{0}=期望摘要，
// 可重试）。成功返回更新后的 RestorePlanDTO（该行 staged=true）。
func (s *SyncService) StageUserObject(input StageUserObjectDTO) (RestorePlanDTO, error) {
	v, err := s.app.StageUserObject(context.Background(), view.StageUserObjectInput{
		PlanID:     input.PlanID,
		ResourceID: input.ResourceID,
		SourcePath: input.SourcePath,
	})
	if err != nil {
		return RestorePlanDTO{}, err
	}
	return restorePlanDTO(v), nil
}
