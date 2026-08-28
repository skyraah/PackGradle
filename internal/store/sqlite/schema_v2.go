package sqlite

// schema_v2 是 v1 → v2 的前向迁移（检视报告 P0-3，票 #12）：
// tasks.plan_id / commit_id 补 REFERENCES 约束，堵住任务对计划/提交的悬挂引用。
//
// SQLite 不支持 ALTER TABLE ADD CONSTRAINT，按官方推荐流程重建表：
// 建 tasks_v2 → 拷贝 → 删旧表 → 改名 → 重建索引。两个 PRAGMA 前提见 migrate.go
// 的 migration.DisableFK/Verify 钩子：
//   - legacy_alter_table=ON：改名期间跳过 schema 重解析与引用改写。否则
//     operation_journal 等对旧表名 tasks 的外键引用会在改名瞬间的重解析中
//     因"表不存在"而报错；
//   - foreign_keys=OFF（事务外设置）：关闭后 DROP TABLE 不做隐式 DELETE 的
//     外键检查，operation_journal 已有行不阻塞重建；拷贝失去的外键校验由
//     Verify 的 PRAGMA foreign_key_check 兜底——悬挂引用令迁移整体回滚。
const schemaV2 = `
PRAGMA legacy_alter_table=ON;
CREATE TABLE tasks_v2 (id TEXT PRIMARY KEY, relation_id TEXT NULL REFERENCES relations(id), kind TEXT NOT NULL CHECK(kind IN ('scan','apply','restore','gc')), status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','cancelled','recovery_required')), phase TEXT NOT NULL, sequence INTEGER NOT NULL DEFAULT 0, outcome TEXT NULL CHECK(outcome IN ('exact','partial')), can_cancel INTEGER NOT NULL DEFAULT 0, completed INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0, message_key TEXT NOT NULL DEFAULT '', message_args_json TEXT NOT NULL DEFAULT '[]', plan_id TEXT NULL REFERENCES sync_plans(id), commit_id TEXT NULL REFERENCES sync_commits(id), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, problem_json TEXT NULL);
INSERT INTO tasks_v2(id, relation_id, kind, status, phase, sequence, outcome, can_cancel, completed, total, message_key, message_args_json, plan_id, commit_id, created_at, updated_at, problem_json)
SELECT id, relation_id, kind, status, phase, sequence, outcome, can_cancel, completed, total, message_key, message_args_json, plan_id, commit_id, created_at, updated_at, problem_json FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_v2 RENAME TO tasks;
CREATE INDEX idx_tasks_relation ON tasks(relation_id, status, created_at DESC);
PRAGMA legacy_alter_table=OFF;
`
