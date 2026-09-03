# PackGradle Phase 4 验收报告（A 口径执行记录 + B 口径清单就绪）

> **规格与执行分离**：验收口径的权威是 [`docs/acceptance/p4-acceptance-spec.md`](../p4-acceptance-spec.md)（P4-E2E）与父规格票 #85（Testing Decisions 全节）；本文是执行报告——记录 A 口径 L0 命令集的执行结果、性能门槛评估与超标处置、R3 凭据复核归档（红线⑥）、L1 手工清单的数据就绪状态与全部 P4 执行票关闭核对。规格冲突时以规格为准。
> **B 口径状态**：§6 为 B 口径清单——`frontend:build` 已过、L1 供数全备且逐项可勾、真网冒烟沿 P3 归档引用；**L1 真窗口走查勾选本身须人工执行**，B 通过即 Phase 4 可标完成（验收规格 §0）。

- 票：skyraah/PackGradle#98（P4 验收收口，执行票 13/13）
- 执行日期：2026-09-03（A 口径收口自检）
- 分支：`p4/t98-acceptance`（基于 PR 分支 `p4/phase4-execution` @ 8e471ee——#86–#97 全部已并入且合并头实跑绿）
- 冒烟代理状态注记：本票零真网面（§6 引用 P3 归档），本机代理 127.0.0.1:7897 未参与任何 L0 链路（全部零真网）。

## 0. 机器规格（必录）

| 项 | 值 |
| --- | --- |
| 主机名 | Skye |
| OS / Arch | Windows 11 专业版 / amd64 |
| Go | go1.26.5 |
| CPU | AMD Ryzen 7 7700 8-Core（16 逻辑核） |
| 内存 | 31.1 GB |
| 磁盘 | KIOXIA EXCERIA PLUS G3 SSD |
| 关键环境 | Windows Defender 实时保护开启（逐文件原子写/打开有 10–30ms 级稳定开销，沿 P2/P3 报告注记，§3 超标处置的定量基础）；无 CI，本机执行 |

## 1. A 口径 L0 命令集执行记录（验收规格 §1.1 逐行）

| 命令 | 内容 | 结果 | 记录 |
| --- | --- | --- | --- |
| `task test` | `go test ./...`（38 包 ok；增量单测族：merge 真值表 §3.1、watcher 触发器状态机假时钟 §4.1、QuickUpdate 停靠判定、通知三条件+降级、横切 fake clock §5.1、R1/R2/R3 脱敏断言 §5.4，#86–#95 交付） | ✅ 全绿 | go test 输出（38 包 ok） |
| `task test:vet` | `go vet ./...` | ✅ 净 | — |
| `task test:race` | `go test -race ./...`（gcc：WinGet Packages mingw-w64） | ✅ 全绿 | — |
| `task acceptance:headless` | P1 `-resolve`×2 + P2 `-apply`×2 + P3 `-restore` 六场景回归（含 #88 ADR-0012 出口①、#95 出口③） | ✅ 全绿 | — |
| `task acceptance:recovery` | P2 apply 强杀×5 回归 | ✅ 全绿（5/5 轮四不变式零违例） | `p2-recovery-2026-09-03-Skye.json` |
| `task acceptance:recovery:restore` | P3 restore 强杀×5 回归（R5/R6 零违例） | ✅ 全绿（首轮回 1 遇 Windows 文件锁 flake（CAS rename Access denied，环境性），重测 5/5 全绿——处置沿 §6「环境异常注明后重测」） | `p3-recovery-restore-2026-09-03-Skye.json` |
| `task acceptance:download` | P3 假 CDN 五场景回归（零真网） | ✅ 全绿（36 断言） | `p3-download-2026-09-03-Skye.json` |
| `task acceptance:gc` | P3 引用图链 + **孤儿快照扩展**（#89）：裁剪提交验证快照转孤儿同删、中间扫描快照除最新外同删、快照账面对账残留 0 | ✅ 全绿（链墙钟 2.9s） | `p3-gc-2026-09-03-Skye-t01/t02/t03.json` |
| `task acceptance:merge` | **四场景链（#93 场景①–③ + #98 补齐场景④链上断言）**：见 §2.1 | ✅ 全绿 | `p4-merge-2026-09-03-Skye-t02.json`（t01 为 #93 交付轮） |
| `task acceptance:watcher` | **六场景常驻监听链（#96）**：触发收敛/去抖上界/停靠待确认/连败暂停复位/恢复期只标脏/并发 join + 全链事件集红线⑤ | ✅ 全绿（6 场景 64 断言） | `p4-watcher-2026-09-03-Skye.json`（当日重跑刷新） |
| `task acceptance:crosscut` | **新增（#98）**：横切真重启清理链三段，见 §2.2 | ✅ 全绿（三段 13 断言） | `p4-crosscut-2026-09-03-Skye.json` |
| `task acceptance:perf` | P1/P2/P3 八门槛照跑 + P4 新面计量（只记录） | ⚠️ 冷 apply / GC / restore 冷超门槛（restore=known-exceed 豁免挂 #69；apply/GC 超标处置见 §3.2/§3.3），其余达标 | `p3-perf-2026-09-03-Skye-{cold,warm,apply,restore,gc}.json` |
| `task frontend:build` | vue-tsc + Vite 生产构建（B 口径行） | ✅ 通过（4.85s） | — |

