package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// ApplyRunRepository 是 ports.ApplyRunRepository 的 SQLite 实现
// （apply_runs 表，schema v5；DDL 照 ADR-0004 §1 原文）。
type ApplyRunRepository struct {
	db DBTX
}

var _ ports.ApplyRunRepository = (*ApplyRunRepository)(nil)

// NewApplyRunRepository 创建共享 *sql.DB 的运行头仓库。
func NewApplyRunRepository(db *sql.DB) *ApplyRunRepository {
	return &ApplyRunRepository{db: db}
}

// rawJSONLiteral 归一化 RawMessage 列：空值落 fallback 字面量（"[]"/"{}"，
// slice 归一先例），非空原样保存（引擎定义形状，仓储层不改写）。
func rawJSONLiteral(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}

// nullableRaw 把可空 RawMessage 映射为 SQL NULL / JSON 文本。
func nullableRaw(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// marshalPreconditions 序列化前置条件集合；nil/空归一为 "[]"。
func marshalPreconditions(pre []model.Precondition) (string, error) {
	if len(pre) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(pre)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// applyRunColumns 是 apply_runs 表读取列清单（与 scanApplyRun 对应）。
const applyRunColumns = `task_id, relation_id, plan_id, plan_digest, relation_revision, state,
	preconditions_json, recovery_refs_json, operation_count, staging_cleared,
	acknowledged_at, commit_id, created_at, updated_at`

// scanApplyRun 把一行 apply_runs 扫描为 model.ApplyRun。
func scanApplyRun(scan func(...any) error) (model.ApplyRun, error) {
	var (
		run                      model.ApplyRun
		preJSON, refsJSON        string
		stagingCleared           int
		acknowledgedAt, commitID sql.NullString
	)
	if err := scan(&run.TaskID, &run.RelationID, &run.PlanID, &run.PlanDigest, &run.RelationRevision,
		&run.State, &preJSON, &refsJSON, &run.OperationCount, &stagingCleared,
		&acknowledgedAt, &commitID, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return model.ApplyRun{}, err
	}
	run.StagingCleared = stagingCleared != 0
	run.AcknowledgedAt = acknowledgedAt.String
	run.CommitID = commitID.String
	if preJSON != "" && preJSON != "[]" && preJSON != "null" {
		if err := json.Unmarshal([]byte(preJSON), &run.Preconditions); err != nil {
			return model.ApplyRun{}, fmt.Errorf("sqlite: 解析运行 %s 前置条件: %w", run.TaskID, err)
		}
	}
	if refsJSON != "" && refsJSON != "[]" && refsJSON != "null" {
		run.RecoveryRefs = json.RawMessage(refsJSON)
	}
	return run, nil
}

// applyRunReferenceSentinel 把运行写入的外键违例翻译为可区分的哨兵错误：
// 按 relation → plan → task → commit 的顺序定位坏引用（与 taskReferenceSentinel 同款）。
func applyRunReferenceSentinel(ctx context.Context, q DBTX, run model.ApplyRun) error {
	if !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM relations WHERE id=?)", run.RelationID) {
		return ErrRelationNotFound
	}
	if !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM sync_plans WHERE id=?)", run.PlanID) {
		return ErrPlanNotFound
	}
	if !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM tasks WHERE id=?)", run.TaskID) {
		return ErrNotFound
	}
	if run.CommitID != "" && !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM sync_commits WHERE id=?)", run.CommitID) {
		return ErrNotFound
	}
	return ErrRelationNotFound
}

// Insert 写入一次 Apply 运行头（初始 state 由调用方给，ConfirmPlan 路径为 prepared）。
// 悬挂引用按哨兵拆码；重复 task_id（一任务一运行）返回唯一约束错误。
func (r *ApplyRunRepository) Insert(ctx context.Context, run model.ApplyRun) error {
	preJSON, err := marshalPreconditions(run.Preconditions)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化运行 %s 前置条件: %w", run.TaskID, err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest, relation_revision,
	state, preconditions_json, recovery_refs_json, operation_count,
	staging_cleared, acknowledged_at, commit_id, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.TaskID, run.RelationID, run.PlanID, run.PlanDigest, run.RelationRevision,
		run.State, preJSON, rawJSONLiteral(run.RecoveryRefs, "[]"), run.OperationCount,
		boolToInt(run.StagingCleared), nullString(run.AcknowledgedAt), nullString(run.CommitID),
		run.CreatedAt, run.UpdatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("sqlite: 写入运行 %s: %w", run.TaskID, applyRunReferenceSentinel(ctx, r.db, run))
		}
		return fmt.Errorf("sqlite: 写入运行 %s: %w", run.TaskID, err)
	}
	return nil
}

// Get 按 task_id（run_id）读取运行头；不存在返回 ErrNotFound。
func (r *ApplyRunRepository) Get(ctx context.Context, taskID string) (model.ApplyRun, error) {
	run, err := scanApplyRun(func(dest ...any) error {
		return r.db.QueryRowContext(ctx,
			"SELECT "+applyRunColumns+" FROM apply_runs WHERE task_id=?", taskID).Scan(dest...)
	})
	if err == sql.ErrNoRows {
		return model.ApplyRun{}, fmt.Errorf("sqlite: 读取运行 %s: %w", taskID, ErrNotFound)
	}
	if err != nil {
		return model.ApplyRun{}, fmt.Errorf("sqlite: 读取运行 %s: %w", taskID, err)
	}
	return run, nil
}

