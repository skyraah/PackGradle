// Package plan 从三方差异生成确定性 draft/resolved 计划（架构文档 §6.5）。
// 计划是纯数据推导：相同输入必须产生完全相同的操作序列与 PlanDigest；
// 本包不做任何 I/O，无副作用，PlanID 由 application 层分配。
package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"packgradle/internal/core/diff"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// BuildInput 是 BuildDraft 的输入。
type BuildInput struct {
	RelationID         string
	RelationRevision   int
	Policy             model.MappingPolicy
	PolicyDigest       string
	Kind               model.PlanKind // initialize（Base==nil 时）| sync
	Base               *model.SyncBaseline
	BaseBaselineDigest string // Base 非空时必填
	Project            model.ObservedSnapshot
	Runtime            model.ObservedSnapshot
	ExpectedBindings   model.ExpectedBindings
	// RequestedExactness 是请求确切度（exact|allow_partial）；空值缺省 allow_partial
	// （保守：未声明 exact 的计划按部分完成对待），显式非法值报错。
	RequestedExactness model.Exactness
	ExpiresAt          time.Time
	// PreserveMaxBytes 是大文件保全阈值（ADR-0007 §7，票 #64；0＝不限）：
	// 非 mod 覆盖/删除行的旧内容超过阈值时操作行固化 preserve_skip=true
	//（「旧版本不留存」警示 + 引擎跳过 before 保全）。判定口径共享
	// model.ShouldSkipPreserve；未设置（负值）按 0=不限处理。
	PreserveMaxBytes int64
	// Merge 是合并判定的三侧全文读取缝（票 #87，ADR-0009 §1）：原样透传给
	// diff.ThreeWay；nil = 合并面禁用（双侧同改维持 conflict_modify 现状）。
	Merge *diff.MergeSources
}

// 校验错误。Resolve 拒绝的输入以 error 返回，不 panic。
var (
	ErrResolutionIncomplete    = errors.New("plan: resolutions 未恰好覆盖全部冲突")
	ErrResolutionUnknown       = errors.New("plan: resolution 指向非冲突资源")
	ErrResolutionInvalidChoice = errors.New("plan: resolution 选择与冲突类型不合法")
)

// 映射规则方向取值（model.MappingRule.Direction）。
const (
	directionBidirectional    = "bidirectional"
	directionProjectToRuntime = "project_to_runtime"
	directionRuntimeToProject = "runtime_to_project"
	directionIgnore           = "ignore"
)

// 侧与存在性取值。
const (
	sideProject = string(model.SideProject)
	sideRuntime = string(model.SideRuntime)

	existencePresent = "present"
	existenceAbsent  = "absent"
)

// 确认码（ConfirmationRequirement.Code）。
const (
	confirmOverwrite     = "overwrite"
	confirmDelete        = "delete"
	confirmWriteProject  = "write_project"
	confirmUnrecoverable = "unrecoverable"
)

