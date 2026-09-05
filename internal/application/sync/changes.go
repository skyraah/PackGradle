package sync

import (
	"context"
	"sort"
	"strings"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/diff"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// 错误码（契约 03 §3；票 #19）。文案由前端 locale 提供。
const (
	// CodeChangesSnapshotPairInvalid 是显式传入的快照对不属同 Relation / 非相对两侧。
	CodeChangesSnapshotPairInvalid = "err.changes.snapshot_pair_invalid"
	// CodeSyncInvalidFilter 是筛选值不在合法枚举内（classification / resource_kind）。
	CodeSyncInvalidFilter = "err.sync.invalid_filter"
)

// changeClassifications 是 GetChanges 分类筛选的合法值（diff 包真值表全集）。
var changeClassifications = map[diff.Classification]bool{
	diff.ClassNoop:                   true,
	diff.ClassProjectToRuntime:       true,
	diff.ClassRuntimeToProject:       true,
	diff.ClassRemoveRuntimeCandidate: true,
	diff.ClassRemoveProjectCandidate: true,
	diff.ClassConverged:              true,
	diff.ClassConflictModify:         true,
	diff.ClassConflictDeleteModify:   true,
	diff.ClassAdoptEqual:             true,
	diff.ClassInitChoice:             true,
	diff.ClassMergedClean:            true, // ADR-0009 §4，票 #87（契约 07 §3.3 枚举扩展）
}

// changeResourceKinds 是 GetChanges 资源类型筛选的合法值。
var changeResourceKinds = map[model.ResourceKind]bool{
	model.ResourceMod:        true,
	model.ResourceTextFile:   true,
	model.ResourceBinaryFile: true,
}

// GetChanges 资源级变更浏览（契约 03 §2.2）：读时计算 head baseline + 指定/最新
// 快照对的三方 Diff，不存储投影、不写库。summary 恒为全量分组计数，筛选只影响
// items；分页按 resource_id 字节序（diff 输出天然有序），cursor 为上一页最后一条
// resource_id，筛选条件由调用方跨页保持不变。
func (a *App) GetChanges(ctx context.Context, input view.GetChangesInput) (view.ChangesPage, error) {
	rel, err := a.deps.Relations.Get(ctx, input.RelationID)
	if err != nil {
		return view.ChangesPage{}, errs.New(CodeRelationNotFound, input.RelationID)
	}
	// 廉价校验先行：非法筛选值与快照缺失同时存在时，稳定返回 invalid_filter
	if input.Classification != "" && !changeClassifications[diff.Classification(input.Classification)] {
		return view.ChangesPage{}, errs.New(CodeSyncInvalidFilter, "classification", input.Classification)
	}
	if input.ResourceKind != "" && !changeResourceKinds[model.ResourceKind(input.ResourceKind)] {
		return view.ChangesPage{}, errs.New(CodeSyncInvalidFilter, "resource_kind", input.ResourceKind)
	}
	// 端点根目录：合并判定的三侧全文读取缝所需（票 #87，ADR-0009 §1）
	projEnd, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		return view.ChangesPage{}, err
	}
	rtEnd, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		return view.ChangesPage{}, err
	}
	snapP, err := a.changesSnapshot(ctx, input.RelationID, input.ProjectSnapshotID, model.SideProject)
	if err != nil {
		return view.ChangesPage{}, err
	}
	snapR, err := a.changesSnapshot(ctx, input.RelationID, input.RuntimeSnapshotID, model.SideRuntime)
	if err != nil {
		return view.ChangesPage{}, err
	}

	var base *model.SyncBaseline
	if rel.HeadBaselineID != "" {
		b, berr := a.deps.Baselines.Get(ctx, rel.HeadBaselineID)
		if berr != nil {
			return view.ChangesPage{}, berr
		}
		base = &b
	}

	result, err := diff.ThreeWay(diff.Input{RelationID: input.RelationID, Base: base, Project: snapP, Runtime: snapR,
		Merge: a.mergeSources(ctx, projEnd.RootPath, rtEnd.RootPath)})
	if err != nil {
		return view.ChangesPage{}, err
	}

	// direction=ignore 的资源已移出受管范围，不进变更列表与计数（票 #100，
	// ADR-0013 §3；与计划构建同口径过滤）。
	policySet, err := a.deps.Mappings.GetPolicy(ctx, input.RelationID)
	if err != nil {
		return view.ChangesPage{}, err
	}
	ignored := ignoreDirectionFilter(policySet, snapP, snapR)
	visible := make([]diff.ResourceDiff, 0, len(result.Diffs))
	for _, d := range result.Diffs {
		if !ignored(d.ResourceID) {
			visible = append(visible, d)
		}
	}

	// 逐资源诊断：取两侧快照中 ResourceID 命中的持久化诊断（映射冲突/未知格式/低置信度等）。
	diagsByResource := changesDiagnostics(snapP, snapR)
	conflictsByResource := make(map[model.ResourceID][]model.Conflict, len(result.Conflicts))
	for _, c := range result.Conflicts {
		conflictsByResource[c.ResourceID] = append(conflictsByResource[c.ResourceID], c)
	}

	// 先筛后页：items 为筛选后按 resource_id 字节序的子序列，cursor 以字节序推进。
	items := make([]view.ChangeView, 0, len(visible))
	for _, d := range visible {
		if input.Classification != "" && string(d.Classification) != input.Classification {
			continue
		}
		if input.ResourceKind != "" && d.Kind != model.ResourceKind(input.ResourceKind) {
			continue
		}
		projRep, rtRep := changeObservedReps(snapP, snapR, d.ResourceID)
		baseRep := changeBaselineRep(base, d.ResourceID)
		path := changeRelativePath(projRep, rtRep, baseRep)
		if input.PathPrefix != "" && !strings.HasPrefix(path, input.PathPrefix) {
			continue
		}
		conflicts := conflictsByResource[d.ResourceID]
		if conflicts == nil {
			conflicts = []model.Conflict{}
		}
		diags := diagsByResource[d.ResourceID]
		if diags == nil {
			diags = []model.Diagnostic{}
		}
		items = append(items, view.ChangeView{
			ResourceID:     string(d.ResourceID),
			ResourceKind:   string(d.Kind),
			RelativePath:   path,
			Classification: string(d.Classification),
			Base:           baseRep,
			Project:        projRep,
			Runtime:        rtRep,
			Conflicts:      conflicts,
			Diagnostics:    diags,
		})
	}

	page := view.ChangesPage{
		SchemaVersion: model.CurrentSchemaVersion,
		Items:         make([]view.ChangeView, 0),
		Summary:       changesSummary(visible),
	}
	limit := ports.PageRequest{Cursor: input.Cursor, Limit: input.Limit}.NormalizeLimit()
	start := 0
	if input.Cursor != "" {
		start = sort.Search(len(items), func(i int) bool { return items[i].ResourceID > input.Cursor })
	}
	if start < len(items) {
		end := start + limit
		if end > len(items) {
			end = len(items)
		}
		page.Items = items[start:end]
		if end < len(items) {
			page.NextCursor = items[end-1].ResourceID
		}
	}
	return page, nil
}

