---
status: accepted
date: 2026-09-02
---

# 0009 · 合并（Merge）语义与 adapter

决策图 #70 票 [#72](https://github.com/skyraah/PackGradle/issues/72) 的产物。[#31](https://github.com/skyraah/PackGradle/issues/31) 调研已定候选与七决议点（[笔记](../research/p4-merge-libraries.md)）；本 ADR 决策化：合并引擎选型与 git 依赖政策、TOML 文本合并路线与注释保真口径、冲突块表示粒度、merge 在计划面的第五操作定位与授权模式口径、永不合并边界、合并产物的管线接入与保全回滚。术语「合并」「冲突块」见 CONTEXT.md。

## 1. 合并引擎与 git 依赖政策

**epiclabs-io/diff3（MIT、零依赖、纯 Go）+ pin commit 伪版本**（无 tag/release，go.mod 伪版本 + go.sum 锁定）。选它因为它是唯一同时满足「纯 Go + 零依赖 + 结构化冲突对象（`Conflict{A,O,B,AIndex,OIndex,BIndex}`，hunk 级三态证据）+ 仍在维护」的候选，hunk 形状到 `conflict_kind`/detail 的映射路径最短。单人小库风险用两道兜底对冲：go.sum 锁定；纯算法小库，必要时可整体 vendor 冻结自查（项目现无 vendor 目录，不预先引入）。

**桌面分发不允许要求/捆绑系统 git**：git `merge-file` 外壳（xdl_merge/zdiff3 最成熟但进程外+Windows 分发负担+stdout 反解析）出局；go-git 无内容级三方合并（调研已排除）；git2go 走 CGO 早已排除；自研否决。评估权重：稳定性 > 维护 ≈ 易用 > 周期。

## 2. 文本合并路线与保真口径

**文本级 diff3 合并，未冲突区域字节级不变**；合并结果用既有 BurntSushi/toml 解码做**合法性校验**（只读一遍，零新增依赖）。未冲突区域不碰字节 → 手工注释、键序、空行、缩进天然保真。packwiz 官方自己重写 pack.toml/index.toml 时就是结构化重写丢注释（上游把这两个文件当生成物）；本项目把含用户手工注释的 modlist 当文本，是唯一保护路径。

**验收口径＝「未冲突区域字节级不变」**（hunk 之外前后 diff 为零，机器可测，进验收规格自动断言），不取「合并输出保留注释」的人工宽口径。

保格式结构化编辑（go-toml v2.5 `unstable/edit`、tomledit）列观察项，发布/成熟前不引入。

## 3. 冲突块表示：每资源一行 + detail JSON

`conflicts` 表主键 `(plan_id, resource_id)` 不动，**一个文件一行**；文件内全部冲突块打包成数组进既有 `Conflict.Detail`（JSON：每块 A/O/B 侧行片段 + 各侧起始行号）。零 schema 变更；「无损存储、有损查询」——SQL 层不按块过滤，UI 读 detail 解析呈现（一个文件一张冲突卡，点开看块列表）。改 PK 每 hunk 一行的方案否决：没有 SQL 层按块查询的消费方，且值级冲突无法从行级结果零损还原；将来真要升级，detail 结构是既定契约、路径清晰。

## 4. 计划面第五操作与授权模式口径

- `Classification` 新增 **`merged_clean`**：双侧同改（digest 不同）、diff3 零冲突块、且合并结果通过类型校验（§5）。
- `ResolutionChoice` 新增 **`take_merged`**，作为 merged_clean 行的默认推荐选择。
- **`merged_clean` 属非冲突操作**：授权模式（ADR-0005 §4）开启时随「批量执行全部非冲突操作」免确认执行，快速更新一键面自然收编 merge 行。
- 含冲突块 → 既有 `conflict_modify` + detail 承载块数组，**永不进自动面**（红线不动）。
- `converged`（双侧 digest 相等）在 diff 层先行拦截，不走 merge；`conflict_delete_modify`（一边删一边改）不适用三方文本合并，维持现状由用户在删/保留里选侧。

