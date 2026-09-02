package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/download"
	"packgradle/internal/errs"
	"packgradle/internal/syncstage"
)

// 回滚计划面（ADR-0006 §1–§5 + 契约 06 §3.1/§3.2/§3.3/§3.5；票 #59）：
// PrepareRestore（四标记判定 + CF 尽力探测 + draft 落 sync_plans kind=restore）、
// ResolveRestorePlan（无冲突决议面，仅 partial 逐资源 skip）、GetRestorePlan
// （对称 GetPlan 的读伴随）、StageUserObject（用户对象补全，staging 绑 plan
// 不进 CAS）。ConfirmRestorePlan 与 restore 运行执行器归票 #60，不在本票。

// 新增错误码（契约 06 §10；文案由前端 locale 提供）。
const (
	// CodeRestoreCommitNotFound 是 PrepareRestore 的提交不存在或跨关系
	//（args {0}=commit_id；GetForRelation 两场景同一口径）。
	CodeRestoreCommitNotFound = "err.restore.commit_not_found"
	// CodeRestoreExactInfeasible 是 exact 决议遇实时就绪面不满的前置拒绝
	//（args {0}=plan_id；引导改 allow_partial 重 resolve，ADR-0006 §4）。
	CodeRestoreExactInfeasible = "err.restore.exact_infeasible"
	// CodeRestoreSkipInvalid 是 skip 决议作用于非阻塞行（args {0}=resource_id）。
	CodeRestoreSkipInvalid = "err.restore.skip_invalid"
	// CodeUserObjectNotRequired 是 StageUserObject 作用于非 user_object_required
	// 行（args {0}=resource_id）。
	CodeUserObjectNotRequired = "err.userobject.not_required"
	// CodeUserObjectNoProjectContent 是 StageUserObject 作用于 no_project_content
	// 降标行（args {0}=resource_id；ADR-0012 §4：补 jar 救不了项目侧，补全通道
	// 对该值关闭——skip 或「项目端改回目标语义后重新 prepare」是仅有的出口，
	// 放行即复现「确认后必败」）。
	CodeUserObjectNoProjectContent = "err.userobject.no_project_content"
	// CodeUserObjectHashMismatch 是用户对象与目标摘要不符（args {0}=期望摘要，
	// 可重试）。
	CodeUserObjectHashMismatch = "err.userobject.hash_mismatch"
)

// RestoreProber 是 CF 探测端口（internal/download.Engine 满足；nil = 不探测，
// 行内不标 availability，保持乐观标记）。
type RestoreProber interface {
	ProbeHead(ctx context.Context, reqs []download.ProbeRequest, onResult func(download.ProbeRequest, download.ProbeResult))
}

// CASStore 是应用层所需的 CAS 能力面（objectstore.CAS 满足）：ContentStore
// 承接 before 保全与写回取数，Has 承接四标记 restorable_from_cas 的实存判定
// （ADR-0006 §2：CAS 对象缺失 → user_object_required）。
type CASStore interface {
	syncstage.ContentStore
	Has(ctx context.Context, digest string) (bool, error)
}

// ---- PrepareRestore ----

