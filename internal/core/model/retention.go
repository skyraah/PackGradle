package model

// 保留策略设置（ADR-0007 §2/§7/§8；契约 06 §3.6，票 #57）：
// config.toml [retention] 承载，不入 SQLite。默认值与可调范围是域事实，
// 加载层（appconfig 读取归一）与写入层（UpdateRetentionSettings）共用
// ValidateRetention 同款校验——单键越界整体拒绝，{0}=字段名。

// RetentionSettings 是保留策略设置值（五键；K=3 硬保底固定不可调，不设键）。
type RetentionSettings struct {
	KeepCommits           int   `json:"keep_commits"`            // 默认 20，范围 5–200
	KeepDays              int   `json:"keep_days"`               // 默认 90，范围 7–365
	RelationCapacityBytes int64 `json:"relation_capacity_bytes"` // 默认 1 GiB，范围 128 MiB–20 GiB
	PreserveMaxBytes      int64 `json:"preserve_max_bytes"`      // 默认 32 MiB，范围 1 MiB–512 MiB；0＝不限
	TrashDays             int   `json:"trash_days"`              // 默认 7，范围 1–90（范围本契约定，ADR 未锁）
}

// 保留设置边界（ADR-0007 §2 表 + 契约 06 §3.6）。
const (
	KeepCommitsMin     = 5
	KeepCommitsMax     = 200
	KeepCommitsDefault = 20

	KeepDaysMin     = 7
	KeepDaysMax     = 365
	KeepDaysDefault = 90

	RelationCapacityMin     int64 = 128 << 20 // 128 MiB
	RelationCapacityMax     int64 = 20 << 30  // 20 GiB
	RelationCapacityDefault int64 = 1 << 30   // 1 GiB

	PreserveMaxMin     int64 = 1 << 20  // 1 MiB
	PreserveMaxMax     int64 = 512 << 20 // 512 MiB
	PreserveMaxDefault int64 = 32 << 20 // 32 MiB；显式 0＝不限（合法）
	// PreserveMaxUnlimited 是「不限」哨兵值（大文件保全跳过，ADR-0007 §7）。
	PreserveMaxUnlimited int64 = 0

	TrashDaysMin     = 1
	TrashDaysMax     = 90
	TrashDaysDefault = 7
)

// DefaultRetention 返回五键默认值（config 无 [retention] 段或键未写时生效）。
func DefaultRetention() RetentionSettings {
	return RetentionSettings{
		KeepCommits:           KeepCommitsDefault,
		KeepDays:              KeepDaysDefault,
		RelationCapacityBytes: RelationCapacityDefault,
		PreserveMaxBytes:      PreserveMaxDefault,
		TrashDays:             TrashDaysDefault,
	}
}

// ValidateRetention 校验五键范围。合法返回空串；越界返回第一个违规字段名
// （契约 06 §3.6：整体拒绝，err.settings.retention_invalid 的 {0}=字段名）。
// preserve_max_bytes=0（不限）合法。
func ValidateRetention(r RetentionSettings) (field string, ok bool) {
	switch {
	case r.KeepCommits < KeepCommitsMin || r.KeepCommits > KeepCommitsMax:
		return "keep_commits", false
	case r.KeepDays < KeepDaysMin || r.KeepDays > KeepDaysMax:
		return "keep_days", false
	case r.RelationCapacityBytes < RelationCapacityMin || r.RelationCapacityBytes > RelationCapacityMax:
		return "relation_capacity_bytes", false
	case r.PreserveMaxBytes != PreserveMaxUnlimited &&
		(r.PreserveMaxBytes < PreserveMaxMin || r.PreserveMaxBytes > PreserveMaxMax):
		return "preserve_max_bytes", false
	case r.TrashDays < TrashDaysMin || r.TrashDays > TrashDaysMax:
		return "trash_days", false
	default:
		return "", true
	}
}

// ShouldSkipPreserve 报告单文件是否超过大文件保全阈值（ADR-0007 §7，票 #64）：
// 非 mod 资源且旧内容字节数 > preserveMaxBytes（显式 0＝不限）→ 不做 before
// 保全（照常同步写，旧版本不留 CAS；回滚零新增枚举，对象缺失走 ADR-0006 §2
// 既有降级分支 user_object_required）。
//
// 这是两侧计划行 preserve_skip 判定的唯一口径：sync 侧计划构建（core/plan）
// 与 restore 侧计划构建（票 #60 消费）都调用本导出函数；执行引擎同样以计划行
// 的固化标记为准（计划即契约）。
func ShouldSkipPreserve(kind ResourceKind, sizeBytes, preserveMaxBytes int64) bool {
	if preserveMaxBytes == PreserveMaxUnlimited {
		return false
	}
	if kind == ResourceMod {
		return false // mod 走重取通道，本就不做 before 保全（架构 §8.2）
	}
	return sizeBytes > preserveMaxBytes
}
