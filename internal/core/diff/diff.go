// Package diff 实现 baseline / project / runtime 三方差异判定（架构文档 §6.3）。
// 输出确定性的逐资源分类与冲突证据，供 plan 包生成候选操作；
// 本包只做纯计算，无 I/O、无副作用。
package diff

import (
	"fmt"
	"sort"

	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// Input 是 ThreeWay 的输入；Base 为 nil 表示执行初始化 diff。
type Input struct {
	RelationID string
	Base       *model.SyncBaseline // nil => 初始化 diff
	Project    model.ObservedSnapshot
	Runtime    model.ObservedSnapshot
}

// Classification 是单资源相对三方的差异分类。
type Classification string

// 差异分类取值（架构文档 §6.3 真值表）。
const (
	// ClassNoop 双端均未变化。
	ClassNoop Classification = "noop"
	// ClassProjectToRuntime project 变、runtime 未变且 project 仍存在。
	ClassProjectToRuntime Classification = "project_to_runtime"
	// ClassRuntimeToProject runtime 变、project 未变且 runtime 仍存在。
	ClassRuntimeToProject Classification = "runtime_to_project"
	// ClassRemoveRuntimeCandidate project 删、runtime 仍为 base 状态。
	ClassRemoveRuntimeCandidate Classification = "remove_runtime_candidate"
	// ClassRemoveProjectCandidate runtime 删、project 仍为 base 状态。
	ClassRemoveProjectCandidate Classification = "remove_project_candidate"
	// ClassConverged 双端同样变为相同指纹（含双端都删除）。
	ClassConverged Classification = "converged"
	// ClassConflictModify 双端均存在但以不同方式修改（modify_modify）。
	ClassConflictModify Classification = "conflict_modify"
	// ClassConflictDeleteModify 一侧删除、另一侧修改或新增。
	ClassConflictDeleteModify Classification = "conflict_delete_modify"
	// ClassAdoptEqual 无 baseline 时双端语义相同的资源可直接采纳。
	ClassAdoptEqual Classification = "adopt_equal"
	// ClassInitChoice 无 baseline 时单侧存在、双端不同或低置信度身份。
	ClassInitChoice Classification = "init_choice"
)

// ResourceDiff 是单资源的三方差异结论，含双侧语义指纹与基线存在性证据。
type ResourceDiff struct {
	ResourceID         model.ResourceID
	Kind               model.ResourceKind
	Classification     Classification
	ProjectPresent     bool
	RuntimePresent     bool
	BaseProjectPresent bool
	BaseRuntimePresent bool
	ProjectSemantic    string // SemanticDigest 结果；absent 为 ""
	RuntimeSemantic    string
}

// Result 是一次三方 diff 的完整输出；两个切片均按 ResourceID 字节序排序。
type Result struct {
	Diffs     []ResourceDiff
	Conflicts []model.Conflict // 含 Base/Project/Runtime 表示副本作证据
}

// ThreeWay 比较 baseline、project、runtime 三方状态并输出逐资源分类。
// 资源集合取 base ∪ project ∪ runtime 的 ResourceID 并集；
// 无 baseline 时按初始化规则判定（架构文档 §6.3）。
func ThreeWay(in Input) (Result, error) {
	ids := unionResourceIDs(in)
	sorted := make([]model.ResourceID, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	result := Result{Diffs: make([]ResourceDiff, 0, len(sorted))}
	for _, id := range sorted {
		d, conflict, err := classifyResource(in, id)
		if err != nil {
			return Result{}, err
		}
		result.Diffs = append(result.Diffs, d)
		if conflict != nil {
			result.Conflicts = append(result.Conflicts, *conflict)
		}
	}
	// Diffs 按字节序遍历，Conflicts 因此天然有序。
	return result, nil
}

// unionResourceIDs 求三方资源 ID 并集。
func unionResourceIDs(in Input) map[model.ResourceID]struct{} {
	ids := make(map[model.ResourceID]struct{})
	for id := range in.Project.Resources {
		ids[id] = struct{}{}
	}
	for id := range in.Runtime.Resources {
		ids[id] = struct{}{}
	}
	if in.Base != nil {
		for id := range in.Base.Resources {
			ids[id] = struct{}{}
		}
	}
	return ids
}

// classifyResource 计算单资源分类与冲突（无冲突时 conflict 为 nil）。
func classifyResource(in Input, id model.ResourceID) (ResourceDiff, *model.Conflict, error) {
	projObs, projPresent := in.Project.Resources[id]
	rtObs, rtPresent := in.Runtime.Resources[id]

	d := ResourceDiff{
		ResourceID:     id,
		ProjectPresent: projPresent,
		RuntimePresent: rtPresent,
	}
	// Kind 优先取观察侧（project 优先于 runtime）；两侧均无观察时从 ID 推导。
	switch {
	case projPresent:
		d.Kind = projObs.Kind
	case rtPresent:
		d.Kind = rtObs.Kind
	default:
		d.Kind = normalize.KindOfResourceID(id)
	}

	projSem, err := observedSemantic(id, "project", projPresent, projObs)
	if err != nil {
		return d, nil, err
	}
	rtSem, err := observedSemantic(id, "runtime", rtPresent, rtObs)
	if err != nil {
		return d, nil, err
	}
	d.ProjectSemantic, d.RuntimeSemantic = projSem, rtSem

	// 基线侧：表示指针即存在性（State=absent 时两侧指针均为 nil）。
	var baseRes *model.BaselineResource
	if in.Base != nil {
		if br, ok := in.Base.Resources[id]; ok {
			baseRes = &br
		}
	}
	var baseProjRep, baseRtRep *model.Representation
	if baseRes != nil {
		baseProjRep, baseRtRep = baseRes.ProjectRepresentation, baseRes.RuntimeRepresentation
	}
	d.BaseProjectPresent = baseProjRep != nil
	d.BaseRuntimePresent = baseRtRep != nil

	baseProjSem, err := baselineSemantic(id, "project", baseProjRep)
	if err != nil {
		return d, nil, err
	}
	baseRtSem, err := baselineSemantic(id, "runtime", baseRtRep)
	if err != nil {
		return d, nil, err
	}

	if in.Base == nil {
		d, conflict := classifyInit(d, projObs, rtObs)
		return d, conflict, nil
	}
	d, conflict := classifyWithBase(d, baseRes, baseProjSem, baseRtSem, projObs, rtObs)
	return d, conflict, nil
}

// classifyWithBase 按架构文档 §6.3 真值表分类（有 baseline）。
func classifyWithBase(
	d ResourceDiff, baseRes *model.BaselineResource, baseProjSem, baseRtSem string,
	projObs, rtObs model.ResourceObservation,
) (ResourceDiff, *model.Conflict) {
	projChanged := changed(d.ProjectPresent, d.ProjectSemantic, d.BaseProjectPresent, baseProjSem)
	rtChanged := changed(d.RuntimePresent, d.RuntimeSemantic, d.BaseRuntimePresent, baseRtSem)

	switch {
	case !projChanged && !rtChanged:
		// 双端均未变
		d.Classification = ClassNoop
		return d, nil

	case projChanged && !rtChanged:
		// project 变、runtime 未变
		if d.ProjectPresent {
			d.Classification = ClassProjectToRuntime
		} else {
			d.Classification = ClassRemoveRuntimeCandidate
		}
		return d, nil

	case !projChanged && rtChanged:
		// runtime 变、project 未变
		if d.RuntimePresent {
			d.Classification = ClassRuntimeToProject
		} else {
			d.Classification = ClassRemoveProjectCandidate
		}
		return d, nil
	}

	// 双端均已变化
	switch {
	case d.ProjectPresent && d.RuntimePresent:
		if d.ProjectSemantic == d.RuntimeSemantic {
			// 同样变为相同指纹
			d.Classification = ClassConverged
			return d, nil
		}
		d.Classification = ClassConflictModify
		return d, &model.Conflict{
			ResourceID: d.ResourceID,
			Kind:       model.ConflictModifyModify,
			Base:       baseEvidence(baseRes),
			Project:    representationCopy(d.ProjectPresent, projObs),
			Runtime:    representationCopy(d.RuntimePresent, rtObs),
		}
	case !d.ProjectPresent && !d.RuntimePresent:
		// 双端都删除
		d.Classification = ClassConverged
		return d, nil
	default:
		// 一侧删除、另一侧修改/新增
		d.Classification = ClassConflictDeleteModify
		return d, &model.Conflict{
			ResourceID: d.ResourceID,
			Kind:       model.ConflictDeleteModify,
			Base:       baseEvidence(baseRes),
			Project:    representationCopy(d.ProjectPresent, projObs),
			Runtime:    representationCopy(d.RuntimePresent, rtObs),
		}
	}
}

// classifyInit 处理无 baseline 的初始化判定（架构文档 §6.3）：
// 双端相同可 adopt_equal；单侧存在、双端不同必须交给用户选择；
// 低置信度 mod 身份绝不参与跨侧等价判定。
func classifyInit(d ResourceDiff, projObs, rtObs model.ResourceObservation) (ResourceDiff, *model.Conflict) {
	switch {
	case d.ProjectPresent && d.RuntimePresent:
		lowConfidence := d.Kind == model.ResourceMod &&
			(projObs.Identity.Confidence != model.ConfidenceHigh ||
				rtObs.Identity.Confidence != model.ConfidenceHigh)
		switch {
		case lowConfidence:
			d.Classification = ClassInitChoice
		case d.ProjectSemantic == d.RuntimeSemantic:
			d.Classification = ClassAdoptEqual
		default:
			d.Classification = ClassInitChoice
		}
	default:
		// 仅单侧存在（无 baseline 时资源并集保证至少一侧存在）
		d.Classification = ClassInitChoice
	}
	if d.Classification != ClassInitChoice {
		return d, nil
	}
	return d, &model.Conflict{
		ResourceID: d.ResourceID,
		Kind:       model.ConflictInitialize,
		Project:    representationCopy(d.ProjectPresent, projObs),
		Runtime:    representationCopy(d.RuntimePresent, rtObs),
	}
}

// changed 判断观察侧相对 baseline 是否变化：
// 存在性翻转，或双侧均在但语义摘要不同。
func changed(present bool, sem string, basePresent bool, baseSem string) bool {
	if present != basePresent {
		return true
	}
	return present && sem != baseSem
}

// observedSemantic 计算观察侧语义摘要；absent 为 ""。
func observedSemantic(id model.ResourceID, side string, present bool, obs model.ResourceObservation) (string, error) {
	if !present {
		return "", nil
	}
	sem, err := normalize.SemanticDigest(obs.Kind, obs.Representation, obs.Identity)
	if err != nil {
		return "", fmt.Errorf("diff: %s 侧资源 %s: %w", side, id, err)
	}
	return sem, nil
}

// baselineSemantic 计算基线侧语义摘要；absent 为 ""。
// 基线不携带 Kind/Identity，统一由 ResourceID 推导。
func baselineSemantic(id model.ResourceID, side string, rep *model.Representation) (string, error) {
	if rep == nil {
		return "", nil
	}
	sem, err := normalize.SemanticDigest(normalize.KindOfResourceID(id), *rep, normalize.IdentityFromResourceID(id))
	if err != nil {
		return "", fmt.Errorf("diff: baseline %s 侧资源 %s: %w", side, id, err)
	}
	return sem, nil
}

// representationCopy 返回观察表示的浅副本；absent 为 nil。
// 副本避免冲突证据与调用方传入的快照/基线共享可变指针。
func representationCopy(present bool, obs model.ResourceObservation) *model.Representation {
	if !present {
		return nil
	}
	rep := obs.Representation
	return &rep
}

// baseEvidence 提取基线侧冲突证据：基线是双方认可状态，两侧表示语义一致，
// 取 project 表示，缺失时回退 runtime 表示。
func baseEvidence(baseRes *model.BaselineResource) *model.Representation {
	if baseRes == nil {
		return nil
	}
	if baseRes.ProjectRepresentation != nil {
		rep := *baseRes.ProjectRepresentation
		return &rep
	}
	if baseRes.RuntimeRepresentation != nil {
		rep := *baseRes.RuntimeRepresentation
		return &rep
	}
	return nil
}