// PrepareRestore 基于目标提交的 result baseline 生成回滚 draft 计划（契约 06
// §3.1）。行为语义：
//
//  1. 门禁：恢复所需期间 restore 与 apply 同门禁（ADR-0006 §8，err.recovery.
//     in_progress）；活跃任务互斥（err.scan.already_running）；双端最新快照
//     缺失即 scan 未就绪（err.scan.incomplete）——availability prepare_restore
//     的三条件在此以同码强制。
//  2. 读目标提交（err.restore.commit_not_found：不存在或跨关系）→ 取其
//     result_baseline 为写回目标（目标 baseline 后端推导，双端强一致化，
//     ADR-0006 §1；head 合法＝空差异计划走既有空计划先例）。
//  3. 逐资源四标记判定（restore_matrix.go 确定函数）+ CF 尽力探测（事务外，
//     尽力而为不阻塞 prepare，ADR-0006 §7）。
//  4. digest/expiry/stale/单活跃计划规则全部沿既有计划机器：draft 落
//     sync_plans(kind=restore)，PlanDigest 沿 normalize.PlanDigest（写回契约
//     由 Operations 前置条件承载；标记/可用性为证据性数据不入 digest），
//     计划级串行在 Confirm 收口（同 P2 Apply，票 #60）。
func (a *App) PrepareRestore(ctx context.Context, input view.PrepareRestoreInput) (view.RestorePlanView, error) {
	if input.RelationID == "" || input.CommitID == "" {
		return view.RestorePlanView{}, errs.New(CodeRestoreCommitNotFound, input.CommitID)
	}
	rel, err := a.deps.Relations.Get(ctx, input.RelationID)
	if err != nil {
		return view.RestorePlanView{}, errs.New(CodeRelationNotFound, input.RelationID)
	}
	if rel.Health == model.HealthRecoveryRequired {
		return view.RestorePlanView{}, errs.New(CodeRecoveryInProgress)
	}
	active, _, err := a.deps.Tasks.ListByRelation(ctx, rel.RelationID, true, ports.PageRequest{Limit: 1})
	if err != nil {
		return view.RestorePlanView{}, err
	}
	if len(active) > 0 {
		return view.RestorePlanView{}, errs.New(CodeRelationScanRunning, rel.RelationID)
	}
	commit, err := a.deps.Commits.GetForRelation(ctx, input.CommitID, rel.RelationID)
	if err != nil {
		// 不存在与跨关系同一口径（契约 06 §3.1：err.restore.commit_not_found）
		return view.RestorePlanView{}, errs.New(CodeRestoreCommitNotFound, input.CommitID)
	}
	if commit.ResultBaselineID == "" {
		// 提交头缺结果基线属数据不完整，无法作为回滚目标，按目标不可得拒绝
		return view.RestorePlanView{}, errs.New(CodeRestoreCommitNotFound, input.CommitID)
	}
	target, err := a.deps.Baselines.Get(ctx, commit.ResultBaselineID)
	if err != nil {
		return view.RestorePlanView{}, err
	}
	snapP, okP, err := a.deps.Snapshots.LatestByRelationSide(ctx, rel.RelationID, model.SideProject)
	if err != nil {
		return view.RestorePlanView{}, err
	}
	snapR, okR, err := a.deps.Snapshots.LatestByRelationSide(ctx, rel.RelationID, model.SideRuntime)
	if err != nil {
		return view.RestorePlanView{}, err
	}
	if !okP || !okR {
		return view.RestorePlanView{}, errs.New(CodeScanIncomplete, rel.RelationID)
	}
	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		return view.RestorePlanView{}, err
	}
	rt, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		return view.RestorePlanView{}, err
	}

	items, ops, summary, currentFileIDs, err := a.buildRestoreDraft(ctx, restoreBuild{
		target: target,
		snapP:  snapP,
		snapR:  snapR,
	})
	if err != nil {
		return view.RestorePlanView{}, err
	}

	// CF 尽力探测（契约 06 §5：仅 PrepareRestore、仅 redownload 候选行；
	// 预算内尽力，超时/预算耗尽保持乐观标记 availability=unknown 不阻塞）。
	a.probeRestoreItems(ctx, items, currentFileIDs)

	// 验收摘要仅 user_object_required 行透出（契约 06 §3.2）。
	for i := range items {
		if items[i].Marker != model.MarkerUserObjectRequired {
			items[i].ExpectedDigest = ""
		}
	}

	now := a.deps.Now().UTC()
	ops = sortAndNumberRestoreOps(ops)
	planID := a.deps.IDs("plan_")
	plan := model.SyncPlan{
		SchemaVersion:              model.CurrentSchemaVersion,
		PlanID:                     planID,
		RelationID:                 rel.RelationID,
		Kind:                       model.PlanRestore,
		BaseBaselineID:             commit.ResultBaselineID,
		BaseBaselineDigest:         target.BaselineDigest,
		InputProjectSnapshotID:     snapP.SnapshotID,
		InputRuntimeSnapshotID:     snapR.SnapshotID,
		InputProjectSnapshotDigest: snapP.SnapshotDigest,
		InputRuntimeSnapshotDigest: snapR.SnapshotDigest,
		RelationRevision:           rel.Revision,
		ExpectedBindings:           model.ExpectedBindings{Project: proj.BindingFingerprint, Runtime: rt.BindingFingerprint},
		// 请求确切度在决议时点才存在（PrepareRestore 输入只有 relation+commit，
		// 契约 06 §3.1/§3.3）；draft 落库沿仓储的 allow_partial 归一口径。
		RequestedExactness:       model.ExactnessAllowPartial,
		Status:                   model.PlanDraft,
		ExpiresAt:                now.Add(planTTL).Format(time.RFC3339),
		CreatedAt:                now.Format(time.RFC3339),
		TargetCommitID:           commit.CommitID,
		Operations:               ops,
		ConfirmationRequirements: restoreConfirmations(items, ops),
		Summary:                  summary,
		RestoreItems:             items,
		// 暂存锚 = 自身（draft 是决议链的根；补全字节落 <staging>/<锚>/）。
		StagingPlanID: planID,
	}
	digest, err := normalize.PlanDigest(plan)
	if err != nil {
		return view.RestorePlanView{}, fmt.Errorf("restore: 计算计划摘要失败: %w", err)
	}
	plan.PlanDigest = digest

	// 单 RunInTx（ADR-0003 doctrine；契约 06 §3.1）：draft 落库沿既有计划机器
	//（sync_plans kind=restore，v1 起预留枚举兑现）。
	if err := a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		return repos.Plans.Insert(ctx, plan)
	}); err != nil {
		return view.RestorePlanView{}, err
	}
	return a.restoreViewWithStatus(ctx, plan, rel)
}

// ---- 计划构建（四标记判定 + 写回操作推导）----

// restoreBuild 是回滚 draft 构建输入。
type restoreBuild struct {
	target model.SyncBaseline // 目标提交的 result baseline（写回目标）
	snapP  model.ObservedSnapshot
	snapR  model.ObservedSnapshot
}

// restoreSides 是双端的固定枚举序（project 先，保持输出确定性）；下标与
// sideIndex/buildRestoreDraft 的数组位一一对应。
var restoreSides = [2]model.Side{model.SideProject, model.SideRuntime}

func sideIndex(s model.Side) int {
	if s == model.SideProject {
		return 0
	}
	return 1
}

