# 横切调研：日志 / Task 事件 / staging / 诊断包保留期限与脱敏规则

> 状态：research 票 #32 供数，未决议。2026-08-31。本文只盘点事实与列候选，不拍板；
> 架构文档 §15.2 开放问题 7（「诊断日志和 Task 事件的保留期限、最大体积与脱敏规则」，
> docs/architecture/packgradle-architecture-redesign.md:1143）即本票来源。

## 1. 代码现状盘点

### 1.1 日志：无 slog、无文件、无轮转（票面前提修正）

**票面说「仓库用 slog」——不成立。** `log/slog` 全仓库 0 命中（含 go.mod，无任何日志库依赖）。
实际全部是标准库 `log` 直写 stderr，共 31 个调用点：

- `main.go:47,51,57,61,100`（GUI 主入口，5 处，全部 `Fatalf`/`Printf` 启动期错误）；
- `cmd/pgheadless/main.go`（11 处，`Fatalf` 为主）；
- `internal/application/sync/recovery.go:22`、`internal/application/sync/scan.go:210`、
  `internal/application/task/runner.go:58,75,88,97`、`internal/service/detect.go:81`、
  `internal/service/mods_watch.go:97,101,147,240,330,362,507`、`internal/service/packwizservice.go:92`（15 处）。

行为特征：

- **无输出重定向**：全仓库无 `log.SetOutput`；`os.OpenFile` 的使用方
  （`internal/adapters/filesystem/atomic.go`、`hash.go`、`filekey_*.go`、`internal/appconfig/tomlfile.go`、
  `internal/perffixture/perffixture.go`、`internal/store/objectstore/cas.go`）全是数据文件，非日志。
- **Windows GUI 子系统下 stderr 无处落地** → 桌面形态运行期日志事实上丢失，只有从终端启动或跑
  `pgheadless` CLI 才可见。前端 `console.warn`（如 `frontend/src/api/events.ts:41,46,59`）只进 WebView devtools。
- **`<root>/logs` 目录已建但零写入方**：`internal/store/paths.go:51` 预留 `LogsDir`，
  `EnsureLayout`（`paths.go:69`）每次启动创建；架构文档规划原文「logs/ 结构化本地日志，按保留策略轮转」
  （redesign:338）未实现。
- `main.go:68-86` 的 `application.Options` 未配置 Wails Logger。
- 结论：Phase 2 引入结构化日志（届时选 slog 合理）是从零建，「保留几个会话/多大」正是拍板窗口。

### 1.2 task_events：只写不读、无限增长、截断无功能影响

- **DDL**：`internal/store/sqlite/schema_v1.go:31`——`event_id` PK、`stream_sequence INTEGER NOT NULL UNIQUE`、
  `event_type` CHECK（`task_updated`/`relation_invalidated`/`watch_failed`）、`relation_id`/`task_id` NULL、
  `payload_json`。无时间列索引、无外键。
- **唯一写点**：`internal/store/sqlite/event_repo.go:28-49` `Append`——依赖 `Open` 的 `MaxOpenConns=1`
  单连接设置，事务内 `SELECT COALESCE(MAX(stream_sequence),0)+1`（:32）后 INSERT。
- **没有任何读路径**：`FROM task_events` 全仓库仅 :32 这一处 MAX 查询；端口 `ports.go:178-180`
  只定义 `Append`；无 SELECT 重放 API、无 DELETE。前端契约（`frontend/src/api/events.ts:5-6` 注释）
  明文「P1 无事件重放/补拉 API，恢复只能经查询面」。
- **前端只存内存值**：`events.ts:29` `let lastSeenSeq: number | null = null` 为模块级变量，
  注释（:27-28）明说「窗口 reload / 应用重启后 JS 状态重置，重新建基线」。