// changesSnapshot 解析单侧快照：显式 ID 必须同属该 Relation 且 side 相对
// （契约 03 §3 err.changes.snapshot_pair_invalid）；缺省取该侧最新，无快照
// 返回 err.sync.snapshot_not_found（args {0}=side）。
func (a *App) changesSnapshot(ctx context.Context, relationID, snapshotID string, side model.Side) (model.ObservedSnapshot, error) {
	if snapshotID != "" {
		s, err := a.deps.Snapshots.GetForRelation(ctx, snapshotID, relationID, side)
		if err != nil {
			return model.ObservedSnapshot{}, errs.NewDetail(CodeChangesSnapshotPairInvalid, err.Error())
		}
		return s, nil
	}
	s, ok, err := a.deps.Snapshots.LatestByRelationSide(ctx, relationID, side)
	if err != nil {
		return model.ObservedSnapshot{}, err
	}
	if !ok {
		return model.ObservedSnapshot{}, errs.New(CodeSyncSnapshotNotFound, string(side))
	}
	return s, nil
}

// changesDiagnostics 汇集两侧快照中按 ResourceID 命中的持久化诊断
// （project 侧在前，runtime 侧在后；无 ResourceID 的整体诊断不挂到单资源行）。
func changesDiagnostics(snapP, snapR model.ObservedSnapshot) map[model.ResourceID][]model.Diagnostic {
	out := make(map[model.ResourceID][]model.Diagnostic)
	collect := func(diags []model.Diagnostic) {
		for _, d := range diags {
			if d.ResourceID == "" {
				continue
			}
			out[d.ResourceID] = append(out[d.ResourceID], d)
		}
	}
	collect(snapP.Diagnostics)
	collect(snapR.Diagnostics)
	return out
}