// BuildDraft 从三方差异生成 draft 计划。
// 候选分类映射为操作：project_to_runtime→write_runtime、
// remove_runtime_candidate→remove_runtime、runtime_to_project→write_project、
// remove_project_candidate→remove_project；noop/converged/adopt_equal 不产生操作
// （adopt_equal 计入 Summary.AdoptEqualCount）。
// 方向为 ignore 的资源完全不进计划（操作/冲突/摘要均剔除）。
func BuildDraft(in BuildInput) (model.SyncPlan, error) {
	if in.Base != nil && in.BaseBaselineDigest == "" {
		return model.SyncPlan{}, errors.New("plan: Base 非空时 BaseBaselineDigest 必填")
	}
	if in.RequestedExactness == "" {
		in.RequestedExactness = model.ExactnessAllowPartial
	}
	if in.RequestedExactness != model.ExactnessExact && in.RequestedExactness != model.ExactnessAllowPartial {
		return model.SyncPlan{}, fmt.Errorf("plan: requested_exactness 非法: %s", in.RequestedExactness)
	}
	res, err := diff.ThreeWay(diff.Input{
		RelationID: in.RelationID,
		Base:       in.Base,
		Project:    in.Project,
		Runtime:    in.Runtime,
		Merge:      in.Merge,
	})
	if err != nil {
		return model.SyncPlan{}, fmt.Errorf("plan: 三方差异计算失败: %w", err)
	}

	conflictByResource := make(map[model.ResourceID]model.Conflict, len(res.Conflicts))
	for _, c := range res.Conflicts {
		conflictByResource[c.ResourceID] = c
	}

	var ops []model.PlannedOperation
	var conflicts []model.Conflict
	summary := model.PlanSummary{}

	for _, d := range res.Diffs { // Diffs 已按 ResourceID 字节序排序
		direction := resourceDirection(in.Policy, in.Project, in.Runtime, d.ResourceID)
		if direction == directionIgnore {
			continue
		}
		summary.ResourceTotal++

		projObs := observation(in.Project, d.ResourceID)
		rtObs := observation(in.Runtime, d.ResourceID)

		var op *model.PlannedOperation
		switch d.Classification {
		case diff.ClassProjectToRuntime:
			op = newOperation(model.OpWriteRuntime, d.ResourceID,
				writePreconditions(d.ResourceID, sideProject, projObs, rtObs))
			markMaterialization(op, projObs)
		case diff.ClassRuntimeToProject:
			op = newOperation(model.OpWriteProject, d.ResourceID,
				writePreconditions(d.ResourceID, sideRuntime, rtObs, projObs))
			markMaterialization(op, rtObs)
		case diff.ClassRemoveRuntimeCandidate:
			op = newOperation(model.OpRemoveRuntime, d.ResourceID,
				removePreconditions(d.ResourceID, sideRuntime, rtObs))
		case diff.ClassRemoveProjectCandidate:
			op = newOperation(model.OpRemoveProject, d.ResourceID,
				removePreconditions(d.ResourceID, sideProject, projObs))
		case diff.ClassAdoptEqual:
			summary.AdoptEqualCount++
		case diff.ClassMergedClean:
			// 干净合并行（ADR-0009 §4，票 #87）：非冲突操作计数，不并入
			// modify；write_merged 操作面归执行票（暂存期按三侧快照重算）。
			summary.MergedCleanCount++
		}
		if op != nil && opAllowed(direction, op.Kind) {
			ops = append(ops, *op)
		}
		if c, ok := conflictByResource[d.ResourceID]; ok {
			// 方向写入 Detail（PlanDigest 只取 resource_id+kind，不受影响），
			// 供 Resolve 在没有 Policy 的情况下过滤 resolution 生成的操作；
			// detail 已携带 hunk JSON 证据时（票 #87）以兄弟键并入，不覆盖证据。
			c.Detail = withDirectionDetail(c.Detail, direction)
			conflicts = append(conflicts, c)
		}
	}

	summary.ConflictCount = len(conflicts)
	summary.CreateCount, summary.ModifyCount, summary.DeleteCount =
		summarizeOps(ops, in.Project, in.Runtime)

	sortAndNumberOperations(ops)
	markPreserveSkip(ops, in)

	out := model.SyncPlan{
		SchemaVersion:              model.CurrentSchemaVersion,
		RelationID:                 in.RelationID,
		Kind:                       in.Kind,
		BaseBaselineID:             baseBaselineID(in.Base),
		BaseBaselineDigest:         in.BaseBaselineDigest,
		InputProjectSnapshotID:     in.Project.SnapshotID,
		InputRuntimeSnapshotID:     in.Runtime.SnapshotID,
		InputProjectSnapshotDigest: in.Project.SnapshotDigest,
		InputRuntimeSnapshotDigest: in.Runtime.SnapshotDigest,
		RelationRevision:           in.RelationRevision,
		PolicyDigest:               in.PolicyDigest,
		ExpectedBindings:           in.ExpectedBindings,
		RequestedExactness:         in.RequestedExactness,
		Status:                     model.PlanDraft,
		ExpiresAt:                  formatExpiry(in.ExpiresAt),
		Operations:                 ops,
		Conflicts:                  conflicts,
		Summary:                    summary,
		// 诊断证据随计划透出（project 侧在前，保持确定性）；不参与 PlanDigest
		Diagnostics: appendDiagnostics(in.Project.Diagnostics, in.Runtime.Diagnostics),
	}
	digest, err := normalize.PlanDigest(out)
	if err != nil {
		return model.SyncPlan{}, fmt.Errorf("plan: 计算计划摘要失败: %w", err)
	}
	out.PlanDigest = digest
	return out, nil
}