- **写入频率**：`internal/application/task/publisher.go:61` 先落库后 Emit（事件桥
  `internal/transport/events.go:29-34` 只 Emit）；`internal/application/task/runner.go:58`（任务创建）
  与 :75（每次进度更新）都发事件——进度事件行数随「任务数 × completed/total 推进粒度」增长，
  `payload_json` 携带整个 Task 快照。
- **无清理**：全仓库唯一 DELETE 语句是 `internal/store/sqlite/hashcache_repo.go:70`（hash_cache 全表清）。
  `tasks` 表同样无删除（票面未列，但与 task_events 同属任务域增长面）。
- **保留决议的技术前提**：因无读方，截断 task_events 今天不影响任何功能；唯一硬约束是
  `stream_sequence` 单调性——清理方案必须规定截断后序号从 MAX+1 继续（清全表则从 1 重来，
  而前端从不持久化 last 值，重启后以首个事件建基线，两种都不会误判漏包）。

### 1.3 staging：目录已建、写入方待 Phase 2、保留规则已冻结一半

- **现状**：`paths.go:50,65` 预留 `<root>/staging`，`EnsureLayout` 创建，无写入方。
  ADR-0004 事实基线原话：「`paths.go:57-75` `EnsureLayout` 建出 `StagingDir` 但**无任何写入方**」
  （docs/adr/0004-phase2-apply-journal-and-recovery.md:14）。
- **已决议部分**（ADR-0004 §3/:61-65、§5/:80-84）：
  - staging 按 Apply 运行隔离，每运行自有目录 + root-relative 临时路径；
    「普通用户视图不得暴露临时绝对路径」（:65）；
  - 仅 `committed` 事务成功提交后清理，并把 `staging_cleared` 记录为事实；
  - 事务失败、取消、磁盘写满、进程强杀、任一操作失败 → 进入 `recovery_required`，**保留 staging 作恢复证据**。
- **未决议部分**：ADR-0004 §7（:97）明确「CAS 长期保留/GC 阈值、Restore UI 或人工恢复界面」不在其范围
  → recovery_required 运行的 staging 保留多久、何时允许人工放行清理，是本票要供数的空档。
  `acknowledged_at` 列（ADR-0004 DDL :39）只覆盖确认动作，不带期限。
- **预留 schema 现状**：`operation_journal`（`schema_v1.go:33`）全仓库零 Go 代码读写（唯一引用
  `guard_test.go:328`，ADR-0004:12）；`objects.state='staging'`（`schema_v1.go:36`）从未写入；
  `apply_runs` 表规划为 schema v5（ADR-0004:20-45，含 `staging_cleared` :38）——**当前库 v4**
  （`internal/store/sqlite/migrate.go:54-55`）。
- 相邻约束：架构文档 :512（失败保留 staging）、:629（「GC 永远不删除未完成 staging 引用的对象」）。

### 1.4 CAS objectstore：去重天然、size 可聚合、GC 缺位

- `internal/store/objectstore/cas.go`：`Put`（:60-115）——`CreateTemp(objectsRoot, ".tmp-*")`（:61）
  → 流式 sha256 + 写盘 → fsync → rename 到 `<objectsRoot>/sha256/<前2字符>/<hex>`（:52-54,92）
  → 事务 UPSERT `objects(algorithm,digest,size,state='ready',created_at)`（:103-107）。
  同内容重复 Put 天然去重（同一路径 + UPSERT 刷新 size）；reader 出错清理临时文件（:66-69）。
- `Has`（:129-153）：`state='ready'` **且**文件存在才可用；`Open`（:156-170）读流。
- **无 GC**：无 `DELETE FROM objects`、无引用计数消费（`object_refs` 表 `schema_v1.go:37` 零消费，
  ADR-0004:15「P1 侧的死值与占位」）。`bootstrap.go:60-63`：P1 无 Put 调用方，启动仅 `Open` 验证布局。
