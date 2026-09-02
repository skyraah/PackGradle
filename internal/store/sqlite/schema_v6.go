package sqlite

// schemaV6 是 v5 → v6 的前向迁移（契约 06 §6，票 #57；P3 存储基线，零新表）：
//
//  1. relations 加列 authorized_apply INTEGER NOT NULL DEFAULT 0——工作区级授权
//     开关（ADR-0005 §4；契约 06 §3.6），既有关系迁移后默认关闭；
//  2. apply_runs state CHECK 增 'failed' 终局——staging 相位下载/物化失败的
//     整场退出终态（契约 06 §6 Q8：网络失败 ≠ 恢复面，不进 recovery_required）。
//     六阶段 CHECK 无法原地改写，沿 v5 先例表重建：legacy_alter_table=ON +
//     事务外 foreign_keys=OFF（migrate.go 的 DisableFK 钩子），INSERT…SELECT
//     拷贝时同样校验 CHECK——生产库不存在非法旧值（Go 写入方均先过状态机），
//     若仍出现（如测试预置）拷贝失败令迁移整体回滚，不静默改写历史；拷贝后由
//     Verify 的 foreign_key_check(apply_runs) 兜底悬挂引用。注意 apply_runs 是
//     被引用端之外的最末表（journal/events 只引用 tasks），重建不影响他表引用；
//  3. 预留枚举不动：tasks.kind 的 'restore'/'gc'、objects.state 的 'quarantined'
//     均 v1 起已预留，本迁移零触碰（契约 06 §6「零新表」）。
const schemaV6 = `
PRAGMA legacy_alter_table=ON;
ALTER TABLE relations ADD COLUMN authorized_apply INTEGER NOT NULL DEFAULT 0;
CREATE TABLE apply_runs_v6 (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id),
  relation_id TEXT NOT NULL REFERENCES relations(id),
  plan_id TEXT NOT NULL REFERENCES sync_plans(id),
  plan_digest TEXT NOT NULL,
  relation_revision INTEGER NOT NULL,
  state TEXT NOT NULL CHECK(state IN
    ('prepared','staged','applying','verifying','committed','recovery_required','failed')),
  preconditions_json TEXT NOT NULL,
  recovery_refs_json TEXT NOT NULL,
  operation_count INTEGER NOT NULL,
  staging_cleared INTEGER NOT NULL DEFAULT 0,
  acknowledged_at TEXT NULL,
  commit_id TEXT NULL REFERENCES sync_commits(id),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO apply_runs_v6(task_id, relation_id, plan_id, plan_digest, relation_revision, state, preconditions_json, recovery_refs_json, operation_count, staging_cleared, acknowledged_at, commit_id, created_at, updated_at)
SELECT task_id, relation_id, plan_id, plan_digest, relation_revision, state, preconditions_json, recovery_refs_json, operation_count, staging_cleared, acknowledged_at, commit_id, created_at, updated_at FROM apply_runs;
DROP TABLE apply_runs;
ALTER TABLE apply_runs_v6 RENAME TO apply_runs;
CREATE INDEX idx_apply_runs_relation_state ON apply_runs(relation_id, state);
PRAGMA legacy_alter_table=OFF;
`
