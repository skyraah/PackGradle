---
status: accepted
date: 2026-08-28
---

# 0003 · 多步元数据写入的单事务 doctrine（CreateRelation / ApplyRebind）

`PrepareRelation → CreateRelation` 的多步写入（检视报告 P1-4；决策图票 #8）：`internal/application/sync/relation.go:185` 起**先消费 preparation**（`MarkConsumed`），再分步登记 Project、登记 Runtime、创建 Relation、保存 Mapping。中途失败留下已登记的部分端点、已消费 preparation 或无 policy 的 Relation，用户不能安全重试。票面给出两个方案方向：SQLite 单事务（要求 store 层支持跨 repository 事务边界）vs 应用层两阶段可恢复提交协议（幂等重试、残留清理）。

事实基线（决议前提）：该五步写入**全程纯元数据**——不碰文件系统、不碰 CAS，全部落在同一 `packgradle.db`；DSN 已配置 WAL + synchronous(FULL) + foreign_keys + 单连接（`open.go`，事务内天然串行、崩溃原子回滚）；ports 现有 9 个仓库接口无任何事务管道；`ErrPreparationExpired` 现混装「已过期」与「已消费」两种含义（`preparation_repo.go` MarkConsumed 的 0 行影响分支）。决议如下：

1. **SQLite 单事务**。CreateRelation 五步（消费 preparation、登记 Project、登记 Runtime、创建 Relation、保存初始 Mapping）收进**一个 SQLite 事务**。决定性论据：流程无跨资源步骤，SQLite 本地 ACID 完整覆盖；roadmap §2.3「数据库事务不能被当作文件系统事务」警告的是反向误用（拿 DB 事务替代 FS 事务），本流程恰是 DB 事务的本职。中途失败自动回滚 → **同一 preparationID 可安全重试直至过期**，票面「残留暴露与安全重试」问题整体消解：成功则零残留，失败则零残留。
2. **UnitOfWork 闭包形态**。`store/sqlite` 新增 `RunInTx(ctx, func(Repos) error)`：事务域构造绑定 `*sql.Tx` 的一套仓库集合，既有单语句方法签名**一概不动**；应用层多步流程整个跑在闭包内，事务边界一眼可见。ApplyRebind 及后续多步元数据用例复用同一入口。
3. **覆盖 ApplyRebind + 事件发布时点全局规则**。契约 03 §2.4 的 ApplyRebind（消费 preparation、更新端点 binding fingerprint、health→healthy、发布 `relation_invalidated`）同为纯元数据多步写，按同一单事务 doctrine 施工。配套全局规则：**事件发布一律在事务提交成功之后**；发布失败不影响提交（事件不是事实源，与 04 事件协议 §1 同构）。
4. **错误码拆分**。`ErrPreparationExpired` 拆两码：`err.relation.prep_expired`（过期 → 引导重新预检）与 `err.relation.prep_consumed`（已被消费 → 引导刷新，关系可能已建成——双击/双窗口真实场景）。契约 03 错误码表小幅增补；locale 文案两条（P1-I18N 执行面）。
5. **schema 影响：无**。v1 表结构与约束已足——`relations` UNIQUE(project_id, runtime_id) 防重复 pair、`preparations.consumed_at` 守卫消费一次、端点 UNIQUE 约束支撑幂等复用；无新表、无新列、无迁移。
6. **用例影响清单**。`CreateRelation`：单事务化，`MarkConsumed` 移入事务；`SavePolicy` 的「联动递增 revision」不参与出生点——初始 policy 事务内直写，ADR-0002 决议 6 的机制落定（出生 revision=1）。`ApplyRebind`：同 doctrine。`PrepareRelation`：只读探测 + Insert，不变。事件发布时点规则约束所有多步写用例（决议 3）。
7. **并发约束**。roadmap §2.3「同一 Relation 最多一个 Scan / 一个 Apply」约束的是运行期长任务，不涉创建流。创建期并发由既有约束覆盖：并发同 prep → 后到者得 `err.relation.prep_consumed`；同 pair 异 prep → 后到者得 `err.relation.duplicate_pair`（UNIQUE 兜底）。两阶段协议自身需恢复探测与残留清扫，本 ADR 不引入。

## Considered Options

- **应用层两阶段可恢复提交**（步骤日志 + 幂等重试 + 残留清扫 + 恢复探测）：为「DB + 文件 + CAS」跨资源提交设计的机制，正是 Phase 2 Apply 的事（operation_journal 已规划）。P1 纯元数据流程引入即白付全部机制成本。**推迟至 Phase 2** 需要跨资源提交时另行决议（地图雾区已记录，不在本 ADR 范围）。
- **Executor 参数**（9 个仓库接口所有方法加 exec 参数）：显式但改动面最大，既有单语句调用与 headless 测试全改签名。否决。
- **store 层复合方法**（只给 CreateRelation 加一个复合存储方法）：最小 diff，但把幂等复用端点、同名实例路径校验等应用层流程下沉 store，层次破坏。否决。

## Consequences

- `store/sqlite` 新增事务管道：`RunInTx` 入口 + 绑定 `*sql.Tx` 的仓库集合；9 个仓库既有单语句方法不变。测试新增「中途失败无残留」用例：注入失败 → 五处写入全部回滚 → 同 preparationID 重试成功。
- `relation.go` 创建路径改造后：出生 revision 精确 == 1（ADR-0002 决议 4 断言随改造收紧）。
- 契约 03 错误码表增补 `err.relation.prep_expired` / `err.relation.prep_consumed`；P1-I18N 补两条文案。
- 事件发布时点规则与 04 事件协议兼容：事件本就只做通知，协议不受影响。
- Phase 2 Apply 跨资源提交（DB + staging + CAS + operation_journal）不适用本 ADR，另行决议。
- CONTEXT.md 新增术语：预检（Preparation）。
