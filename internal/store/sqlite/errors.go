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
	// ErrPreparationExpired 预检已被消费或已过期。
	ErrPreparationExpired = ports.ErrPreparationExpired
	// ErrRelationNotFound 被引用的 Relation 不存在（FK 语义转换）。
	ErrRelationNotFound = ports.ErrRelationNotFound
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
