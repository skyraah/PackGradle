package sqlite

// gc_repo.go 是 ports.GCRepository 的 SQLite 实现（票 #64，ADR-0007 执行要点）：
// 修剪决策输入读取、级联删除（先提交后基线的 FK 顺序 + 事务内先重连）、
// 对象账目与隔离态操作。零新表零迁移：隔离态即回收账目，trash 文件名即
// digest 映射（ADR-0007 后果）。

import (
	"context"
	"database/sql"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// GCRepository 是 ports.GCRepository 的实现，共享 *sql.DB。
type GCRepository struct {
	db DBTX
}

var _ ports.GCRepository = (*GCRepository)(nil)

// NewGCRepository 创建共享 *sql.DB 的 GC 仓库。
func NewGCRepository(db *sql.DB) *GCRepository {
	return &GCRepository{db: db}
}

// RelationCommitsChain 返回关系全部存活提交（id 升序 = 链序 oldest-first）。
func (r *GCRepository) RelationCommitsChain(ctx context.Context, relationID string) ([]model.SyncCommit, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+commitColumns+" FROM sync_commits WHERE relation_id=? ORDER BY id ASC", relationID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读关系 %s 提交链: %w", relationID, err)
	}
	defer rows.Close()
	var out []model.SyncCommit
	for rows.Next() {
		c, err := scanCommit(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 读关系 %s 提交链: %w", relationID, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RelationObjectRefs 返回关系全部存活提交的 object_refs 行（联 objects 取 size）。
func (r *GCRepository) RelationObjectRefs(ctx context.Context, relationID string) ([]ports.GCObjectRef, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT rf.owner_id, rf.digest, COALESCE(o.size, rf.size)
FROM object_refs rf
JOIN sync_commits c ON c.id = rf.owner_id AND rf.owner_type='commit'
LEFT JOIN objects o ON o.algorithm = rf.algorithm AND o.digest = rf.digest
WHERE c.relation_id=?`, relationID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读关系 %s 对象引用: %w", relationID, err)
	}
	defer rows.Close()
	var out []ports.GCObjectRef
	for rows.Next() {
		var ref ports.GCObjectRef
		if err := rows.Scan(&ref.OwnerID, &ref.Digest, &ref.Size); err != nil {
			return nil, fmt.Errorf("sqlite: 读关系 %s 对象引用: %w", relationID, err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// RelationUsageBytes 关系占用（ADR-0007 §2 口径）：存活提交引用对象去重 digest
// 的 SUM(size)。
func (r *GCRepository) RelationUsageBytes(ctx context.Context, relationID string) (int64, error) {
	var total sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
SELECT SUM(o.size) FROM (
	SELECT DISTINCT rf.algorithm, rf.digest
	FROM object_refs rf
	JOIN sync_commits c ON c.id = rf.owner_id AND rf.owner_type='commit'
	WHERE c.relation_id=?
) d JOIN objects o ON o.algorithm = d.algorithm AND o.digest = d.digest`, relationID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sqlite: 统计关系 %s 占用: %w", relationID, err)
	}
	return total.Int64, nil
}

// ProtectedBaselineIDs 屏障基线：头基线 ∪ 活跃计划的 base 基线。「活跃」口径
//（ADR-0007 §4 计划引用通道 + 单活跃计划规则）：每 relation 只取**最新**一条
// draft/resolved 计划——prepare/resolve 每轮落新行且 status 列不随 apply 推进
//（applied/expired 是读时投影），历史计划行已被后续 prepare 自然废弃，若全部
// 入屏障则所有基线被钉死、数量/容量锚点永久失效；最新计划再排除已被 committed
// 运行消费的（确认应用后未再 prepare 的旧计划同样不屏障）。
func (r *GCRepository) ProtectedBaselineIDs(ctx context.Context, relationID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT head_baseline_id FROM relations WHERE id=? AND head_baseline_id IS NOT NULL
UNION
SELECT base_baseline_id FROM sync_plans
WHERE relation_id=? AND base_baseline_id IS NOT NULL AND status IN ('draft','resolved')
  AND id = (
    SELECT MAX(id) FROM sync_plans
    WHERE relation_id=? AND status IN ('draft','resolved')
      AND NOT EXISTS(SELECT 1 FROM apply_runs r WHERE r.plan_id = sync_plans.id AND r.state='committed')
  )
  AND NOT EXISTS(SELECT 1 FROM apply_runs r WHERE r.plan_id = sync_plans.id AND r.state='committed')`,
		relationID, relationID, relationID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读关系 %s 屏障基线: %w", relationID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlite: 读关系 %s 屏障基线: %w", relationID, err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ApplyPrune 单事务级联删除（ports.GCRepository.ApplyPrune 的执行细节）。
func (r *GCRepository) ApplyPrune(ctx context.Context, relationID string, prunedCommits, droppedBaselines []string,
	reconnectCommitID, reconnectBaselineID string) error {

	if len(prunedCommits) == 0 {
		return nil
	}
	return beginOrJoin(ctx, r.db, "GC 级联修剪", func(tx DBTX) error {
		// ---- 第一步：解除全部指向被裁行的引用（SQLite 立即外键）----
		// 首个存活提交：parent_id（指向末位被裁提交）与 previous_baseline_id
		//（指向末位被裁基线）置空——ADR-0007 §1「元数据重连，内容不改」。
		if reconnectCommitID != "" {
			if _, err := tx.ExecContext(ctx,
				"UPDATE sync_commits SET parent_id=NULL, previous_baseline_id=NULL WHERE id=?",
				reconnectCommitID); err != nil {
				return fmt.Errorf("sqlite: 重连提交 %s: %w", reconnectCommitID, err)
			}
		}
		// 首个存活基线：parent_id 指向末位被裁基线，同批重连。
		if reconnectBaselineID != "" {
			if _, err := tx.ExecContext(ctx,
				"UPDATE sync_baselines SET parent_id=NULL WHERE id=?", reconnectBaselineID); err != nil {
				return fmt.Errorf("sqlite: 重连基线 %s: %w", reconnectBaselineID, err)
			}
		}
		// 被裁提交/基线自身的 parent 引用（被裁集合内部成链，删行前统一置空）。
		if _, err := tx.ExecContext(ctx,
			"UPDATE sync_commits SET parent_id=NULL WHERE relation_id=? AND id IN "+inList(len(prunedCommits)),
			argsWith(relationID, prunedCommits)...); err != nil {
			return fmt.Errorf("sqlite: 断开被裁提交链: %w", err)
		}
		if len(droppedBaselines) > 0 {
			if _, err := tx.ExecContext(ctx,
				"UPDATE sync_baselines SET parent_id=NULL WHERE id IN "+inList(len(droppedBaselines)),
				anySlice(droppedBaselines)...); err != nil {
				return fmt.Errorf("sqlite: 断开被裁基线链: %w", err)
			}
			// 失效计划（非 draft/resolved）的 base 引用：历史行的元数据投影，
			// 活跃计划已被屏障保护不落此处。
			if _, err := tx.ExecContext(ctx,
				"UPDATE sync_plans SET base_baseline_id=NULL WHERE base_baseline_id IN "+inList(len(droppedBaselines)),
				anySlice(droppedBaselines)...); err != nil {
				return fmt.Errorf("sqlite: 解除计划基线引用: %w", err)
			}
		}
		// 被裁提交对应的运行头 commit 引用（apply_runs.commit_id 外键）。
		if _, err := tx.ExecContext(ctx,
			"UPDATE apply_runs SET commit_id=NULL WHERE commit_id IN "+inList(len(prunedCommits)),
			anySlice(prunedCommits)...); err != nil {
			return fmt.Errorf("sqlite: 解除运行提交引用: %w", err)
		}
		// 被裁提交对应的任务头 commit 引用（tasks.commit_id 外键，schema v2 起
		// REFERENCES sync_commits——置空是删提交行的 FK 前提；墓碑计数改由
		// PrunedBeforeCount 的「committed 运行数−现存提交数」读时推导承担）。
		if _, err := tx.ExecContext(ctx,
			"UPDATE tasks SET commit_id=NULL WHERE commit_id IN "+inList(len(prunedCommits)),
			anySlice(prunedCommits)...); err != nil {
			return fmt.Errorf("sqlite: 解除任务提交引用: %w", err)
		}

		// ---- 第二步：先提交后基线（ADR-0007 执行要点）----
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM commit_changes WHERE commit_id IN "+inList(len(prunedCommits)),
			anySlice(prunedCommits)...); err != nil {
			return fmt.Errorf("sqlite: 删提交变化行: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM object_refs WHERE owner_type='commit' AND owner_id IN "+inList(len(prunedCommits)),
			anySlice(prunedCommits)...); err != nil {
			return fmt.Errorf("sqlite: 删对象引用行: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM sync_commits WHERE relation_id=? AND id IN "+inList(len(prunedCommits)),
			argsWith(relationID, prunedCommits)...); err != nil {
			return fmt.Errorf("sqlite: 删提交行: %w", err)
		}
		if len(droppedBaselines) > 0 {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM baseline_resources WHERE baseline_id IN "+inList(len(droppedBaselines)),
				anySlice(droppedBaselines)...); err != nil {
				return fmt.Errorf("sqlite: 删基线资源行: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM sync_baselines WHERE id IN "+inList(len(droppedBaselines)),
				anySlice(droppedBaselines)...); err != nil {
				return fmt.Errorf("sqlite: 删基线行: %w", err)
			}
		}
		return nil
	})
}

// PrunedBeforeCount 墓碑计数（读时推导，零新表零迁移）：关系下 committed
// 运行数 − 现存提交数。每个 committed 运行恰产生一个提交（AttachCommit 1:1
// 回填，失败/recovery 运行不产生提交），ApplyPrune 删被裁提交行时同步置空
// tasks/apply_runs 的 commit_id（FK 前提），两侧行均永不删除——差值即按保留
// 策略已清理的提交数。
func (r *GCRepository) PrunedBeforeCount(ctx context.Context, relationID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM apply_runs WHERE relation_id=? AND state='committed')
     - (SELECT COUNT(*) FROM sync_commits WHERE relation_id=?)`, relationID, relationID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlite: 统计关系 %s 墓碑计数: %w", relationID, err)
	}
	return n, nil
}

// ReadyDigests 全部 ready 对象 digest。
func (r *GCRepository) ReadyDigests(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT digest FROM objects WHERE state='ready' ORDER BY digest ASC")
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列 ready 对象: %w", err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// BaselineDigestHits 保护根集 1 的基线通道：存活基线的 logical_digest 命中
// objects 表的部分（ADR-0007 §4）。
func (r *GCRepository) BaselineDigestHits(ctx context.Context, relationIDs []string) ([]string, error) {
	if len(relationIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT br.logical_digest
FROM baseline_resources br
JOIN sync_baselines b ON b.id = br.baseline_id
JOIN objects o ON o.digest = br.logical_digest
WHERE b.relation_id IN `+inList(len(relationIDs)), anySlice(relationIDs)...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 查基线 digest 命中: %w", err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// PlanBaseDigestHits 活跃计划 base 基线的资源对象（ADR-0007 §4 计划引用通道
// 的对象面；活跃口径同 ProtectedBaselineIDs 的单活跃推导）。
func (r *GCRepository) PlanBaseDigestHits(ctx context.Context, relationIDs []string) ([]string, error) {
	if len(relationIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT br.logical_digest
FROM sync_plans p
JOIN baseline_resources br ON br.baseline_id = p.base_baseline_id
JOIN objects o ON o.algorithm = 'sha256' AND o.digest = br.logical_digest
WHERE p.relation_id IN `+inList(len(relationIDs))+`
  AND p.base_baseline_id IS NOT NULL AND p.status IN ('draft','resolved')
  AND p.id = (
    SELECT MAX(id) FROM sync_plans
    WHERE relation_id = p.relation_id AND status IN ('draft','resolved')
      AND NOT EXISTS(SELECT 1 FROM apply_runs r WHERE r.plan_id = sync_plans.id AND r.state='committed')
  )
  AND NOT EXISTS(SELECT 1 FROM apply_runs r WHERE r.plan_id = p.id AND r.state='committed')`,
		anySlice(relationIDs)...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 查计划基线对象命中: %w", err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// UnresolvedRunRefs 活跃/未处置运行的恢复引用原文（Go 侧解析 kind=cas 条目；
// 口径同 HasUnresolvedRuns——已人工确认的 recovery_required 不再保护）。
func (r *GCRepository) UnresolvedRunRefs(ctx context.Context) ([][]byte, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT recovery_refs_json FROM apply_runs
WHERE (state IN ('prepared','staged','applying','verifying')
       OR (state='recovery_required' AND acknowledged_at IS NULL))
  AND recovery_refs_json IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读未收口运行恢复引用: %w", err)
	}
	defer rows.Close()
	return scanBlobs(rows)
}

// JournalCASRefs 运行日志恢复引用原文（未收口运行的 journal 行）。
func (r *GCRepository) JournalCASRefs(ctx context.Context) ([][]byte, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT j.recovery_ref_json FROM operation_journal j
JOIN apply_runs ru ON ru.task_id = j.task_id
WHERE (ru.state IN ('prepared','staged','applying','verifying')
       OR (ru.state='recovery_required' AND ru.acknowledged_at IS NULL))
  AND j.recovery_ref_json IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读未收口运行日志恢复引用: %w", err)
	}
	defer rows.Close()
	return scanBlobs(rows)
}

// QuarantineObjects 单事务 ready→quarantined（WHERE state='ready' 保可重入）。
func (r *GCRepository) QuarantineObjects(ctx context.Context, digests []string) (int64, error) {
	var total int64
	err := beginOrJoin(ctx, r.db, "GC 隔离候选对象", func(tx DBTX) error {
		for _, d := range digests {
			res, err := tx.ExecContext(ctx,
				"UPDATE objects SET state='quarantined' WHERE digest=? AND algorithm='sha256' AND state='ready'", d)
			if err != nil {
				return fmt.Errorf("sqlite: 隔离对象 %s: %w", d, err)
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

// ListQuarantined 全部隔离对象（回收站搬运与对账的账目侧）。
func (r *GCRepository) ListQuarantined(ctx context.Context) ([]ports.GCObjectRef, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT digest, size FROM objects WHERE state='quarantined' ORDER BY digest ASC")
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列隔离对象: %w", err)
	}
	defer rows.Close()
	var out []ports.GCObjectRef
	for rows.Next() {
		var ref ports.GCObjectRef
		if err := rows.Scan(&ref.Digest, &ref.Size); err != nil {
			return nil, fmt.Errorf("sqlite: 列隔离对象: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// RestoreObject 人工复活：quarantined→ready。幂等：行已 ready 直接成功
//（重复执行复活 CLI 不报错）；无行返回 ErrNotFound。
func (r *GCRepository) RestoreObject(ctx context.Context, digest string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE objects SET state='ready' WHERE digest=? AND algorithm='sha256' AND state='quarantined'", digest)
	if err != nil {
		return fmt.Errorf("sqlite: 复活对象 %s: %w", digest, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		state, err := r.ObjectState(ctx, digest)
		if err != nil {
			return err
		}
		if state == "ready" {
			return nil // 已复活，幂等
		}
		return fmt.Errorf("sqlite: 对象 %s 不在隔离态: %w", digest, ErrNotFound)
	}
	return nil
}

// PurgeQuarantinedRows 物理删除隔离/孤儿行（仅零引用行；外键兜底）。
func (r *GCRepository) PurgeQuarantinedRows(ctx context.Context, digests []string) error {
	if len(digests) == 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx,
		"DELETE FROM objects WHERE state IN ('quarantined','ready') AND NOT EXISTS("+
			"SELECT 1 FROM object_refs rf WHERE rf.algorithm=objects.algorithm AND rf.digest=objects.digest)"+
			" AND digest IN "+inList(len(digests)), anySlice(digests)...)
	if err != nil {
		return fmt.Errorf("sqlite: 清除对象行: %w", err)
	}
	return nil
}

// ObjectState 单对象状态（"" = 无行）。
func (r *GCRepository) ObjectState(ctx context.Context, digest string) (string, error) {
	var state string
	err := r.db.QueryRowContext(ctx,
		"SELECT state FROM objects WHERE digest=? AND algorithm='sha256'", digest).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: 查对象 %s 状态: %w", digest, err)
	}
	return state, nil
}

// ReferencedMissingRows 返回给定 digest 中仍被 object_refs 引用的部分
//（row-without-file 且被引用：保留行，Has() 已按文件缺失返回不可见）。
func (r *GCRepository) ReferencedMissingRows(ctx context.Context, digests []string) ([]string, error) {
	if len(digests) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT rf.digest FROM object_refs rf WHERE rf.digest IN `+inList(len(digests)),
		anySlice(digests)...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 查悬挂引用行: %w", err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// HasUnresolvedRuns 安全窗口构成项（ADR-0007 §3）：存在活跃运行或未处置的
// recovery_required 运行——prepared/staged/applying/verifying 是活跃；已人工
// 确认（acknowledged_at 落库、关系复位 healthy）的 recovery_required 运行不再
// 未处置；committed/failed 是收口终局（票 #57 的 failed=staging 期失败不进
// 恢复面）。无行即窗口开。
func (r *GCRepository) HasUnresolvedRuns(ctx context.Context) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM apply_runs
WHERE state IN ('prepared','staged','applying','verifying')
   OR (state='recovery_required' AND acknowledged_at IS NULL)`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("sqlite: 统计未收口运行: %w", err)
	}
	return n > 0, nil
}

// SnapshotRefFacts 采集孤儿快照判定的引用图事实（ADR-0011 §4，票 #89）：
// 全部快照、存活提交验证快照、现存计划输入快照、各 relation 每端最新一份。
// 「最新」序与 SnapshotRepository.LatestByRelationSide 同款（captured_at
// DESC, id DESC 取 1），此处以反证式子查询逐行判定（不存在更新者即最新）。
func (r *GCRepository) SnapshotRefFacts(ctx context.Context) (ports.SnapshotGCFacts, error) {
	facts := ports.SnapshotGCFacts{}
	collect := func(dest *[]string, query string, what string) error {
		rows, err := r.db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("sqlite: 采集%s: %w", what, err)
		}
		defer rows.Close()
		*dest, err = scanStrings(rows)
		if err != nil {
			return fmt.Errorf("sqlite: 采集%s: %w", what, err)
		}
		return nil
	}
	if err := collect(&facts.All, "SELECT id FROM observed_snapshots", "快照全集"); err != nil {
		return facts, err
	}
	if err := collect(&facts.CommitVerified, `
SELECT verified_project_snapshot_id FROM sync_commits
UNION
SELECT verified_runtime_snapshot_id FROM sync_commits`, "提交验证快照"); err != nil {
		return facts, err
	}
	if err := collect(&facts.PlanInput, `
SELECT input_project_snapshot_id FROM sync_plans
UNION
SELECT input_runtime_snapshot_id FROM sync_plans`, "计划输入快照"); err != nil {
		return facts, err
	}
	if err := collect(&facts.Latest, `
SELECT s.id FROM observed_snapshots s WHERE NOT EXISTS(
	SELECT 1 FROM observed_snapshots newer
	WHERE newer.relation_id = s.relation_id AND newer.side = s.side
	  AND (newer.captured_at > s.captured_at
	       OR (newer.captured_at = s.captured_at AND newer.id > s.id)))`, "各端最新快照"); err != nil {
		return facts, err
	}
	return facts, nil
}

// DeleteSnapshots 单事务物理删除快照行并随行级联删资源表示行（ADR-0011 §4：
// resource_representations PK 前缀即 snapshot_id；先子后父满足立即外键）。
func (r *GCRepository) DeleteSnapshots(ctx context.Context, snapshotIDs []string) error {
	if len(snapshotIDs) == 0 {
		return nil
	}
	return beginOrJoin(ctx, r.db, "清扫孤儿快照", func(tx DBTX) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM resource_representations WHERE snapshot_id IN "+inList(len(snapshotIDs)),
			anySlice(snapshotIDs)...); err != nil {
			return fmt.Errorf("sqlite: 删快照资源表示行: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM observed_snapshots WHERE id IN "+inList(len(snapshotIDs)),
			anySlice(snapshotIDs)...); err != nil {
			return fmt.Errorf("sqlite: 删快照行: %w", err)
		}
		return nil
	})
}

// ---- SQL 拼接小工具（占位符个数有限、参数全走 ?，无注入面）----

func inList(n int) string {
	s := "("
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += "?"
	}
	return s + ")"
}

func anySlice(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func argsWith(first string, rest []string) []any {
	return append([]any{first}, anySlice(rest)...)
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanBlobs(rows *sql.Rows) ([][]byte, error) {
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
