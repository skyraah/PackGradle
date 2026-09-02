# P4 前置调研：回滚 redownload 行 metafile 漂移时 write_project 无内容源（重取语义缺口三出口事实面）

> 状态：research 票 [#68](https://github.com/skyraah/PackGradle/issues/68) 供数。**决议已收口 = [ADR-0012](../adr/0012-metafile-content-capture-and-restore-degradation.md)**（票 [#77](https://github.com/skyraah/PackGradle/issues/77)，父图 [#70](https://github.com/skyraah/PackGradle/issues/70)）：出口①为主（扫描期捕获入 CAS）＋③收编存量降级（`no_project_content`）＋②判不可行；九决议点归位见 ADR 各节。
> 证据采集日 2026-09-02（main @ `9bbe7b9` 工作树实读；体积数字来自 `docs/acceptance` 既有产物本机实测）。本文只答事实面与代价，不替 #77 选边。

## 1. 缺口的确切机制（现状代码事实）

### 1.1 触发条件

回滚计划某 mod 资源行同时满足：

1. **metafile 漂移**：目标 commit 的项目侧表示与当前观测语义不同（`restore.go` buildRestoreDraft 逐侧 `SemanticDigest` 对照；mod 侧摘要键 = identity + version + side + 声明 hash（算法+值），`internal/core/normalize/semantic.go`。file-id 不进摘要）；
2. **CAS miss 且有 CF 重取信息**：判定矩阵（`internal/application/sync/restore_matrix.go`）判为 `redownload_required`——rec=redownload（mod 缺省，`apply_actions.go` defaultRecoverability）∧ 重取信息实存（目标基线项目侧 `cf_file_id`+`filename`，`restore.go` redownloadInfoOf）∧ 声明 hash 可验 ∧ CASReady=false（全部写回侧 digest 逐侧 `CAS.Has` 通过才算 ready，`restore.go` judgeRestoreRow）。

此时该资源产生两个写回操作：`write_runtime`（jar）+ `write_project`（metafile）。

### 1.2 三内容源分派与失败路径

执行期内容来源按行 marker 分派（`internal/application/sync/restore_apply.go` deriveRestoreFilePlans）：

| 分支 | 条件 | 对 metafile 侧（write_project）的覆盖情况 |
|---|---|---|
| 用户补全字节 | `item.StageRel` 非空 ∧ `ExpectedDigest == fp.afterDigest` | 拒之门外：StageUserObject 仅对 `user_object_required` 行合法（`restore.go`，`err.userobject.not_required`）；且 ExpectedDigest 取 runtime 侧摘要优先（jar），与本操作 afterDigest 不同值（代码注释自证「mod 的 jar 摘要不会匹配 metafile 侧操作」） |
| CF 重取 | marker=redownload ∧ **`tgtSide == SideRuntime`** ∧ Redownload 非 nil | **明确不含 project 侧**：CDN 上只有 jar 没有 TOML（包注释：「project 侧（metafile）不在 CDN 上、mod 字节不进 CAS（ADR-0005 §7），其目标内容缺失即操作失败」） |
| CAS 写回（default） | 其余一律 `afterFromCAS = afterDigest` | metafile 目标 digest 在 CAS 必然 miss：mod 资源 rec=redownload → before-preserve 从不保全（`syncstage/preserve.go` RequiresCASBackup 对 redownload 返回 false），after 字节也从不进 CAS（CAS 的唯一写入点是 PreserveBeforeContent，全仓核实） |

失败落点：`afterContentReader` 打不开 CAS 对象 → `stageOneRestoreOperation` 以 `resultContentUnavailable`（字面 `content_unavailable`，`apply_actions.go`）失败 → 首个失败行驱动**整场 failed 终局**（restore 无剔除语义：staging 相位任一操作失败 ⇒ run=failed + task=failed + Problem 承载原因码，零部分提交、不进 recovery_required，契约 06 §6 Q8）。

### 1.3 就绪面与决议面的现状（缺口比「缩水」更深）

- **exact 就绪面谎报**：`restoreExactReady`（`restore.go`）把 `redownload_required` 一律计入就绪（就绪 = cas ∪ redownload ∪ (user_object ∧ staged)）→ 含缺口行的计划 `ExactFeasible=true`，exact 决议不被 `err.restore.exact_infeasible` 拦截，**确认后运行确定性失败**。
- **skip 非法**：`ResolveRestorePlan` 的 skip 合法集 = 未补全的 `user_object_required` 行 ∪ `unrecoverable` 行；`redownload_required` 行 skip → `err.restore.skip_invalid`。即 allow_partial 也无法剔除该行。
- **补全拒收**：StageUserObject 见 1.2，`err.userobject.not_required`。
- 三者合并：**该行出现在计划中 ⇒ 本次回滚在产品内无任何完成路径**（除非用户在项目端手工把 metafile 改回目标语义后重新 prepare）。这与 triage 评论「仅 allow_partial+skip 可走」的表述不同——skip 对 redownload 行非法，是实测代码口径；#66 验收夹具的绕开方式（packwiz 降版/删 jar 造数、只造 runtime 侧漂移）正说明五场景全避开该组合。
- 唯一近似出口：CF 探测 404/403 会把行降标 `user_object_required + cf_unavailable`（`restore.go` probeRestoreItems）——降标后 skip 变合法，但补全通道验的是 jar 摘要，metafile 侧依旧无源（且引出 1.4 的错写隐患）。

### 1.4 根因：baseline 对 metafile 既无字节、亦无自身摘要

- **扫描器不落 metafile 字节摘要**：packwiz 扫描器 mod 分支（`internal/adapters/packwiz/scanner.go`）只抽取 7 个元数据键（display_name/version/side/declared_hash_format/declared_hash/cf_file_id/filename），Representation **不带 Content**——ScanOptions 里的 `HashFile` 闭包（含 hash cache）只被 managedfiles（text/binary 文件）消费，mod 分支不调用。因此快照与 result baseline 的项目侧 mod 表示是纯语义投影。
- **写回摘要的兜底是 jar 摘要**：`restoreTargetDigest`（`restore.go`）Content 优先、声明 sha256 兜底——对项目侧 mod 表示，兜底取到的是 `[download] hash`（**jar 内容摘要**，非 metafile 自身）。两个子情形：
  - 声明 hash 非 sha256（CF metafile 现实中多为 murmur2/sha1）：项目侧目标摘要为空 → `write_project` 无 ObjectRefs → derive 期即 `blockedCode = content_unavailable`（`restore_apply.go`）。
  - 声明 hash 为 sha256：`write_project` 的 afterDigest = **jar 摘要（误标）** → 走 CAS miss 失败；特别的，若该行已被探测降标且用户补全了 jar 字节，用户补全分支的摘要比对会**通过**（ExpectedDigest=jar 摘要=afterDigest），把 **jar 字节写进 metafile 路径**，直到 verifying 复扫才因「项目侧观测无内容指纹」判 violation → verify_mismatch → recovery_required。即现状存在一条「先错写、后验证拦截」的潜在路径（字节已落盘）。
- **结论**：缺口不是某条分支写漏，而是三源（CAS/CDN/用户暂存）的数据面都不携带「metafile 目标字节」——CAS 因 ADR-0005 §7 从不收，CDN 没有 TOML，用户通道的验收摘要锚在 jar 上。

### 1.5 测试覆盖现状

`restore_t60_test.go` 的 redownload 主角行（chrono）造数方式是「jar v2 + .index 声明 v2（运行侧语义漂移）」——**metafile 不动**，writeSides 只含 runtime，重取路径干净走通。`restore_t59_test.go` 的 chrono（metafile 漂移、sha256 声明）只断言到 prepare 的标记面，未走到执行。即：缺口组合（metafile 漂移 × redownload）在现有测试中没有执行期覆盖。

## 2. 出口①事实面：result baseline 存 metafile 字节

### 2.1 体积实测（docs/acceptance 既有产物）

生产规模 fixture（验收规格 §2.1 的 3,000 受管资源 = 300 mod + 2,400 文本/二进制；`build/perf/fixture`，perffixture 确定性重放产物）：

| 项 | 实测 | 说明 |
|---|---|---|
| metafile 数量 | 300 个（`mods/*.pw.toml`） | 混合 modrinth/curseforge/url 三来源 |
| metafile 总字节 | **88,810 B**（均 296 B/个，样本 246–312 B） | `cat mods/*.pw.toml \| wc -c` |
| index.toml | 42,023 B | 300 条 `[[files]]` |
| pack.toml | 187 B | — |
| 项目侧 packwiz 层合计 | ≈ 131 KB | 三者之和 |
| 同规模 runtime jar 层 | ≈ 1.0–1.1 GB（期望值） | jarSize 分布：90% 200KB–5MB、10% 5–20MB（perffixture.go） |
| 真网单 jar 对照 | 1,409,495 B / 1,778,129 B / 11,162,987 B | p3 验收报告 §6 三黄金向量 |

换算：**整个 300-mod 包的 metafile 层 ≈ 单个中型 jar 的 1/16–1/126，≈ jar 层整体的 0.008%**。真实包规模外推：大包 600–1000 mod、真实 pw.toml 带 x- 字段/注释约 0.5–1 KB/个 → 全量 metafile 快照 0.3–1 MB/提交量级。若走 CAS（内容寻址、跨提交天然去重），每提交只有**变更过的** metafile 产生新对象——典型上游 pull 触碰个位数 mod，增量是 KB 级。

小样本交叉验证：`build/headless/fixture`（6 mod）mods 目录 1,772 B ≈ 295 B/个，与 perf fixture 一致。

### 2.2 ADR-0005 §7 边界辨析（两读法各自的文本证据）

§7 原文关键句：「**mod 资源字节一律不进 CAS**。下载的 after 字节与删除/覆盖的 before 字节均不留 CAS 副本（**不提供 JAR 缓存**）；数据库只登记 identity/hash/重取信息。mod 资源的恢复补偿与回滚统一走「**远端重查 → 重新物化落盘**」，失败降级用户提供/不可恢复。」

| 读法 | 依据 | 推论 |
|---|---|---|
| **字面读**：mod 资源的任意侧字节 | BaselineResource 是资源级（一个 mod 资源挂 project/runtime 两个表示，`model.go`）；metafile 是 mod 资源的项目侧表示 | metafile TOML 入 CAS 破 §7 字面，需 ADR 明文修订边界 |
| **目的读**：针对 JAR 二进制 | ① 括注「不提供 JAR 缓存」；② 成本论证全程 JAR 量级（后果节「用磁盘负担换」，评估权重 稳定性>维护≈易用>周期）；③ §7 承诺的恢复通路「远端重查→重新物化」**对 metafile 事实不可用**（CF file-id 直链只解析 jar，CDN 无 TOML）；④ ADR-0006 §5「packwiz 管理的 mod：不保全，**丢失后可重取**」的可重取性同样只对 jar 成立 | §7 的让渡对象是「CDN 可取回的二进制」；metafile 是远端（pack git 仓库）未集成的文本清单，落在 §7 的盲缝上——正是本缺口 |

另两条相邻事实：ADR-0005 §1 把「版本决策（写 metafile）」划给 packwiz CLI/pack 仓库，PackGradle 正常 sync 从不写 metafile（项目侧是源），因此 §7 论证中「下载的 after 字节」「覆盖/删除的 before 字节」两口径都只考虑过 runtime 侧；ADR-0007 §7 已有「非 mod 单文件 >32 MiB 不保全」的先例——按**体积**切保全边界在本项目 ADR 体系里有直接先例可循。

### 2.3 实现面事实（若选此出口）

- **捕获时点**：扫描期即可——ScanOptions 已携带 `HashFile` 闭包（含 hash cache），mod 分支接入是 O(metafile 字节) 的小改；或提交期复扫（buildVerifiedBaseline 从复扫快照取）。扫描期捕获还能顺带修正 1.4 的「摘要缺席/误标」根因。
- **载体**：CAS 对象按 digest 寻址——`afterFromCAS` 写回分支、`restorable_from_cas` 判定、StageContent 复核全部现成；baseline 只需 Content 指针（结构已有 `Content *ContentRef` 字段位）。备选载体是 baseline JSON 内联（进 SQLite、BaselineDigest 规范化需处理二进制/编码，且无去重）。
- **GC 耦合**：ADR-0006 §10.1 已有硬约束「GC 不得回收被任何 sync_commits.result_baseline 传递引用的对象」——metafile 对象入 CAS 后自动落进既有保护根语义；代价是保留窗口内逐 commit 的活对象集变大（但见 2.1：KB 级增量）。ADR-0007 §4 保护根集的实现（引用图断言器）是否需要把 baseline 的新引用形态纳入断言，属执行票核对项。

## 3. 出口②事实面：写回前从项目端现取目标 metafile

### 3.1 目标版本必然不在工作树（定义性结论）

缺口的行前提就是「当前项目侧 metafile ≠ 目标」（语义级）。工作树每个 metafile 只有一个当前版本，**「从项目端目录读目标版本」在该行前提下必然读到非目标字节**。metafile 的外部写者（CONTEXT.md「上游变更」术语；代码核实）：

1. **git 拉取**：packwiz 官方定位 "git-friendly TOML format"（docs/research/p4-merge-libraries.md §3 核实）——上游协作默认 git 工作流，pull 后工作树只剩新版，旧版只存在于 `.git` 历史；
2. **packwiz CLI 版本决策**：可经 PackGradle 已有的 subprocess 集成触发（`internal/packwiz/cli.go`：RunRefresh/RunCheckUpdates/RunUpdateMods）或用户手动执行——写完即覆盖，旧字节同样消失；
3. **手工编辑**：同理。

PackGradle 现状对项目端的全部假设 = 「以 pack.toml 为根的目录」（scanner 仅探测 pack.toml/index.toml 存在性）；**全仓零 git 集成**（无 exec git、无 go-git 依赖），BindingFingerprint 是路径级，baseline/commit **不携带任何 git commit 映射**。项目端不是 git 仓库（导出包/解压目录）的情形无法排除。

### 3.2 现取源盘点

| 现取源 | 可行性事实 | 风险面 |
|---|---|---|
| 工作树直读 | 该行前提下定义性不可行（读到的就是漂移后的当前版） | — |
| `.git` 历史读取 | 技术上字节可取（`git show <rev>:<path>`），且 baseline 的目标 digest 可作验收锚 | 三个前置缺口：①项目端可能非 git 仓库；②baseline 无 git commit 映射（不知道目标对应哪个 rev，需按 digest 在历史里搜）；③PackGradle 需新增 git 依赖面（subprocess 或 go-git——p4-merge-libraries.md 已盘点 go-git v5 纯 Go 可用）与历史被 rewrite/prune 的窗口 |
| packwiz 再生成 | subprocess 先例已有（cli.go）；按 file-id 重装指定版本可重产 metafile | **字节精确性无保证**：目标字节由某个历史时点的 packwiz 版本/格式生成，再生成结果与目标 sha256 不符即失败（当前写回契约按字节 digest 验收）；且需网络、需可执行文件在场 |
| 语义重建（从 baseline 元数据键重组 TOML） | baseline 只有 7 键语义投影，非全文 | 字节精确性更无保证；等价于改验证口径为语义级（见决议点 5） |

### 3.3 一致性窗口与失败语义

- **窗口依赖**：即便取数源成立，prepare→apply 期间项目端仍可被外部写者推进（上游写者不持 PackGradle 的关系锁）。既有机器已覆盖该窗口：操作前置条件断言 prepare 时点现状（`verifyApplyPreconditions`，漂移即 staging 失败），取数字节再过 StageContent 的 digest 复核——出口②的取数失败可完全套进既有「staging 相位失败 ⇒ 整场 failed 终局」容器，失败语义与今天的 content_unavailable 同形，只是成因不同（是否需要专码属 #77）。
- **双端配对一致性**：redownload 行的 jar 重取以目标 metafile 的声明 hash 为验收锚（RedownloadInfo 取自目标基线项目侧元数据）——若 jar 侧成功、metafile 侧失败，磁盘上会留下「目标版 jar + 漂移版 metafile」的错配对；现状该错配由 relation 保持 dirty + 下轮扫描差异重现兜底（但如 1.3 所述，现状根本走不到这一步——整场 failed）。

## 4. 出口③事实面：显式降级语义的现状成本

### 4.1 现状失败路径（逐步）

prepare：行被判 `redownload_required`（乐观）→ 探测只探 jar 直链（该组合下 jar 可达性与此缺口无关，探测定不了 metafile 侧）→ exact_feasible=true。resolve：exact 放行；skip 拒绝。confirm → 运行：staging 相位 `write_project` 操作因内容不可得失败（非 sha256 声明子情形在 derive 期即 blocked）→ **整场 failed 终局**：run=failed、task=failed、Problem=`content_unavailable`、零提交、关系健康不动、同 plan 可重 Confirm（结果同样确定失败）。用户可见的全部信息是一个通用 `content_unavailable`——不区分「CDN 挂了」与「metafile 侧根本无源」。

### 4.2 marker_reason 枚举与影响点

现状三值（`internal/core/model/restore.go`，契约 06 §3.2，仅 user_object_required 行非空）：

| 值 | 语义 | 赋值点 |
|---|---|---|
| `no_redownload_info` | 无重取信息 / rec=cas 但 CAS 缺失 | restore_matrix.go judgeRestoreMarker |
| `cf_unavailable` | CF 探测 404/403，prepare 时点降标 | restore.go probeRestoreItems |
| `hash_format_unsupported` | 声明 hash 引擎不可验（murmur2 等） | restore_matrix.go |

新增第 4 值的触及面（事实盘点，非推荐）：`internal/core/model/restore.go` 枚举 → 契约 06 §3.2 注释 → `internal/transport/dto.go` 投影注释 → 前端 locale 文案（marker_reason 的展示先例 = waystones 行降标原因透出，契约 06 §5）。**无 schema 变更**（marker_reason 是 plan_json 内的字符串字段）。

### 4.3 prepare 时点确定性可判

该组合在 prepare 期是**纯静态可判**的（marker=redownload ∧ 写回侧含 project ∧ 项目侧目标内容无源——判定输入全部在目标基线与 CAS 探测里），与 CF 探测的「unknown 不阻塞」不同类：不存在「乐观标记、运行期才知道」的不确定面。这意味着「prepare 时点降标 + 专码」在机制上零探测成本，事实可行；是否这么做归 #77。

### 4.4 出口③的隐藏前置

最小版 C（专码 + UI 承认手工）不需要任何数据面改造；但「让用户真的能补全」需要 expected_digest 以 **metafile 自身摘要**为口径——而现状 baseline 根本没有这个摘要（1.4），且 sha256 声明子情形的 jar 摘要误标会让补全通道验收错锚（1.4 的错写隐患）。即：**三个出口里有两个半（A、C 的完整形态、B 的字节验收形态）共享同一个前置——扫描/baseline 捕获 metafile 自身 sha256**。

## 5. 三出口交互面汇总（描述性，不选边）

| 影响面 | 现状 | 出口①（baseline 存字节） | 出口②（项目端现取） | 出口③（显式降级） |
|---|---|---|---|---|
| 四标记矩阵输入维度 | Rec/HasRedownloadInfo/HashSupported/CASReady 四输入 | 无新维度（CASReady 自然为真） | 需新维度「project 侧目标内容可取性」（源探测） | 无新维度（降标复用 user_object + 新 reason） |
| exact 就绪面公式 | redownload 一律计就绪（谎报） | 缺口行转 restorable_from_cas，公式不变 | redownload 就绪定义需按写回侧细化 | 缺口行转 user_object，公式自然正确 |
| restore failed 终局 | 确定性整场 failed | 缺口消失 | 取数失败沿用 failed 终局（可加专码） | prepare 降标后不再进运行 |
| 数据面前置 | — | 扫描/提交期捕获字节与摘要 | 摘要（字节验收形态）或语义验收（改契约） | 完整形态需摘要；最小版零前置 |
| 正确性修正 | jar 摘要误标 + 错写隐患存在 | 随摘要捕获一并消解 | 同左（字节验收形态） | 需独立修正（否则补全锚错） |

## 决议点清单（留给 #77 的 ADR 拍板）

1. **§7 边界解释权**：ADR-0005 §7「mod 资源字节不进 CAS」按字面（资源任意侧字节）还是目的（JAR 二进制）读——metafile TOML 入 CAS 是否构成破线、是否需 ADR 明文修订或边界注记（2.2 两栏证据）。
2. **字节/摘要捕获时点与载体**（若决议需要）：扫描期（HashFile 闭包接入 mod 分支）vs 提交期复扫；CAS 对象（digest 寻址、写回路径全现成、跨提交去重）vs baseline JSON 内联（无去重、BaselineDigest 规范化影响）。
3. **GC/保留窗口耦合**：metafile 对象若入 CAS 是否直接落进 ADR-0006 §10.1 传递引用保护根；保留窗口内逐提交增量（KB 级）是否需要纳入 ADR-0007 的容量口径。
4. **出口②的取数源裁决**：git 历史读取（引入 git 依赖面 + baseline 无 commit 映射 + 非 git 项目存在）vs packwiz 再生成（subprocess 先例已有但字节精确性无保证）vs 判定出口②整体不可行。
5. **出口②的验证口径**：字节级（需摘要前置）vs 语义级（改写回契约与 verifyRestore 的复验面）。
6. **出口③的形态**：marker_reason 第 4 值的命名与降标时机（prepare 确定性降标 vs 保持乐观标记仅改 UI）；降标后是否把该行纳入 skip 合法集/开放 StageUserObject。
7. **正确性独立修正项**：sha256 声明子情形的「jar 摘要误标 → 用户补全路径可把 jar 字节写进 metafile 路径（verify 才拦截）」——无论出口如何都需修正（afterDigest 口径或补全分支的侧别匹配），是否随 ADR-0009 一并立项。
8. **exact 就绪面修订**：双侧写回的 redownload 行就绪定义是否细化为「runtime 可取 ∧ project 侧有源」，或引入新就绪态——现状的 exact 谎报（resolve 放行、运行必败）无论出口如何都要修。
9. **存量数据兼容**：既有 baseline（无 metafile 摘要/字节）上对旧提交发起回滚的过渡语义（保持现状降级 / 声明该组合不支持 / 提示重扫后再回滚）。