// buildRestoreDraft 逐资源推导四标记判定行与写回操作（ADR-0006 §2 判定矩阵 +
// §5 删除面）。返回的 items 确定性排序（ResourceID 字节序）；currentFileIDs 是
// 当前项目侧 metafile 的 file-id（newer_available 本地比对用，零网络）。
func (a *App) buildRestoreDraft(ctx context.Context, b restoreBuild) (
	items []model.RestorePlanItem, ops []model.PlannedOperation,
	summary model.PlanSummary, currentFileIDs map[model.ResourceID]int64, err error,
) {
	ids := make(map[model.ResourceID]struct{})
	for id := range b.target.Resources {
		ids[id] = struct{}{}
	}
	for id := range b.snapP.Resources {
		ids[id] = struct{}{}
	}
	for id := range b.snapR.Resources {
		ids[id] = struct{}{}
	}
	sorted := make([]model.ResourceID, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	items = make([]model.RestorePlanItem, 0, len(sorted))
	ops = make([]model.PlannedOperation, 0, len(sorted))
	currentFileIDs = map[model.ResourceID]int64{}

	for _, id := range sorted {
		tgt := b.target.Resources[id] // 目标 absent = 不在基线（tombstone 不入基线）
		curP := snapshotObs(b.snapP, id)
		curR := snapshotObs(b.snapR, id)
		if curP != nil {
			if fid, perr := strconv.ParseInt(curP.Representation.Metadata[model.MetaCFFileID], 10, 64); perr == nil && fid > 0 {
				currentFileIDs[id] = fid
			}
		}

		// 逐侧对照目标（语义摘要同 diff 口径：normalize.SemanticDigest），
		// 折叠出写回侧（目标 present ∧ 当前缺失/漂移）与删除侧（目标 absent ∧
		// 当前 present）。写回侧的目标表示即该侧写回目标（双端强一致化）。
		var writeSides, deleteSides []model.Side
		var writeTargetDigests [2]string
		var writeCurrentPresent [2]bool
		for _, side := range restoreSides {
			si := sideIndex(side)
			tRep := baselineRep(&tgt, side)
			curObs := sideObservation(curP, curR, side)
			tSem, serr := restoreTargetSemantic(id, tRep)
			if serr != nil {
				return nil, nil, summary, nil, serr
			}
			cSem, serr := restoreCurrentSemantic(id, curObs)
			if serr != nil {
				return nil, nil, summary, nil, serr
			}
			switch {
			case tRep != nil && (curObs == nil || cSem != tSem):
				writeSides = append(writeSides, side)
				writeTargetDigests[si] = restoreTargetDigest(side, tRep)
				writeCurrentPresent[si] = curObs != nil
			case tRep == nil && curObs != nil:
				deleteSides = append(deleteSides, side)
			}
		}
		if len(writeSides) == 0 && len(deleteSides) == 0 {
			continue // 双端 digest 均等于目标：无操作行（ADR-0006 §2）
		}

		item := model.RestorePlanItem{ResourceID: id}
		switch {
		case len(writeSides) == 0:
			// 删除行：目标之后新增的资源随回滚删除，不占四标记（ADR-0006 §5）。
			item.ChangeKind = restoreChangeDelete
			item.RelativePath = restoreDisplayPath(nil, nil, curP, curR)
			item.DeletionWarn = restoreDeletionWarn(id, curP)
		default:
			// 重建行：create（写回侧当前全缺）/ modify（任一写回侧存在但漂移）。
			item.ChangeKind = restoreChangeModify
			for _, side := range writeSides {
				if sideObservation(curP, curR, side) == nil {
					item.ChangeKind = restoreChangeCreate
					break
				}
			}
			item.RelativePath = restoreDisplayPath(
				repOfSide(&tgt, writeSides, model.SideProject),
				repOfSide(&tgt, writeSides, model.SideRuntime), curP, curR)
			item.StageRel = restoreStageRel(&tgt, writeSides)
			item.Marker, item.MarkerReason, item.Redownload, item.ExpectedDigest =
				a.judgeRestoreRow(ctx, &tgt, writeSides, writeTargetDigests)
			// ADR-0012 §4 存量宽判降标（四标记判定后置覆写，矩阵零新维度）：
			// 写回侧含 project ∧ 目标基线项目侧表示无实测 Content → 不区分原
			// marker 统一降 user_object_required + no_project_content（纯静态
			// 零探测——覆写清空 Redownload，probeRestoreItems 自然不再探测）。
			item = degradeNoProjectRow(item, writeSidesContains(writeSides, model.SideProject),
				baselineRep(&tgt, model.SideProject))
		}

		// 写回操作（执行票 #60 消费）：前置条件断言 prepare 时点的目标侧现状
		//（存在性 + 内容指纹，Apply 阶段漂移即 stale）；ObjectRefs 固化写回目标
		// 内容引用（CAS/下载/补全三类来源由行 marker 决定）。删除行同理断言
		// 被删侧现状。
		for _, side := range restoreSides {
			si := sideIndex(side)
			switch {
			case writeSidesContains(writeSides, side):
				ops = append(ops, restoreWriteOp(id, side, writeCurrentPresent[si],
					curP, curR, &tgt, writeTargetDigests[si]))
			case deleteSidesContains(deleteSides, side):
				ops = append(ops, restoreRemoveOp(id, side, sideObservation(curP, curR, side)))
			}
		}
		items = append(items, item)
	}

	summary = model.PlanSummary{ResourceTotal: len(items)}
	for _, it := range items {
		switch it.ChangeKind {
		case restoreChangeCreate:
			summary.CreateCount++
		case restoreChangeModify:
			summary.ModifyCount++
		case restoreChangeDelete:
			summary.DeleteCount++
		}
	}
	return items, ops, summary, currentFileIDs, nil
}

// 行变化类别（与契约 06 §3.2 change_kind 同字面）。
const (
	restoreChangeCreate = "create"
	restoreChangeModify = "modify"
	restoreChangeDelete = "delete"
)

// judgeRestoreRow 对重建行执行四标记判定（restore_matrix.go 确定函数的取数侧）：
// rec 取目标基线记录；重取信息取目标基线项目侧 metafile 元数据（重取性看数据
// 不看出身）；CASReady 要求全部写回侧目标内容 sha256 实存。返回验收摘要
// （runtime 侧优先——mod 的补全/重取对象是 jar）与重取信息（redownload 行）。
func (a *App) judgeRestoreRow(ctx context.Context, tgt *model.BaselineResource, writeSides []model.Side, digests [2]string) (
	marker model.RestoreMarker, reason string, rd *model.RedownloadInfo, expectDigest string,
) {
	projRep := baselineRep(tgt, model.SideProject)
	info, hasInfo := redownloadInfoOf(projRep)

	casReady := len(writeSides) > 0
	for _, side := range writeSides {
		d := digests[sideIndex(side)]
		if d == "" {
			casReady = false
			break
		}
		ok, herr := a.deps.CAS.Has(ctx, d)
		if herr != nil || !ok {
			casReady = false
			break
		}
	}

	marker, reason = judgeRestoreMarker(restoreMarkerInput{
		Rec:               tgt.Recoverability,
		HasRedownloadInfo: hasInfo,
		HashSupported:     hasInfo && download.SupportsHashFormat(info.HashFormat),
		CASReady:          casReady,
	})
	if marker == model.MarkerRedownloadRequired {
		rd = info
	}
	// 验收摘要：runtime 写回侧优先，project 兜底；非 sha256 摘要不可作 staging
	// 验收口径（留空 = 不可补全行，StageUserObject 按 not_required 拒绝）。
	if writeSidesContains(writeSides, model.SideRuntime) {
		expectDigest = digests[sideIndex(model.SideRuntime)]
	} else if len(writeSides) > 0 {
		expectDigest = digests[sideIndex(model.SideProject)]
	}
	return marker, reason, rd, expectDigest
}

// probeRestoreItems 对 redownload_required 行并发 HEAD 探测并回填行内可用性
// （契约 06 §5）。探测证据（状态码/Content-Length/耗时）为内部诊断不透出 DTO：
// ok → availability=ok（+newer_available 本地比对）；404/403 → prepare 时点降标
// user_object_required + cf_unavailable（ADR-0006 §7「不可用提前降标」的投影）；
// 其余（超时/预算耗尽/网络错误）→ availability=unknown 保持乐观标记不阻塞。
func (a *App) probeRestoreItems(ctx context.Context, items []model.RestorePlanItem, currentFileIDs map[model.ResourceID]int64) {
	if a.deps.Probes == nil {
		return
	}
	var idxs []int
	var reqs []download.ProbeRequest
	for i := range items {
		it := &items[i]
		if it.Marker == model.MarkerRedownloadRequired && it.Redownload != nil {
			idxs = append(idxs, i)
			reqs = append(reqs, download.ProbeRequest{FileID: it.Redownload.FileID, Filename: it.Redownload.Filename})
		}
	}
	if len(reqs) == 0 {
		return
	}
	var mu sync.Mutex
	results := make(map[int]download.ProbeResult, len(reqs))
	a.deps.Probes.ProbeHead(ctx, reqs, func(req download.ProbeRequest, res download.ProbeResult) {
		mu.Lock()
		defer mu.Unlock()
		for k := range reqs {
			if reqs[k] == req {
				results[idxs[k]] = res
				return
			}
		}
	})
	for i, res := range results {
		it := &items[i]
		switch {
		case res.OK:
			it.Availability = model.RestoreAvailabilityOK
			// newer_available：本地比对 head vs 目标的 metafile file-id（零网络），
			// 仅提示「目标非该 mod 最新版」，版本决策归 packwiz；仅 ok 行透出。
			it.NewerAvailable = currentFileIDs[it.ResourceID] != 0 &&
				currentFileIDs[it.ResourceID] != it.Redownload.FileID
		case res.Demote:
			it.Marker = model.MarkerUserObjectRequired
			it.MarkerReason = model.MarkerReasonCFUnavailable
			it.Availability = ""
			it.NewerAvailable = false
			it.Redownload = nil
		default:
			// 超时/预算耗尽/网络错误：unknown 不阻塞 prepare（探测是辅助非承诺）
			it.Availability = model.RestoreAvailabilityUnknown
			it.NewerAvailable = false
		}
	}
}

// ---- ResolveRestorePlan ----

// ResolveRestorePlan 固化回滚决议为新的不可变 resolved plan（旧 plan 不修改，
// 沿 P2 计划机器；契约 06 §3.3）。无冲突决议面（ADR-0006 §3）：决议输入仅
// requested_exactness 与 partial 的逐资源 skip。行为语义：
//
//  1. 校验 status=draft 且非 stale/expired（既有码，与 ResolvePlan 同判）；
//  2. exact 决议遇实时就绪面不满 → err.restore.exact_infeasible 前置拒绝
//     （ADR-0006 §4：不在 Confirm 时才拦截）；就绪面 = skip 后剩余行全部就绪
//     （就绪 = cas ∪ redownload ∪ user_object∧staged，契约 06 §3.5）；
//  3. skip 仅对未 staged 的 user_object_required 与 unrecoverable 行合法，
//     其余 → err.restore.skip_invalid；
//  4. 固化 requested_exactness 与 skip 清单（Resolutions，参与 PlanDigest），
//     status→resolved，TTL 重置沿既有机器。
func (a *App) ResolveRestorePlan(ctx context.Context, input view.ResolveRestorePlanInput) (view.RestorePlanView, error) {
	ex := model.Exactness(input.RequestedExactness)
	if ex == "" {
		ex = model.ExactnessAllowPartial
	}
	if ex != model.ExactnessExact && ex != model.ExactnessAllowPartial {
		return view.RestorePlanView{}, errs.New(CodeSyncInvalidExactness, input.RequestedExactness)
	}
	draft, err := a.deps.Plans.Get(ctx, input.PlanID)
	if err != nil || draft.Kind != model.PlanRestore {
		// 不存在与跨类计划同一口径：回滚决议只消费 restore 计划
		return view.RestorePlanView{}, errs.New(CodePlanNotFound, input.PlanID)
	}
	if draft.Status != model.PlanDraft {
		return view.RestorePlanView{}, errs.New(CodePlanStale, input.PlanID)
	}
	if expired(draft.ExpiresAt, a.deps.Now().UTC()) {
		return view.RestorePlanView{}, errs.New(CodePlanExpired, input.PlanID)
	}
	rel, err := a.deps.Relations.Get(ctx, draft.RelationID)
	if err != nil {
		return view.RestorePlanView{}, errs.New(CodeRelationNotFound, draft.RelationID)
	}
	if err := a.restorePlanFresh(ctx, draft, rel); err != nil {
		return view.RestorePlanView{}, errs.New(CodePlanStale, input.PlanID)
	}

	staged := a.restoreStagedDigests(draft)
	skipSet := make(map[model.ResourceID]struct{}, len(input.SkipResourceIDs))
	for _, rawID := range input.SkipResourceIDs {
		rid := model.ResourceID(rawID)
		it := restoreFindItem(draft, rid)
		if it == nil {
			return view.RestorePlanView{}, errs.New(CodeRestoreSkipInvalid, rawID)
		}
		stagedOK := it.ExpectedDigest != "" && staged[it.ResourceID] == it.ExpectedDigest
		switch {
		case it.Marker == model.MarkerUserObjectRequired && !stagedOK:
			// 未补全的 user_object_required 行：合法 skip
		case it.Marker == model.MarkerUnrecoverable:
			// 不可恢复行：合法 skip（用户显式降级，ADR-0006 §2）
		default:
			return view.RestorePlanView{}, errs.New(CodeRestoreSkipInvalid, rawID)
		}
		skipSet[rid] = struct{}{}
	}
	if ex == model.ExactnessExact && !restoreExactReady(draft.RestoreItems, staged, skipSet) {
		return view.RestorePlanView{}, errs.New(CodeRestoreExactInfeasible, input.PlanID)
	}

	resolved := draft
	resolved.PlanID = a.deps.IDs("plan_")
	resolved.ResolvedFromPlanID = draft.PlanID
	resolved.Status = model.PlanResolved
	resolved.RequestedExactness = ex
	resolved.ExpiresAt = a.deps.Now().UTC().Add(planTTL).Format(time.RFC3339)
	// 暂存锚继承根 draft（draft 上已补全的字节对 resolved 计划可见，就绪面
	// 跨决议延续，契约 06 §3.5「draft/resolved 均可补全」）。
	resolved.StagingPlanID = draft.StagingPlanID
	resolutions := make([]model.Resolution, 0, len(skipSet))
	for rid := range skipSet {
		resolutions = append(resolutions, model.Resolution{ResourceID: rid, Choice: model.ChoiceSkip})
	}
	sort.Slice(resolutions, func(i, j int) bool { return resolutions[i].ResourceID < resolutions[j].ResourceID })
	resolved.Resolutions = resolutions
	digest, err := normalize.PlanDigest(resolved)
	if err != nil {
		return view.RestorePlanView{}, fmt.Errorf("restore: 计算计划摘要失败: %w", err)
	}
	resolved.PlanDigest = digest

	if err := a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		return repos.Plans.Insert(ctx, resolved)
	}); err != nil {
		return view.RestorePlanView{}, err
	}
	return a.restoreViewWithStatus(ctx, resolved, rel)
}

