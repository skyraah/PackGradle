---
status: accepted
date: 2026-09-01
---

# 0007 · CAS 保留与垃圾回收（GC）

决策图 #49 票 [#52](https://github.com/skyraah/PackGradle/issues/52) 的产物。P1 起 CAS 只进不出（架构篇 §8.2、boundaries 明言「P1 不做 GC，保留策略随 Phase 3 Restore 需求再定」）；[ADR-0006](0006-restore-rollback-semantics.md) §10 锁定两条硬约束并把保留窗口/封顶/GC 门控移交本票，回滚可用窗口＝GC 保留窗口的函数。本 ADR 决策化：两层删除模型、保留锚点、GC 任务化与安全窗口、保护根集、两阶段删除协议、大文件保全阈值与设置承载。

## 1. 两层模型：修剪提交、回收孤儿

保留策略作用在**同步提交**上，GC 只回收「零存活引用」的对象。直接删 CAS 文件无法回答「还有没有人要」，而提交是引用的根：提交被裁，它独占的引用随之消失，对象自然沦为孤儿。

- **连续前缀修剪**：只从历史最旧端连续裁剪，存活提交的 `parent_id` 链不出现断链，历史页从 head 向根遍历完整。
- **级联范围**：裁提交＝删 `sync_commits` 行 + `commit_changes` + `object_refs`（owner=该提交）+ 其 result baseline（`sync_baselines` + `baseline_resources`）。基线必须随提交同生命期——基线是 GC 的引用根，留基线不留提交，对象永受保护，容量锚点失效。被裁链的首个存活提交 `previous_baseline_id` 置空（仅元数据重连，内容不改）。
- **不动**：`sync_plans`、`observed_snapshots` 等 DB 增长面归 #32 横切雾区，本票不碰。
- **与 ADR-0006 §9 的关系**：不冲突。「历史不改写」约束的是回滚不得改写既有提交；保留策略是显式声明、有账目的删除机制。历史页显示墓碑行「更早 N 条提交已按保留策略清理」（落地归契约 06）。

## 2. 保留锚点与默认值

三锚点组合 + 硬保底，用户可调（承载见 §8）：

| 锚点 | 口径 | 默认 | 可调范围 |
| --- | --- | --- | --- |
| N 数量 | 每条 relation 保留最近 N 个同步提交 | 20 | 5–200 |
| D 时间 | 超过 D 天的旧提交裁 | 90 天 | 7–365 |
| C 容量 | **单条 relation** 的历史 CAS 占用超 C 时，从最早可删提交继续裁 | 1 GiB | 128 MiB–20 GiB |
| K 保底 | 最近 3 个提交**任何情况下不裁**（head 天然在内） | 3 | 固定，不可调 |

- 修剪条件＝超出 N ∨ 早于 D；超 C 触发追加修剪（仍受 K 约束：容量宁超不违保底）。
- **关系占用口径**：`SUM(objects.size)` over 该 relation 存活提交引用的对象（`object_refs` join）。CAS 跨 relation 去重意味着裁 A 的历史不保证立刻释放字节（B 仍引用同一对象）——锚点按 relation 记账，**回收判定始终全局**。

## 3. GC 任务化：触发通道与安全窗口

GC 是一个后台 **Task**（`kind='gc'`，schema CHECK 已预留），不是裸文件清理操作——任务创建、进度、事件、终态全走既有任务面，可观测可追溯。

- **触发三通道**：①启动后异步建 task；②每次提交收口后廉价检查关系占用，超 C 才建 task；③CLI 手动（`pgheadless gc`）。
- **安全窗口**＝不存在活跃 Apply/Restore run **且**没有任何 relation 处于 `recovery_required`。窗口不开时 GC task 停在 **pending**，窗口打开（任务终态、恢复处置等事件触发复查）后**自动继续执行**——三通道一视同仁排队，不拒绝。
- **全局单飞**：同一时刻至多一个 GC task 在执行。

## 4. 保护根集（P3 验收红线）

GC 候选集＝`objects WHERE state='ready'` 减去**存活集**。存活集：

1. **全部存活提交的传递引用**：`baseline_resources.logical_digest` 命中 `objects` 的部分 + `object_refs` 全部行——ADR-0006 §10 硬约束 1 的落地口径（否则旧提交的回滚承诺缩水）；
2. **恢复引用**：活跃/未处置 run 的 journal `recovery_ref_json`（kind=cas 条目，`recovery_probe.go` 重建路径同源）；
3. **未提交 staging 引用**——ADR-0006 §10 硬约束 2 的 staging 边界复核前提。

1/2/3 是「永不回收」。用户提供字节（`user_object_required` 路径）经验收入 CAS 后进入新提交的结果基线，随存活提交自然落入根 1，无需额外引用形态。

