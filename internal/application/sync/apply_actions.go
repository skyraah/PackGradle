package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/diff"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/syncstage"
)

// Apply 引擎的文件层推导与执行原语封装（票 #37）：
// 从计划操作 + 输入快照推导逐操作文件执行计划（目标侧/路径/前后 digest/内容来源），
// staging 期做前置条件复核、before-content CAS 保全与 after 内容暂存，applying 期
// 经 syncstage 动作原语落地。mod 资源的 jar 内容 P2 无下载器（materialization
// modes=["copy"]，可再获取内容不入 CAS，架构 §8.2）：仅当目标已达成声明 digest
// （幂等已应用）或 CAS 恰有该内容时可执行，否则操作失败进恢复路径。

// 文件动作类别（与 syncstage.OwnershipProof.Kind 同一三分法）。
const (
	applyActionCreate = "create"
	applyActionModify = "modify"
	applyActionDelete = "delete"
)

// 操作终局结果码（journal result_json 顶层 code；T06 ListApplyOperations
// ResultCode 的数据源。引擎定义字符串，非 err.* 码，不经 locale）。
const (
	resultPreconditionViolated = "precondition_violated"
	resultContentUnavailable   = "content_unavailable"
	resultUnsupportedOp        = "unsupported_operation"
	resultTargetModified       = "target_modified"
	resultDigestMismatch       = "digest_mismatch"
	resultTargetNotFile        = "target_not_file"
	resultPathEscape           = "path_escape"
	resultProofInvalid         = "proof_invalid"
	resultCancelled            = "cancelled"
	resultVerifyMismatch       = "verify_mismatch"
	resultJournalAdvance       = "journal_advance_failed"
	resultIOError              = "io_error"
)

// objectRefPurposeBefore 是 before-content 保全引用的 purpose（object_refs 行）。
const objectRefPurposeBefore = "before_preservation"

// applyFilePlan 是单操作的文件执行计划（staging 前从计划与输入快照推导，
// 纯函数无副作用；推导失败不报错——记录为操作级不可执行原因，走失败面）。
type applyFilePlan struct {
	op             model.PlannedOperation
	action         string               // create|modify|delete；空串 = 不可执行
	targetSide     model.Side           // 动作作用侧
	root           string               // 目标侧端点 canonical root
	targetRel      string               // 目标 root-relative 路径（斜杠）
	sourceRoot     string               // after 内容源侧端点 root（写操作非空）
	sourceRel      string               // after 内容源文件 root-relative（相对 sourceRoot）
	beforeDigest   string               // 覆盖/删除前的期望内容 sha256（create 为空）
	afterDigest    string               // write 的目标内容 sha256（delete 为空）
	afterFromCAS   string               // after 内容取自 CAS 对象的 digest
	targetReady    bool                 // 目标已达成 after（幂等重放，动作无需内容）
	recoverability model.Recoverability // before 保全策略（旧基线值或按类别缺省）
	blockedCode    string               // 非空 = staging 期直接失败的结果码
}

// applySideForOp 返回操作的源侧与目标侧（写操作源侧=内容来源，删除只有目标侧）。
func applySideForOp(kind model.OperationKind) (src, tgt model.Side, ok bool) {
	switch kind {
	case model.OpWriteRuntime:
		return model.SideProject, model.SideRuntime, true
	case model.OpWriteProject:
		return model.SideRuntime, model.SideProject, true
	case model.OpRemoveRuntime:
		return "", model.SideRuntime, true
	case model.OpRemoveProject:
		return "", model.SideProject, true
	default:
		return "", "", false // materialize（junction）属 Phase 4
	}
}

// snapshotObs 取输入快照中的资源观察（absent 为 nil）。
func snapshotObs(s model.ObservedSnapshot, id model.ResourceID) *model.ResourceObservation {
	if obs, ok := s.Resources[id]; ok {
		return &obs
	}
	return nil
}