// ---- GetRestorePlan ----

// GetRestorePlan 查询回滚计划（对称 GetPlan 的读伴随）；stale/expired 为读取
// 时投影，不写库。
func (a *App) GetRestorePlan(ctx context.Context, planID string) (view.RestorePlanView, error) {
	p, err := a.deps.Plans.Get(ctx, planID)
	if err != nil || p.Kind != model.PlanRestore {
		// 不存在、跨类计划同一口径：不泄露其他类计划的形状
		return view.RestorePlanView{}, errs.New(CodePlanNotFound, planID)
	}
	rel, relErr := a.deps.Relations.Get(ctx, p.RelationID)
	if relErr != nil {
		// 关系被删除的场景：仍可返回计划本体（GetPlan 先例），状态不投影
		return a.restorePlanView(p, nil), nil
	}
	return a.restoreViewWithStatus(ctx, p, rel)
}

// ---- StageUserObject ----

// StageUserObject 用户对象补全（契约 06 §3.5）：draft/resolved 均可补全（confirm
// 前补齐）；仅对 user_object_required 行合法（否则 err.userobject.not_required；
// no_project_content 降标行补全通道关闭，另码 err.userobject.no_project_content
// 拒收，ADR-0012 §4）；按目标 expected_digest 验收（不符 →
// err.userobject.hash_mismatch {0}=期望摘要，可重试）。通过后字节进 staging 绑
// plan（<stagingRoot>/<plan_id>，沿 syncstage 暂存机器与 StageContent 校验）、
// 不进 CAS、不参与 plan_digest；marker 是 prepare 时点确定函数，补全不改标记，
// 只改就绪面（staged 即入 exact 就绪面，ExactFeasible 实时翻转）。
func (a *App) StageUserObject(ctx context.Context, input view.StageUserObjectInput) (view.RestorePlanView, error) {
	p, err := a.deps.Plans.Get(ctx, input.PlanID)
	if err != nil || p.Kind != model.PlanRestore {
		return view.RestorePlanView{}, errs.New(CodePlanNotFound, input.PlanID)
	}
	if p.Status != model.PlanDraft && p.Status != model.PlanResolved {
		return view.RestorePlanView{}, errs.New(CodePlanStale, input.PlanID)
	}
	if expired(p.ExpiresAt, a.deps.Now().UTC()) {
		return view.RestorePlanView{}, errs.New(CodePlanExpired, input.PlanID)
	}
	rel, err := a.deps.Relations.Get(ctx, p.RelationID)
	if err != nil {
		return view.RestorePlanView{}, errs.New(CodeRelationNotFound, p.RelationID)
	}
	if err := a.restorePlanFresh(ctx, p, rel); err != nil {
		return view.RestorePlanView{}, errs.New(CodePlanStale, input.PlanID)
	}
	it := restoreFindItem(p, model.ResourceID(input.ResourceID))
	if it != nil && it.MarkerReason == model.MarkerReasonNoProjectContent {
		// no_project_content 降标行补全通道关闭（ADR-0012 §4，先于 not_required
		// 拆码）：该行无项目侧目标内容，补 jar 救不了项目侧——放行即复现
		// 「确认后必败」，拒绝而非落盘是错写链第二道锁。
		return view.RestorePlanView{}, errs.New(CodeUserObjectNoProjectContent, input.ResourceID)
	}
	if it == nil || it.Marker != model.MarkerUserObjectRequired || it.ExpectedDigest == "" || it.StageRel == "" {
		// 非补全行（含缺验收摘要的不可补全行）一律 not_required（契约 06 §3.5）
		return view.RestorePlanView{}, errs.New(CodeUserObjectNotRequired, input.ResourceID)
	}

	f, err := os.Open(input.SourcePath)
	if err != nil {
		return view.RestorePlanView{}, errs.NewDetail("err.file.read", err.Error(), input.SourcePath)
	}
	defer f.Close()
	// 暂存目录以计划链的根 draft plan_id 为名（validateID 同字形约束）绑定计划；
	// 字节经既有 StageContent 机器原子落盘并 sha256 复核，不符即删不留半成品
	//（可重试）。
	run, err := syncstage.OpenRun(a.deps.StagingRoot, restoreStagingAnchor(p))
	if err != nil {
		return view.RestorePlanView{}, err
	}
	if _, err := run.StageContent(it.StageRel, f, it.ExpectedDigest); err != nil {
		if errors.Is(err, syncstage.ErrDigestMismatch) {
			return view.RestorePlanView{}, errs.New(CodeUserObjectHashMismatch, it.ExpectedDigest)
		}
		return view.RestorePlanView{}, err
	}
	return a.restoreViewWithStatus(ctx, p, rel)
}

