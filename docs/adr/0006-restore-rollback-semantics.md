---
status: accepted
date: 2026-09-01
---

# 0006 · 回滚（Restore）语义

决策图 #49 票 [#51](https://github.com/skyraah/PackGradle/issues/51) 的产物。架构篇 §8.4 已定回滚框架（以历史基线生成新 `RestorePlan` 走 Scan→前置检查→确认→Apply 全流程、产生新 `SyncCommit`、历史不改写）；本 ADR 把逐资源判定规则、exact/partial 语义、删除面、重取降级链、授权模式适用面与 CAS/GC 耦合决策化，并关闭 [ADR-0005](0005-mod-update-channel.md) §7 留下的 before-preserve 现状核对项。术语「回滚」专指本语境，见 CONTEXT.md。

## 1. 目标选择面

回滚目标＝**任意历史 commit 的 result baseline**，语义单一：「回到该提交完成后的状态」。含 initialize commit 与历史 restore commit——回滚一次回滚＝重做（前进）。目标 baseline 的逐侧表示即写回目标，**双端强一致化**：project 侧与 runtime 侧各自写至目标的对应表示。当前 head 作为目标＝空操作，UI 禁选（防误触），后端不新增特判路径（空差异计划走既有空计划先例）。

## 2. 四标记判定矩阵

逐资源判定是 prepare 时点的确定函数：**目标状态 × 当前观测 × 可恢复途径 × 重取信息可得性**。

| 场景 | 标记 |
| --- | --- |
| 目标 present，当前 digest≠目标（或 absent 需重建） | `restorable_from_cas`：rec=cas 且 CAS 对象实存；`redownload_required`：rec=redownload 且重取信息可得；`user_object_required`：rec=redownload 但无重取信息，或 rec=cas 但对象缺失（凭目标 digest 验收用户提供字节）；`unrecoverable`：rec=unrecoverable |
| 目标 absent，当前 present | 删除操作行，不占四标记（§5） |
| 双端 digest 均等于目标 | 无操作行 |

- **重取性看数据不看出身**：`redownload_required` 以重取信息（CF slug+file-id 或项目 metafile 对应条目）实际存在为前提，kind=mod 不自动等于可重取。现状 `defaultRecoverability` 仅按 kind 分派是实现缺口，实现票按本判定函数对齐（手放 mod 归 `user_object_required`）。
- **`restorable_from_cas` 定义**：目标内容字节已作为整文件对象实存 CAS（P1 起 apply 保全机制），回滚＝取字节→暂存→原子写，零网络零用户介入；非增量非差量。
- `unrecoverable` 默认阻止 exact；用户可显式降级，结果必须标 partial（架构 §8.4 已定，本票不松）。

## 3. 无冲突决议面

回滚目标唯一，不存在 P2 意义的三方冲突选择；计划不含冲突决议控件。`ResolveRestorePlan` 的决议面仅剩 **partial 的逐资源 skip 选择**，固化于 resolved plan（架构 §5 已定 Apply 不接收临时 resolution）。exact 决议无选择面，直接确认。

## 4. exact 不可行前置

`requested_exactness` 沿既有枚举 `exact|allow_partial`（P2 已建模）。PrepareRestore 出 draft 时四标记已判完：exact 请求遇任何非 `restorable_from_cas`/`redownload_required` 资源，计划即标 **`exact_infeasible`** 并附 blocked-by 清单，`ResolveRestorePlan` 拒绝 exact 决议；UI 引导改 `allow_partial` 重 resolve。不在 Confirm 时才拦截。

## 5. 删除面

目标 absent 而当前 present 的资源（目标之后新增，含用户手放文件）随回滚删除，分三类：

- **非 mod**：before-preserve 照旧进 CAS，之后可从对象库找回（现状规则自然覆盖）；
- **packwiz 管理的 mod**：不保全（ADR-0005 §7 红线），丢失后可重取；
- **用户手放的 mod**（无重取信息）：照删不保全，删除即永久丢失——计划删除行带**「不可重取」警示标记**，确认页可见损失面。红线严格成立：不为此类开 CAS 例外（磁盘负担优先，评估权重：稳定性 > 维护 ≈ 易用 > 周期）。

