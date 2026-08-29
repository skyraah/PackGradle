package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// repository 写入边界的完整性守卫（检视报告 P0-3，票 #12）。
//
// SQLite 是元数据唯一权威；任何跨 Relation、跨 side、错误 parent、伪造 digest
// 的对象引用写入都在此被拒绝，保证审计链不被装配错误污染。合法流程
// （plan.BuildDraft / Resolve 均以 normalize 重算 digest 并携带真实输入快照
// digest）不受影响。
//
// 两种执行模式：
//   - Plan/Baseline 写入开显式事务，守卫查询传入 *sql.Tx，与写入同事务原子；
//   - Task 写入为单语句自动提交，守卫查询传入 *sql.DB 先查后写（非原子；
//     MaxOpenConns=1 串行化了访问，且 schema v2 的外键在库层兜底悬挂引用）。

// guardQuerier 是守卫校验所需的只读查询面（*sql.Tx / *sql.DB 均满足）。
type guardQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// requireSnapshotOfRelation 校验快照存在、属于指定 Relation 且 side 相符，
// 返回库中持久化的 snapshot_digest 供 digest 链校验。
func requireSnapshotOfRelation(ctx context.Context, q guardQuerier, snapshotID, relationID string, side model.Side) (string, error) {
	var (
		gotRelation, gotSide, digest string
	)
	err := q.QueryRowContext(ctx,
		"SELECT relation_id, side, snapshot_digest FROM observed_snapshots WHERE id=?",
		snapshotID).Scan(&gotRelation, &gotSide, &digest)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("sqlite: 输入快照 %s 不存在: %w", snapshotID, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: 读取快照 %s: %w", snapshotID, err)
	}
	if gotRelation != relationID {
		return "", fmt.Errorf("sqlite: 快照 %s 属于 Relation %s，期望 %s: %w",
			snapshotID, gotRelation, relationID, ErrCrossRelation)
	}
	if gotSide != string(side) {
		return "", fmt.Errorf("sqlite: 快照 %s side=%s，期望 %s: %w",
			snapshotID, gotSide, side, ErrSideMismatch)
	}
	return digest, nil
}

// requireBaselineOfRelation 校验基线属于指定 Relation，返回库中持久化的 baseline_digest。
func requireBaselineOfRelation(ctx context.Context, q guardQuerier, baselineID, relationID string) (string, error) {
	var gotRelation, digest string
	err := q.QueryRowContext(ctx,
		"SELECT relation_id, baseline_digest FROM sync_baselines WHERE id=?",
		baselineID).Scan(&gotRelation, &digest)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("sqlite: 基线 %s 不存在: %w", baselineID, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: 读取基线 %s: %w", baselineID, err)
	}
	if gotRelation != relationID {
		return "", fmt.Errorf("sqlite: 基线 %s 属于 Relation %s，期望 %s: %w",
			baselineID, gotRelation, relationID, ErrCrossRelation)
	}
	return digest, nil
}

// rowRelation 读取指定表的 relation_id 单列；行不存在时以 notFound 哨兵包装。
func rowRelation(ctx context.Context, q guardQuerier, table, id string, notFound error) (string, error) {
	var gotRelation string
	err := q.QueryRowContext(ctx,
		"SELECT relation_id FROM "+table+" WHERE id=?", id).Scan(&gotRelation)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("sqlite: %s %s 不存在: %w", table, id, notFound)
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: 读取 %s %s: %w", table, id, err)
	}
	return gotRelation, nil
}

// requirePlanOfRelation 校验计划属于指定 Relation。
func requirePlanOfRelation(ctx context.Context, q guardQuerier, planID, relationID string) error {
	gotRelation, err := rowRelation(ctx, q, "sync_plans", planID, ErrPlanNotFound)
	if err != nil {
		return err
	}
	if gotRelation != relationID {
		return fmt.Errorf("sqlite: 计划 %s 属于 Relation %s，期望 %s: %w",
			planID, gotRelation, relationID, ErrCrossRelation)
	}
	return nil
}

// requireParentBaseline 校验基线的 parent 存在且属于同一 Relation
// （parent 跨 Relation 用专属哨兵 ErrParentMismatch，与普通引用的
// ErrCrossRelation 区分，便于上层定位装配错误的具体位置）。
func requireParentBaseline(ctx context.Context, q guardQuerier, b model.SyncBaseline) error {
	gotRelation, err := rowRelation(ctx, q, "sync_baselines", b.ParentBaselineID, ErrNotFound)
	if err != nil {
		return err
	}
	if gotRelation != b.RelationID {
		return fmt.Errorf("sqlite: parent 基线 %s 属于 Relation %s，期望 %s: %w",
			b.ParentBaselineID, gotRelation, b.RelationID, ErrParentMismatch)
	}
	return nil
}

// verifyClaimedDigest 比对声称 digest 与重算值；不一致返回 ErrDigestMismatch。
func verifyClaimedDigest(what, claimed string, recompute func() (string, error)) error {
	actual, err := recompute()
	if err != nil {
		return fmt.Errorf("sqlite: 重算%s digest: %w", what, err)
	}
	if claimed != actual {
		return fmt.Errorf("sqlite: %s digest 不一致（声称 %s，重算 %s）: %w",
			what, claimed, actual, ErrDigestMismatch)
	}
	return nil
}