## 5. 删除协议：quarantined → 回收站（zstd）→ 清除

两阶段删除 + 暂存删除区，任意时点崩溃不损引用完整性：

1. **标记**（单事务）：候选对象 `state: ready→quarantined`（枚举预留值）。`Has()` 只认 ready，标记完成即对 restore/apply 不可见、无任何副作用；`quarantined` 行即回收账目。
2. **入回收站**：对象文件 zstd 压缩移入 `<root>/trash/sha256/<前缀>/<digest>.zst`；文件 mtime 即 7 天时钟起点，digest 可从文件名复原。原文件删除。
3. **清除**：trash 中超 7 天的文件删除，随删 `quarantined` 行。

- **崩溃幂等**：压缩完成前原文件仍在盘，任一步崩溃下一轮重算重扫自然续上；最坏是垃圾晚清一轮，绝不误删活引用。GC 全程可重入。
- **复活**：7 天内可人工从 trash 解压回 objects 并置回 ready（CLI 形态归执行票）——GC 误收的最后一道保险。
- **Put 幂等复活**：quarantine 期间同 digest 再 Put，UPSERT 置回 ready、文件重物化，trash 副本到期清除，天然正确。
- 压缩算法 zstd（`klauspost/compress`，纯 Go）。**CAS 本体不压缩**（§9）。

## 6. 孤儿清扫（三向，GC 流程末位执行）

- **file-without-row**（Put 后事务失败的盘上残留）：入回收站，走 §5 时钟；
- **`.tmp-*` 写中断残渣**：直接删，无账目可挂；
- **row-without-file**（盘上已被外部删除）：直接删行对账；后续 restore 对该资源走既有降级分支（rec=cas 对象缺失 → `user_object_required`），GC 不追溯损失。

孤儿清扫只在安全窗口内、存活集计算之后执行，避免与在途 Put 竞争。

## 7. 大文件保全阈值

现状非 mod 一律 `cas`（`defaultRecoverability`「宁可多存」）无大小上限，存档/光影类大文件每次覆盖都进 CAS，容量必失控。决议：

- 非 mod **单文件**超过阈值**不做 before 保全**（照常同步写，旧版本不留 CAS）。默认 **32 MiB**，可调 1 MiB–512 MiB，0＝不限。
- 计划确认页对受影响的覆盖/删除行显示**「旧版本不留存」警示**（同「不可重取」警示先例，损失面确认可见）。
- 回滚语义**零新增枚举**：此类资源 rec 仍=cas，对象缺失即走 ADR-0006 §2 矩阵既有分支 → `user_object_required`；用户提供字节凭目标 digest 验收入 CAS。版本再次变更后如需可回滚须**再次手动提供**——无自动跟踪、无自动刷新，维护责任在用户。

## 8. 设置承载：config.toml

保留设置不入 SQLite，落 **config.toml**（appconfig 扩展 `[retention]` 段）：`keep_commits`(20)、`keep_days`(90)、`relation_capacity_bytes`(1 GiB)、`preserve_max_bytes`(32 MiB，0=不限)、`trash_days`(7)；范围校验在加载层。架构篇 §8.3「SQLite 管理设置」**在本域修订为配置文件承载**（2026-09-01 用户决议），其余设置域不受影响。UI 编辑面归契约 06。

## 9. 不做

- **提交 pin/锁定**：不做，有真实需求再议（K=3 保底已兜住最低回滚窗口）。
- **CAS 本体压缩**：不做——内容以小文本为主、去重天然，容量优化角色由回收站 zstd 承担。

## 现状基线（2026-09-01 核对）

`objects` 全仓库零 DELETE；`object_refs` 仅 apply 提交与恢复重建两个写点（owner_type=commit，purpose=before_preservation）；`tasks.kind` CHECK 已预留 `'gc'`、`objects.state` 已预留 `'quarantined'`；无设置表（appconfig 为 legacy 面貌，新栈设置面从本票起建）。**本 ADR 为纯增量，不改既有行为。**

## 后果

- **零新表、零 schema 迁移**：quarantined 态即回收账目，trash 文件名即 digest 映射；设置走 config.toml。schema 维持 v5。
- **新依赖**：`klauspost/compress`（zstd）。
- **契约 06 新增面**：gc task 文案/事件、history 墓碑行、「旧版本不留存」警示、`[retention]` 设置读写、`pgheadless gc` 子命令。
- **执行要点**（归执行规格）：关系占用 SUM 与存活集的 SQL 视图、修剪事务的 FK 删除顺序（先提交后基线）、trash 复活子命令形态、启动/任务终态的安全窗口复查点。