// Resolve 校验 resolutions 恰好覆盖 draft 的全部冲突后应用选择，生成 resolved plan。
// project/runtime 快照用于生成 resolution 操作的前置条件；Resolve 不修改 draft，
// 也不假设快照仍与 draft 一致（不一致时前置条件会在 Apply 阶段拦截）。
// preserveMaxBytes 是大文件保全阈值（与 BuildDraft.PreserveMaxBytes 同源，ADR-0007
// §7；可变参数缺省 0=不限，生产路径恒显式传入）：决议生成的操作行在决议时点
// 重新做 preserve_skip 判定——决议可能改变目标侧，旧内容所在侧随选择而变，
// 不能照抄 draft 标记。
func Resolve(draft model.SyncPlan, project, runtime model.ObservedSnapshot, resolutions []model.Resolution,
	preserveMaxBytes ...int64) (model.SyncPlan, error) {
	threshold := int64(0)
	if len(preserveMaxBytes) > 0 {
		threshold = preserveMaxBytes[0]
	}
	conflictByResource := make(map[model.ResourceID]model.Conflict, len(draft.Conflicts))
	for _, c := range draft.Conflicts {
		conflictByResource[c.ResourceID] = c
	}

	covered := make(map[model.ResourceID]bool, len(resolutions))
	sorted := make([]model.Resolution, 0, len(resolutions))
	for _, r := range resolutions {
		conflict, ok := conflictByResource[r.ResourceID]
		if !ok {
			return model.SyncPlan{}, fmt.Errorf("%w: %s", ErrResolutionUnknown, r.ResourceID)
		}
		if covered[r.ResourceID] {
			return model.SyncPlan{}, fmt.Errorf("%w: %s 重复", ErrResolutionIncomplete, r.ResourceID)
		}
		if !validChoice(conflict.Kind, r.Choice) {
			return model.SyncPlan{}, fmt.Errorf("%w: %s 不接受 %s", ErrResolutionInvalidChoice, r.ResourceID, r.Choice)
		}
		// 源侧证据校验：不能「从不存在的一侧初始化」，否则会生成永不可执行的写操作
		if r.Choice == model.ChoiceInitializeFromProject && conflict.Project == nil {
			return model.SyncPlan{}, fmt.Errorf("%w: %s 的 project 侧不存在，不能 initialize_from_project", ErrResolutionInvalidChoice, r.ResourceID)
		}
		if r.Choice == model.ChoiceInitializeFromRuntime && conflict.Runtime == nil {
			return model.SyncPlan{}, fmt.Errorf("%w: %s 的 runtime 侧不存在，不能 initialize_from_runtime", ErrResolutionInvalidChoice, r.ResourceID)
		}
		covered[r.ResourceID] = true
		sorted = append(sorted, r)
	}
	for id := range conflictByResource {
		if !covered[id] {
			return model.SyncPlan{}, fmt.Errorf("%w: 缺少 %s", ErrResolutionIncomplete, id)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ResourceID < sorted[j].ResourceID })

	// resolution 生成的操作 merge 进 draft 操作后统一重排序编号
	ops := make([]model.PlannedOperation, 0, len(draft.Operations)+len(sorted))
	ops = append(ops, draft.Operations...)
	for _, r := range sorted {
		conflict := conflictByResource[r.ResourceID]
		direction := detailDirection(conflict.Detail)
		projObs := observation(project, r.ResourceID)
		rtObs := observation(runtime, r.ResourceID)
		var op *model.PlannedOperation
		switch r.Choice {
		case model.ChoiceInitializeFromProject:
			op = newOperation(model.OpWriteRuntime, r.ResourceID,
				writePreconditions(r.ResourceID, sideProject, projObs, rtObs))
			markMaterialization(op, projObs)
		case model.ChoiceInitializeFromRuntime:
			op = newOperation(model.OpWriteProject, r.ResourceID,
				writePreconditions(r.ResourceID, sideRuntime, rtObs, projObs))
			markMaterialization(op, rtObs)
		case model.ChoiceTakeProject:
			// 使 runtime 匹配 project 状态：project 表示非 nil 则写 runtime，否则删 runtime
			if conflict.Project != nil {
				op = newOperation(model.OpWriteRuntime, r.ResourceID,
					writePreconditions(r.ResourceID, sideProject, projObs, rtObs))
				markMaterialization(op, projObs)
			} else {
				op = newOperation(model.OpRemoveRuntime, r.ResourceID,
					removePreconditions(r.ResourceID, sideRuntime, rtObs))
			}
		case model.ChoiceTakeRuntime:
			// 对称：使 project 匹配 runtime 状态
			if conflict.Runtime != nil {
				op = newOperation(model.OpWriteProject, r.ResourceID,
					writePreconditions(r.ResourceID, sideRuntime, rtObs, projObs))
				markMaterialization(op, rtObs)
			} else {
				op = newOperation(model.OpRemoveProject, r.ResourceID,
					removePreconditions(r.ResourceID, sideProject, projObs))
			}
		}
		if op != nil && opAllowed(direction, op.Kind) {
			ops = append(ops, *op)
		}
	}
	sortAndNumberOperations(ops)
	markPreserveSkip(ops, BuildInput{PreserveMaxBytes: threshold})

	out := model.SyncPlan{
		SchemaVersion:              draft.SchemaVersion,
		RelationID:                 draft.RelationID,
		Kind:                       draft.Kind,
		ResolvedFromPlanID:         draft.PlanID,
		BaseBaselineID:             draft.BaseBaselineID,
		BaseBaselineDigest:         draft.BaseBaselineDigest,
		InputProjectSnapshotID:     draft.InputProjectSnapshotID,
		InputRuntimeSnapshotID:     draft.InputRuntimeSnapshotID,
		InputProjectSnapshotDigest: draft.InputProjectSnapshotDigest,
		InputRuntimeSnapshotDigest: draft.InputRuntimeSnapshotDigest,
		RelationRevision:           draft.RelationRevision,
		PolicyDigest:               draft.PolicyDigest,
		ExpectedBindings:           draft.ExpectedBindings,
		// exactness 从 draft 继承（不可变：resolution 不改变请求确切度）
		RequestedExactness: draft.RequestedExactness,
		Status:             model.PlanResolved,
		ExpiresAt:          draft.ExpiresAt,
		Operations:         ops,
		Conflicts:          draft.Conflicts, // 保留作证据
		Resolutions:        sorted,
		Diagnostics:        draft.Diagnostics,
		Summary: model.PlanSummary{
			ResourceTotal:    draft.Summary.ResourceTotal,
			AdoptEqualCount:  draft.Summary.AdoptEqualCount,
			MergedCleanCount: draft.Summary.MergedCleanCount,
			ConflictCount:    len(draft.Conflicts),
		},
	}
	out.Summary.CreateCount, out.Summary.ModifyCount, out.Summary.DeleteCount =
		summarizeOps(ops, project, runtime)
	out.ConfirmationRequirements = computeConfirmations(ops, project, runtime)

	digest, err := normalize.PlanDigest(out)
	if err != nil {
		return model.SyncPlan{}, fmt.Errorf("plan: 计算计划摘要失败: %w", err)
	}
	out.PlanDigest = digest
	return out, nil
}

