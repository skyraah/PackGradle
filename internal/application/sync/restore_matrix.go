package sync

import (
	"packgradle/internal/core/model"
)

// 四标记判定矩阵（ADR-0006 §2；契约 06 §3.1 行为语义 2）。
//
// 判定是 prepare 时点的确定函数：目标状态 × 当前观测 × 可恢复途径 × 重取信息
// 可得性。目标/当前两维在本包 buildRestoreDraft 中折叠为「该行需要重建」前提
// （双端 digest 均等于目标 → 无操作行；目标 absent 而当前 present → 删除行，
// 不占四标记），本函数只对需要重建的行判「重建途径」，全格如下：
//
//	rec=cas（及未记录策略 none/空，沿「宁可多存」缺省）：
//	  CAS 对象实存               → restorable_from_cas
//	  CAS 对象缺失               → user_object_required + no_redownload_info
//	rec=redownload：
//	  重取信息实存 ∧ hash 可验   → redownload_required（乐观标记，探测可降标）
//	  重取信息实存 ∧ hash 不可验 → user_object_required + hash_format_unsupported
//	  重取信息缺失               → user_object_required + no_redownload_info
//	rec=unrecoverable           → unrecoverable（默认阻止 exact）
//
// 「重取性看数据不看出身」（ADR-0006 §2 实现缺口对齐）：redownload_required
// 以重取信息实际存在为前提（CF file-id+filename+可验声明 hash），kind=mod 不
// 自动等于可重取——手放 mod（无 update 段 / runtime-only jar）由此归
// user_object_required，替代旧 defaultRecoverability 的按 kind 分派口径。
// Apply 的 before 保全缺省策略（defaultRecoverability）不动：那是写入路径的
// fail-safe 保全，与回滚判定是两个语义面。

// restoreMarkerInput 是四标记判定的输入格（纯函数，全格表驱动单测覆盖）。
type restoreMarkerInput struct {
	// Rec 是目标基线记录的恢复途径（apply 时点固化）。
	Rec model.Recoverability
	// HasRedownloadInfo 报告重取信息实际存在（目标基线项目侧 metafile 元数据：
	// CF file-id + filename 非空）。
	HasRedownloadInfo bool
	// HashSupported 报告声明 hash 格式在引擎可验集内（md5/sha1/sha256/sha512；
	// murmur2 等不可验即不能安全重取，ADR-0008 不验不装）。
	HashSupported bool
	// CASReady 报告目标内容字节 CAS 实存（全部写回侧逐 digest Has 通过）。
	CASReady bool
}

// judgeRestoreMarker 返回四标记与 marker_reason（仅 user_object_required 行非空）。
func judgeRestoreMarker(in restoreMarkerInput) (model.RestoreMarker, string) {
	switch in.Rec {
	case model.RecoverabilityRedownload:
		switch {
		case !in.HasRedownloadInfo:
			return model.MarkerUserObjectRequired, model.MarkerReasonNoRedownloadInfo
		case !in.HashSupported:
			return model.MarkerUserObjectRequired, model.MarkerReasonHashFormatUnsupported
		default:
			return model.MarkerRedownloadRequired, ""
		}
	case model.RecoverabilityUnrecoverable:
		return model.MarkerUnrecoverable, ""
	default:
		// cas 与未记录策略（none/空/未知值）：CAS 写回途径，实存判定兜底
		//（restorable_from_cas 要求 CAS 对象实存，缺失 → user_object_required，
		// 凭目标 digest 验收用户提供字节，ADR-0006 §2）。
		if in.CASReady {
			return model.MarkerRestorableFromCAS, ""
		}
		return model.MarkerUserObjectRequired, model.MarkerReasonNoRedownloadInfo
	}
}

// noProjectContentDegrade 报告行是否落 ADR-0012 §4 的存量宽判降标（prepare 期
// 纯静态零探测的确定函数，全格表驱动单测覆盖）：写回侧含 project ∧ 目标基线
// 项目侧表示无实测 Content。判定与四标记矩阵正交（矩阵零新维度），在
// buildRestoreDraft 中作判定后置覆写——不区分原 marker，统一降
// user_object_required + no_project_content：错写修正（ADR-0012 §7.2）落地后，
// redownload 成因与 user_object 成因的漂移行同根同死法（项目侧无内容源，
// 确认后整场 failed），统一语义最少特判。
func noProjectContentDegrade(projectInWriteSides bool, projRep *model.Representation) bool {
	return projectInWriteSides && (projRep == nil || projRep.Content == nil)
}

// degradeNoProjectRow 对已判行执行 ADR-0012 §4 的后置覆写（纯函数）：marker
// 统一降 user_object_required ＋ marker_reason=no_project_content，重取信息与
// 验收摘要一并清空——重取信息清空使探测与执行面零残留（探测只认
// redownload_required 行）；验收摘要清空使该行在就绪公式（ADR-0006 §3.5：
// user_object ∧ staged）下永不就绪，含它的计划 exact 如实判 infeasible
//（ADR-0012 §6「修输入不修公式」的输入面），补全通道对降标行另码拒收。
func degradeNoProjectRow(item model.RestorePlanItem, projectInWriteSides bool, projRep *model.Representation) model.RestorePlanItem {
	if !noProjectContentDegrade(projectInWriteSides, projRep) {
		return item
	}
	item.Marker = model.MarkerUserObjectRequired
	item.MarkerReason = model.MarkerReasonNoProjectContent
	item.Redownload = nil
	item.ExpectedDigest = ""
	return item
}