// declaredSHA256 返回 mod 表示的声明内容 sha256（仅声明算法为 sha256 时可信——
// ownership proof 的 digest 域固定 sha256 hex）；否则空串。
func declaredSHA256(rep model.Representation) string {
	algo := strings.ToLower(rep.Metadata[model.MetaDeclaredHashAlgo])
	value := rep.Metadata[model.MetaDeclaredHashValue]
	if algo == "sha256" && value != "" {
		return strings.ToLower(value)
	}
	return ""
}

// defaultRecoverability 按资源类别给 before 保全缺省策略：mod 可再获取（不入
// CAS，架构 §8.2），其余文件内容保全进 CAS（fail-safe，宁可多存）。
func defaultRecoverability(id model.ResourceID) model.Recoverability {
	if normalize.KindOfResourceID(id) == model.ResourceMod {
		return model.RecoverabilityRedownload
	}
	return model.RecoverabilityCAS
}

// deriveApplyFilePlans 推导全部操作的文件执行计划。输入快照提供逐侧路径与内容
// 指纹，旧基线提供 recoverability 策略。写操作的 after digest 取计划前置条件中
// 源侧期望内容（计划即契约）；mod 源表示无内容指纹，回退声明 hash。
func deriveApplyFilePlans(plan model.SyncPlan, snapP, snapR model.ObservedSnapshot,
	base *model.SyncBaseline, projRoot, rtRoot string) []applyFilePlan {

	rootBySide := map[model.Side]string{model.SideProject: projRoot, model.SideRuntime: rtRoot}
	snaps := map[model.Side]model.ObservedSnapshot{model.SideProject: snapP, model.SideRuntime: snapR}
	baseRec := map[model.ResourceID]model.Recoverability{}
	if base != nil {
		for id, res := range base.Resources {
			if res.Recoverability != "" {
				baseRec[id] = res.Recoverability
			}
		}
	}

	out := make([]applyFilePlan, 0, len(plan.Operations))
	for _, op := range plan.Operations {
		fp := applyFilePlan{op: op, recoverability: defaultRecoverability(op.ResourceID)}
		if rec, ok := baseRec[op.ResourceID]; ok {
			fp.recoverability = rec
		}
		srcSide, tgtSide, known := applySideForOp(op.Kind)
		if !known {
			fp.blockedCode = resultUnsupportedOp
			out = append(out, fp)
			continue
		}
		fp.targetSide = tgtSide
		fp.root = rootBySide[tgtSide]
		fp.sourceRoot = rootBySide[srcSide]

		preBySide := map[string]model.Precondition{}
		for _, pre := range op.Preconditions {
			preBySide[pre.Side] = pre
		}
		srcObs := snapshotObs(snaps[srcSide], op.ResourceID)
		tgtObs := snapshotObs(snaps[tgtSide], op.ResourceID)

		if op.Kind == model.OpRemoveRuntime || op.Kind == model.OpRemoveProject {
			// 删除：目标必须存在（计划前置条件 present），路径取目标侧观察。
			if tgtObs == nil {
				fp.blockedCode = resultPreconditionViolated
				out = append(out, fp)
				continue
			}
			fp.action = applyActionDelete
			fp.targetRel = tgtObs.Representation.RelativePath
			if pre := preBySide[string(tgtSide)]; pre.Expected != nil {
				fp.beforeDigest = pre.Expected.Digest
			} else if tgtObs.Representation.Content != nil {
				fp.beforeDigest = tgtObs.Representation.Content.Digest
			}
			out = append(out, fp)
			continue
		}

		// 写操作：源侧观察必须存在（计划断言 present）。
		if srcObs == nil {
			fp.blockedCode = resultPreconditionViolated
			out = append(out, fp)
			continue
		}
		// after digest：计划源侧前置条件期望值优先；mod 无内容指纹回退声明 hash。
		if pre, ok := preBySide[string(srcSide)]; ok && pre.Expected != nil {
			fp.afterDigest = pre.Expected.Digest
		}
		if fp.afterDigest == "" && normalize.KindOfResourceID(op.ResourceID) == model.ResourceMod {
			fp.afterDigest = declaredSHA256(srcObs.Representation)
		}
		// before digest：目标侧前置条件期望值。
		if pre, ok := preBySide[string(tgtSide)]; ok && pre.Existence == "present" && pre.Expected != nil {
			fp.beforeDigest = pre.Expected.Digest
		}
		if fp.beforeDigest == "" && tgtObs != nil && tgtObs.Representation.Content != nil {
			fp.beforeDigest = tgtObs.Representation.Content.Digest
		}
		if fp.beforeDigest != "" {
			fp.action = applyActionModify
		} else {
			fp.action = applyActionCreate
		}

		// 目标路径：文件资源两侧同路径（资源 ID 内嵌路径）；mod 取目标侧观察路径，
		// 缺观察时回退源侧 filename 元数据（packwiz jar 命名）。
		if normalize.KindOfResourceID(op.ResourceID) == model.ResourceMod {
			switch {
			case tgtObs != nil:
				fp.targetRel = tgtObs.Representation.RelativePath
			case srcObs.Representation.Metadata[model.MetaFilename] != "":
				fp.targetRel = "mods/" + srcObs.Representation.Metadata[model.MetaFilename]
			default:
				fp.blockedCode = resultUnsupportedOp
				out = append(out, fp)
				continue
			}
		} else {
			fp.targetRel = srcObs.Representation.RelativePath
		}

		// after 内容来源优先级：源侧文件（文件资源恒命中）→ 目标已达成（幂等）
		// → CAS 对象。全不命中 = P2 copy 物化无法离线取得内容（如需下载的 jar）。
		switch {
		case srcObs.Representation.Content != nil && srcObs.Representation.Content.Digest == fp.afterDigest:
			fp.sourceRel = srcObs.Representation.RelativePath
		case tgtObs != nil && tgtObs.Representation.Content != nil && tgtObs.Representation.Content.Digest == fp.afterDigest:
			fp.targetReady = true
		case fp.afterDigest != "":
			fp.afterFromCAS = fp.afterDigest
		default:
			fp.blockedCode = resultContentUnavailable
		}
		out = append(out, fp)
	}
	return out
}

