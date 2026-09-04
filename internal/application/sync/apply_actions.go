package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/diff"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/download"
	"packgradle/internal/syncstage"
)

// Apply 引擎的文件层推导与执行原语封装（票 #37；P3 下载接线票 #63）：
// 从计划操作 + 输入快照推导逐操作文件执行计划（目标侧/路径/前后 digest/内容来源），
// staging 期做前置条件复核、before-content CAS 保全与 after 内容暂存，applying 期
// 经 syncstage 动作原语落地。物化数据源两路（ADR-0008 §6）：copy=本地源文件/CAS
// （P2 既有），download=CF 免钥匙直链（票 #58 引擎产出已过声明 hash 校验的字节，
// 下载相位喂既有 StageContent，不另起写路径）。

// 文件动作类别（与 syncstage.OwnershipProof.Kind 同一三分法）。
const (
	applyActionCreate = "create"
	applyActionModify = "modify"
	applyActionDelete = "delete"
)

// actionChangeKind 把文件动作类别映射为提交变化类别（同一 create/modify/delete
// 三分法一一对应；空串/未知动作返回空串）。verifyRescan 复扫判定、buildCommitChanges
// 提交变化行与恢复补偿（recovery_probe）三处级联共享此映射，作为动作分支的唯一词汇表。
func actionChangeKind(action string) model.ChangeKind {
	switch action {
	case applyActionCreate:
		return model.ChangeCreate
	case applyActionModify:
		return model.ChangeModify
	case applyActionDelete:
		return model.ChangeDelete
	default:
		return ""
	}
}

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
	afterDigest    string               // write 的目标内容 sha256（delete 为空；download 行在下载相位回填实测值）
	afterFromCAS   string               // after 内容取自 CAS 对象的 digest
	targetReady    bool                 // 目标已达成 after（幂等重放，动作无需内容）
	recoverability model.Recoverability // before 保全策略（旧基线值或按类别缺省）
	blockedCode    string               // 非空 = staging 期直接失败的结果码
	// 物化模式（契约 06 §3.7，票 #63）：计划行推导，空串（旧行）按 copy。
	materialization string
	// dlReq/dlPath/dlDone 是 download 行的取数状态（下载相位填）：dlReq 由
	// 源侧 metafile 元数据构造；成功后 dlPath 指运行暂存 downloads/ 下的成品
	//（已过声明 hash 校验），afterDigest 回填成品实测 sha256（两层校验第二层
	// 的复核基准，journal after_digest 同源）。
	dlReq  *download.Request
	dlPath string
	dlDone bool
	// dlFail* 是下载相位的取数失败事实（剔出本场，票 #63）：dlFailCode 为
	// 跳过原因码（err.download.* / hash_format_unsupported）。
	dlFailCode  string
	dlFailArgs  []string
	dlFailCause string
	// mergeProduct 标记 write_merged 的按侧展开行（票 #93）：after 内容=
	// 暂存期按计划锁定的三侧快照确定性重算（mergeBaseDigest=基线内容 CAS
	// 摘要；sourceRel=双端同路径的合并资源路径；sourceRoot=另一侧端点根）。
	// afterDigest 在重算后回填（计划不锁定产物摘要——同算法同输入同输出）。
	mergeProduct   bool
	mergeBaseDigest string
}

// skipReasonContentUnavailable 是 copy 取数失败的跳过原因码（与既有操作终局
// 结果码同字面：本地数据源无目标字节）。
const skipReasonContentUnavailable = resultContentUnavailable

// mergedExpansionSuffix 是 write_merged 操作在运行期按侧展开的 journal/证明
// 行 ID 后缀（票 #93）：计划面一资源一操作（契约 07 §3.3），执行面 journal、
// 所有权证明与恢复裁决逐侧成行——计划操作 ID 加后缀派生行 ID（op_0001_p /
// op_0001_r，syncstage.validateID 字符集内）。恢复路径凭后缀反解目标侧。
const (
	mergedExpansionProject = "_p"
	mergedExpansionRuntime = "_r"
)

// mergedExpansionSide 从展开行 ID 反解 write_merged 的目标侧；非展开行返回
// false。引擎自身生成的 op_%04d ID 不以 _p/_r 结尾，后缀空间无碰撞。
func mergedExpansionSide(operationID string) (model.Side, bool) {
	switch {
	case strings.HasSuffix(operationID, mergedExpansionProject):
		return model.SideProject, true
	case strings.HasSuffix(operationID, mergedExpansionRuntime):
		return model.SideRuntime, true
	default:
		return "", false
	}
}

