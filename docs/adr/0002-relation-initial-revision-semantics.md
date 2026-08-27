---
status: accepted
date: 2026-08-28
---

# 0002 · Relation 初始 revision 语义

Relation 的修订号（revision）语义（决策图票 #4；背景见检视报告 P1-8、roadmap §2.1「MappingPolicy 修改会递增 relation_revision」）。`internal/application/sync/relation.go:235-252` 先以 revision=1 插入 Relation，再 `SavePolicy` 联动 +1，新关系出生即 rev=2；「创建时写入初始 policy 算不算一次修改」无定论，headless 测试只断言 `>=1`，且 `mappings.revision`（策略模板版本）与 `relations.revision`（关系修订）两个计数在出生点错位。数据语义落库后难改，P1 收官前锁定。决议如下：

1. **创建即第 1 代**。Relation 出生即 revision=1 且已带初始 MappingPolicy；创建时写初始 policy **不算** policy 修改，不递增 revision。修订号语义 =「策略代次 = 1 + 创建后的策略修改次数」。
2. **唯一递增源是 policy 修改**。`relation_revision` 只随 MappingPolicy 修改（SavePolicy）递增；rebind（binding 变化）、health 状态变化、baseline/commit 推进均不递增。binding 变化由 Apply 前 fingerprint 重验覆盖（roadmap §2.3），不需要修订号兜底。
3. **UI 不展示数字**。修订号是内部一致性字段，不进入任何用户可见文案或界面；计划过期提示用「策略已更新」类文案，不带数字对比。契约层保留 `RelationDTO.revision` 与 `WorkspaceStateDTO.relation_revision` 字段——前端 PrepareSync 需回传 revision 参与 `err.sync.revision_mismatch` 校验（`internal/transport/syncservice.go:81`）——保留字段 ≠ 展示数字。
4. **测试固定精确断言**。CreateRelation 后 revision 精确 == 1，废除 `>=1` 宽松断言（`headless_test.go:268`）；SavePolicy 后 == 前值+1。store 测试 `TestMappingSavePolicyBumpsRevision` 不变（其 fixture 建模「创建后的首次修改」，仍 == 2）；新增断言：CreateRelation 后 GetPolicy 可读且 policy 自身 Revision == 1。
5. **双计数独立**。`MappingPolicy.Revision`（策略集模板自身版本，随模板演进如 default-v2 变化）与 `Relation.Revision`（关系级策略代次）语义独立、互不驱动；出生点两者恰好同值 1 属巧合，不是耦合。契约面（P1-CONTRACT 票）按此语义写 DTO 注释。
6. **初始写入机制归 P1-PREP（#8）**。本 ADR 只定不变量：初始 policy 写入不得递增 revision；Relation 对外可见时必已带 policy（「有 revision 无 policy」的可见中间态非法）。实现机制（创建事务内直写 vs SavePolicy 不递增变体）由 P1-PREP 的事务决议确定。
7. **无迁移**。本 fork 无发布、无外部用户（ADR-0001），现存 dev 数据库不写迁移脚本，本地删除重建即可；语义自本 ADR 起生效。

## Considered Options

- **初始写入算修改（出生即 rev=2）**：维持现状。否决：新建工作区即见「第 2 版」无解释来源；出生点 `policy.Revision`(1) 与 `relation.Revision`(2) 错位；roadmap §2.1「修改会递增」天然排除「建立」。
- **UI 展示「策略版本 N」**：数字对排查有价值，但用户无决策用途，展示引入解释负担；如日后需要另议。
- **合并双计数**：模板演进（default-v2）与关系代次是两个时间轴，合并会让「换模板」与「改策略」语义纠缠。

## Consequences

- `relation.go` 创建路径按 #8 改造后必须满足决议 6 的不变量；执行时测试断言按决议 4 收紧。
- 事件协议不携带 revision，本决议对事件面无影响（P1-EVENT 票不受影响）。
- `err.sync.revision_mismatch` 参数保持回传权威 revision（现有实现已如此，无需变更）。
- CONTEXT.md 新增术语：修订号（Revision）、策略集版本（Policy Set Version）。
- 前端 P1-CUTOVER 执行面不实现任何 revision 展示；P1-I18N 文案不得包含数字。