// changeObservedReps 取观察侧表示（absent 为 nil）。
func changeObservedReps(snapP, snapR model.ObservedSnapshot, id model.ResourceID) (*model.Representation, *model.Representation) {
	var projRep, rtRep *model.Representation
	if obs, ok := snapP.Resources[id]; ok {
		rep := obs.Representation
		projRep = &rep
	}
	if obs, ok := snapR.Resources[id]; ok {
		rep := obs.Representation
		rtRep = &rep
	}
	return projRep, rtRep
}

// changeBaselineRep 取基线表示（无基线/tombstone 为 nil）：基线是双方认可状态，
// 两侧语义一致，与 diff.baseEvidence 同序取 project 表示、缺失回退 runtime 表示。
func changeBaselineRep(base *model.SyncBaseline, id model.ResourceID) *model.Representation {
	if base == nil {
		return nil
	}
	res, ok := base.Resources[id]
	if !ok {
		return nil
	}
	if res.ProjectRepresentation != nil {
		rep := *res.ProjectRepresentation
		return &rep
	}
	if res.RuntimeRepresentation != nil {
		rep := *res.RuntimeRepresentation
		return &rep
	}
	return nil
}

// changeRelativePath 取资源展示路径：观察侧优先（project → runtime），
// 基线回退；全缺失（双侧删除后 tombstone）为空串。
func changeRelativePath(projRep, rtRep, baseRep *model.Representation) string {
	for _, rep := range []*model.Representation{projRep, rtRep, baseRep} {
		if rep != nil && rep.RelativePath != "" {
			return rep.RelativePath
		}
	}
	return ""
}

// changesSummary 按契约 03 §2.2 分类表归组（全量计数，不受筛选影响）。
func changesSummary(diffs []diff.ResourceDiff) view.ChangesSummary {
	s := view.ChangesSummary{Total: len(diffs)}
	for _, d := range diffs {
		switch d.Classification {
		case diff.ClassNoop:
			s.NoopCount++
		case diff.ClassConverged:
			s.ConvergedCount++
		case diff.ClassAdoptEqual:
			s.AdoptEqualCount++
		case diff.ClassInitChoice:
			s.InitChoiceCount++
		case diff.ClassProjectToRuntime:
			if d.RuntimePresent {
				s.ModifyCount++
			} else {
				s.CreateCount++
			}
		case diff.ClassRuntimeToProject:
			if d.ProjectPresent {
				s.ModifyCount++
			} else {
				s.CreateCount++
			}
		case diff.ClassRemoveRuntimeCandidate, diff.ClassRemoveProjectCandidate:
			s.DeleteCount++
		case diff.ClassConflictModify, diff.ClassConflictDeleteModify:
			s.ConflictCount++
		case diff.ClassMergedClean:
			s.MergedCleanCount++
		}
	}
	return s
}
