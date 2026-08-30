---
status: accepted
date: 2026-08-30
---

# 0004 · Phase 2 Apply 的 operation journal 持久化与崩溃恢复协议

ADR-0003 决议 1 把 CreateRelation 的五步纯元数据写入收进单个 SQLite 事务，同时把「DB + 文件 + CAS 跨资源提交」显式推迟：「正是 Phase 2 Apply 的事（operation_journal 已规划）……需要跨资源提交时另行决议」。本 ADR 兑现该决议，覆盖决策图票 #28 的三组待决问题（journal 持久化格式、崩溃恢复协议、与 P1 基建的衔接）。

事实基线（决议前提，2026-08-30 实测）：

- **journal 的存储形状已冻结**：`operation_journal` 由 `internal/store/sqlite/schema_v1.go:33` 建表，12 列、`PRIMARY KEY(task_id, operation_id)` + `UNIQUE(task_id, ordinal)`，与 redesign §8.3 的 DDL 一致。全仓库**无任何 Go 代码读写它**（唯一引用是 `guard_test.go:328` 预置一行验证 v2 表重建不丢数据）。`status` 是该表唯一**没有 CHECK 约束**的状态列（`tasks`/`sync_plans`/`objects`/`sync_commits` 均有）。
- **协议骨架已写死**：redesign §6.6 给出六阶段（`prepared`/`staged`/`applying`/`verifying`/`committed`/`recovery_required`）、逐操作两段式状态（写入前 `pending`，再独立记 `running`/`applied`/`verified`/`compensated`/`failed`）、四路恢复裁决与幂等/所有权铁律。故本 ADR 决的不是「设计 journal」，而是补齐 §6.6 未落到列上的空档。
- **P1 侧可复用基建**：`tx.go:63` `RunInTx(ctx, func(ports.Repos) error)`（`ports.go:185` 现 10 个仓库）；`objectstore/cas.go:60` `Put` 流式 sha256 + 原子 rename + UPSERT `state='ready'`；`filesystem/atomic.go:15` `WriteFileAtomic`（现无调用方）；`filesystem/endpoint.go:52` `NewResolver`/`:74` `Resolve` 路径逃逸拦截；`paths.go:57-75` `EnsureLayout` 建出 `StagingDir` 但**无任何写入方**。
- **P1 侧的死值与占位**：`tasks.status` 的 `recovery_required`（`schema_v1.go:29`、`event.go:33-38`）代码零赋值；`objects.state` 的 `'staging'`（`schema_v1.go:36`）从未写入；`object_refs`（`:37`）、`sync_commits`/`commit_changes`（`:34`/`:35`）、`plan_confirmations`（`:38`）四表零消费；`model.Recoverability`（`model.go:124`）标注「Phase 3 消费，P1 先建模」；`plan.go:356` 注释「构造 Reversible=true 的操作（P2 将以 CAS staging 兑现可回滚性）」。
- **现状与 §6.6 的矛盾**：`sync/recovery.go:14` `RecoverInterruptedTasks` 启动时把**所有**活跃任务无差别 `MarkFailed`，既不读 journal 也不 stat 文件系统；§6.6 步骤 6 要求的是「发现未完成 journal → 阻止该 Relation 新 Apply → probe 后裁决」。P1 无 Apply，该实现是合理占位，Phase 2 必须改造。

决议如下。

## 1. Apply 运行级状态落 `apply_runs` 新表（schema v5）

§6.6 的六阶段是**一次 Apply 整体**的状态，`operation_journal` 的粒度是 `(task_id, operation_id)` 逐操作行，表上无任何一列能装它。且步骤 1 `prepared` 要求持久化「计划 digest、全部前置条件、恢复对象引用和有序操作清单」——前三样在逐操作表上没有归宿。

新建运行头表，一行 = 一次 Apply，`task_id` 为主键（与 Q5 的投影决议同源）：

```sql
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
```

否决 `tasks.phase`（现为无 CHECK 的自由文本列）：该列在 04 事件协议里是给前端展示的进度短语，把恢复裁决的事实源压在展示字段上会让两个用途互相绑架。否决「从 operation 行聚合推导」：运行级前置条件与恢复对象引用无处可存。

## 2. Journal 采用运行头、操作行与追加历史三层语义

一次 Apply 的事实由三层组成：

