# Phase 3 验收口径与回滚/下载/GC 场景验收（P3-E2E）

> **状态：已决议（验收规格）。** 2026-09-01 经决策图票 #55 grilling 拍板定稿（七决策点全按推荐；Q4 经用户追问修订为「注入 + 真网冒烟」两层口径）；执行会话按本规格执行验收，不再做隐式决策。
> 来源：决策图「PackGradle Phase 3 决策图（回滚·CAS GC·下载物化）」票 [#55](https://github.com/skyraah/PackGradle/issues/55)。
> 依据：[P2 验收规格](p2-acceptance-spec.md)（骨架沿用基准）、架构篇「Phase 3 必须满足」四条、[ADR-0006](../adr/0006-restore-rollback-semantics.md)（回滚事实模型）、[ADR-0007](../adr/0007-cas-retention-gc.md)（保留/GC 权威）、[ADR-0008](../adr/0008-cf-download-materialization.md)（下载物化细案）、[契约 06](../contract/06-p3-restore-contract.md)（验收对象面）。

## 0. 两个口径（沿用 P2 §0）

- **A = 后端单独验收**：headless/transport 边界，前端未齐时可先行、可多轮执行。
- **B = Phase 3 完整验收**：= A + `frontend:build` + L1 增量清单全勾 + **P1/P2 双回归不回退** + 真网冒烟已执行并记录（§4.2，结果不设通过门槛，判因记录强制）。**B 是 Phase 3 完成门槛**——B 通过即 Phase 3 可标完成。

验收在 Windows 开发机执行，不建 CI；报告必须记录机器规格（§7）。

## 1. 分层与自动化边界（沿用 P2 §1）

**L0 = 无人值守自动化层**（headless，可重复执行，命令集挂 Taskfile）；**L1 = 真实窗口手工清单层**。不引入窗口驱动自动化与组件自动化——技术债注记沿用 P1 §5.5。覆盖边界原则沿用：headless 能验的不上窗口层；窗口层只验 headless 验不了的。

### 1.1 L0 命令集

| 命令 | 内容 | A | B |
| --- | --- | --- | --- |
| `task test` | `go test ./...`（**增量**：四标记矩阵、失败分桶、直链黄金向量、GC 决策函数、CAS 篡改、探测超时，§2/§3/§5） | ✅ | ✅ |
| `task test:vet` | `go vet ./...` | ✅ | ✅ |
| `task test:race` | `go test -race ./...`（硬门槛，gcc 前提沿用） | ✅ | ✅ |
| `task acceptance:headless` | P2 `-apply` 两遍照跑（回归）+ **扩展 `-restore`**：四场景断言链（§3.1） | ✅ | ✅ |
| `task acceptance:recovery` | P2 apply 强杀×5 照跑（回归，§2.1 不变式沿用） | ✅ | ✅ |
| `task acceptance:recovery:restore` | **新增**：restore 运行强杀×5，覆盖 staging 下载/applying/verifying 相位（§4） | ✅ | ✅ |
| `task acceptance:download` | **新增**：本地假 CDN 失败注入链（§5.1） | ✅ | ✅ |
| `task acceptance:gc` | **新增**：GC 引用图正反用例链（§6） | ✅ | ✅ |
| `task acceptance:perf` | P2 三门槛照跑 + **restore/GC 新门槛**（§7） | ✅ | ✅ |
| `task frontend:build` | `vue-tsc --noEmit` + Vite 生产构建（口径沿用） | — | ✅ |

### 1.2 L1 手工清单（B 口径，契约 06 §9 全量 + 授权编排专项，逐项勾选）

**回滚链路**
- [ ] 回滚入口唯一：历史**详情页**主操作「回滚到此状态」（`restore_preview` 门控）；head 详情禁选（横幅说明）；历史列表行无入口
- [ ] 计划页结构 B 单表全列（资源/判定/CF 可用性/处理说明 + 顶部计数条）；draft 只读 → 决策（exact/allow_partial + skip）→ resolved 确认
- [ ] 用户对象补全对话框：busy（校验中）→ ready（「已校验 · 字节就绪」）/ miss（`err.userobject.hash_mismatch` + 重选重试）三态；全部就绪且无 unrecoverable ⇒ exact 解锁、横幅转绿
- [ ] 确认框四要素：①删除损失面（含不可找回/不留存警示行）②CF 重取失败＝整场退出可重试不进崩溃恢复 ③回滚恒人工确认 ④成功产生新记录、历史不改写
- [ ] 双警示渲染：`deletion_warn`「不可重取」/ `preserve_skip`「旧版本不留存」
- [ ] restore 任务：任务中心进度短语（`msg.task.restore.*`）、可离开页面、committed 后历史新增 kind=restore 记录
- [ ] 计划 stale/expired 路径：过期计划页给重 prepare 引导，不白屏

**GC/设置**
- [ ] 设置页「保留策略」节：五参数编辑 + 越界整体拒绝文案（`err.settings.retention_invalid`）+ 「立即回收空间」建 gc 任务
- [ ] gc 任务排队文案「等待空闲时段（安全窗口未开 · 自动续排）」（安全窗口关闭态）；gc 完成后历史列表墓碑行「更早 N 条提交已按保留策略清理」，N=0 不渲染

**快速更新与授权模式（新交互流专项）**
- [ ] 工作区详情设置区授权开关（`SetWorkspaceAuthorized`）→ 概览主操作区「快速更新」入口点亮（`quick_update` 门控）
- [ ] 授权模式全编排一次点击链：扫描 → 计划 →（`confirmation_requirements` 空）免确认直达 committed；含冲突 → 转待确认计划页走 P2 确认流
- [ ] 授权开关关闭 → 入口灰置 `err.auth_mode.disabled` 文案；restore 计划恒带 `restore_acknowledge` ⇒ 授权模式下回滚仍必经确认页（零特判口径的人工面）

**恢复流回归**
- [ ] restore 运行产生 `recovery_required`（用 §4 真实强杀产物）：恢复详情页复用 P2 投影、恢复期间 `prepare_restore`/`quick_update`/`apply_sync` 入口均不可用（`err.recovery.in_progress`）

**P1/P2 回归不回退**
- [ ] P2 §1.2 应用链路与恢复流各抽验一例；P1 §1.3 事件/互斥/locale/无 legacy 复活抽验

## 2. 架构篇 P3 四条红线场景化（全落 L0 硬断言）

| 红线（架构篇原文） | L0 断言落点 |
| --- | --- |
| ①所有声称可恢复的文本对象都能从 CAS 校验并恢复 | 范围＝CAS 存量文本对象（mod 字节本就不进 CAS，ADR-0005 §7）。restore 链路 committed 后逐字节+digest 复验（fixture 同 seed 重放比对）；单测：CAS 对象字节篡改 → 校验失败拒绝写回 |
| ②Restore 对不可恢复 mod 明确阻止 exact 或要求选择 partial | 四标记判定矩阵单测（ADR-0006 §2 全格，含负例「重取性看数据不看出身」「CAS miss → user_object_required」）；headless 场景 2/3（§3.1）：`exact_infeasible`+`blocked_by` 断言、`StageUserObject` 补齐 → `ExactFeasible` 翻转、exact 遇未就绪 → `err.restore.exact_infeasible` |
| ③GC 不删除活跃 Commit、Baseline、Plan、staging 引用 | §6 保护红线三正例 + 引用图不变式（GC 后 CAS 存活集=可达闭包∪隔离区，超集为零） |
| ④降级恢复不会被标记为 exact success | headless 场景 2：partial restore → commit=`partial` + relation 保持 dirty；下载失败 → 整场 `failed` 零 commit 不进 recovery（§5.1 场景 3） |

## 3. restore headless 链（`-restore` 四场景，离线面无 CDN）

fixture 沿 pgfixture 确定性生成器（同 seed 重放特性是全部逐字节断言的底座）。

1. **exact 经 CAS**：apply×2 建两轮历史 → `PrepareRestore(commit_1)` → 断言 draft `RestorePlanDTO` → resolve exact → confirm → 轮询 committed → 逐字节+digest 复验 → 断言 `ListCommits` 新增 kind=restore 行且**原历史记录不改写**（ADR-0006 §6）；
2. **partial（红线④）**：构造含 unrecoverable 行的计划 → resolve `allow_partial` + skip → confirm → committed kind=`partial` + relation dirty；
3. **补全就绪面（红线② + 契约 06 §3.5）**：构造含 `user_object_required` 行计划（夹具提供 CAS miss 构造路径，如直删 CAS 对象）→ prepare 断言 `exact_infeasible` + `blocked_by` → `StageUserObject` 提供错字节 → `err.userobject.hash_mismatch`；提供对字节 → `staged=true`、`ExactFeasible` 翻转 → resolve exact 成功 committed；
4. **重做语义（ADR-0006 §1）**：restore 目标=head 提交（API 合法，UI 禁选仅防误触）→ 反向差异计划合法收口 committed。

夹具需求：pgfixture 增「无 CF 声明 mod」变体（unrecoverable/deletion_warn 行来源）。

## 4. 回滚中断强杀（`acceptance:recovery:restore`）

沿 P2 §2.1 注入法：restore 子进程**随机时机**（种子入记录，覆盖 staging 下载/applying/verifying 相位；假 CDN 由 harness 拉起供给子进程）`taskkill /F` → 重启走恢复管线（ADR-0004 §4）→ 断言不变式：

1. P2 四不变式照用：无部分完成假象（终局∈ {`committed`, `recovery_required`, `failed`}——staging 下载期**网络失败**才 `failed`，**崩溃**走恢复矩阵）、收口后重扫 diff 归零、收口后可重跑、重复恢复幂等；
2. restore 特有：staging 下载期被杀 ⇒ 重启后 `.part`/用户对象 staging 随暂存目录按 ADR-0004 恢复矩阵处置，绝不出现假 committed；kind=restore 终局后历史不改写。

轮数固定 5；不做逐轮裁决断言（随机时机下裁决本就不定，不变式才是硬门槛）。P2 `acceptance:recovery`（apply×5）原样保留作回归。

## 5. 下载失败注入与真网冒烟（两层口径）

**分层原则**：确定性故障逻辑（分桶/重试/续传/hash 校验/并发）只有注入能精确复现——真网时机随机无法按需触发断言；真网不确定面唯余「CDN 对 URL 形状的持续认可」（直链构造公式已由研究分支实测钉死+黄金向量固化；现役 packwiz CF metafile 全 sha1，在 v1 校验集内，ADR-0008 §2）。前者 L0 硬门槛，后者手动冒烟。

### 5.1 假 CDN 注入（L0，零真网）

- **假 CDN**＝本地 HTTP 服务（`pgfixture -serve` 或等价小进程），脚本化故障：404 / 403（频控体形状）/ 429 / 503 / 连接拒绝 / **半截断流**（Content-Length 截断 → 触发 `.part` Range 续传，假 CDN 记录 Range 头证续传发生）/ 错误字节（hash 不符）。pgheadless/pgrecovery 以 `-cdn <url>` 参数接入。
- **单测族**（表驱动）：失败分桶矩阵——429/503→`rate_limited`、403 体嗅探频控→`rate_limited`、404→`unavailable`、超时/断连重试 4 次耗尽→`network`、hash 不符→全量重取一次→仍败 `hash_mismatch`；直链构造黄金向量（整数除法不补零、8 位 fileID 越界记日志）；`[download] concurrency` 1–16 越界校验；CF 探测慢响应（>5s/预算 10s 耗尽）→ `availability=unknown` 不阻塞。
- **`acceptance:download` 链**（真 HTTP 栈）：
  1. 成功链：restore 含 redownload 行（fixture pack 声明 sha1 + fileID 指假 CDN）→ committed，字节经两层校验（声明 hash「取对了」+ sha256 复核「写对了」，ADR-0008 §3）；
  2. 探测降标：假 CDN 回 404 → prepare 时点降标 `user_object_required` + `marker_reason="cf_unavailable"`（契约 06 §5）；
  3. failed 终局可重入：假 CDN 回 429/断连 → confirm 后 run=`failed` + task=`failed` + `problem_json` 承载 `err.download.*`，**不进 `recovery_required`**（契约 06 §6）→ 假 CDN 恢复 → 同 plan 重 Confirm → **新运行** committed（§3.4.3）；
  4. sync 剔除语义（ADR-0008）：快速更新 apply 含两个 download 行、假 CDN 挂其一 → 剔出本场照常原子提交（commit=partial）；全部失败 → `failed`，重试=重新快速更新（restore 不变整场退出）；
  5. 续传：半截断流 → `.part` Range 续传 → 最终成功。

### 5.2 真网冒烟（手动，非门槛，B 口径必做记录）

验收会话执行一次：真实 packwiz 包（现役 CF metafile）→ headless 真实下载链路（快速更新或 restore 含真实 redownload 行）→ 断言全缝：探测 HEAD 2xx → 直链 GET 200 → 声明 hash（sha1）验收过 → 装进实例 → committed。

- 记录入报告：日期、fileID、字节数、**代理状态**（本机 7897 时开时关，Go http 吃 `HTTPS_PROXY`，必须注记排除环境干扰）；
- **失败不判 A/B 口径失败**，但必须判因记录；确认 CDN 通道关门（403 形状变化）→ 触发 ADR-0008 预留 C 方案（`curseforge_api_key` 落点）回票；
- 不做真网自动化硬门槛：CDN 侧漂移与本仓代码无关，硬门槛只产假警报。

## 6. GC 验收（五件套）

1. **决策函数单测**（纯函数+fake clock）：锚点选择（N=20/D=90 天/C=1 GiB 触发）、连续前缀修剪、级联基线/引用行、K=3 硬保底、`trash_days` 老化清除、隔离区人工复活、安全窗口不开 → pending 自动续排；
2. **headless 链 `acceptance:gc`**：连续小 apply 造 >20 提交历史 → `pgheadless gc`（CLI 通道）→ 断言：①墓碑 `pruned_before_count`>0 且被裁行消失；②最老存活提交 restore 仍成功（「回滚到保留窗口内任意点」的用户承诺）；③**引用图不变式**：GC 后 CAS 存活对象集 = 存活提交可达闭包 ∪ 隔离区，超集为零（断言器逐 digest 对账）；④K=3 硬保底：`keep_commits` 设最小（5）仍 ≥3 锚点存活；
3. **保护红线三正例**（架构红线③）：活跃 draft/resolved 计划引用、进行中 restore 的 staging 绑定、`recovery_required` 运行引用——三种夹具下 gc 后引用对象一律存活；
4. **时间面**：7 天回收站/老化清除全在 fake clock 单测，headless 链不等真实时间；
5. **孤立路径**：quarantined→回收站→清除、孤儿三向清扫由决策函数单测覆盖（ADR-0007 §4/§5）。

## 7. 性能门槛（3,000 fixture，沿 P2 §3 模式）

| 指标 | 门槛 | 来源 |
| --- | --- | --- |
| 冷扫描 ≤10s / 热扫描 ≤2s / 热命中率 ≥95% | 沿用（P3 面不回退） | P1 §2.3 |
| 冷 apply ≤30s / 峰值内存增量 <256MiB | 沿用（P3 面不回退） | P2 §3 |
| **restore 全链路冷 ≤30s** | **新增硬门槛**（对齐冷 apply 量级：CAS 写回+验证） | Q6 |
| **restore 峰值内存增量 <256MiB** | **新增硬门槛** | Q6 |
| **GC ≤30s** | **新增硬门槛**（回归绊线：防引用图意外退化平方级） | Q6 |
| download 相位（staging 下载墙钟） | **只记录不设门槛**（假 CDN 数值无真网代表性）；内存门槛照断言 | Q6 |
| UI 查询 P95 ≤200ms | **继续推迟**（技术债注记沿用） | P1/P2 |

- `-metrics` 增量：restore 分相计时（prepare/staging 下载/applying/verifying）+ 峰值内存采样；GC 计时。超标处置沿 P2 §3（记录原因与机器规格，环境异常注明后重测）。

## 8. 记录与报告（沿 P2 §4）

- 原始记录：`docs/acceptance/records/p3-perf-<date>-<machine>.json`、`p3-recovery-<date>-<host>.json`（apply 回归）、`p3-recovery-restore-<date>-<host>.json`、`p3-download-<date>-<host>.json`、`p3-gc-<date>-<host>.json`（同日多轮 `-tNN` 后缀）。恢复注入逐轮记录含随机种子与收口路径。
- 报告 `docs/acceptance/reports/p3-acceptance-<date>.md`，规格与执行报告分离。必填沿 P2 §4 + **真网冒烟节**（结果/判因/代理状态）。
- **A 口径通过** = L0 全绿（test/vet/race/headless -apply+-restore/recovery/recovery:restore/download/gc/perf）+ 性能达标 + 报告归档。
- **B 口径通过** = A + `frontend:build` + L1 全勾 + P1/P2 回归不回退 + 真网冒烟已执行已记录。

## 9. 明确不在 Phase 3 验收范围

1. 跨平台一致性深测、UI P95 与窗口驱动自动化、硬件级真断电（P1/P2 技术债延续）；
2. 真网自动化门槛（§5.2 决议）；
3. murmur2 实现（ADR-0008 §3 条件后门）——撞上 murmur2 老包走 `hash_format_unsupported` 降标用户自备，验收只断言降标路径文案正确；
4. watcher/启动 hook/Junction（图 Out of scope，Phase 4 面）；
5. 保存所有 JAR、任意时点无条件恢复（架构篇排除）。

## 10. 执行会话工作项（验收基建清单）

本规格定口径；以下基建由 Phase 3 执行会话实现：

1. pgheadless `-restore` 链（§3 四场景断言 + §7 restore 计量）；
2. 假 CDN 服务（`pgfixture -serve` 或等价）+ pgheadless/pgrecovery `-cdn` 参数接入；
3. `acceptance:download` 链（§5.1 五场景）；
4. pgrecovery `-mode restore`（假 CDN 供给子进程 + restore 不变式断言 + 逐轮记录）；
5. `acceptance:gc` 链 + 引用图不变式断言器（逐 digest 对账）；
6. 单测族：四标记矩阵、失败分桶、直链黄金向量、GC 决策函数（fake clock）、CAS 篡改、CF 探测超时；
7. `-metrics` 增 restore 分相与 GC 计时；
8. Taskfile 任务挂接（§1.1 对齐）；
9. B 口径前 L1 walkthrough 数据准备（真实强杀产生的 restore recovery_required 工作区、exact/partial restore 历史各一、补全对话框数据、授权模式开态、>20 提交可触发 gc 的长历史）。