// validChoice 校验 choice 与冲突类型匹配（架构文档 §6.3/§6.5）。
func validChoice(kind model.ConflictKind, choice model.ResolutionChoice) bool {
	switch kind {
	case model.ConflictInitialize:
		switch choice {
		case model.ChoiceInitializeFromProject, model.ChoiceInitializeFromRuntime,
			model.ChoiceSkip, model.ChoiceManual:
			return true
		}
	case model.ConflictModifyModify, model.ConflictDeleteModify:
		switch choice {
		case model.ChoiceTakeProject, model.ChoiceTakeRuntime,
			model.ChoiceSkip, model.ChoiceManual:
			return true
		}
	}
	return false
}

// resourceDirection 查资源观察命中的映射规则方向：project 侧观察优先，
// 其 PolicyID 未命中规则时回退 runtime 侧；找不到规则视为 bidirectional。
func resourceDirection(policy model.MappingPolicy, project, runtime model.ObservedSnapshot, id model.ResourceID) string {
	for _, s := range []model.ObservedSnapshot{project, runtime} {
		obs := observation(s, id)
		if obs == nil || obs.PolicyID == "" {
			continue
		}
		for i := range policy.Rules {
			if policy.Rules[i].ID == obs.PolicyID {
				return policy.Rules[i].Direction
			}
		}
	}
	return directionBidirectional
}