## 6. 授权模式零适用

授权模式语义＝「非冲突 Apply 免逐次确认」（ADR-0005 §4）。**回滚（含其中的删除）永远人工确认，不进免确认面**，无论开关状态。

## 7. 重取判定时机与失败降级链

- PrepareRestore **乐观标记**（有重取信息即标 `redownload_required`），辅以 CF 查询**尽力探测**（#50 免 key 直链）：不可用提前降标，离线/查询失败保持乐观标记**不阻塞 prepare**——探测是可用性辅助，非承诺。
- Apply 物化失败＝**整场失败退出、零部分提交**（同 P2 verify_mismatch 先例，不搞运行中逐资源降级）；用户重新 prepare→改 `allow_partial`→re-resolve。
- 降级链：`redownload_required` →（重取失败）`user_object_required` →（用户不提供）`unrecoverable`。

## 8. 与 P2 运行/恢复协议的复用边界

ApplyRestore 复用 P2 全套：apply_runs/operation_journal/staging/恢复探测/acknowledge（ADR-0004）。**`recovery_required` 期间 restore 与 apply 同门禁**（禁发起新回滚，防止恢复一半又叠一层回滚）；同一 Relation 串行（Scan/Apply/Restore 互斥）不变。pgheadless 已覆盖，无新工具形态。

## 9. 历史与账目

回滚成功产生新 `SyncCommit`（`CommitKind=restore`，`ParentCommitID`=当前 head），结果 baseline＝apply 后复扫**新建**（不指针回拨），历史不改写。历史页呈现「回滚（回到目标提交）」，逐资源表 before=滚前实际值、after=目标值；**restore commit 本身是合法回滚目标**。partial 的 `remaining_change_count`＝skip＋user_object 未提供＋unrecoverable 之和；**partial 后 relation 保持 dirty，不显示 clean**（缺失资源不得谎报为成功恢复）。

## 10. CAS/GC 耦合（→ CAS 保留与 GC 票的需求输出）

回滚可用窗口＝GC 保留窗口的函数。本 ADR 只锁两条硬约束，保留窗口/封顶/LRU/压缩/GC 运行门控（relation 忙时暂停）归 [#52](https://github.com/skyraah/PackGradle/issues/52)：

1. GC 不得回收被任何 `sync_commits.result_baseline` **传递引用**的对象——否则旧提交的回滚承诺缩水（降级 `user_object_required`，正确性不破但可用性受损）；
2. restore 的 CAS 引用在 **staging 边界复核**：对象缺失→该操作失败→整场失败退出，不部分提交。

## 11. `.index` 不回写（重申 ADR-0005 §5）

回滚把 JAR 换回旧版不回写 `mods/.index`，Prism 下次交互自行重算；权威判定只认字节与 digest。

## 现状核对结论（ADR-0005 §7 核对项关闭）

before-preserve **不存在**「无条件存字节」：`defaultRecoverability`（`apply_actions.go`）按类别分派——mod→`redownload`（不入 CAS），其余→`cas`（宁可多存）；`RequiresCASBackup`（`syncstage/preserve.go`）对 `redownload`/`none` 不保全；下载的 after 字节只走 staging、提交后清理。**现状已对齐 ADR-0005 §7，无需改码。**

## 后果

- **实现缺口移交**：`defaultRecoverability` 按 kind 分派 vs 本判定函数按「重取信息可得」——回滚实现票对齐（含手放 mod 的 `user_object_required` 归类与删除行警示标记）。
- **CF 探测接线、物化模式表扩展（`download`）、staging 取数、网络失败语义**→ 下载物化票 #54；本 ADR 只定「探测尽力、失败整场退出、降级链」语义。
- **存储负担优化**（保留窗口/封顶/压缩/GC 门控）→ #52；本 ADR 只锁 §10 两条约束。
- 回滚完备性继承 ADR-0005 让渡：下架 mod 无法自动回滚或补偿，降级用户提供/不可恢复。
- `exact_infeasible` 标记与 blocked-by 清单进入契约 06 的计划视图字段。