// verifyPersistedDigest 比对声称 digest 与库中持久化值（digest 链校验）；
// 不一致返回 ErrDigestMismatch。
func verifyPersistedDigest(what, id, claimed, persisted string) error {
	if claimed != persisted {
		return fmt.Errorf("sqlite: %s %s digest 不一致（声称 %s，持久化 %s）: %w",
			what, id, claimed, persisted, ErrDigestMismatch)
	}
	return nil
}

// verifyPlanIntegrity 是 PlanRepository.Insert 的守卫：输入快照同 Relation 且 side
// 正确、digest 链与库中持久化值一致、base/resolved_from 引用同 Relation、plan digest 重算一致。
func verifyPlanIntegrity(ctx context.Context, q guardQuerier, p model.SyncPlan) error {
	projectDigest, err := requireSnapshotOfRelation(ctx, q, p.InputProjectSnapshotID, p.RelationID, model.SideProject)
	if err != nil {
		return err
	}
	if err := verifyPersistedDigest("项目侧快照", p.InputProjectSnapshotID,
		p.InputProjectSnapshotDigest, projectDigest); err != nil {
		return err
	}
	runtimeDigest, err := requireSnapshotOfRelation(ctx, q, p.InputRuntimeSnapshotID, p.RelationID, model.SideRuntime)
	if err != nil {
		return err
	}
	if err := verifyPersistedDigest("运行时侧快照", p.InputRuntimeSnapshotID,
		p.InputRuntimeSnapshotDigest, runtimeDigest); err != nil {
		return err
	}
	if p.BaseBaselineID != "" {
		baseDigest, err := requireBaselineOfRelation(ctx, q, p.BaseBaselineID, p.RelationID)
		if err != nil {
			return err
		}
		if err := verifyPersistedDigest("base 基线", p.BaseBaselineID,
			p.BaseBaselineDigest, baseDigest); err != nil {
			return err
		}
	}
	if p.ResolvedFromPlanID != "" {
		if err := requirePlanOfRelation(ctx, q, p.ResolvedFromPlanID, p.RelationID); err != nil {
			return err
		}
	}
	return verifyClaimedDigest("计划", p.PlanDigest, func() (string, error) {
		return normalize.PlanDigest(p)
	})
}

// verifyBaselineIntegrity 是 BaselineRepository.Insert 的守卫：
// parent 同 Relation、baseline digest 重算一致。
func verifyBaselineIntegrity(ctx context.Context, q guardQuerier, b model.SyncBaseline) error {
	if b.ParentBaselineID != "" {
		if err := requireParentBaseline(ctx, q, b); err != nil {
			return err
		}
	}
	return verifyClaimedDigest("基线", b.BaselineDigest, func() (string, error) {
		return normalize.BaselineDigest(b)
	})
}

// verifyTaskIntegrity 是 TaskRepository 写入的守卫：plan/commit 引用必须存在
// 且属于任务自己的 Relation（relation 可空的全局任务不得引用 Relation 作用域对象）。
func verifyTaskIntegrity(ctx context.Context, q guardQuerier, t model.Task) error {
	if t.PlanID != "" {
		if err := requirePlanOfRelation(ctx, q, t.PlanID, t.RelationID); err != nil {
			return err
		}
	}
	if t.CommitID != "" {
		gotRelation, err := rowRelation(ctx, q, "sync_commits", t.CommitID, ErrNotFound)
		if err != nil {
			return err
		}
		if gotRelation != t.RelationID {
			return fmt.Errorf("sqlite: 提交 %s 属于 Relation %s，期望 %s: %w",
				t.CommitID, gotRelation, t.RelationID, ErrCrossRelation)
		}
	}
	return nil
}

// taskReferenceSentinel 把任务写入的外键违例翻译为可区分的哨兵错误：
// FK 只能说明"有引用坏了"，这里按 relation → plan → commit 的顺序定位坏引用。
func taskReferenceSentinel(ctx context.Context, q DBTX, t model.Task) error {
	if t.RelationID != "" && !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM relations WHERE id=?)", t.RelationID) {
		return ErrRelationNotFound
	}
	if t.PlanID != "" && !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM sync_plans WHERE id=?)", t.PlanID) {
		return ErrPlanNotFound
	}
	if t.CommitID != "" && !rowExists(ctx, q, "SELECT EXISTS(SELECT 1 FROM sync_commits WHERE id=?)", t.CommitID) {
		return ErrNotFound
	}
	return ErrRelationNotFound
}

// rowExists 执行 EXISTS 查询；查询出错按 false 处理（外层已有原始错误）。
func rowExists(ctx context.Context, q DBTX, query string, args ...any) bool {
	var exists bool
	if err := q.QueryRowContext(ctx, query, args...).Scan(&exists); err != nil {
		return false
	}
	return exists
}