// opAllowed 判断操作类别是否被方向允许：project_to_runtime 禁止
// write_project/remove_project，runtime_to_project 对称；ignore 在进入
// 本函数前已被整体剔除。
func opAllowed(direction string, kind model.OperationKind) bool {
	switch direction {
	case directionProjectToRuntime:
		return kind != model.OpWriteProject && kind != model.OpRemoveProject
	case directionRuntimeToProject:
		return kind != model.OpWriteRuntime && kind != model.OpRemoveRuntime
	default:
		return true
	}
}

// observation 返回快照中资源的观察；absent 为 nil。
func observation(s model.ObservedSnapshot, id model.ResourceID) *model.ResourceObservation {
	if obs, ok := s.Resources[id]; ok {
		return &obs
	}
	return nil
}

// newOperation 构造 Reversible=true 的操作（P2 将以 CAS staging 兑现可回滚性）。
func newOperation(kind model.OperationKind, id model.ResourceID, preconditions []model.Precondition) *model.PlannedOperation {
	return &model.PlannedOperation{
		Kind:          kind,
		ResourceID:    id,
		Preconditions: preconditions,
		Reversible:    true,
	}
}

// markMaterialization 就地推导写操作的物化模式（契约 06 §3.7 / ADR-0008 §6，
// 票 #63）：mod 写操作且源侧表示携带 CF 直链重取信息（file-id + filename +
// 声明 hash 三要素齐备）→ download；其余写操作显式填 copy（契约「P3 起填充，
// 旧行空值＝copy 兼容」）。删除操作无取数面，留空。
// 纯函数无副作用；murmur2 等不支持格式的降级不在推导期判定——执行期引擎
// 「不验不装」gate 返回 hash_format_unsupported，走剔除语义的跳过清单。
func markMaterialization(op *model.PlannedOperation, source *model.ResourceObservation) {
	if op == nil || source == nil {
		return
	}
	if op.Kind != model.OpWriteRuntime && op.Kind != model.OpWriteProject {
		return
	}
	if source.Kind != model.ResourceMod {
		op.Materialization = model.MaterializationCopy
		return
	}
	m := source.Representation.Metadata
	if m[model.MetaCFFileID] != "" && m[model.MetaFilename] != "" &&
		m[model.MetaDeclaredHashAlgo] != "" && m[model.MetaDeclaredHashValue] != "" {
		op.Materialization = model.MaterializationDownload
		return
	}
	op.Materialization = model.MaterializationCopy
}

// operationTargetSide 返回操作的作用侧（旧内容所在侧）。
func operationTargetSide(kind model.OperationKind) string {
	switch kind {
	case model.OpWriteRuntime, model.OpRemoveRuntime:
		return sideRuntime
	case model.OpWriteProject, model.OpRemoveProject:
		return sideProject
	default:
		return ""
	}
}

// markPreserveSkip 大文件保全阈值判定（ADR-0007 §7，票 #64；契约 06 §3.7）：
// 覆盖（write）与删除（remove）操作的目标侧旧内容为非 mod 资源且超过
// preserve_max_bytes（0＝不限）时，操作行固化 preserve_skip=true——确认页
// 「旧版本不留存」警示的判定源，执行引擎据此跳过 before 保全（照常写，
// 旧版本不留 CAS）。旧内容字节数取目标侧前置条件期望内容（与 stale 判定
// 同一证据源）。restore 侧计划行判定由票 #60 复用 model.ShouldSkipPreserve。
// 判定只改操作行标记、不改 PlanDigest 的输入语义（digest 随计划整体计算，
// 同输入同 digest 的确定性不变）。
func markPreserveSkip(ops []model.PlannedOperation, in BuildInput) {
	if in.PreserveMaxBytes <= 0 {
		return // 显式 0＝不限（负值按同口径防御）
	}
	for i := range ops {
		op := &ops[i]
		kind := normalize.KindOfResourceID(op.ResourceID)
		target := operationTargetSide(op.Kind)
		if target == "" {
			continue
		}
		for _, pre := range op.Preconditions {
			if pre.Side != target || pre.Existence != existencePresent || pre.Expected == nil {
				continue
			}
			if model.ShouldSkipPreserve(kind, pre.Expected.Size, in.PreserveMaxBytes) {
				op.PreserveSkip = true
			}
			break
		}
	}
}