// ---- 投影与辅助 ----

// restoreViewWithStatus 计算读取时有效状态（对称 planViewWithStatus）：
// resolved 且最新运行 committed → applied（契约 05 §5 读取时推导）；draft/resolved
// 的过期与修订/绑定失配 → expired/stale（重绑不递增修订号的失效由绑定指纹承担）。
func (a *App) restoreViewWithStatus(ctx context.Context, p model.SyncPlan, rel model.Relation) (view.RestorePlanView, error) {
	effective := p.Status
	if p.Status == model.PlanResolved {
		if run, found, err := a.deps.ApplyRuns.LatestByPlan(ctx, p.PlanID); err == nil && found &&
			run.State == model.ApplyRunCommitted {
			effective = model.PlanApplied
			return a.restorePlanView(p, &effective), nil
		}
	}
	if (p.Status == model.PlanDraft || p.Status == model.PlanResolved) && expired(p.ExpiresAt, a.deps.Now().UTC()) {
		effective = model.PlanExpired
	} else if p.Status == model.PlanDraft || p.Status == model.PlanResolved {
		if err := a.restorePlanFresh(ctx, p, rel); err != nil {
			if !errors.Is(err, errRestorePlanStale) {
				return view.RestorePlanView{}, err
			}
			effective = model.PlanStale
		}
	}
	return a.restorePlanView(p, &effective), nil
}

