package model

// 回滚计划领域类型（ADR-0006 事实模型 + 契约 06 §3.2 投影；票 #59）。
// RestorePlanItem 随 sync_plans(kind=restore) 的 plan_json 持久化，是 prepare
// 时点确定函数的固化证据；与 Diagnostics 同类——证据性数据，不参与 PlanDigest
// （normalize.PlanDigest 不读取本类型；计划的写回契约由 Operations/前置条件承载）。

// RestoreMarker 是回滚计划逐资源的四标记（ADR-0006 §2 判定矩阵）。
// delete 行不占四标记（marker 为空串，ADR-0006 §2/§5）。
type RestoreMarker string

const (
	// MarkerRestorableFromCAS 目标内容字节已作为整文件对象实存 CAS：回滚＝
	// 取字节→暂存→原子写，零网络零用户介入（非增量非差量）。
	MarkerRestorableFromCAS RestoreMarker = "restorable_from_cas"
	// MarkerRedownloadRequired 重取信息实际存在（CF file-id+filename+可验声明
	// hash）：乐观标记，Apply 物化失败＝整场失败退出（ADR-0006 §7）。
	MarkerRedownloadRequired RestoreMarker = "redownload_required"
	// MarkerUserObjectRequired 无重取信息、CF 探测不可用或 CAS 对象缺失：
	// 用户提供字节，凭目标 digest 验收入 staging 绑计划（不进 CAS）。
	MarkerUserObjectRequired RestoreMarker = "user_object_required"
	// MarkerUnrecoverable 无任何恢复途径；默认阻止 exact（ADR-0006 §2）。
	MarkerUnrecoverable RestoreMarker = "unrecoverable"
)

// marker_reason 枚举（契约 06 §3.2，仅 user_object_required 行）。
const (
	// MarkerReasonNoRedownloadInfo 无重取信息：手放 mod（重取性看数据不看出身）、
	// rec=cas 但 CAS 对象缺失等——无自动重取途径，需用户提供。
	MarkerReasonNoRedownloadInfo = "no_redownload_info"
	// MarkerReasonCFUnavailable CF 探测 404/403：prepare 时点降标（ADR-0006 §7
	// 「不可用提前降标」的投影；契约 06 §5）。
	MarkerReasonCFUnavailable = "cf_unavailable"
	// MarkerReasonHashFormatUnsupported 声明 hash 格式引擎不可验（murmur2 等，
	// ADR-0008 不验不装）：与下载引擎 hash_format_unsupported 信号同字面
	//（internal/download codeHashFormatUnsupported）。
	MarkerReasonHashFormatUnsupported = "hash_format_unsupported"
)

// 行内 CF 可用性枚举（契约 06 §5：ok|unknown；unavailable 不是行内态而是降标）。
const (
	// RestoreAvailabilityOK 探测 2xx：可获取。
	RestoreAvailabilityOK = "ok"
	// RestoreAvailabilityUnknown 超时/预算耗尽/离线：保持乐观标记不阻塞 prepare，
	// 按可重新下载执行（探测是可用性辅助非承诺）。
	RestoreAvailabilityUnknown = "unknown"
)

// 确认要求 restore_acknowledge（契约 06 §3.2/§0.4）：restore 计划恒追加，
// 保证 confirmation_requirements 非空——授权模式零特判而自然不适用回滚
//（ADR-0006 §6）。
const (
	ConfirmRestoreAcknowledge = "restore_acknowledge"
	ConfirmSeverityWarning    = "warning"
)

// RedownloadInfo 是重取信息（prepare 时点从目标基线项目侧 metafile 元数据固化；
// redownload_required 行专用，执行票 #60 经引擎消费，不透出 DTO）。
type RedownloadInfo struct {
	FileID       int64  `json:"file_id"`                  // update.curseforge.file-id
	Filename     string `json:"filename"`                 // pw.toml filename（jar 落盘名）
	HashFormat   string `json:"hash_format"`              // 声明 hash 格式（引擎可验集）
	DeclaredHash string `json:"declared_hash"`            // 声明 hex 摘要（jar 内容）
}

// RestorePlanItem 是回滚计划单资源行（契约 06 §3.2 RestorePlanItemDTO 的持久化
// 对应物；Skipped/Staged 是运行时投影不入库——skip 固化于 Resolutions，staged
// 由 plan 暂存目录实时推导）。
type RestorePlanItem struct {
	ResourceID   ResourceID    `json:"resource_id"`
	RelativePath string        `json:"relative_path"` // 展示路径（写回侧优先，project 兜底）
	ChangeKind   string        `json:"change_kind"`   // create|modify|delete（delete 行不占四标记）
	Marker       RestoreMarker `json:"marker"`        // delete 行为空串
	MarkerReason string        `json:"marker_reason,omitempty"`
	DeletionWarn bool          `json:"deletion_warn,omitempty"` // 手放 mod 删除＝「不可重取」警示（ADR-0006 §5）
	// PreserveSkip 是「旧版本不留存」警示位（ADR-0007 §7）：本票仅落字段位，
	// 判定（>32 MiB 非 mod 阈值改造）归票 #64。
	PreserveSkip   bool   `json:"preserve_skip,omitempty"`
	Availability   string `json:"availability,omitempty"`    // ok|unknown，仅 redownload_required 行
	NewerAvailable bool   `json:"newer_available,omitempty"` // 仅 ok 行：目标非最新（本地比对 file-id，零网络）
	// ExpectedDigest 是 user_object_required 行的验收入库目标摘要（sha256 hex；
	// 契约 06 §3.5 StageUserObject 凭此验收）。
	ExpectedDigest string `json:"expected_digest,omitempty"`
	// StageRel 是补全字节与 CAS 写回的暂存目标路径（写回侧目标表示的
	// root-relative 路径，prepare 时点从目标基线解析；staging 绑 plan 共用）。
	StageRel string `json:"stage_rel,omitempty"`
	// Redownload 是重取信息（redownload_required 行非 nil）。
	Redownload *RedownloadInfo `json:"redownload,omitempty"`
}