// writePreconditions 生成 write 操作前置条件：源侧必须 present 且指纹匹配
// （有 Content 则填期望值，源侧在快照中缺失时断言 absent，使 Apply 判定 stale），
// 加目标侧存在性断言（present 时带期望指纹，absent 时断言 absent）。
func writePreconditions(id model.ResourceID, sourceSide string, source, target *model.ResourceObservation) []model.Precondition {
	src := model.Precondition{ResourceID: id, Side: sourceSide}
	if source != nil {
		src.Existence = existencePresent
		if c := source.Representation.Content; c != nil {
			expected := *c
			src.Expected = &expected
		}
	} else {
		src.Existence = existenceAbsent
	}
	tgt := model.Precondition{ResourceID: id, Side: oppositeSide(sourceSide)}
	if target != nil {
		tgt.Existence = existencePresent
		if c := target.Representation.Content; c != nil {
			expected := *c
			tgt.Expected = &expected
		}
	} else {
		tgt.Existence = existenceAbsent
	}
	return []model.Precondition{src, tgt}
}

// removePreconditions 生成 remove 操作前置条件：被删除侧当前必须 present
// 且指纹匹配（有 Content 则填期望值）。
func removePreconditions(id model.ResourceID, side string, target *model.ResourceObservation) []model.Precondition {
	pc := model.Precondition{ResourceID: id, Side: side, Existence: existencePresent}
	if target != nil {
		if c := target.Representation.Content; c != nil {
			expected := *c
			pc.Expected = &expected
		}
	}
	return []model.Precondition{pc}
}

// oppositeSide 返回另一端侧名。
func oppositeSide(side string) string {
	if side == sideProject {
		return sideRuntime
	}
	return sideProject
}

// kindRank 是操作类别的确定性排序权重。
func kindRank(k model.OperationKind) int {
	switch k {
	case model.OpWriteRuntime:
		return 0
	case model.OpWriteProject:
		return 1
	case model.OpRemoveRuntime:
		return 2
	case model.OpRemoveProject:
		return 3
	case model.OpMaterialize:
		return 4
	default:
		return 99 // 未知类别排最后，保证确定性
	}
}

// sortAndNumberOperations 按（kindRank, ResourceID 字节序）排序并从 op_0001 起编号。
func sortAndNumberOperations(ops []model.PlannedOperation) {
	sort.Slice(ops, func(i, j int) bool {
		ri, rj := kindRank(ops[i].Kind), kindRank(ops[j].Kind)
		if ri != rj {
			return ri < rj
		}
		return ops[i].ResourceID < ops[j].ResourceID
	})
	for i := range ops {
		ops[i].ID = fmt.Sprintf("op_%04d", i+1)
	}
}

// summarizeOps 统计操作计数：目标侧 absent 的 write 为 create，
// 目标侧 present 的 write 为 modify，remove 计 delete。
func summarizeOps(ops []model.PlannedOperation, project, runtime model.ObservedSnapshot) (createCount, modifyCount, deleteCount int) {
	for _, op := range ops {
		switch op.Kind {
		case model.OpWriteRuntime:
			if observation(runtime, op.ResourceID) == nil {
				createCount++
			} else {
				modifyCount++
			}
		case model.OpWriteProject:
			if observation(project, op.ResourceID) == nil {
				createCount++
			} else {
				modifyCount++
			}
		case model.OpRemoveRuntime, model.OpRemoveProject:
			deleteCount++
		}
	}
	return createCount, modifyCount, deleteCount
}