// LatestByRelation 返回该 Relation 当前/最近一次运行（created_at 最新，
// task_id 决胜）；无运行返回 ok=false。
func (r *ApplyRunRepository) LatestByRelation(ctx context.Context, relationID string) (model.ApplyRun, bool, error) {
	run, err := scanApplyRun(func(dest ...any) error {
		return r.db.QueryRowContext(ctx,
			"SELECT "+applyRunColumns+" FROM apply_runs WHERE relation_id=?"+
				" ORDER BY created_at DESC, task_id DESC LIMIT 1", relationID).Scan(dest...)
	})
	if err == sql.ErrNoRows {
		return model.ApplyRun{}, false, nil
	}
	if err != nil {
		return model.ApplyRun{}, false, fmt.Errorf("sqlite: 读取关系 %s 最新运行: %w", relationID, err)
	}
	return run, true, nil
}

// LatestByPlan 返回该计划当前/最近一次运行（created_at 最新，task_id 决胜）；
// 无运行返回 ok=false。ConfirmPlan 幂等重入三分支按「本计划的运行」判定
// （契约 05 §3.1 D4：活跃重入 / committed 拆码 / recovery 拆码；票 #36）。
func (r *ApplyRunRepository) LatestByPlan(ctx context.Context, planID string) (model.ApplyRun, bool, error) {
	run, err := scanApplyRun(func(dest ...any) error {
		return r.db.QueryRowContext(ctx,
			"SELECT "+applyRunColumns+" FROM apply_runs WHERE plan_id=?"+
				" ORDER BY created_at DESC, task_id DESC LIMIT 1", planID).Scan(dest...)
	})
	if err == sql.ErrNoRows {
		return model.ApplyRun{}, false, nil
	}
	if err != nil {
		return model.ApplyRun{}, false, fmt.Errorf("sqlite: 读取计划 %s 最新运行: %w", planID, err)
	}
	return run, true, nil
}

// AdvanceState 沿六阶段状态机推进运行阶段（ADR-0004 §5）。状态判定与写入在
// 同一事务内原子完成；终态与非法跳变返回 ErrInvalidTransition。
func (r *ApplyRunRepository) AdvanceState(ctx context.Context, taskID, state, updatedAt string) error {
	return beginOrJoin(ctx, r.db, "推进运行状态", func(tx DBTX) error {
		var current string
		err := tx.QueryRowContext(ctx, "SELECT state FROM apply_runs WHERE task_id=?", taskID).Scan(&current)
		if err == sql.ErrNoRows {
			return fmt.Errorf("sqlite: 运行 %s 不存在: %w", taskID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("sqlite: 读取运行 %s 状态: %w", taskID, err)
		}
		if !model.ApplyRunCanTransition(current, state) {
			return fmt.Errorf("sqlite: 运行 %s 不允许 %s→%s: %w", taskID, current, state, ErrInvalidTransition)
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE apply_runs SET state=?, updated_at=? WHERE task_id=?", state, updatedAt, taskID); err != nil {
			return fmt.Errorf("sqlite: 推进运行 %s 至 %s: %w", taskID, state, err)
		}
		return nil
	})
}

// SetRecoveryRefs 落运行级恢复对象引用（ADR-0004 §1/§3：引擎 staged 前收集的
// CAS/staging 引用集合，JSON 形状引擎定义、仓储原样保存；nil 归一为 "[]"）。
// 运行不存在返回 ErrNotFound。
func (r *ApplyRunRepository) SetRecoveryRefs(ctx context.Context, taskID string, refs json.RawMessage, updatedAt string) error {
	return updateApplyRun(ctx, r.db, taskID, "落恢复引用",
		"UPDATE apply_runs SET recovery_refs_json=?, updated_at=? WHERE task_id=?",
		rawJSONLiteral(refs, "[]"), updatedAt, taskID)
}

// MarkStagingCleared 将 staging_cleared 记录为事实（ADR-0004 §5：staging 仅在
// 提交事务成功后清理）。
func (r *ApplyRunRepository) MarkStagingCleared(ctx context.Context, taskID, updatedAt string) error {
	return updateApplyRun(ctx, r.db, taskID, "标记 staging 已清理",
		"UPDATE apply_runs SET staging_cleared=1, updated_at=? WHERE task_id=?", updatedAt, taskID)
}

// MarkAcknowledged 记录人工确认时间；幂等，保留首次确认时间（COALESCE）。
func (r *ApplyRunRepository) MarkAcknowledged(ctx context.Context, taskID, acknowledgedAt, updatedAt string) error {
	return updateApplyRun(ctx, r.db, taskID, "记录人工确认",
		"UPDATE apply_runs SET acknowledged_at=COALESCE(acknowledged_at, ?), updated_at=? WHERE task_id=?",
		acknowledgedAt, updatedAt, taskID)
}

// AttachCommit 在 committed 收口时回填提交引用；悬挂 commit 被外键拒绝（ErrNotFound）。
func (r *ApplyRunRepository) AttachCommit(ctx context.Context, taskID, commitID, updatedAt string) error {
	err := updateApplyRun(ctx, r.db, taskID, "回填提交引用",
		"UPDATE apply_runs SET commit_id=?, updated_at=? WHERE task_id=?", commitID, updatedAt, taskID)
	if err != nil && isForeignKeyViolation(err) {
		return fmt.Errorf("sqlite: 运行 %s 引用的提交 %s 不存在: %w", taskID, commitID, ErrNotFound)
	}
	return err
}

// updateApplyRun 执行单条运行头 UPDATE；影响 0 行返回 ErrNotFound。
func updateApplyRun(ctx context.Context, q DBTX, taskID, what, query string, args ...any) error {
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("sqlite: 运行 %s %s: %w", taskID, what, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("sqlite: 运行 %s %s: %w", taskID, what, ErrNotFound)
	}
	return nil
}