- 决议归属：Phase 3（ADR-0004:97；redesign:1138 开放问题 2「按总容量、Commit 数量、时间还是组合阈值」）。
- **观测基础已就位**：`objects.size` 列天然可 `SUM`/`MAX`；`.tmp-*` 残留文件数可作为写中断指标。

### 1.5 诊断/导出面：不存在诊断包功能，但存在三个未脱敏的诊断类数据面

全仓库无 telemetry/analytics/crash 上报代码（grep 零命中）；`<root>/exports` 目录预留
（`paths.go:52,67`）零写入方；docs/REQUIREMENTS.md:167 的 P3「导出」指 modpack 打包导出，非诊断。
redesign:914 规划的 `/settings`（含「保留和诊断」）未实现。现存三个面：

1. **扫描诊断**：`internal/core/model/model.go:91-98` `Diagnostic{Severity,Code,Args,Detail,ResourceID,RelativePath}`
   → 随快照持久化到 `observed_snapshots.diagnostics_json`（`schema_v1.go:22`）；
   查询面 `GetSnapshotDiagnostics`（`internal/application/sync/diagnostics.go:16-31`）。
   **注意**：`Detail` 今天就携带绝对路径——`internal/adapters/filesystem/endpoint.go:32,36,39`（`abs`/`real`
   进错误串）与 :97（`解析到 %s` 携带解析后绝对路径）的错误，被 `internal/adapters/packwiz/scanner.go:101`、
   `internal/adapters/prism/scanner.go:86` 原样写进 `Diagnostic.Detail` 落库。
2. **任务问题**：`tasks.problem_json`（`schema_v1.go:29`）+ `model.Problem{Code,Args,Detail}`
   （`internal/core/model/event.go:66-68`），经 `GetTask`/`ListTasks` 出口给前端。
3. **pgheadless -metrics JSON**（T14 性能基线）：`cmd/pgheadless/main.go:250-276` 写用户指定路径，
   内容含 `ProjectRoot`/`InstanceDir`/`DataRoot` 绝对路径 + `machineInfo{Host,OS,Arch,GoVersion,CPUs}`
   （:229-236，`Host = os.Hostname()` :276-284）+ hash cache 命中差值。**这是仓库目前最接近
   「诊断导出」的制品，且完全未脱敏（主机名、含用户名的路径明文）。**

凭据面：legacy 配置 `%AppData%\PackGradle\config.toml` 明文 `curseforge_api_key`
（`internal/appconfig/config.go:32,45`），请求头携带（`internal/curseforge/client.go:50`）。

### 1.6 相邻增长面（盘点顺带确认，供保留决议一并考虑）

- `observed_snapshots` + `resource_representations`：每次扫描插 2 行快照
  （`internal/application/sync/scan.go:195-199`）+ 每资源一行表示，无清理；
- `sync_plans.plan_json`（全量计划 JSON）、`preparations.input_json`：有 `expires_at`
  但仅读取时判过期（`internal/application/sync/plan.go:120,172`），无物理删除；
- `packgradle.db` 本体 + WAL 增长；迁移升级前 `VACUUM INTO` 备份（`migrate.go:63-65`，事务外）会临时翻倍占盘。

## 2. 同类桌面工具的通行做法（3-5 例，数字优先）

### 2.1 VS Code：按启动会话清理，保留 10 个会话目录