// mergedExpansionID 为 write_merged 的指定侧派生展开行 ID。
func mergedExpansionID(operationID string, side model.Side) string {
	if side == model.SideRuntime {
		return operationID + mergedExpansionRuntime
	}
	return operationID + mergedExpansionProject
}

// applySideForOp 返回操作的源侧与目标侧（写操作源侧=内容来源，删除只有目标侧）。
// write_merged（票 #93）无单一内容源侧（内容=暂存期确定性重算的合并产物），
// 目标侧由展开行 ID 后缀反解；计划原行（无后缀）不可执行——执行前必经
// deriveApplyFilePlans 按侧展开。
func applySideForOp(op model.PlannedOperation) (src, tgt model.Side, ok bool) {
	switch op.Kind {
	case model.OpWriteRuntime:
		return model.SideProject, model.SideRuntime, true
	case model.OpWriteProject:
		return model.SideRuntime, model.SideProject, true
	case model.OpRemoveRuntime:
		return "", model.SideRuntime, true
	case model.OpRemoveProject:
		return "", model.SideProject, true
	case model.OpWriteMerged:
		if side, expanded := mergedExpansionSide(op.ID); expanded {
			return "", side, true
		}
		return "", "", false
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
		// 物化模式（票 #63）：计划行推导值直传；空串（P2 旧行）按 copy 兼容。
		// write_merged 不设物化——内容源是本运行确定性重算（Kind 即内容源分派）。
		if op.Materialization == model.MaterializationDownload {
			fp.materialization = model.MaterializationDownload
		} else {
			fp.materialization = model.MaterializationCopy
		}
		if rec, ok := baseRec[op.ResourceID]; ok {
			fp.recoverability = rec
		}
		if op.Kind == model.OpWriteMerged {
			// 双端写合并产物（票 #93）：一资源一操作按侧展开为两份文件执行
			// 计划（journal/证明/恢复逐侧成行，行 ID 加侧后缀）。内容源在
			// staging 期重算，此处不推导 afterDigest。
			out = append(out, deriveMergedFilePlans(op, base, rootBySide, snaps)...)
			continue
		}
		srcSide, tgtSide, known := applySideForOp(op)
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
		// ADR-0012 §2 捕获后项目侧 metafile 表示自带实测 Content——但 sync 写往
		// 运行端的载体是 jar（声明 hash 所指对象），metafile 自身字节的摘要绝不
		// 可作写盘内容源（否则会把 .pw.toml 字节当 jar 落盘）：项目侧源前置条件
		// 对 mod 行跳过（捕获前该值恒空，行为与捕获前逐点一致）。
		if pre, ok := preBySide[string(srcSide)]; ok && pre.Expected != nil &&
			!(normalize.KindOfResourceID(op.ResourceID) == model.ResourceMod && srcSide == model.SideProject) {
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

		// after 内容来源优先级（ADR-0008 §6）：源侧文件（文件资源恒命中）→ 目标
		// 已达成（幂等）→ download 行经引擎直链取数 → CAS 对象。全不命中 = copy
		// 物化无法离线取得内容（操作按取数失败剔出本场，票 #63）。
		switch {
		case srcObs.Representation.Content != nil && srcObs.Representation.Content.Digest == fp.afterDigest:
			fp.sourceRel = srcObs.Representation.RelativePath
		case tgtObs != nil && tgtObs.Representation.Content != nil && tgtObs.Representation.Content.Digest == fp.afterDigest:
			fp.targetReady = true
		case fp.materialization == model.MaterializationDownload:
			// download 行：数据源无本地字节 → 下载相位经票 #58 引擎取数。取数
			// 请求从源侧 metafile 元数据构造（计划推导保证三要素齐备；缺失为
			// 防御分支，按 copy 不可得处理走剔除）。afterDigest 可能为空（声明
			// 非 sha256），下载成功后回填成品实测 sha256。
			fp.dlReq = downloadRequestFor(srcObs)
			if fp.dlReq == nil {
				fp.blockedCode = resultContentUnavailable
			}
		case fp.afterDigest != "":
			fp.afterFromCAS = fp.afterDigest
		default:
			fp.blockedCode = resultContentUnavailable
		}
		out = append(out, fp)
	}
	return out
}

// deriveMergedFilePlans 把 write_merged 计划操作按侧展开为两份文件执行计划
//（票 #93，ADR-0009 §8）：双端各一份 modify/create 行（journal、所有权证明、
// 恢复裁决逐侧成行，行 ID 由计划操作 ID 加侧后缀派生）。内容源不在此推导——
// staging 期按计划锁定的三侧快照确定性重算（mergeProduct 标记）；基线内容
// 摘要取计划基线该资源 project 侧表示的 Content（与计划期合并判定同一取数
// 口径），缺失即计划不可执行（合并判定依赖基线内容，正常不可达）。
func deriveMergedFilePlans(op model.PlannedOperation, base *model.SyncBaseline,
	rootBySide map[model.Side]string, snaps map[model.Side]model.ObservedSnapshot) []applyFilePlan {

	// 双端同路径（合并资源是文件资源，mod 永不进合并面）：路径取任一侧观察，
	// 观察缺失属防御分支（前置条件 present 必有观察）。
	relPath := ""
	for _, side := range []model.Side{model.SideProject, model.SideRuntime} {
		if obs := snapshotObs(snaps[side], op.ResourceID); obs != nil {
			relPath = obs.Representation.RelativePath
			break
		}
	}
	baseDigest := ""
	if base != nil {
		if res, ok := base.Resources[op.ResourceID]; ok && res.ProjectRepresentation != nil &&
			res.ProjectRepresentation.Content != nil {
			baseDigest = res.ProjectRepresentation.Content.Digest
		}
	}
	preBySide := map[string]model.Precondition{}
	for _, pre := range op.Preconditions {
		preBySide[pre.Side] = pre
	}

	out := make([]applyFilePlan, 0, 2)
	for _, side := range []model.Side{model.SideProject, model.SideRuntime} {
		expanded := op
		expanded.ID = mergedExpansionID(op.ID, side)
		fp := applyFilePlan{
			op:              expanded,
			recoverability:  defaultRecoverability(op.ResourceID),
			materialization: model.MaterializationCopy,
			mergeProduct:    true,
			mergeBaseDigest: baseDigest,
			targetSide:      side,
			root:            rootBySide[side],
			sourceRoot:      rootBySide[oppositeModelSide(side)],
			sourceRel:       relPath,
			targetRel:       relPath,
		}
		if rec, ok := baseRecOf(base, op.ResourceID); ok {
			fp.recoverability = rec
		}
		if baseDigest == "" || relPath == "" {
			// 基线内容/路径不可得：合并产物无法重算，staging 期整场失败
			//（合并判定的前置事实，正常不可达）。
			fp.blockedCode = resultPreconditionViolated
			out = append(out, fp)
			continue
		}
		// before/after 与动作类别：目标侧前置条件 present+期望摘要 → modify
		//（干净合并以双侧同改为前提，present 为常态）；absent → create。
		if pre, ok := preBySide[string(side)]; ok && pre.Existence == "present" {
			if pre.Expected != nil {
				fp.beforeDigest = pre.Expected.Digest
			}
			fp.action = applyActionModify
		} else {
			fp.action = applyActionCreate
		}
		out = append(out, fp)
	}
	return out
}

// baseRecOf 取旧基线记录的恢复途径（deriveApplyFilePlans 的 baseRec 装配在
// 展开路径复用）。
func baseRecOf(base *model.SyncBaseline, id model.ResourceID) (model.Recoverability, bool) {
	if base == nil {
		return "", false
	}
	res, ok := base.Resources[id]
	if !ok || res.Recoverability == "" {
		return "", false
	}
	return res.Recoverability, true
}

// oppositeModelSide 返回另一端侧。
func oppositeModelSide(side model.Side) model.Side {
	if side == model.SideProject {
		return model.SideRuntime
	}
	return model.SideProject
}

// downloadRequestFor 从源侧 mod 表示元数据构造 CF 直链取数请求（file-id +
// filename + 声明 hash；票 #58 引擎输入契约）。要素缺失或 file-id 非法返回 nil
//（调用方按取数失败处理）。
func downloadRequestFor(src *model.ResourceObservation) *download.Request {
	m := src.Representation.Metadata
	fid, err := strconv.ParseInt(strings.TrimSpace(m[model.MetaCFFileID]), 10, 64)
	if err != nil || fid <= 0 || m[model.MetaFilename] == "" {
		return nil
	}
	return &download.Request{
		FileID:     fid,
		Filename:   m[model.MetaFilename],
		HashFormat: m[model.MetaDeclaredHashAlgo],
		Hash:       m[model.MetaDeclaredHashValue],
	}
}

// verifyApplyPreconditions 复核单操作的全部前置条件在磁盘上仍成立（ADR-0004 §1：
// prepared 落列的前置条件由引擎在 staged 前复核）。快照观察提供 root-relative
// 路径；Expected 指纹逐一重算比对。返回 code 非空 = 不成立；srcFailed 归因失败
// 断言在源侧（取数面不可得，票 #63 剔除语义的分流依据：源侧失效无写坏风险，
// copy/download 一条规矩剔出本场；目标侧失效=文件一致性风险，保持恢复面）。
func verifyApplyPreconditions(op model.PlannedOperation, snaps map[model.Side]model.ObservedSnapshot,
	rootBySide map[model.Side]string) (code string, srcFailed bool) {

	srcSide, _, known := applySideForOp(op)
	for _, pre := range op.Preconditions {
		obs := snapshotObs(snaps[model.Side(pre.Side)], pre.ResourceID)
		rel := prePathOf(pre, obs, snaps)
		if rel == "" {
			// 断言路径不可定位（观察缺失且无法按资源类别回退）：计划时点 diff
			// 已证明该侧缺席（present 断言必有观察路径，不会走到这里），无文件
			// 系统证据不误判——absent 断言直接放行（票 #63 下载接线修：mod 的
			// absent 断言此前落到空路径=端点根目录，Lstat 命中目录恒 violated）。
			if pre.Existence == "absent" {
				continue
			}
			return resultPreconditionViolated, known && pre.Side == string(srcSide)
		}
		abs := filepath.Join(rootBySide[model.Side(pre.Side)], filepath.FromSlash(rel))
		_, statErr := os.Lstat(abs)
		switch pre.Existence {
		case "absent":
			if statErr == nil {
				return resultPreconditionViolated, known && pre.Side == string(srcSide)
			}
			continue
		default: // present
			if statErr != nil {
				return resultPreconditionViolated, known && pre.Side == string(srcSide)
			}
		}
		if pre.Expected == nil {
			continue
		}
		ref, err := syncstage.HashFile(abs)
		if err != nil || ref.Digest != pre.Expected.Digest {
			return resultPreconditionViolated, known && pre.Side == string(srcSide)
		}
	}
	return "", false
}

// prePathOf 返回前置条件对应资源的 root-relative 路径：观察存在取观察路径；
// 缺失时 file: 资源按 ID 内嵌路径回退（两侧同路径），mod 资源按任一侧观察的
// filename 元数据推 mods/<filename>（与写动作落盘路径同源——缺观察的 mod
// absent 断言以真实 jar 路径裁决，票 #63）；仍不可得返回空串（调用方对 absent
// 放行、present 判 violated）。
func prePathOf(pre model.Precondition, obs *model.ResourceObservation,
	snaps map[model.Side]model.ObservedSnapshot) string {

	if obs != nil {
		return obs.Representation.RelativePath
	}
	s := string(pre.ResourceID)
	if strings.HasPrefix(s, "file:") {
		return strings.TrimPrefix(s, "file:")
	}
	if normalize.KindOfResourceID(pre.ResourceID) == model.ResourceMod {
		for _, side := range []model.Side{model.SideProject, model.SideRuntime} {
			if src := snapshotObs(snaps[side], pre.ResourceID); src != nil {
				if name := src.Representation.Metadata[model.MetaFilename]; name != "" {
					return "mods/" + name
				}
			}
		}
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
// 落地前即失败，目标不受影响）；write_merged 展开（票 #93）同样返回空读器
// ——内容源是 staging 期已复核的暂存副本（双端同路径共享同一暂存文件），
// StageContent 复用短路不会消费该读器，副本缺失时空内容必被自身 digest
// 复核拒绝；download 行读下载相位的引擎成品（运行暂存 downloads/ 下已过
// 声明 hash 校验的字节）；源侧文件/CAS 对象按需重开（动作可能重放）。
func (a *App) afterContentReader(ctx context.Context, fp applyFilePlan) (io.Reader, func(), error) {
	switch {
	case fp.targetReady:
		return strings.NewReader(""), func() {}, nil
	case fp.mergeProduct:
		return strings.NewReader(""), func() {}, nil
	case fp.dlDone && fp.dlPath != "":
		f, err := os.Open(fp.dlPath)
		if err != nil {
			return nil, nil, err
		}
		return f, func() { f.Close() }, nil
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
//  1. 逐操作目标达成——write 双侧语义一致、remove 目标侧缺席；download 行
//     （票 #63）目标达成判定=复扫目标侧实测 sha256 与下载成品一致（声明面可
//     为 sha1/murmur2 等非可比格式，双侧语义比对对其豁免——两层校验的第一层
//     已证明来源正确性）；
//  2. 未选资源相对既有基线未漂移（跳过/手动裁决资源与本场剔出的取数失败资源
//     豁免 violation，但计入剩余差异——partial 语义）；direction=ignore 的资源
//     （票 #100，ADR-0013 §3）已移出受管范围：不 violation、不计剩余差异，
//     否则「忽略后下一次 apply 必炸」。
//
// skipped 是本场剔出的取数失败清单（可空）；policySet 是复扫所用现行策略
// （与复扫观察的 PolicyID 同源）。返回违规清单与剩余差异数。
func verifyRescan(plan model.SyncPlan, plans []applyFilePlan, rescanP, rescanR model.ObservedSnapshot,
	base *model.SyncBaseline, skips []stagedOp, policySet model.MappingPolicy) (violations []string, remaining int, err error) {

	snaps := map[model.Side]model.ObservedSnapshot{model.SideProject: rescanP, model.SideRuntime: rescanR}
	ignored := ignoreDirectionFilter(policySet, rescanP, rescanR)

	for _, fp := range plans {
		switch actionChangeKind(fp.action) {
		case model.ChangeCreate, model.ChangeModify:
			if fp.materialization == model.MaterializationDownload && fp.dlDone {
				// download 行专用判定：目标侧字节=引擎验过的字节（实测 sha256）。
				obs := snapshotObs(snaps[fp.targetSide], fp.op.ResourceID)
				if obs == nil || obs.Representation.Content == nil ||
					obs.Representation.Content.Digest != fp.afterDigest {
					violations = append(violations,
						fmt.Sprintf("download %s: 复扫目标侧 digest 与下载成品不一致", fp.op.ResourceID))
				}
				continue
			}
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
		case model.ChangeDelete:
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
	for _, s := range skips {
		exempt[s.fp.op.ResourceID] = true // 剔出本场的取数失败资源：不 violation，计入剩余
	}
	// download 行目标已由专用判定证明：其资源在本场已达成，剩余差异不再累计
	//（声明面与实测面的格式差异会让 diff 给出非 clean 分类，属预期形态）。
	dlDone := map[model.ResourceID]bool{}
	for _, fp := range plans {
		if fp.materialization == model.MaterializationDownload && fp.dlDone {
			dlDone[fp.op.ResourceID] = true
		}
	}
	clean := map[diff.Classification]bool{
		diff.ClassNoop: true, diff.ClassConverged: true, diff.ClassAdoptEqual: true,
	}
	counted := map[model.ResourceID]bool{}
	for _, d := range res.Diffs {
		if clean[d.Classification] || dlDone[d.ResourceID] || ignored(d.ResourceID) {
			continue
		}
		counted[d.ResourceID] = true
		remaining++
		if !opRes[d.ResourceID] && !exempt[d.ResourceID] {
			violations = append(violations,
				fmt.Sprintf("unselected %s: 复扫分类 %s（基线%v）", d.ResourceID, d.Classification, base != nil))
		}
	}
	// 剔出本场的取数失败资源恒计入剩余差异（ADR-0008 §7「成功 N + 跳过 M」，
	// 票 #63：partial 不谎报 exact）——源侧失效场景复扫 diff 可能已无该资源条目
	//（双侧缺席 = 分类消失），逐项补计防漏。
	for _, s := range skips {
		if !counted[s.fp.op.ResourceID] && !dlDone[s.fp.op.ResourceID] {
			remaining++
		}
	}
	return violations, remaining, nil
}