// errRestorePlanStale 是 restorePlanFresh 的内部失配信号。
var errRestorePlanStale = errors.New("restore: 计划与关系现状失配")

// restorePlanFresh 校验计划锁定的修订与绑定指纹仍与关系一致；失配返回
// errRestorePlanStale（投影为 stale；Resolve/Stage 路径以 err.plan.stale 拆码）。
func (a *App) restorePlanFresh(ctx context.Context, p model.SyncPlan, rel model.Relation) error {
	if rel.Revision != p.RelationRevision {
		return errRestorePlanStale
	}
	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		return nil // 端点读取失败不作 stale 判定（bindingsMismatch 同判）
	}
	rt, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		return nil
	}
	if p.ExpectedBindings.Project != proj.BindingFingerprint ||
		p.ExpectedBindings.Runtime != rt.BindingFingerprint {
		return errRestorePlanStale
	}
	return nil
}

// restoreStagingAnchor 返回计划的补全暂存锚（决议链根 draft 的 plan_id；
// draft 无祖先即自身）。执行票 #60 消费暂存同用此锚。
func restoreStagingAnchor(p model.SyncPlan) string {
	if p.StagingPlanID != "" {
		return p.StagingPlanID
	}
	return p.PlanID
}

// restoreStagedDigests 汇总计划暂存目录中已补全字节的逐资源摘要（就绪面实时
// 推导的数据源）。目录不存在（从未补全）返回 nil；运行目录证据不完整（密钥
// 缺失）按未补全投影——读取路径不产生 staging 副作用，也不因暂存证据受损
// 而拒绝整张计划（运行态处置归恢复矩阵，票 #60 消费）。
func (a *App) restoreStagedDigests(p model.SyncPlan) map[model.ResourceID]string {
	if !syncstage.RunExists(a.deps.StagingRoot, restoreStagingAnchor(p)) {
		return nil
	}
	run, err := syncstage.OpenRun(a.deps.StagingRoot, restoreStagingAnchor(p))
	if err != nil {
		return nil
	}
	out := make(map[model.ResourceID]string, len(p.RestoreItems))
	for _, it := range p.RestoreItems {
		if it.StageRel == "" {
			continue
		}
		d, ok, err := run.StagedDigest(it.StageRel)
		if err == nil && ok {
			out[it.ResourceID] = d
		}
	}
	return out
}

// restoreExactReady 计算实时就绪面（契约 06 §3.5）：就绪 = restorable_from_cas ∪
// redownload_required ∪ (user_object_required ∧ staged)；skip 行不再计入；
// delete 行不占四标记（ADR-0006 §2/§5）、天然可执行，不计入就绪面。
// ExactFeasible = 全部行就绪。
func restoreExactReady(items []model.RestorePlanItem, staged map[model.ResourceID]string, skipSet map[model.ResourceID]struct{}) bool {
	for _, it := range items {
		if _, ok := skipSet[it.ResourceID]; ok {
			continue
		}
		if it.ChangeKind == restoreChangeDelete {
			continue // 删除行不占四标记，天然可执行（ADR-0006 §5）
		}
		switch it.Marker {
		case model.MarkerRestorableFromCAS, model.MarkerRedownloadRequired:
		case model.MarkerUserObjectRequired:
			if it.ExpectedDigest == "" || staged[it.ResourceID] != it.ExpectedDigest {
				return false
			}
		default: // unrecoverable
			return false
		}
	}
	return true
}

// restoreConfirmations 从判定行与操作推导确认要求（既有推导沿 P2 词汇：
// overwrite/delete/write_project/unrecoverable），并恒追加 restore_acknowledge
// （severity=warning，resource_count=操作行数）——confirmation_requirements 恒
// 非空，授权模式零特判而自然不适用回滚（ADR-0006 §6，契约 06 §3.2）。
func restoreConfirmations(items []model.RestorePlanItem, ops []model.PlannedOperation) []model.ConfirmationRequirement {
	var overwriteCount, deleteCount, writeProjectCount, unrecoverableCount int
	for _, it := range items {
		switch it.ChangeKind {
		case restoreChangeModify:
			overwriteCount++
		case restoreChangeDelete:
			deleteCount++
		}
		if it.Marker == model.MarkerUnrecoverable {
			unrecoverableCount++
		}
	}
	for _, op := range ops {
		if op.Kind == model.OpWriteProject {
			writeProjectCount++
		}
	}
	reqs := make([]model.ConfirmationRequirement, 0, 5)
	add := func(code, severity string, count int) {
		if count > 0 {
			reqs = append(reqs, model.ConfirmationRequirement{Code: code, Severity: severity, ResourceCount: count})
		}
	}
	add("overwrite", "info", overwriteCount)
	add("delete", "warning", deleteCount)
	add("write_project", "warning", writeProjectCount)
	add("unrecoverable", "warning", unrecoverableCount)
	reqs = append(reqs, model.ConfirmationRequirement{
		Code:          model.ConfirmRestoreAcknowledge,
		Severity:      model.ConfirmSeverityWarning,
		ResourceCount: len(items),
	})
	return reqs
}