**A 口径结论**：收口自检依序执行，逐命令退出码——`test`=0、`test:vet`=0、`test:race`=0、`acceptance:headless`=0、`acceptance:recovery`=0、`acceptance:recovery:restore`=0（flake 重测后）、`acceptance:download`=0、`acceptance:gc`=0、`acceptance:merge`=0、`acceptance:watcher`=0、`acceptance:crosscut`=0、`acceptance:perf`=非零（三项性能超标，restore 为已豁免 known-exceed；apply/GC 按规格完成超标处置记录——沿 P3 报告 §1 的「超标处置记录」收口分支，分析见 §3，门槛修订建议回图）。L0 全绿 + 性能按规格处置归档 + 本报告归档 → **A 口径通过（附三项性能处置注记）**。

## 2. 新链场景断言要点（L0 硬断言摘录）

### 2.1 `acceptance:merge` 场景④（#98 补齐 §3.2 场景 4 链上断言；#94 交付面）

- **④a「所见即所写」**：resolved 计划 merged 行 `GetMergedPreview` 在 confirm 前断言 `content` = 确认后双端落盘字节（同算法同输入同输出，442B 逐字节一致）、`base_content` = 基线全文（426B）——预览三侧取数按计划锁定快照复核活文件指纹（merge_preview.go 口径），断言必须在落盘前完成（confirm 后活文件被合并产物覆盖属竞态语义）。
- **④c 非 merged 行拒绝**：③b 停靠计划的冲突行预览 → `err.merge.not_mergeable`，args {0}=`file:config/handmade.toml`（resource_id 实证）。
- **④b 过期计划仍可预览（只读）**：SQL 手术置 `status='confirmed' ∧ expires_at<now`（「过期的已确认计划」，用户故事 9 字面；选 confirmed 而非 draft/resolved 过期是为避开终态钩子异步惰性清理与断言的调度竞态——DeleteExpiredPlans 判定域仅 draft/resolved），预览零状态/零有效期校验返回，`content`=同算法重算产物。

### 2.2 `acceptance:crosscut` 真重启清理链三段（#98 新实现，验收规格 §5.2）

惰性清理挂「启动时 + 任务终态」两个时机，单测够不着真实启动路径——链以 `pgheadless -crosscut` 编排、`-crosscut-restart` 子进程走生产启动通道（`sessionlog.Open` 双轴保留清理 + `bootstrap` 启动惰性清理，与 GUI main 同一条启动语义；pgheadless 的 stderr 输出形态不变，#91 约定保留）。