// computeConfirmations 从 resolved plan 的最终操作推导确认要求（仅 count>0 项，
// 顺序固定）：overwrite=write 操作中目标侧当前 present 的数量；delete=remove 操作
// 数量；write_project=write_project 操作数量；unrecoverable=源侧为低置信度 mod 的
// 操作数量。
func computeConfirmations(ops []model.PlannedOperation, project, runtime model.ObservedSnapshot) []model.ConfirmationRequirement {
	var overwriteCount, deleteCount, writeProjectCount, unrecoverableCount int
	for _, op := range ops {
		projObs := observation(project, op.ResourceID)
		rtObs := observation(runtime, op.ResourceID)
		switch op.Kind {
		case model.OpWriteRuntime:
			if rtObs != nil {
				overwriteCount++
			}
			if lowConfidenceMod(op.ResourceID, projObs) {
				unrecoverableCount++
			}
		case model.OpWriteProject:
			writeProjectCount++
			if projObs != nil {
				overwriteCount++
			}
			if lowConfidenceMod(op.ResourceID, rtObs) {
				unrecoverableCount++
			}
		case model.OpRemoveRuntime:
			deleteCount++
			if lowConfidenceMod(op.ResourceID, rtObs) {
				unrecoverableCount++
			}
		case model.OpRemoveProject:
			deleteCount++
			if lowConfidenceMod(op.ResourceID, projObs) {
				unrecoverableCount++
			}
		}
	}
	var reqs []model.ConfirmationRequirement
	add := func(code, severity string, count int) {
		if count > 0 {
			reqs = append(reqs, model.ConfirmationRequirement{Code: code, Severity: severity, ResourceCount: count})
		}
	}
	add(confirmOverwrite, "info", overwriteCount)
	add(confirmDelete, "warning", deleteCount)
	add(confirmWriteProject, "warning", writeProjectCount)
	add(confirmUnrecoverable, "warning", unrecoverableCount)
	return reqs
}

// lowConfidenceMod 判断操作源侧是否为低置信度 mod 身份。
// write 的源侧是内容来源侧；remove 的源侧是被删除侧（身份不可靠时删除不可恢复）；
// 快照中无观察时按 ResourceID 推导。
func lowConfidenceMod(id model.ResourceID, source *model.ResourceObservation) bool {
	kind := normalize.KindOfResourceID(id)
	confidence := normalize.IdentityFromResourceID(id).Confidence
	if source != nil {
		kind = source.Kind
		confidence = source.Identity.Confidence
	}
	return kind == model.ResourceMod && confidence != model.ConfidenceHigh
}

// directionDetail 将方向编码进 Conflict.Detail，供 Resolve 过滤 resolution 操作。
// bidirectional 不写入，保持 Detail 干净。
func directionDetail(direction string) string {
	if direction == directionBidirectional {
		return ""
	}
	return "direction=" + direction
}

// withDirectionDetail 在保留既有 Detail 证据的前提下写入方向（票 #87）：
// detail 为空走既有纯文本形态（directionDetail）；detail 已是 hunk JSON
// （契约 07 §3.3）时以 direction 兄弟键并入，不覆盖块证据；非 JSON 形态
// 原样保留（防御，方向仍可由 Policy 重推导）。
func withDirectionDetail(detail, direction string) string {
	if detail == "" {
		return directionDetail(direction)
	}
	if direction == directionBidirectional {
		return detail
	}
	if !json.Valid([]byte(detail)) {
		return detail
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(detail), &obj); err != nil {
		return detail
	}
	obj["direction"] = direction
	b, err := json.Marshal(obj)
	if err != nil {
		return detail
	}
	return string(b)
}

// detailDirection 反解 Conflict.Detail 中的方向；兼容纯文本（direction= 前缀）
// 与 hunk JSON（direction 兄弟键）两种形态，缺失视为 bidirectional
// （兼容非本包构建的 draft）。
func detailDirection(detail string) string {
	if strings.HasPrefix(detail, "direction=") {
		return strings.TrimPrefix(detail, "direction=")
	}
	if json.Valid([]byte(detail)) {
		var obj struct {
			Direction string `json:"direction"`
		}
		if err := json.Unmarshal([]byte(detail), &obj); err == nil && obj.Direction != "" {
			return obj.Direction
		}
	}
	return directionBidirectional
}

// appendDiagnostics 合并两侧快照诊断（project 在前）；诊断是证据性数据，
// 不参与 PlanDigest。始终返回非 nil，保证 JSON 归一。
func appendDiagnostics(project, runtime []model.Diagnostic) []model.Diagnostic {
	out := make([]model.Diagnostic, 0, len(project)+len(runtime))
	out = append(out, project...)
	out = append(out, runtime...)
	return out
}

// baseBaselineID 返回基线 ID；无基线为空。
func baseBaselineID(base *model.SyncBaseline) string {
	if base == nil {
		return ""
	}
	return base.BaselineID
}

// formatExpiry 将过期时间格式化为 RFC3339（UTC）；零值留空。
func formatExpiry(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