- 日志按会话一目录（Windows `%APPDATA%\Code\logs\<YYYYMMDDTHHMMSS>\`）；
  源码 `logsDataCleaner.ts`：启动约 10 秒后执行一次清理，**保留当前会话 + 最近 9 个旧会话**
  （`oldSessions.slice(0, Math.max(0, oldSessions.length - 9))`），无单文件大小上限。
  来源：[microsoft/vscode · logsDataCleaner.ts](https://github.com/microsoft/vscode/blob/main/src/vs/code/electron-utility/sharedProcess/contrib/logsDataCleaner.ts)
- 反例：[issue #69160](https://github.com/microsoft/vscode/issues/69160) 有用户被 342GB 日志撑爆硬盘——
  「只按会话数清理」对长驻进程 + 异常重复输出不够，需叠加体积上限。

### 2.2 JetBrains IDE：按大小轮转 idea.log，数量上限固定

- 官方：「idea.log 按文件大小轮转」，报工单时手动 zip 最近几个日志附带。
  来源：[Locating IDE log files](https://intellij-support.jetbrains.com/hc/en-us/articles/207241085-Locating-IDE-log-files)
- 随 IDE 分发的 `bin/log.xml`（2020.2–2021.3 标签实测）：`MaxFileSize=1Mb`、`MaxBackupIndex=12`
  → 上限约 13 个文件。来源：[JetBrains/intellij-community · bin/log.xml（213.7172 标签）](https://github.com/JetBrains/intellij-community/blob/213.7172/bin/log.xml)。
  标注：新版平台轮转参数可能改由内部 registry 键控制，此数字以标签内核实为准；「按大小轮转 + 固定份数」
  的形态稳定。

### 2.3 Docker Desktop：诊断包上传制，官方承认含个人信息，令牌事故是前车之鉴

- Troubleshoot → Get support：收集诊断 → 上传得到 Diagnostic ID（官方示例格式 = 用户 ID + 时间戳），
  本地同时生成 zip 供自查。来源：[Troubleshoot Docker Desktop](https://docs.docker.com/desktop/troubleshoot-and-support/troubleshoot/)
- 官方明示：**「上传的诊断包可能包含用户名与 IP 地址等个人数据」**，仅 Docker 内部可访问——
  不承诺自动脱敏，靠访问控制兜底。来源：[Get support for Docker products](https://docs.docker.com/support/)
- 反面教材：CVE-2025-13743（Docker Desktop < 4.54.0）：错误对象序列化把**过期的 Hub PAT 带进诊断包日志**。
  来源：[Docker security announcements](https://docs.docker.com/security/security-announcements/)、
  [Tenable 插件页](https://www.tenable.com/plugins/nessus/278980)
- 本地/远端保留期限均未公开文档化。

### 2.4 Minecraft 原版 + Modrinth App（同域参照）：本地普遍不清理，分享侧 90 天

- 原版：`logs/latest.log` + 按日 gz 归档（`yyyy-MM-dd-N.log.gz`）；Mojang 官方 bug 记录
  同日第 8 次启动起覆盖最旧（同日上限 7 份），**跨日无总量上限**。
  来源：[MC-100524 · Mojang bug tracker](https://bugs.mojang.com/browse/MC-100524)（转引自检索摘要，未直接抓取页面）
- Modrinth App：帮助中心只教用户手动删日志，App 本身不做本地自动清理；社区有用户报 145GB 日志吃满 C 盘。
  来源：[Minecraft logs · Modrinth Help Center](https://support.modrinth.com/en/articles/9005261-minecraft-logs)、
  [Reddit 案例帖](https://www.reddit.com/r/feedthebeast/comments/1mmo6bo/found_out_what_was_clogging_up_my_c_drive/)
- 分享侧唯一成文数字：上传到 mclo.gs 的日志**保留 90 天（自最后查看起）**。
  来源：同上 Modrinth Help Center 页
- 启示：同域工具的「本地保留」普遍缺位（VS Code 342GB / Modrinth 145GB 两个事故），PackGradle 应引以为戒；
  Phase 2 staging 一旦开始产生数据，增长速度远超纯文本日志（staging 装的是真实 modpack 内容）。

### 2.5 Sentry（脱敏清单的行业参照）

服务端默认脱敏：类信用卡数字正则匹配；`authorization`/`authentication`/`cookie` 等敏感头默认清除；
IP 地址存储有独立开关。来源：[Server-Side Data Scrubbing](https://docs.sentry.io/security-legal-pii/scrubbing/server-side-scrubbing/)、
[Sentry for Elixir · Data Collected](https://docs.sentry.io/platforms/elixir/data-management/data-collected/)

## 3. 脱敏规则候选清单（桌面工具惯例 → PackGradle 对应物）

| # | 通用类别 | 通行做法 | PackGradle 对应物（落点） | 候选规则 |
| --- | --- | --- | --- | --- |
| R1 | 家目录/用户名路径 | Docker 承认 bundle 含 username；社区惯例替换 `~` | 端点绝对路径（`projects.root_path`/`runtimes.root_path`，`schema_v1.go:17-18`）；`Diagnostic.Detail`（**今天已落库**，endpoint.go:97 → scanner.go:101/86）；pgheadless metrics 的 ProjectRoot/InstanceDir/DataRoot | 分三档：UI 显示原样；本机 DB/日志可原样（本机数据不外泄则无风险）；**导出/诊断包强制替换**——家目录前缀 → `~/`，或按端点根别名化（`<project>/...`、`<runtime>/...`，ADR-0004:65 的 root-relative 原则推广到全诊断面） |
| R2 | 机器名 | 惯例省略或哈希 | pgheadless `machineInfo.Host = os.Hostname()`（main.go:229,276-284） | 导出可保留 OS/Arch/GoVersion/CPUs（通用环境信息）；Host 省略或截断哈希 |
| R3 | 令牌/凭据 | Sentry 默认清 auth 头；Docker PAT 泄漏事故（CVE-2025-13743） | `curseforge_api_key`（config.toml 明文，config.go:45）；任何携带 config 值的错误串 | 硬规则：日志/诊断/导出永不渲染 config 值；错误出口统一走 `errs.AppError`（code/args/detail），detail 白名单化，禁止把底层错误体原样透传 |
| R4 | 内容指纹/digest | ——（非个人身份） | `binding_fingerprint`、`snapshot_digest`、sha256 hex（schema_v1.go:17-22,36） | 默认可导出（调试必需，不可逆）；文档标注为内容身份，非机器身份 |
| R5 | 配置/策略内容 | 导出最小化 | `mappings.policy_json`、`preparations.input_json/policy_json/checks_json`（schema_v1.go:20-21） | 诊断包默认不含整库副本；只含结构化摘要 + 任务/事件/journal 摘要；含 R1 的策略字段先过 R1 |
| R6 | 临时绝对路径 | ADR-0004 已定先例 | staging root-relative 临时路径（ADR-0004:65「普通用户视图不得暴露临时绝对路径」） | 把「root-relative 优先」定为全诊断面默认；必须出现绝对路径时（如恢复裁决）仅在 recovery 面显示、不进导出 |
| R7 | IP/网络信息 | Sentry IP 开关 | P1 无网络面（仅 CurseForge API 出站） | Phase 2+ 引入下载源时再议；暂列占位 |

即：**R1/R3 是唯一有真实事故与落库现状支撑的必做项**（绝对路径今天就在 `diagnostics_json` 里，见 1.5），
R2/R5/R6 是导出面建立时的默认档，R4/R7 是记录在案的「不需要脱敏」结论。

## 4. staging 与 CAS 磁盘占用观测口径候选

采集先例：`GetHashCacheStats`（`internal/application/sync/metrics.go:12-20`）——进程生命周期原子计数
（`cacheHits/cacheMisses` atomic，删除缓存不重置），返回 `view.HashCacheStatsView`
（`internal/application/view/views.go:135-140`），挂在 `sync.Application` 接口（`sync/app.go:34-35`）；
pgheadless `-metrics` 已做 before/after 差值导出（`hashDelta`，main.go:120-140）。建议照此先例新增
`GetStorageStats`（惰性查询 + 任务终态时采集，不做后台定时器），指标候选：

| 指标 | 口径 | 来源 |
| --- | --- | --- |
| `cas_object_count` / `cas_total_bytes` / `cas_max_object_bytes` | `SELECT COUNT(*), SUM(size), MAX(size) FROM objects WHERE state='ready'` | `objects.size`（schema_v1.go:36，Put 时已写入 cas.go:104-107） |
| `cas_tmp_leftovers` | `<objectsRoot>` 根下 `.tmp-*` 文件数与字节数 | cas.go:61 的 `.tmp-` 前缀；写中断残留观测 |
| `staging_run_count` / `staging_total_bytes` / `staging_max_run_bytes` | `<root>/staging` 一层子目录遍历（运行隔离布局下每子目录 = 一次 Apply 运行）；schema v5 落地后与 `apply_runs`（`state`/`created_at`）JOIN 对账 | paths.go:65 + ADR-0004 DDL |
| `task_events_count` / `task_events_max_seq` / `tasks_open_count` | 轻量 `COUNT(*)` + `MAX(stream_sequence)` | schema_v1.go:29,31 |
| `db_size_bytes`（含 `-wal`） | `os.Stat` | `Layout.DBPath`（paths.go:63） |
| `free_disk_bytes` | `<root>` 所在卷剩余空间 | `golang.org/x/sys/windows` `GetDiskFreeSpaceEx`（go.mod 已依赖 x/sys，零新增依赖） |

用途对齐：`cas_total_bytes`+`free_disk_bytes` 供 CAS GC/容量红线（Phase 3 决议，redesign:1138）供数；
`staging_max_run_bytes`+`staging_run_count` 供「recovery_required 保留多久」决议供数；
`task_events_count`/`db_size_bytes` 供 task_events 与整库保留决议供数。headless 侧可复用
`-metrics` 通道把同一组数打进 T14 式 JSON，无需新导出面。

## 5. 决议点清单（供后续成票）

**保留期限/上限**
1. 日志落地形态（Phase 2 引入 slog 时一并拍）：按会话分目录（VS Code 式，保留 N 个）还是按大小轮转
   （JetBrains 式，单文件 M、保留 K 份）；是否叠加总量上限（两个事故案例的教训）。候选基线：
   会话数 10（VS Code）或 10MB×若干份（JetBrains 量级）。
2. task_events 保留窗口：按时间/条数/DB 体积三选一或组合；清理时机（启动时/任务终态后/定时）；
   **截断后 `stream_sequence` 从 MAX+1 继续**写入决议（1.2 的硬约束）。当前无读方，此项零功能风险。
3. `tasks`/`preparations`/`sync_plans`/`observed_snapshots` 是否引入 TTL 物理清理
   （preparations 已有 `expires_at` 语义但无删除执行）；快照是否只保每 relation 每侧最近 N 份。
4. staging：`recovery_required` 证据保留多久；用户确认（`acknowledged_at`）后是否即刻清理；
   超期未处置是否降级提示或自动裁决（涉及恢复安全性，需与 ADR-0004 §4 恢复矩阵联审）。
5. CAS 保留/GC：Phase 3 决议不变（ADR-0004:97），但第 4 节观测指标应随 Phase 2 staging 落地同票埋点。
6. 诊断包（未来导出面）：打包内容边界（DB 摘要 vs 整库）、Diagnostic ID 形态、分享侧保留参考
   mclo.gs 90 天惯例。

**容量**
7. 容量红线由哪个指标承载（CAS 总量、staging 单运行、`<root>` 卷剩余），阈值数值后置到有观测数据后决议。

**脱敏**
8. R1 路径规范化是否**现在**就在 scanner 层落地（`Diagnostic.Detail` 今天已把含用户名的绝对路径写进
   `observed_snapshots`——先改存储层，导出面后建不返工）。
9. R3 凭据硬规则成文（config 值永不入日志/诊断/错误 detail），并复核现有 `log.Printf("%v", err)`
   调用点是否可能透传含敏感值的底层错误。
10. R2 metrics 的 Host 字段去留（已有制品，改动成本最低的先例）。