// restorePlanView 组装投影：staged 由计划暂存目录实时推导（digest 复核相符才
// 算就绪），skip 固化于 Resolutions；blocked_by 是非就绪标记行的静态清单
// （ADR-0006 §4 的 exact_infeasible 证据）；ExactFeasible 实时翻转。
func (a *App) restorePlanView(p model.SyncPlan, effective *model.PlanStatus) view.RestorePlanView {
	staged := a.restoreStagedDigests(p)
	skipSet := make(map[model.ResourceID]struct{}, len(p.Resolutions))
	for _, r := range p.Resolutions {
		if r.Choice == model.ChoiceSkip {
			skipSet[r.ResourceID] = struct{}{}
		}
	}
	items := make([]view.RestorePlanItemView, 0, len(p.RestoreItems))
	blocked := make([]view.RestoreBlockedItemView, 0)
	for _, it := range p.RestoreItems {
		stagedOK := it.ExpectedDigest != "" && staged[it.ResourceID] == it.ExpectedDigest
		_, isSkipped := skipSet[it.ResourceID]
		items = append(items, view.RestorePlanItemView{
			RestorePlanItem: it,
			Skipped:         isSkipped,
			Staged:          stagedOK,
		})
		if it.ChangeKind != restoreChangeDelete &&
			it.Marker != model.MarkerRestorableFromCAS && it.Marker != model.MarkerRedownloadRequired {
			blocked = append(blocked, view.RestoreBlockedItemView{
				ResourceID:   string(it.ResourceID),
				RelativePath: it.RelativePath,
				Marker:       string(it.Marker),
			})
		}
	}
	status := p.Status
	if effective != nil {
		status = *effective
	}
	return view.RestorePlanView{
		SchemaVersion:            model.CurrentSchemaVersion,
		PlanID:                   p.PlanID,
		RelationID:               p.RelationID,
		TargetCommitID:           p.TargetCommitID,
		Status:                   string(status),
		ExactFeasible:            restoreExactReady(p.RestoreItems, staged, skipSet),
		BlockedBy:                blocked,
		Items:                    items,
		RequestedExactness:       string(p.RequestedExactness),
		ConfirmationRequirements: p.ConfirmationRequirements,
		ExpiresAt:                p.ExpiresAt,
		CreatedAt:                p.CreatedAt,
	}
}

// ---- 取数辅助（判定矩阵的数据面；表示/语义口径与 core/diff 一致）----

// baselineRep 取基线资源在指定侧的表示（absent 为 nil）。
func baselineRep(b *model.BaselineResource, side model.Side) *model.Representation {
	if b == nil {
		return nil
	}
	if side == model.SideProject {
		return b.ProjectRepresentation
	}
	return b.RuntimeRepresentation
}

// sideObservation 取当前观测在指定侧的观察（absent 为 nil）。
func sideObservation(curP, curR *model.ResourceObservation, side model.Side) *model.ResourceObservation {
	if side == model.SideProject {
		return curP
	}
	return curR
}

// repOfSide 取指定侧在「有写回侧」前提下的目标表示（该侧无写回则 nil）。
func repOfSide(tgt *model.BaselineResource, writeSides []model.Side, side model.Side) *model.Representation {
	if !writeSidesContains(writeSides, side) {
		return nil
	}
	return baselineRep(tgt, side)
}

// restoreTargetSemantic 计算目标基线侧语义摘要（absent 为空）；基线不携带
// Kind/Identity，统一由 ResourceID 推导（diff.baselineSemantic 同口径）。
func restoreTargetSemantic(id model.ResourceID, rep *model.Representation) (string, error) {
	if rep == nil {
		return "", nil
	}
	sem, err := normalize.SemanticDigest(normalize.KindOfResourceID(id), *rep, normalize.IdentityFromResourceID(id))
	if err != nil {
		return "", fmt.Errorf("restore: 目标基线资源 %s: %w", id, err)
	}
	return sem, nil
}

// restoreCurrentSemantic 计算当前观测侧语义摘要（absent 为空；diff.observedSemantic 同口径）。
func restoreCurrentSemantic(id model.ResourceID, obs *model.ResourceObservation) (string, error) {
	if obs == nil {
		return "", nil
	}
	sem, err := normalize.SemanticDigest(obs.Kind, obs.Representation, obs.Identity)
	if err != nil {
		return "", fmt.Errorf("restore: 当前观测资源 %s: %w", id, err)
	}
	return sem, nil
}

// redownloadInfoOf 从项目侧 metafile 元数据提取重取信息（「重取性看数据不
// 看出身」的数据面）：CF file-id + filename 实存即有信息；hash 格式可验性由
// download.SupportsHashFormat 另判。runtime 表示不携带 CF 信息（pw.toml 是
// packwiz 侧唯一权威），file-id 非法（非正整数）视为无信息。
func redownloadInfoOf(projRep *model.Representation) (*model.RedownloadInfo, bool) {
	if projRep == nil {
		return nil, false
	}
	fileIDStr := projRep.Metadata[model.MetaCFFileID]
	filename := projRep.Metadata[model.MetaFilename]
	if fileIDStr == "" || filename == "" {
		return nil, false
	}
	fileID, err := strconv.ParseInt(strings.TrimSpace(fileIDStr), 10, 64)
	if err != nil || fileID <= 0 {
		return nil, false
	}
	return &model.RedownloadInfo{
		FileID:       fileID,
		Filename:     filename,
		HashFormat:   strings.ToLower(strings.TrimSpace(projRep.Metadata[model.MetaDeclaredHashAlgo])),
		DeclaredHash: strings.ToLower(strings.TrimSpace(projRep.Metadata[model.MetaDeclaredHashValue])),
	}, true
}