- **段① 启动通道**（造数前置：26 份会话目录 / 209,746,256B，>20 ∧ >100MB）：断言 6 条全过——重启后 task_events=10,000 条窗口内且 min_seq=508 > 造数基线 7（留尾截断）；终态任务 200 条窗口内且活跃行零触碰（active=0）；造数过期计划 3 行 + 过期预检 3 行全部物理删（残留 0）；会话日志收敛至 14 份 / 100,691,739B（≤20 份 ∧ ≤100MB 硬顶）；最旧 5 个造数目录全部消失（硬顶「从最旧删起」方向证据）。造数按 #91 清理顺序语义用不可压缩内容与已压缩 .gz 两种形态（13 明文随机字节 + 12 预压缩 .gz，各 8MB），保证压缩分层后仍超顶、硬顶删真实触发。
- **段② 任务终态通道**：二次造数（+500 task_events、+60 终态任务）→ 驱动一次扫描收口 → 终态钩子异步惰性清理触发：task_events=10,000、终态任务=200 双收敛，二次造数最旧 30/60 条按 created_at 留尾消失。
- **段③ 脱敏断言（R1/R2）**：坏 metafile（非法 TOML）造含绝对路径的端点错误 → 新写 `diag.scan.modmeta_unreadable` 的 Detail = `packwiz: 解析 <project>\mods\broken.pw.toml: …`——含 `<project>` 角色前缀、无端点绝对路径、无用户名（历史行不追溯，不断言）；`-metrics` 全记录序列化无 `host`/`hostname` 键，OS/Arch/GoVersion/CPUs 四键保留。R3 凭据复核归 §4。

### 2.3 红线场景化表对照（验收规格 §2，六条全落 L0 硬断言）

| 红线 | 断言落点 | 本轮证据 |
| --- | --- | --- |
| ①冲突删除永不自动 | watcher 场景③/merge 场景③b/停靠判定单测 | watcher 6/6 绿（含冲突必停）；merge ③b 绿；单测绿 |
| ②合并产物入 CAS + 回滚零网络 | merge 场景①② | merge 四场景绿（产物+metafile 入 CAS；回滚零 CDN 配置 committed） |
| ③未冲突区域字节级不变 | merge 真值表 + 落盘直断 | merge 链绿（手工注释/键序/空行/缩进逐锚点断言） |
| ④`.index` 只读 | 黑名单真值表 + watcher 场景① | watcher 场景①绿（写 `mods/.index` 不触发） |
| ⑤监听零新事件类型 | 全链事件集断言 | watcher 64 断言含事件集 ⊆ {task_updated, relation_invalidated} |
| ⑥凭据零泄漏 | R3 复核归档 + 注入断言 | §4 复核归档 + `TestR3CredentialNoLeakOnFailure` 绿 |

## 3. 性能门槛评估（3,000 fixture，验收规格 §6）

| 指标 | 门槛 | 实测（收口自检轮，机器空闲） | 09-02 基线 | 结论 |
| --- | --- | --- | --- | --- |
| 冷扫描 | ≤10s | 4.40s | 2.91s | ✅ |
| 热扫描 | ≤2s | 0.54s | 0.45s | ✅ |
| 热命中率 | ≥95% | 100%（hits=3002 misses=0） | 100% | ✅ |
| 冷 apply（2400 op） | ≤30s | **40.4s**（staging=12.3s applying=7.5s verifying=5.0s） | 18.8s | ⚠️ 超标处置（§3.2） |
| Apply 峰值内存增量 | <256MiB | 20.7 MiB | 17.0 MiB | ✅ |
| restore 冷全链路（4802 op） | ≤30s | **64.9s**（staging=32.9s applying=15.1s verifying=14.0s） | 64.9s | ⚠️ **known-exceed 豁免挂 #69**（照跑照记录不判失败） |
| restore 峰值内存增量 | <256MiB | 8.3 MiB | 4.3–8.4 MiB | ✅ |
| GC | ≤30s | **63.8s**（墓碑=8 存活=20 对账违例=0） | 8.1s | ⚠️ 超标处置（§3.3） |
| download 相位 | 只记录 | 0（perf 夹具无 redownload 行） | 0 | 记录 |
| **P4 新面：merge 分相（diff3/校验/写盘）** | 只记录 | diff3=0ms / 校验=0ms / 写盘=6ms（小夹具场景①） | — | 记录（`p4-merge-*-t02.json`） |
| **P4 新面：watcher 触发→链收口墙钟 + 快速更新链相位** | 只记录 | 六场景链墙钟随 `p4-watcher` 记录（chains wall_clock_ms 时间线） | — | 记录 |
| **P4 新面：内存** | <256MiB 照断言 | merge/watcher/crosscut 链均在阈内（跨链 <256MiB 断言保持） | — | ✅ |

