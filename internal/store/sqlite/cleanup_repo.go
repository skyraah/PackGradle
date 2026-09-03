package sqlite

// cleanup_repo.go 是 ports.CleanupRepository 的 SQLite 实现（票 #89，
// ADR-0011 §2/§3）：task_events 条数窗口截断 + 旧数据行物理删除。
// 判定 = 过期 ∧ 无存活引用，全部内联为 SQL 守卫（执行可重入：重复执行
// 幂等，已删行不再命中）。零新表零迁移：全部沿 v6 既有列。

import (
	"context"
	"database/sql"
	"fmt"

	"packgradle/internal/application/ports"
)

// CleanupRepository 是 ports.CleanupRepository 的实现，共享 *sql.DB。
type CleanupRepository struct {
	db DBTX
}

var _ ports.CleanupRepository = (*CleanupRepository)(nil)

// NewCleanupRepository 创建共享 *sql.DB 的惰性清理仓库。
func NewCleanupRepository(db *sql.DB) *CleanupRepository {
	return &CleanupRepository{db: db}
}

// TruncateTaskEvents 保最近 keep 条（按 stream_sequence 留尾，ADR-0011 §2）。
// 截断后 stream_sequence 从 MAX+1 续是 EventRepository.Append 的既有硬约束
//（MAX 读取），本方法不动余下行的序号；清全表（外部清空）则从 1 重来。
func (r *CleanupRepository) TruncateTaskEvents(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx, `
DELETE FROM task_events WHERE stream_sequence NOT IN (
	SELECT stream_sequence FROM task_events ORDER BY stream_sequence DESC LIMIT ?)`, keep)
	if err != nil {
		return 0, fmt.Errorf("sqlite: 截断 task_events 至 %d 条: %w", keep, err)
	}
	return res.RowsAffected()
}

// deleteExpiredPlansStmt 是过期/修订过时历史计划行的候选判定（各删除步骤
// 共用；步骤间行集随前置删除收窄，单事务内逐步推进）：
//   - 读取时投影 expired（expires_at ≤ now）或 stale（关系修订号已前进，
//     绑定指纹失配的 stale 行最迟随 planTTL 过期进入同一通道）；
//   - 计划行被任一 apply_runs 行引用即不删——apply_runs.plan_id 为 NOT NULL
//     且运行行永不删（墓碑计数基石，ADR-0011 §3），confirmed 计划随其运行
//     行结构上永久保留（schema v6 冻结零迁移的保守语义：applied 行由
//     sync_commits.plan_id 与运行行共同钉住，只可能「晚于提交存亡」，绝不
//     可能早删）；
//   - 存活子计划（resolved_from_plan_id 自外键）钉住的 draft 不删。
const deleteExpiredPlansStmt = `
SELECT p.id FROM sync_plans p
JOIN relations r ON r.id = p.relation_id
WHERE p.status IN ('draft','resolved')
  AND (p.expires_at <= ? OR p.relation_revision != r.revision)
  AND NOT EXISTS(SELECT 1 FROM sync_commits c WHERE c.plan_id = p.id)
  AND NOT EXISTS(SELECT 1 FROM apply_runs ru WHERE ru.plan_id = p.id)
  AND NOT EXISTS(SELECT 1 FROM sync_plans child WHERE child.resolved_from_plan_id = p.id)`

// DeleteExpiredPlans 单事务物理删除历史计划行（ADR-0011 §3）。执行顺序
//（SQLite 立即外键：先删子行，后删父行）：
//  1. 随行删确认令牌与冲突行（plan_confirmations/conflicts.plan_id 外键）；
//  2. 先删 resolved（子）后删 draft（父）——draft 判定随 resolved 删除
//     收窄，同轮即可放行仅被子计划钉住的 draft。
func (r *CleanupRepository) DeleteExpiredPlans(ctx context.Context, now string) (int64, error) {
	var total int64
	err := beginOrJoin(ctx, r.db, "清理历史计划行", func(tx DBTX) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM plan_confirmations WHERE plan_id IN ("+deleteExpiredPlansStmt+")", now); err != nil {
			return fmt.Errorf("sqlite: 删计划确认令牌: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM conflicts WHERE plan_id IN ("+deleteExpiredPlansStmt+")", now); err != nil {
			return fmt.Errorf("sqlite: 删计划冲突行: %w", err)
		}
		for _, status := range []string{"resolved", "draft"} {
			// 占位符按文本次序：status=? 在前、候选判定的 expires_at <= ? 在后。
			res, err := tx.ExecContext(ctx,
				"DELETE FROM sync_plans WHERE status=? AND id IN ("+deleteExpiredPlansStmt+")", status, now)
			if err != nil {
				return fmt.Errorf("sqlite: 删 %s 历史计划行: %w", status, err)
			}
			if n, err := res.RowsAffected(); err == nil {
				total += n
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// DeleteExpiredPreparations 删除过期或已消费的预检行（ADR-0011 §3：
// preparations 按 expires_at 过期/consumed 即删，表无外键被引用；
// rebind_preparations 同判定——CONTEXT.md「预检」覆盖创建与重绑两表）。
func (r *CleanupRepository) DeleteExpiredPreparations(ctx context.Context, now string) (int64, error) {
	var total int64
	err := beginOrJoin(ctx, r.db, "清理过期预检", func(tx DBTX) error {
		for _, table := range []string{"preparations", "rebind_preparations"} {
			res, err := tx.ExecContext(ctx,
				"DELETE FROM "+table+" WHERE consumed_at IS NOT NULL OR expires_at <= ?", now)
			if err != nil {
				return fmt.Errorf("sqlite: 清理 %s: %w", table, err)
			}
			if n, err := res.RowsAffected(); err == nil {
				total += n
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// terminalTaskStatuses 是终态任务状态集（queued/running 活跃行绝不触碰；
// recovery_required 按任务面口径视作终态——其运行行若未收口，apply_runs
// 守卫必然拦截）。
const terminalTaskStatuses = `('succeeded','failed','cancelled','recovery_required')`

// PruneTerminalTasks 终态任务行保最近 keep 条（ADR-0011 §3：终态 tasks 保
// 最近 200 条）。留尾序 = created_at DESC, id DESC（id 为 ULID，创建序决胜）。
// apply_runs.task_id 是运行行主键（运行行永不删——墓碑计数基石）、
// operation_journal.task_id 外键指向 append-only 日志：被引用行结构上不可删，
// 守卫跳过（apply/restore 任务行随其运行行永久保留，窗口实际收敛 scan/gc 行）。
func (r *CleanupRepository) PruneTerminalTasks(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx, `
DELETE FROM tasks WHERE status IN `+terminalTaskStatuses+`
  AND id NOT IN (
    SELECT id FROM tasks WHERE status IN `+terminalTaskStatuses+`
    ORDER BY created_at DESC, id DESC LIMIT ?)
  AND NOT EXISTS(SELECT 1 FROM apply_runs ru WHERE ru.task_id = tasks.id)
  AND NOT EXISTS(SELECT 1 FROM operation_journal j WHERE j.task_id = tasks.id)`, keep)
	if err != nil {
		return 0, fmt.Errorf("sqlite: 修剪终态任务至 %d 条: %w", keep, err)
	}
	return res.RowsAffected()
}