// verifyApplyPreconditions 复核单操作的全部前置条件在磁盘上仍成立（ADR-0004 §1：
// prepared 落列的前置条件由引擎在 staged 前复核）。快照观察提供 root-relative
// 路径；Expected 指纹逐一重算比对。返回空串 = 通过，否则为失败结果码。
func verifyApplyPreconditions(op model.PlannedOperation, snaps map[model.Side]model.ObservedSnapshot,
	rootBySide map[model.Side]string) string {

	for _, pre := range op.Preconditions {
		obs := snapshotObs(snaps[model.Side(pre.Side)], pre.ResourceID)
		abs := filepath.Join(rootBySide[model.Side(pre.Side)], filepath.FromSlash(prePathOf(obs, pre)))
		_, statErr := os.Lstat(abs)
		switch pre.Existence {
		case "absent":
			if statErr == nil {
				return resultPreconditionViolated
			}
			continue
		default: // present
			if statErr != nil {
				return resultPreconditionViolated
			}
		}
		if pre.Expected == nil {
			continue
		}
		ref, err := syncstage.HashFile(abs)
		if err != nil || ref.Digest != pre.Expected.Digest {
			return resultPreconditionViolated
		}
	}
	return ""
}

// prePathOf 返回前置条件对应资源的 root-relative 路径（观察缺失时按资源 ID 的
// file: 前缀回退——absent 断言无需真实路径，存在性检查以空路径失败兜底）。
func prePathOf(obs *model.ResourceObservation, pre model.Precondition) string {
	if obs != nil {
		return obs.Representation.RelativePath
	}
	s := string(pre.ResourceID)
	if strings.HasPrefix(s, "file:") {
		return strings.TrimPrefix(s, "file:")
	}
	return ""
}