## 5. 永不合并边界与结果校验（黑名单）

永不合并只有两类：**二进制资源**（按行合并无意义）、**`.index` 元数据**（ADR-0005 §5 只读不写，不在写面）。其余文本资源（pack.toml、mods/*.pw.toml、config 下 toml/json 等）默认可合并。

合并结果合法性校验按资源类型分派：toml→BurntSushi 解码、json→标准库解码、其余纯文本→不校验。**校验失败＝合并提议不成立**，降级 `conflict_modify`（块证据保留在 detail），不得把语法残缺的 merged_clean 写出去。

## 6. Packwiz 条目子资源化：不做

index.toml 整文件一个 resource，不把 `[[files]]` 条目拆成子 resource_id。mods/*.pw.toml 天然一 mod 一文件，高频合并场景已被文件粒度天然覆盖；条目级拆分要重构 digest/分类/冲突三层，而 index.toml 双侧同改本身低频（packwiz 自管、可重算）。

## 7. 决议粒度：整文件取侧 + 手动兜底

`ResolutionChoice` 维持整文件取侧（take_project / take_runtime / take_merged / skip / manual…），**逐冲突块选择 P4 不做**——价值集中在「一个文件多处真冲突」的低频场景，manual（用户外部编辑后再扫描收编）今天就能到达同样结果；冲突块明细在 UI 只读展示辅助决策。逐块交互组件留给后续版本按真实频率再议，不是 P4 承诺。

## 8. 产物管线：计划期出证据，暂存期重算

- **计划期**：跑 diff3 只为出分类与块证据（detail JSON），不落任何文件字节。
- **Apply 暂存期**：按计划锁定的三侧内容快照**确定性重算**合并（同算法同输入同输出，文本量小重算比缓存便宜），字节写入本次运行的 staging，之后 ownership proof、验证、提交、暂存清理与 ADR-0004 恢复协议**全部走既有管线，零新增环节**；ConfirmPlan token 幂等重入对 merged 行同样适用。
- 执行时端点字节与计划快照不符 → 既有前置条件拦截，不部分提交。

## 9. 保全与回滚：合并产物一律入 CAS

合并产物是**本地合成内容，远端不存在、永远无法重取**——after 字节**一律写入 CAS 保全**：

- 非 mod 文本＝现状（本就进库）；
- **mod 的 metafile＝ADR-0005 §7 文本例外**：红线意图是不为几十 MB 的 JAR 开缓存，metafile 层实测 ≈ jar 层 0.008%（300 mod 共 88.8KB）；本地合成内容无重取通道是硬事实，不保全则回滚到含 merge 的提交时永远降级「用户提供」，说不通。JAR 红线不动。

回滚到含 merge 的提交：merged 行 `restorable_from_cas`（取字节→暂存→原子写，零网络零用户介入）。Apply 崩溃恢复＝既有四路裁决，staging 完整即幂等 redo，无需新机制。mod metafile **非合并路径**的重取缺口（write_project 无内容源）归 [#77](https://github.com/skyraah/PackGradle/issues/77)，本票不开面。

## 后果

- merged_clean / take_merged / detail 块数组 / 错误码与事件面 → 契约 07（#75）。
- 验收场景（未冲突区域字节级不变断言、授权模式含 merge 行、metafile 入 CAS 断言、校验失败降级 conflict）→ P4 验收规格（#76）。
- 引擎接入（go.mod pin commit）、adapter 实现与施工面涌现需求 → 图外执行规格沿 P2/P3 模式。
- 观察项：go-toml v2.5 `unstable/edit`、tomledit（保格式结构化编辑，成熟后重评路线 B）。
- CONTEXT.md「授权模式」「快速更新」词条的「非冲突操作」集合显式扩至 merged_clean（词条措辞不动，语义自然包含）。
