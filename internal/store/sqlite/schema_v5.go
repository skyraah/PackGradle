package sqlite

// schemaV5 是 v4 → v5 的前向迁移（ADR-0004 §1/§2，票 #34）：
//
//  1. apply_runs 运行头新表：一行 = 一次 Apply，task_id 为主键；六阶段状态 CHECK、
//     前置条件/恢复引用/操作数量/staging 清理/人工确认/提交引用按 ADR-0004 §1 原文落列，
//     附 idx_apply_runs_relation_state 索引；
//  2. operation_journal 重建补 status CHECK——六状态单调路径
//     pending/running/applied/verified/failed/compensated（ADR-0004 §2）。
//     该列是事实基线中唯一无 CHECK 的状态列。重建沿 v2 先例：legacy_alter_table=ON +
//     事务外 foreign_keys=OFF（migrate.go 的 DisableFK 钩子），拷贝后由 Verify 的
//     foreign_key_check(operation_journal) 兜底悬挂引用。注意 SQLite 在 INSERT…SELECT
//     拷贝时同样校验 CHECK：事实基线确认 journal 无任何 Go 写入方，生产库不存在
//     非法旧值；若仍出现（如测试预置），拷贝失败令迁移整体回滚，不静默改写历史；
//  3. operation_journal_events 追加历史表（ADR-0004 §2 授权的等价 append-only 实现）：
//     seq 为任务内单调序号，PK(task_id, seq) 使「seq 最大的一行」即最后已持久化意图；
//     复合外键挂 operation_journal(task_id, operation_id) 保证事件只引用已存在的操作行；
//     from_status 允许空串（初始意图持久化时无前态）；另建 RAISE(ABORT) 触发器在库层
//     拒绝 UPDATE/DELETE，仓储层（journal_repo.go）再收口为只提供插入与查询的接口；
//  4. plan_confirmations 增 consumed_at 消费标记列：确认令牌一次性消费语义
//     （ConfirmPlan 幂等重入，契约 05 §7 收口，T03 消费）。
const schemaV5 = `
PRAGMA legacy_alter_table=ON;
CREATE TABLE operation_journal_v5 (task_id TEXT NOT NULL REFERENCES tasks(id), operation_id TEXT NOT NULL, ordinal INTEGER NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending','running','applied','verified','failed','compensated')), target_relative_path TEXT NOT NULL, before_digest TEXT NULL, after_digest TEXT NULL, temp_relative_path TEXT NULL, recovery_ref_json TEXT NULL, ownership_proof_json TEXT NOT NULL, operation_json TEXT NOT NULL, result_json TEXT NULL, PRIMARY KEY(task_id, operation_id), UNIQUE(task_id, ordinal));
INSERT INTO operation_journal_v5(task_id, operation_id, ordinal, status, target_relative_path, before_digest, after_digest, temp_relative_path, recovery_ref_json, ownership_proof_json, operation_json, result_json)
SELECT task_id, operation_id, ordinal, status, target_relative_path, before_digest, after_digest, temp_relative_path, recovery_ref_json, ownership_proof_json, operation_json, result_json FROM operation_journal;
DROP TABLE operation_journal;
ALTER TABLE operation_journal_v5 RENAME TO operation_journal;
CREATE TABLE apply_runs (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id),
  relation_id TEXT NOT NULL REFERENCES relations(id),
  plan_id TEXT NOT NULL REFERENCES sync_plans(id),
  plan_digest TEXT NOT NULL,
  relation_revision INTEGER NOT NULL,
  state TEXT NOT NULL CHECK(state IN
    ('prepared','staged','applying','verifying','committed','recovery_required')),
  preconditions_json TEXT NOT NULL,
  recovery_refs_json TEXT NOT NULL,
  operation_count INTEGER NOT NULL,
  staging_cleared INTEGER NOT NULL DEFAULT 0,
  acknowledged_at TEXT NULL,
  commit_id TEXT NULL REFERENCES sync_commits(id),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_apply_runs_relation_state ON apply_runs(relation_id, state);
CREATE TABLE operation_journal_events (
  task_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  operation_id TEXT NOT NULL,
  from_status TEXT NOT NULL DEFAULT '' CHECK(from_status IN
    ('','pending','running','applied','verified','failed','compensated')),
  to_status TEXT NOT NULL CHECK(to_status IN
    ('pending','running','applied','verified','failed','compensated')),
  occurred_at TEXT NOT NULL,
  detail_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY(task_id, seq),
  FOREIGN KEY(task_id, operation_id) REFERENCES operation_journal(task_id, operation_id)
);
CREATE INDEX idx_operation_journal_events_task ON operation_journal_events(task_id, operation_id, seq);
CREATE TRIGGER operation_journal_events_no_update BEFORE UPDATE ON operation_journal_events
BEGIN SELECT RAISE(ABORT, 'operation_journal_events is append-only'); END;
CREATE TRIGGER operation_journal_events_no_delete BEFORE DELETE ON operation_journal_events
BEGIN SELECT RAISE(ABORT, 'operation_journal_events is append-only'); END;
ALTER TABLE plan_confirmations ADD COLUMN consumed_at TEXT NULL;
PRAGMA legacy_alter_table=OFF;
`