// applyResultCode 把引擎/原语错误映射为操作终局结果码（syncstage 哨兵映射，
// 票面「ErrTargetModified 等哨兵映射 result_code」）。
func applyResultCode(err error) string {
	switch {
	case errors.Is(err, syncstage.ErrTargetModified):
		return resultTargetModified
	case errors.Is(err, syncstage.ErrDigestMismatch):
		return resultDigestMismatch
	case errors.Is(err, syncstage.ErrTargetNotFile):
		return resultTargetNotFile
	case errors.Is(err, syncstage.ErrPathEscape):
		return resultPathEscape
	case errors.Is(err, syncstage.ErrProofInvalid):
		return resultProofInvalid
	case errors.Is(err, ports.ErrInvalidTransition):
		return resultJournalAdvance
	default:
		if errors.Is(err, context.Canceled) {
			return resultCancelled
		}
		return resultIOError
	}
}

// afterContentReader 打开 after 内容流：目标已达成（幂等重放）返回空读器
// （syncstage 动作先查目标 digest，already_applied 路径不会消费内容；若目标
// 在 staging 与 applying 之间被外部改动，StageContent 的 hash 复核令空内容
// 落地前即失败，目标不受影响）；源侧文件/CAS 对象按需重开（动作可能重放）。
func (a *App) afterContentReader(ctx context.Context, fp applyFilePlan) (io.Reader, func(), error) {
	switch {
	case fp.targetReady:
		return strings.NewReader(""), func() {}, nil
	case fp.sourceRel != "":
		f, err := os.Open(filepath.Join(fp.sourceRoot, filepath.FromSlash(fp.sourceRel)))
		if err != nil {
			return nil, nil, err
		}
		return f, func() { f.Close() }, nil
	case fp.afterFromCAS != "":
		rc, err := a.deps.CAS.Open(ctx, fp.afterFromCAS)
		if err != nil {
			return nil, nil, err
		}
		return rc, func() { rc.Close() }, nil
	default:
		return nil, nil, errors.New("apply: after 内容不可得")
	}
}

// applyActionRunner 执行单文件动作。变量形态仅为测试注入文件层故障
// （意图先行/恢复路径断言）；生产路径恒 execApplyAction。
var applyActionRunner = execApplyAction

func execApplyAction(act *syncstage.Actions, kind string, p syncstage.OwnershipProof, content io.Reader) (syncstage.ApplyResult, error) {
	switch kind {
	case applyActionCreate:
		return act.ApplyCreate(p, content)
	case applyActionModify:
		return act.ApplyModify(p, content)
	case applyActionDelete:
		return act.ApplyDelete(p)
	default:
		return syncstage.ApplyResult{}, fmt.Errorf("apply: 未知动作类别 %q", kind)
	}
}

// buildVerifiedBaseline 从复扫快照构造新基线（redesign §6.6 步骤 5：committed
// 事务内写入）。recoverability 沿旧基线值，无旧值按类别缺省；absent tombstone
// 不入新基线（复扫资源并集即受管现状）。
func buildVerifiedBaseline(relID, parentID string, rescanP, rescanR model.ObservedSnapshot,
	oldBase *model.SyncBaseline) (model.SyncBaseline, error) {

	resources := make(map[model.ResourceID]model.BaselineResource)
	for _, side := range []model.ObservedSnapshot{rescanP, rescanR} {
		for id := range side.Resources {
			if _, done := resources[id]; done {
				continue
			}
			rec := defaultRecoverability(id)
			if oldBase != nil {
				if old, ok := oldBase.Resources[id]; ok && old.Recoverability != "" {
					rec = old.Recoverability
				}
			}
			pRep := repOf(rescanP, id)
			rRep := repOf(rescanR, id)
			projSem, err := sideSemantic(id, pRep)
			if err != nil {
				return model.SyncBaseline{}, err
			}
			rtSem, err := sideSemantic(id, rRep)
			if err != nil {
				return model.SyncBaseline{}, err
			}
			resources[id] = model.BaselineResource{
				State:                 "present",
				LogicalDigest:         normalize.LogicalDigest(projSem, rtSem),
				ProjectRepresentation: pRep,
				RuntimeRepresentation: rRep,
				Recoverability:        rec,
			}
		}
	}
	b := model.SyncBaseline{
		SchemaVersion:        model.CurrentSchemaVersion,
		BaselineID:           "", // 由调用方分配
		RelationID:           relID,
		ParentBaselineID:     parentID,
		CreatedAt:            "", // 由调用方填充
		NormalizationVersion: normalize.NormalizationVersion,
		Resources:            resources,
	}
	digest, err := normalize.BaselineDigest(b)
	if err != nil {
		return model.SyncBaseline{}, err
	}
	b.BaselineDigest = digest
	return b, nil
}