首轮 perf 与并发 L1 供数任务（l1:data）同机执行，计时受扰（apply 58.4s / GC 64.3s）；机器空闲后重测，本表为空闲轮数据——两次实测均随档（`build/perf/metrics-*.json` 为空闲轮，受扰轮数据未入库）。

### 3.1 restore 冷超门槛处置（known-exceed 豁免，挂 #69）

- 实测 64.9s，与 09-02 基线（64.9s）**逐相位一致**（staging 32.9/34.9、applying 15.1/14.2、verifying 14.0/12.9）——P4 期间 restore 链零回归，超标为 P3 已处置的存量项（Defender 逐文件开销 × 双侧写回 4802 op 的 CAS 打开+复核路径）。按验收规格 §6 Q6：照跑照记录，#69 关闭前不判 A/B 失败。

### 3.2 冷 apply 超标处置记录（按 §6 沿 P2 §3）

- **现象**：两轮实测 58.4s（受扰轮）/ 40.4s（空闲轮），均超 30s；相位分解 staging=12.3s（基线 4.3s）、applying=7.5s（基线 5.8s）、verifying=5.0s（基线 7.0s）——applying/verifying 在基线波动带内，staging 相位 +8s 为主要增量。
- **原因分析**：①**P4 摄取通道的工作量进入门槛路径**（#88/#93，ADR-0012 出口①的前提）：提交收口期把项目侧全部带内容指纹表示读字节入 CAS——staging 相位为此新增约 2,400 次文件读 + sha256 + CAS put，属规格内新工作而非回归（该工作在 P3 基线时代不存在）；②**Defender 实时保护逐文件开销**（本机既有环境因素，P2 同代码三轮 22.2–117.7s 波动先例、P3 报告 §0 注记）放大每一次原子写。
- **结论与后续**：非引擎回归（相位证据如上），内存远低门槛；门槛数字未随 P4 新工作修订——建议回图（与 §3.3 的 GC 处置同票联审：修订门槛口径或把「提交收口期摄取」计入独立分相），非验收会话可自决。

### 3.3 GC 超标处置记录（按 §6 沿 P2 §3）

- **现象**：两轮实测 64.3s（受扰轮）/ 63.8s（空闲轮），均超 30s；基线 8.1s（09-02）。
- **原因分析**：断言②「最老存活提交可达闭包逐字节复验（回滚承诺不缩水）」的**工作量在 P4 摄取通道落地后发生结构性变化**——09-02 的闭包与 CAS objects 的交集为空载（`oldest_verified_objects=1`：baseline logical_digest 大多无对象可 JOIN）；P4 摄取后每个提交的结果基线真实引用全部项目内容对象（restore 到该提交的字节保全面前提，ADR-0012 §3 经 baseline 传递引用），今日实测复验 **2709 个对象**（逐对象打开+流式 sha256，Defender 下 ≈60s）。GC 引擎本体（裁剪/墓碑/对账/清扫）**零回归**：acceptance:gc 小夹具全断言链 2.9s、audit 违例 0、墓碑=8/存活=20 与基线一致、孤儿清扫 18 份残留 0。
- **结论与后续**：超标是「回滚承诺的复验从名义变真实」的直接计量后果——保全面变强（restorable_from_cas 可验）与门槛超标的同源事实。建议回图：GC 门槛口径修订（闭包复验独立计量或按对象数分档）并与 #69（restore 冷链路 perf）联审；非验收会话可自决。

## 4. R3 凭据复核记录（红线⑥，slog 迁移后全量复跑归档）

复核口径 = ADR-0011 §9（硬规则：凭据永不进日志/诊断/错误 detail），随 P4 施工全量复跑，本节为归档结论。