1. `apply_runs` 是运行头，保存本次 Apply 的计划身份、关系修订号、前置条件、恢复对象引用、操作数量和整体阶段。
2. `operation_journal` 每个 `(task_id, operation_id)` 只有一行，保存有序操作、root-relative 目标、before/after digest、临时路径、恢复引用、ownership proof 和当前逐操作状态。
3. 状态变化另写不可变的操作历史（后续 schema/仓储实现可采用 `operation_journal_events` 或等价 append-only 表），用于审计与恢复解释；当前操作行是查询投影，不得回退或覆盖已发生的历史。

操作状态沿单调路径推进：`pending -> running -> applied -> verified`，失败分支为 `failed`，补偿完成分支为 `compensated`。每次状态变更必须先持久化意图，再执行对应文件动作；已达成 `verified` 的操作重复执行不得改变结果。

选择追加历史而非单纯重写，原因是恢复必须回答“最后一个已持久化意图是什么”，而产品又需要保留逐操作审计。选择保留当前行而非仅事件溯源，原因是启动恢复、Relation 阻塞和任务投影都需要低成本读取当前状态。

## 3. staging 与对象引用

进入 `staged` 前，所有将被覆盖或删除、且 recoverability policy 要求保留的旧内容必须已写入 CAS 或 staging，并完成 hash 复核。对象只有在完整落盘并校验后，才能在 SQLite 中建立引用；SQLite 事务不得包住文件复制、rename 或 fsync。

staging 布局按 Apply 运行隔离，每个运行拥有自己的目录和 root-relative 临时路径；普通用户视图不得暴露临时绝对路径。staging 中的每个文件都必须能关联到 `apply_runs`/`operation_journal` 的恢复引用和 ownership proof。

## 4. 恢复裁决

启动或显式恢复时，发现未完成的 Apply journal 后，先将对应 Relation 标记为 `recovery_required` 并禁止新的 Apply。恢复器对每个未终态操作 probe 实际目标路径、before/after digest、临时内容和所有权证明，按以下矩阵裁决：

| Probe 结果 | 裁决 |
| --- | --- |
| 目标已达到 after digest，且所有权证明匹配 | `mark-applied`，随后进入可验证路径 |
| 目标尚未写入，staging 完整，前置条件仍成立 | 幂等 `redo` |
| 目标部分写入，但仍能证明属于本次 Apply | `compensate` 或继续完成该操作，具体取决于操作类型与可恢复对象 |
| 状态含糊、路径已被外部修改或无法证明所有权 | 保持 `recovery_required`，要求人工确认 |

恢复不得依据文件名、mtime、目录数量或“看起来相同”进行猜测；无法证明归属时不得删除、覆盖或再次执行破坏性动作。重复恢复必须幂等，不能对同一目标重复删除或覆盖。

## 5. 成功、失败与 staging 清理

成功路径固定为：`prepared -> staged -> applying -> verifying -> committed`。只有完整复扫受管范围成功、验证快照与计划目标一致，并在一个 SQLite 事务中写入新 Baseline、SyncCommit、对象引用和 Relation head 后，Apply 才能进入 `committed`。

`staging` 仅在上述事务成功提交后清理，并将 `staging_cleared` 记录为事实。清理必须按本次运行的 ownership proof 执行且可重试；事务失败、取消、磁盘写满、进程强杀或任一已选操作失败时，不推进 Baseline，不创建 Commit，运行进入 `recovery_required` 并保留 staging 证据。

## 6. 与 P1 基建的衔接

- `RunInTx` 只覆盖 SQLite 元数据；它不包住文件系统写入。
- `prepared` intent、`apply_runs`、操作当前行、操作历史、Task 状态、Baseline、SyncCommit 与 object refs 共同构成 SQLite 权威状态。
- CAS 是可恢复内容的权威；引用前必须完成原子落盘和 digest 校验。
- 事件只在 SQLite 事务提交后发布；事件 payload 只是通知，前端与恢复器必须通过查询 API 读取事实。
- 同一 Relation 同时最多一个 Apply/Restore；存在未完成 journal 或 `recovery_required` 时，新的 Apply 不可用。
- hash cache 只服务于扫描性能；完整复扫成功后自然更新，不参与恢复裁决，也不能替代 before/after digest。

## 7. 后果与后续边界

本 ADR 使 Phase 2 的施工可以围绕稳定的 Apply 事实模型实现，而不再依赖 `tasks.phase` 或隐式文件状态。它不决定具体 copy Materializer、资源级补偿算法、CAS 长期保留/GC 阈值、Restore UI 或人工恢复界面；这些仍属于后续 Phase 2/3 施工票和独立决策。