// restoreTargetDigest 提取表示的 sha256 内容摘要（CAS/staging 均为 sha256 寻址
// 口径）：Content 优先（扫描实测）。声明 sha256 兜底仅限 runtime 侧——项目侧
// mod 表示的声明 hash（packwiz [download] hash）所指对象是 jar 载体而非
// metafile 自身，作项目侧兜底即「jar 摘要误标为 metafile 目标摘要」的错写链
// 源头（补全分支 digest 等值通过 → jar 字节整文件写入 .pw.toml → verify 才拦，
// 字节已落盘）：项目侧目标摘要只认实测 Content，声明哈希一律不作项目侧兜底
//（ADR-0012 §7.2，兜底删除＋降标行补全拒收＋digest 等值自然失配＋verify 复扫
// 四层锁定）；两者皆无返回空串。
func restoreTargetDigest(side model.Side, rep *model.Representation) string {
	if rep == nil {
		return ""
	}
	if rep.Content != nil && strings.EqualFold(rep.Content.Algorithm, "sha256") && rep.Content.Digest != "" {
		return strings.ToLower(rep.Content.Digest)
	}
	if side == model.SideRuntime && strings.EqualFold(rep.Metadata[model.MetaDeclaredHashAlgo], "sha256") {
		if v := strings.TrimSpace(rep.Metadata[model.MetaDeclaredHashValue]); v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}

// restoreDeletionWarn 判定删除行的「不可重取」警示（ADR-0006 §5）：被删内容是
// mod 且当前无重取信息（手放 mod 照删不保全，删除即永久丢失）；packwiz 管理的
// mod（项目侧 pw.toml 带重取信息）可重取，不警示；非 mod 删除走 before-preserve
// 进 CAS，可从对象库找回，不警示。
func restoreDeletionWarn(id model.ResourceID, curP *model.ResourceObservation) bool {
	if normalize.KindOfResourceID(id) != model.ResourceMod {
		return false
	}
	if curP == nil {
		return true // runtime-only jar：无 metafile，无重取信息
	}
	_, hasInfo := redownloadInfoOf(&curP.Representation)
	return !hasInfo
}

// restoreDisplayPath 选展示路径：写回侧目标表示优先（runtime 侧是 mod jar 的
// 实体），project 兜底；删除行回退当前观测路径。
func restoreDisplayPath(tProj, tRt *model.Representation, curP, curR *model.ResourceObservation) string {
	switch {
	case tRt != nil:
		return tRt.RelativePath
	case tProj != nil:
		return tProj.RelativePath
	case curP != nil:
		return curP.Representation.RelativePath
	case curR != nil:
		return curR.Representation.RelativePath
	default:
		return ""
	}
}

// restoreStageRel 选补全/写回的暂存目标路径（写回侧目标表示的 root-relative
// 路径；runtime 优先——用户补全对象与 jar 落盘同路径）。
func restoreStageRel(tgt *model.BaselineResource, writeSides []model.Side) string {
	if writeSidesContains(writeSides, model.SideRuntime) {
		if rep := baselineRep(tgt, model.SideRuntime); rep != nil {
			return rep.RelativePath
		}
	}
	if rep := baselineRep(tgt, model.SideProject); rep != nil {
		return rep.RelativePath
	}
	return ""
}

// restoreWriteOp 构造写回操作：前置条件断言该侧 prepare 时点现状（存在性 +
// 当前内容指纹，Apply 阶段漂移即 stale 拦截）；ObjectRefs 固化写回目标内容
// （sha256 摘要可得时）。
func restoreWriteOp(id model.ResourceID, side model.Side, curPresent bool,
	curP, curR *model.ResourceObservation, tgt *model.BaselineResource, targetDigest string) model.PlannedOperation {
	pre := model.Precondition{ResourceID: id, Side: string(side)}
	if curPresent {
		pre.Existence = "present"
		if obs := sideObservation(curP, curR, side); obs != nil && obs.Representation.Content != nil {
			c := *obs.Representation.Content
			pre.Expected = &c
		}
	} else {
		pre.Existence = "absent"
	}
	op := model.PlannedOperation{
		Kind:          model.OpWriteProject,
		ResourceID:    id,
		Preconditions: []model.Precondition{pre},
		Reversible:    true,
	}
	if side == model.SideRuntime {
		op.Kind = model.OpWriteRuntime
	}
	if targetDigest != "" {
		op.ObjectRefs = []model.ContentRef{{Algorithm: "sha256", Digest: targetDigest}}
	}
	return op
}

// restoreRemoveOp 构造删除操作：前置条件断言被删侧 prepare 时点 present 且
// 指纹一致（removePreconditions 同构）。
func restoreRemoveOp(id model.ResourceID, side model.Side, curObs *model.ResourceObservation) model.PlannedOperation {
	pre := model.Precondition{ResourceID: id, Side: string(side), Existence: "present"}
	if curObs != nil && curObs.Representation.Content != nil {
		c := *curObs.Representation.Content
		pre.Expected = &c
	}
	op := model.PlannedOperation{
		Kind:          model.OpRemoveProject,
		ResourceID:    id,
		Preconditions: []model.Precondition{pre},
		Reversible:    true,
	}
	if side == model.SideRuntime {
		op.Kind = model.OpRemoveRuntime
	}
	return op
}

// sortAndNumberRestoreOps 对回滚操作做与 plan.sortAndNumberOperations 同构的
// 确定性排序编号（kindRank 序 + ResourceID 字节序，op_%04d）。
func sortAndNumberRestoreOps(ops []model.PlannedOperation) []model.PlannedOperation {
	kindRank := func(k model.OperationKind) int {
		switch k {
		case model.OpWriteRuntime:
			return 0
		case model.OpWriteProject:
			return 1
		case model.OpRemoveRuntime:
			return 2
		case model.OpRemoveProject:
			return 3
		default:
			return 4
		}
	}
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
	return ops
}

func writeSidesContains(sides []model.Side, s model.Side) bool {
	for _, v := range sides {
		if v == s {
			return true
		}
	}
	return false
}

func deleteSidesContains(sides []model.Side, s model.Side) bool {
	return writeSidesContains(sides, s)
}

// restoreFindItem 按资源 ID 查判定行（缺失返回 nil）。
func restoreFindItem(p model.SyncPlan, id model.ResourceID) *model.RestorePlanItem {
	for i := range p.RestoreItems {
		if p.RestoreItems[i].ResourceID == id {
			return &p.RestoreItems[i]
		}
	}
	return nil
}