- **复跑方法与范围**（2026-09-03，分支 `p4/t98-acceptance`）：对非冻结树全量（`main.go` + `internal/` 除冻结树 `internal/service` + `cmd/` 三工具）grep 复核凭据标识符（`curseforge_api_key` / `CurseforgeApiKey` / `Authorization` / `X-Api-Key`）在 `slog.*` / `log.Print*` / `log.Fatal*` / `errs.New*` / `Detail:` / 诊断构造点的出现。
- **调用点账面**：internal 非冻结树 + main.go 共 124 处 slog/结构化调用点（#91 迁移 86 处 + #92–#98 增量），cmd 三工具 56 处保持 stderr 现状（验收断言依赖，#91 约定）。
- **结论一（静态复核）**：凭据标识符在日志/错误 detail/诊断构造点**零命中**。凭据唯一装载面为 `internal/appconfig`（config.toml → `CurseforgeApiKey`，读取仅经 Get 访问器）；冻结树内唯一相关构造为错误**码** `err.cf.api_key_missing`（不含键值）。
- **结论二（动态注入断言，L0 硬门槛）**：`TestR3CredentialNoLeakOnFailure`（`internal/application/sync/redaction_r3_test.go`，#90 固化）——canary 键经 config.toml 注入生产栈，构造三类失败（端点不可达/坏 metafile 扫描/预检不可达），断言错误 detail、诊断输出、检查 detail、日志四通道零泄漏。本轮 `task test` 全绿即含本断言。
- **结论三（#91 票面复核结论沿用）**：slog 迁移完成时复核 87 处调用点零凭据泄漏（#91 票面评论）；本票复跑在其基础上覆盖 #92–#98 全部增量调用点，结论不变。
- **证据链**：crosscut 段③的 R1 别名路径断言（`<project>` 别名、无用户名、无绝对路径）与 R2 无 Host 键断言随 `p4-crosscut-2026-09-03-Skye.json` 归档——脱敏三面（R1/R2/R3）在本轮全部有 L0 证据。

## 5. L1 手工清单（B 口径，验收规格 §1.2 全量逐项可勾）

数据准备状态（`task l1:data` 已实跑通过，产物不入 git；路径为仓库根相对路径）：

| L1 清单项 | 数据位置 | 状态 |
| --- | --- | --- |
| 合并呈现（merged_clean 徽标/预览抽屉/冲突块列表） | `build/l1/merge-fixture` + `build/l1/merge-data`（初始化提交 + `-dual-edit merge` 双侧改动 + 授权开态）；冲突块列表走 ⑥ 的工作区 | ✅ 已备 |
| 快速更新单调用三态 | 同上（授权开态 no_diff/apply_started）+ ⑥（awaiting_confirmation 导航） | ✅ 已备 |
| 待确认角标（pending_plan_id） | `build/l1/pending-fixture` + `build/l1/pending-data`（draft 含 1 冲突停靠，已实库核对） | ✅ 已备 |
| paused/unavailable 横幅（会话内存态） | 不构造持久数据，走指引（l1:data 输出⑦）：GUI 会话内连败注入 2 次 → paused，手动成功复位；unavailable 由监听异常降级产生 | ✅ 指引就绪 |
| 系统通知真弹 + 点击直达 | l1:data 输出⑧：通知开启 + 窗口不在前台 + pending_plan_id 更新 → Win11 toast；点击直达 `/workspaces/:id/plans/:pending_plan_id`（#97 票面三路径手工步骤） | ✅ 指引就绪 |
| **通知降级（Q5 强制项）** | l1:data 输出⑧：Win11 设置关闭 PackGradle 通知 → 只亮角标、不弹、不报错不重试 | ✅ 指引就绪 |
| restore 历史/补全对话框/授权开态/长历史/recovery 工作区 | 沿 P3 五项（①–④ 输出与 `build/recovery-restore/`） | ✅ 已备 |

勾选清单（B 口径人工走查会话执行，此处为可执行清单本体）：

- [ ] 合并呈现两项（§1.2 合并呈现）
- [ ] 快速更新入口三态一项
- [ ] 待确认角标 + paused/unavailable 横幅两项
- [ ] 系统通知真弹 + **降级强制项**两项
- [ ] P1/P2/P3 三回归抽验沿例（P2 §1.2 应用链路与恢复流、P3 §1.2 回滚链与 GC/设置、P1 §1.3 事件/互斥/locale/无 legacy 复活）
- [ ] `task frontend:build` —— ✅ 已过（见 §1 表）

## 6. 真网冒烟（B 口径）