// repOf 取快照中资源表示的副本指针（absent 为 nil）。
func repOf(s model.ObservedSnapshot, id model.ResourceID) *model.Representation {
	obs := snapshotObs(s, id)
	if obs == nil {
		return nil
	}
	rep := obs.Representation
	return &rep
}

// sideSemantic 计算单侧表示的语义摘要（nil 表示 = absent 语义 ""）。
func sideSemantic(id model.ResourceID, rep *model.Representation) (string, error) {
	if rep == nil {
		return "", nil
	}
	return normalize.SemanticDigest(normalize.KindOfResourceID(id), *rep, normalize.IdentityFromResourceID(id))
}

// verifyRescan 验证复扫快照与计划目标一致（ADR-0004 §5：完整复扫 + 快照与计划
// 目标一致才可 committed）：
//  1. 逐操作目标达成——write 双侧语义一致、remove 目标侧缺席；
//  2. 未选资源相对既有基线未漂移（跳过/手动裁决资源豁免——用户显式保留差异）。
//
// 返回违规清单与剩余差异数（提交 completeness 的数据源）。
func verifyRescan(plan model.SyncPlan, plans []applyFilePlan, rescanP, rescanR model.ObservedSnapshot,
	base *model.SyncBaseline) (violations []string, remaining int, err error) {

	snaps := map[model.Side]model.ObservedSnapshot{model.SideProject: rescanP, model.SideRuntime: rescanR}

	for _, fp := range plans {
		switch fp.action {
		case applyActionCreate, applyActionModify:
			pSem, err := sideSemantic(fp.op.ResourceID, repOf(rescanP, fp.op.ResourceID))
			if err != nil {
				return nil, 0, err
			}
			rSem, err := sideSemantic(fp.op.ResourceID, repOf(rescanR, fp.op.ResourceID))
			if err != nil {
				return nil, 0, err
			}
			if pSem == "" || rSem == "" || pSem != rSem {
				violations = append(violations,
					fmt.Sprintf("write %s: 复扫双侧语义不一致（project=%q runtime=%q）", fp.op.ResourceID, pSem, rSem))
			}
		case applyActionDelete:
			if snapshotObs(snaps[fp.targetSide], fp.op.ResourceID) != nil {
				violations = append(violations,
					fmt.Sprintf("delete %s: 复扫目标侧 %s 仍存在", fp.op.ResourceID, fp.targetSide))
			}
		}
	}

	res, err := diff.ThreeWay(diff.Input{RelationID: plan.RelationID, Base: base, Project: rescanP, Runtime: rescanR})
	if err != nil {
		return nil, 0, err
	}
	opRes := map[model.ResourceID]bool{}
	for _, fp := range plans {
		opRes[fp.op.ResourceID] = true
	}
	exempt := map[model.ResourceID]bool{}
	for _, r := range plan.Resolutions {
		if r.Choice == model.ChoiceSkip || r.Choice == model.ChoiceManual {
			exempt[r.ResourceID] = true
		}
	}
	clean := map[diff.Classification]bool{
		diff.ClassNoop: true, diff.ClassConverged: true, diff.ClassAdoptEqual: true,
	}
	for _, d := range res.Diffs {
		if clean[d.Classification] {
			continue
		}
		remaining++
		if !opRes[d.ResourceID] && !exempt[d.ResourceID] {
			violations = append(violations,
				fmt.Sprintf("unselected %s: 复扫分类 %s（基线%v）", d.ResourceID, d.Classification, base != nil))
		}
	}
	return violations, remaining, nil
}
