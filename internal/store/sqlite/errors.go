package sqlite

import (
	"strings"

	"packgradle/internal/application/ports"
)

// 哨兵错误：与 ports 包共享同一错误值（application 用 errors.Is 跨接口识别），
// 不解析底层 SQLite 错误对象（modernc 驱动只在 open.go 引用）。
var (
	// ErrNotFound 查询目标不存在。
	ErrNotFound = ports.ErrNotFound
	// ErrDuplicate 违反唯一约束（重复创建）。
	ErrDuplicate = ports.ErrDuplicate
	// ErrSequenceConflict 任务 Sequence 乐观锁冲突（旧快照试图覆盖新状态）。
	ErrSequenceConflict = ports.ErrSequenceConflict
	// ErrPreparationExpired 预检已过期（引导重新预检）。
	ErrPreparationExpired = ports.ErrPreparationExpired
	// ErrPreparationConsumed 预检已被消费（引导刷新；ADR-0003 决议 4 拆码）。
	ErrPreparationConsumed = ports.ErrPreparationConsumed
	// ErrRelationNotFound 被引用的 Relation 不存在（FK 语义转换）。
	ErrRelationNotFound = ports.ErrRelationNotFound
	// ErrCrossRelation 被引用对象属于另一 Relation（完整性守卫拒绝）。
	ErrCrossRelation = ports.ErrCrossRelation
	// ErrSideMismatch 快照 side 与引用语义不符（完整性守卫拒绝）。
	ErrSideMismatch = ports.ErrSideMismatch
	// ErrDigestMismatch 持久化 digest 与重算值不一致（完整性守卫拒绝）。
	ErrDigestMismatch = ports.ErrDigestMismatch
	// ErrParentMismatch parent 对象属于另一 Relation（完整性守卫拒绝；
	// 与 ErrCrossRelation 区分以定位装配错误发生在 parent 位）。
	ErrParentMismatch = ports.ErrParentMismatch
	// ErrPlanNotFound 被引用的计划不存在（任务 plan_id 外键语义转换）。
	ErrPlanNotFound = ports.ErrPlanNotFound
	// ErrInvalidTransition 状态机不允许该迁移（apply_runs 六阶段 /
	// operation_journal 六状态单调路径，ADR-0004 §2/§5）。
	ErrInvalidTransition = ports.ErrInvalidTransition
	// ErrConfirmationConsumed 确认令牌已被消费（ConfirmPlan 幂等重入口径由 application 决定）。
	ErrConfirmationConsumed = ports.ErrConfirmationConsumed
	// ErrConfirmationExpired 确认令牌已过期（引导重新 Resolve 计划）。
	ErrConfirmationExpired = ports.ErrConfirmationExpired
)

// isUniqueViolation 判断是否 SQLite 唯一约束冲突。
// modernc 错误文本形如 "constraint failed: UNIQUE constraint failed: tbl.col (2067)"。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// isForeignKeyViolation 判断是否 SQLite 外键约束冲突。
// modernc 错误文本形如 "constraint failed: FOREIGN KEY constraint failed (787)"。
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