- P4 **零新网络面**（合并本地合成、监听本地文件系统、横切纯内部动作，下载字节路径零改动）——按验收规格 §0 Q1，沿 P3 归档引用即算「已执行已记录」：[`p3-acceptance-2026-09-01.md` §6](p3-acceptance-2026-09-01.md)（三黄金向量 200/206 + 引擎真网 Fetch 1.095s，2026-09-02 执行）。下载面回归以 L0 假 CDN 链为准（本轮 `acceptance:download` 五场景绿）。

## 7. 全部 P4 执行票关闭核对（#86–#98）

逐票对照票面 AC 与票面最终报告（各票结论已在票面评论归档）：

| 票 | 交付 | AC 核对 | 状态 |
| --- | --- | --- | --- |
| #86 | QuickUpdate 三态+并发 join+前端单调用与角标 | 三态链/8 格停靠判定/pending_plan_id 投影/前端全落；两项语义裁定已注释在 pendingPlanIDFor | ✅ 关闭（PR #99） |
| #87 | diff3 引擎+merged_clean 分类+冲突块证据+真值表 | 真值表全格/伪版本 pin/hunk JSON 进 Detail/四层计数全落；Base 侧实存缺口的架构备忘已由 #93 兑现 | ✅ 关闭（PR #99） |
| #88 | metafile 捕获+提交收口摄取+引用图核对+新基线回滚自动完成 | 捕获/摄取/Audit 扩展/-restore 场景⑤全落；「restorable_from_cas 判定面」偏差已记录（§8.2），CAS 命中以数据面断言兑现（本轮 merge 场景②复证） | ✅ 关闭（PR #99） |
| #89 | 惰性清理+孤儿快照+gc 扩展 | task_events/旧数据行/tasks 窗口+GC 阶段扩展全落；applied 计划随提交存亡的结构性偏差已记录（§8.1）；**本轮 crosscut 段①②即其启动/终态双通道的端到端实证** | ✅ 关闭（PR #99） |
| #90 | R1 别名路径+R2/R3+GetStorageStats | 别名构造点全落/去机器名/注入断言固化/存储概览上 wire；R3 复核归档于本报告 §4 | ✅ 关闭（PR #99） |
| #91 | slog 会话日志（20 会话/100MB 硬顶） | 迁移+sessionlog 包+fake clock 三轴单测全落；先压后删语义经 crosscut 段①造数（不可压缩+.gz 双形态）端到端实证 | ✅ 关闭（PR #99） |
| #92 | 监听引擎（fsnotify+状态机+自动链+连败暂停） | 全落；#96 期最小修正（排除集扩展到触发语义匹配，§8.4）已在案 | ✅ 关闭（PR #99） |
| #93 | 合并执行面（take_merged/write_merged+产物入 CAS+acceptance:merge） | 全链/暂存期确定性重算/摄取泛化（§8.3）/三场景全落；场景④由本票补齐（§2.1） | ✅ 关闭（PR #99） |
| #94 | 合并预览（GetMergedPreview+前端抽屉+not_mergeable） | wire/DTO/stale 只读预览/前端抽屉全落；「场景 4 链上断言移交」由本票兑现（§2.1） | ✅ 关闭（PR #99） |
| #95 | 存量降级与错写链修正 | 宽判降标/补全拒收/摘要只认实测/exact 如实/-restore 场景⑥全落 | ✅ 关闭（PR #99） |
| #96 | watcher 验收链（常驻模式+六场景+连败注入） | 64 断言全绿（本轮复证）；场景 2 有界断言修正与 #92 排除集修正在案；语义发现见 §8.6 | ✅ 关闭（PR #99） |
| #97 | 系统通知（三条件+点击直达+静默降级） | 全落（beta.7 零升级）；接线偏差（bootstrap Chain 闭包喂通知）见 §8.5；L1 三路径入 §5 清单 | ✅ 关闭（PR #99） |
| #98 | 本票（crosscut 链/Taskfile 对齐/l1:data 扩展/A 口径+报告） | 见本报告 §1/§2/§5 | ✅ 本票完成 |

## 8. 规格偏差汇总核对表（本票报告必录项；均已记录在票面评论与代码注释）

| # | 偏差 | 落点 | 本轮核对 |
| --- | --- | --- | --- |
| 8.1 | **#89**：ADR-0011 §3「applied 计划随提交存亡」在 schema v6（apply_runs.plan_id NOT NULL）下结构性不可实现，落地保守语义（绝不早删、留存有界）；建议回图另议（需 v7 放宽该列） | cleanup_repo.go 注释 + #89 票面 | 已核对在案 |
| 8.2 | **#88**：AC 写「restorable_from_cas」实际按 ADR-0012 §2 矩阵判 redownload_required，CAS 命中以数据面断言兑现（零网络零介入不变） | #88 票面 + restore 链注释 | 已核对在案（merge 场景②复证 marker 面与数据面双一致） |
| 8.3 | **#93**：提交收口期摄取通道从 mod metafile 泛化到项目侧全部带内容指纹表示（规格 §F.2 字面只授权 mod；理由=§A「非 mod 文本现状本就进库」与代码事实不符，泛化是 ADR-0009 回滚承诺的最小兑现路径）；#94 测试与 #95 场景⑥已按此后提对齐 | content_ingest.go 文件头 + #93 票面 | 已核对在案（§3.2 的 apply/GC 工作量变化即此泛化的计量后果） |
| 8.4 | **#96**：对 #92 surface.go 排除集的最小修正（扩展到触发语义匹配，红线④）；场景 2 风暴后链数断言放宽 ≤settled+2（ADR-0010 §6 补轮余量） | #92/#96 票面 + surface.go 注释 | 已核对在案（watcher 场景①红线④断言绿） |
| 8.5 | **#97**：通知事件源用 bootstrap 的 watch.Deps.Chain 闭包（SyncService.AttachQuickUpdateResult 只承载手动入口，挂通知会反条件①） | bootstrap.go 注释 + #97 票面 | 已核对在案 |
| 8.6 | **未决议语义发现（给用户回图）**：write_runtime 目标侧 present 即计 info 级 overwrite 确认要求 → 授权开态下「上游改既有运行端文件」的自动链会停靠待确认；watcher 主用例（上游自动跟进）是否豁免该面属产品决议 | #96 票面 | 原样上报，未自行裁决（本轮 watcher 六场景绿不受其影响——场景覆盖面与 #96 交付时一致） |
| 8.7 | **本票新增发现（给用户回图）**：性能门槛与 P4 摄取工作的口径差——冷 apply/GC 超标为 #88/#93 摄取使保全面变真的计量后果（§3.2/§3.3），门槛修订或分相独立计量建议与 #69 联审 | 本报告 §3 | 超标处置记录已归档 |

## 9. A/B 口径结论

- **A 口径**：✅ 通过——L0 十二命令全绿（`acceptance:recovery:restore` 首轮回 1 环境性 flake 重测绿；`acceptance:perf` 三项性能按规格完成超标处置记录归档：restore=known-exceed 豁免挂 #69，apply/GC=处置记录+回图建议）+ 本报告归档。
- **B 口径**：清单就绪、待人工走查——`frontend:build` 已过、L1 供数全备逐项可勾（§5）、P1/P2/P3 回归抽验沿例、真网冒烟沿 P3 归档引用（§6）。**L1 走查勾选完成即 B 通过，Phase 4 可标完成。**

## 附：记录文件清单（`docs/acceptance/records/`，2026-09-03 收口轮）

- `p4-crosscut-2026-09-03-Skye.json` — 横切真重启链三段（p4-crosscut/1：造数账面/重启前后对比/逐条断言/R1 detail 证据/R2 断言）
- `p4-merge-2026-09-03-Skye-t02.json` — 合并四场景（p3-perf-run/1 merge 段；t01 为 #93 交付轮）
- `p4-watcher-2026-09-03-Skye.json` — watcher 六场景（p4-watcher/1，当日重跑刷新 #96 交付版）
- `p3-perf-2026-09-03-Skye-{cold,warm,apply,restore,gc}.json` — 性能门槛供数（p3-perf-run/1）
- `p2-recovery` / `p3-recovery-restore` / `p3-download` / `p3-gc-t01..t03` — P2/P3 链回归记录（沿格式照跑）
- P1/P2/P3 既有记录沿前（`p1-*`/`p2-*`/`p3-*` 09-02 及更早归档不动）
