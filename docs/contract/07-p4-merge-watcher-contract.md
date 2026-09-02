# Phase 4 合并与监听契约（P4-MERGE-WATCH）

> **状态：已决议（契约草案定稿）。** 2026-09-03 经决策图票 #75 用户拍板定稿（十题决议见 §7）；执行会话按本规格施工，不再做隐式决策。
> 来源：决策图「PackGradle Phase 4 决策图（合并·watcher·横切保留脱敏）」票 #75。
> 依据：[ADR-0009](../adr/0009-merge-semantics-and-adapter.md)（合并事实模型权威）、[ADR-0010](../adr/0010-watcher-trigger-and-scan-protocol.md)（watcher 权威）、[ADR-0011](../adr/0011-crosscutting-retention-and-redaction.md)（横切决议，零 wire 项）、契约 03 §2、契约 04、契约 05、契约 06、CONTEXT.md 词条（合并/冲突块/监听/自动快速更新/快速更新/授权模式）。

## 0. 覆盖范围与硬约束

覆盖：**合并的计划面投影（分类/操作/决议/冲突块证据/预览）+ 统一快速更新用例 + watcher 状态投影与系统通知 + 事件增量 + 前端交互契约** 的 DTO、方法签名、错误码与交互口径。

硬约束（沿契约 03 §0 / 05 §0 / 06 §0，全程生效）：

1. `dto.go` 既有 DTO 字段只增不删；顶层 `schema_version`、slice 归一 `[]`、错误用 `AppErrorDTO/ProblemDTO`；
2. ADR-0009/0010 是事实模型权威：合并引擎与黑名单、触发语义、静默期、连败暂停、监听面均**只投影不重定**；
3. 免确认判定**全入口唯一口径**不变：计划 `confirmation_requirements` 为空 ⇔ 授权模式下免确认执行（契约 06 §0.4）；
4. 冲突与删除永不自动（ADR-0005 §4 红线）；`merged_clean` 属非冲突操作，随授权模式批量执行（ADR-0009 §4）；
5. ADR-0011 横切决议均为后端内部动作，**零 wire 项**（`GetStorageStats` 随执行规格新增，ADR-0011 §8 勘误，本文不重定）。

## 1. 能力与可用性增量

`WorkspaceFeaturesDTO` Phase 4 变更仅一处：

| feature | P3 | P4 | 说明 |
| --- | --- | --- | --- |
| `conflict_resolution` | `choose_side` | **`merge`** | P1 预留枚举值点亮（ADR-0009） |
| `watcher` feature 位 | — | **不加** | 自动同步无用户入口动作可门控；工作状态经 `watch_status` 投影（§3.2），feature 位无消费方 |

availability 零变更：`quick_update` 维持契约 06 §1 口径（授权模式开启 + 无活跃任务 + 非 recovery + scan ready，`err.auth_mode.disabled` 等）；watcher 自动链不经过 availability（内部自查前置：恢复期只标脏不自动物化，ADR-0010 §4），但不绕过任务互斥守卫（ADR-0010 §5）。

## 2. 用例签名总览（SyncService 2 个新方法）

| 用例 | application 层 | transport 层（Wails 方法） |
| --- | --- | --- |
| 统一快速更新 | `QuickUpdate(ctx, QuickUpdateInput) (view.QuickUpdateResultView, error)` | `QuickUpdate(relationID string) (QuickUpdateResultDTO, error)` |
| 合并预览 | `GetMergedPreview(ctx, GetMergedPreviewInput) (view.MergedPreviewView, error)` | `GetMergedPreview(planID, resourceID string) (MergedPreviewDTO, error)` |

服务归属：两方法均进 `internal/application/sync` + `SyncService`（relation 域用例）。快速更新下沉后端是 ADR-0010 §2 对契约 06 §4「零新方法」的局部推翻（P3 语境的快速更新=纯前端编排，窗口外无法自动执行）。服务方法总数 79 → 81（SyncService）+ 4（SettingsService，零扩容：watcher 无开关——授权模式即自动执行总闸；保留设置面已在契约 06）。前端 `utils/quickUpdate.ts` 编排（含 `waitForTask` 500ms 轮询）整体退役。

## 3. DTO 规格

### 3.1 QuickUpdate（统一快速更新用例）

```go
// QuickUpdateResultDTO 是一次快速更新链的收口结果（Q1：同步三态）。
type QuickUpdateResultDTO struct {
    SchemaVersion int    `json:"schema_version"`
    RelationID    string `json:"relation_id"`
    Outcome       string `json:"outcome"` // no_diff|apply_started|awaiting_confirmation
    PlanID        string `json:"plan_id,omitempty"`        // apply_started/awaiting_confirmation 回填
    ApplyTaskID   string `json:"apply_task_id,omitempty"`  // 仅 apply_started 回填
}
```

行为语义（链 = ADR-0010 §1：扫描 → PrepareSync → ResolvePlan → 免确认判定 → ConfirmPlan）：

1. **阻塞到链收口再返回**（时长 ≈ 扫描 + 计划推导，冷启动秒级、有 P1 性能门槛背书；与被退役的前端轮询等价，对 wire 是一次 Promise）；
2. **无差异短路**：扫描后无差异 → `no_diff`，不建计划（对契约 06 前端编排的补口——今天空计划会走完全链）；有差异才继续；
3. `requested_exactness` **恒 exact**（沿今天前端硬编码），不设入参；PrepareSync 输入（revision/双端快照）由用例内部取最新；
4. 停靠判定与契约 06 §4 同口径：draft 含冲突（无决议输入）或 resolved `confirmation_requirements` 非空或授权关闭 → `awaiting_confirmation`（计划停留既有流）；requirements 空 ∧ 授权开启 → `ConfirmPlan` → `apply_started`；
5. **并发 join**：同关系链进行中时，并发调用等待并返回**同一结果**（双击/双窗口安全，对齐 ConfirmPlan 幂等先例）；其他来源的活跃任务照常互斥（`err.scan.already_running` 透传）；
6. 链内失败 `AppError` 透传（`err.scan.*`、`err.recovery.in_progress` 等，零新码）；watcher 自动链与手动入口调同一用例，自动链的触发层（静默期/单飞/连败计数/暂停复位）在用例之外（ADR-0010 §5/§6）。

### 3.2 WorkspaceStateDTO 增量（watcher 状态 + 待确认计划）

```go
// WorkspaceStateDTO 增量（只增不删）：
//   PendingPlanID string `json:"pending_plan_id,omitempty"` —— 最新一张待人工计划
//     （status ∈ {draft, resolved} 且非 stale/expired，按创建时间最新；无则空）
//   WatchStatus   string `json:"watch_status,omitempty"`     —— active|unavailable|paused
```

- `watch_status` 空值 = 未挂载（非健康关系不常驻监听，ADR-0010 §4）；恢复期挂载保持、值为 `active`（触发只标脏不自动物化是链内行为，不是监听状态）；
- `paused` = 该关系自动物化连败 2 次暂停（ADR-0010 §6）；`unavailable` = 监听死亡（`watch_failed`，ADR-0010 §7）；`paused` 由手动快速更新成功复位；
- 两态均为**会话内存态**（重启复位：持续故障会再次连败再次暂停，有界；零持久化零 schema）；
- `pending_plan_id` 同时是系统通知去重依据（§3.5）与前端角标数据源。

### 3.3 合并的计划面投影

| 层 | 增量 | 说明 |
| --- | --- | --- |
| `Classification`（diff 包） | 新值 **`merged_clean`** | 双侧同改（digest 不同）、diff3 零冲突块、类型校验通过（ADR-0009 §4）；校验失败降级 `conflict_modify`（块证据保留，非错误）；`GetChanges` 的 classification 筛选枚举随之扩展 |
| `ResolutionChoice` | 新值 **`take_merged`** | `merged_clean` 行的默认推荐选择；作用于非 merged_clean 行 → 既有 `err.plan.resolution_invalid` |
| `OperationDTO.Kind` | 新值 **`write_merged`** | 一资源一操作 = 双端写合并产物；`reversible=true`（产物一律入 CAS，ADR-0009 §9）；Kind 即内容源分派（`materialize`=下载源先例），暂存期按计划锁定的三侧快照确定性重算（ADR-0009 §8），执行/回滚/补偿走既有管线，前置条件=双端字节与快照相符 |
| `ConflictDTO.Detail` | hunk JSON 定形 | `{"hunks":[{"project":{"start":N,"lines":[...]},"base":{"start":N,"lines":[...]},"runtime":{"start":N,"lines":[...]}}]}`——域词汇（project/base/runtime），非 diff3 的 A/O/B（CONTEXT.md 词条口径）；一文件全部块打包成数组，SQL 层零 schema 变更（ADR-0009 §3） |
| `ChangesSummaryDTO` / `PlanSummaryDTO` | 各增 **`merged_clean_count`** | 只增；不并入 modify 计数（「这次合并了 N 处」是用户可读信息） |

### 3.4 GetMergedPreview（合并预览，Q10）

```go
// MergedPreviewDTO 是 merged_clean 行的合并结果预览（实时计算，不落库）。
type MergedPreviewDTO struct {
    SchemaVersion int    `json:"schema_version"`
    PlanID        string `json:"plan_id"`
    ResourceID    string `json:"resource_id"`
    RelativePath  string `json:"relative_path"`
    Content       string `json:"content"`       // 合并后全文（与暂存期重算同一确定性逻辑，所见即所写）
    BaseContent   string `json:"base_content"`  // 基线全文（增删改标注的比对锚点）
}
```

- 仅 `merged_clean` 行合法；其他行（含冲突行、非合并行）→ **`err.merge.not_mergeable`**（{0}=resource_id，本契约唯一新错误码，Q8 双零的例外）；
- 实时计算不落库（计划期不落字节纪律不变，ADR-0009 §8）；stale/expired 计划仍可预览（只读）；
- **行级增删改标注由前端对 `content` vs `base_content` 计算**（行级粒度，与冲突块证据同粒度，不做字符级）：绿=新增、红=删除、黄=修改；语法高亮按扩展名分派（toml/json/js/java），未识别类型退纯文本——标注与高亮是渲染层职责，后端只供两段全文。

### 3.5 系统通知（Windows 11 通知中心 toast，Q2/Q9）

- **触发三条件同时满足才弹**：① 自动链停于 `awaiting_confirmation`（手动入口停靠不弹——人就在界面）；② 应用窗口不在前台（正开着看时界面角标已可见）；③ `pending_plan_id` 发生更新（无→有或换新；同一张计划重复停靠不重弹）；
- **点击行为**：窗口前置 + 直达 `/workspaces/:id/plans/:pending_plan_id`（与角标同落点）；
- **实现落点**：后端进程内 WinRT toast（库选型归执行规格，沿评估权重；约束=进程内点击回调直达，不注册系统协议、不改单实例）。事实注记：Wails v3 beta.7 的 Windows 封装未接 WebView2 `NotificationReceived`，前端网页通知路径不可用（WebView2 默认拒绝 Notification 权限）；
- **降级**：toast 不可用或被系统拒绝（通知关闭/勿扰）→ 静默退回应用内角标，不报错不重试。

## 4. 事件增量（契约 04 管线零改动）

- **零新 event_type**；
- `relation_invalidated` **新增一个发射点**：用例停于 `awaiting_confirmation` 之后（手动/自动入口都发）。扫描提交的既有发射在链中段，前端重查会扑空（计划尚未生成），此发收口时序（Q2 竞态）；
- `watch_failed` 按契约 04 §2.5 预留语义**原样启用**：envelope 带 relation_id、payload `{}`、前端按 invalidation 处理 + 一次性「监听不可用」提示（ADR-0010 §7 重建仍败时发出）。

## 5. schema 与错误码

**schema 零推进（v6 止）**：计划整包存 `plan_json`（classification/choice/operation kind 枚举自由扩展）；`conflicts.detail` 为 TEXT 承载 hunk JSON；连败计数/暂停态会话内存；无新任务 kind（链内用 scan/apply 既有任务，`tasks.kind` CHECK 不动）。

**错误码 +1**（Q8 双零、Q5 预览破例）：

| code | args | 场景 |
| --- | --- | --- |
| `err.merge.not_mergeable` | {0}=resource_id | GetMergedPreview 作用于非 merged_clean 行 |

既有复用：`err.plan.resolution_invalid`（take_merged 非法行）、`err.plan.not_found/stale/expired`、`err.scan.*`、`err.recovery.in_progress`、`err.auth_mode.disabled`。locale 增量：`err.merge.not_mergeable` + watch 状态横幅/一次性提示 + 待确认角标 + 通知文案（均非 err.* 的界面文案）。

## 6. 前端投影与交互

- **快速更新入口**（工作区概览主操作，`quick_update` availability 门控）：改调 `QuickUpdate` 单调用；进行中按钮统一 busy（scan/plan/apply 三阶段文案退役——链在后端，前端不再可感知阶段）；`no_diff` → 提示「已是最新」；`apply_started` → 任务中心移交（UX §7.9 既有）；`awaiting_confirmation` → 导航计划页；`utils/quickUpdate.ts` 删除；
- **待确认角标**：工作区列表行/概览以 `pending_plan_id` 渲染「有待确认计划」徽标 → 计划页；`relation_invalidated` 到达即刷新（§4 新发射点）；
- **watch 状态**：`paused` 横幅「自动同步已暂停，手动快速更新一次即可恢复」；`unavailable` 横幅「监听不可用，已回退手动」+ `watch_failed` 一次性 toast；`active` 不渲染；
- **计划页 merge 呈现**：`merged_clean` 行「将自动合并」徽标 + `take_merged` 默认推荐 + 「查看合并结果」入口 → 预览抽屉（§3.4：全文 + 语法高亮 + 绿红黄行级标注）；冲突卡点开 hunk 列表（project/base/runtime 行片段 + 起始行号）；`manual` 兜底入口照旧（外部编辑后再扫描收编）；
- **系统通知**：§3.5 三条件触发、点击直达计划页；降级静默。

## 7. 决议对照表（2026-09-03，票 #75 十题）

| 题 | 决议 | 理由要点 |
| --- | --- | --- |
| Q1 用例返回形状 | 同步三态 + 并发 join：`no_diff/apply_started/awaiting_confirmation`，阻塞到链收口，重复点击同结果 | 与今天前端返回形状同构（committed/manual ↔ apply_started/awaiting_confirmation），前端只删编排；补 no_diff 短路 |
| Q2 待人工发现面 | `WorkspaceStateDTO.pending_plan_id` + `relation_invalidated` 新发射点（停于 awaiting_confirmation 后）+ **Windows 系统通知**（用户加码） | watcher 卖点=该你出面时立刻知道；扫描发射在链中段有竞态，补发收口 |
| Q3 watch 状态 | 单枚举 `watch_status: active\|unavailable\|paused`，会话内存态；watch_failed 沿契约 04 §2.5 预留语义启用 | 双布尔有非法组合（死了还谈暂停）；零持久化零协议改动 |
| Q4 merged 操作投影 | `OperationDTO.Kind` 新值 `write_merged`，一资源一操作=双端写 | Kind 即内容源分派（materialize 先例）；清单一行一文件 |
| Q5 merge 证据 | hunk JSON 域词汇命名 + `merged_clean_count` 计数 + **合并预览要做**（用户翻案） | 预览=实时计算不落库，锚定「所见即所写」 |
| Q6 features | `conflict_resolution` → `merge`；不加 watcher feature 位 | 能力声明管入口显隐，watcher 无按钮 |
| Q7 SettingsService | 零扩容：无 watcher 开关（授权模式=总闸）；GetStorageStats 留执行规格 | 关授权=照常监控只停计划不出手；彻底静默等真实需求 |
| Q8 schema/错误码 | schema 零推进；错误码零新增**例外一处**（预览的 not_mergeable，0→1） | plan_json 整包、枚举自由扩展、无新任务 kind |
| Q9 通知触发 | 三条件：自动链停靠 ∧ 窗口不在前台 ∧ pending_plan_id 更新；点击前置窗口直达计划页；失败静默降级角标 | 全是现成信息，防重复通知轰炸 |
| Q10 预览形状 | 纯文本全文 + 语法高亮（toml/json/js/java）+ 行级增删改绿红黄标注；锚点=基线全文；标注/高亮前端算 | 后端只供两段全文，渲染归前端；行级粒度与冲突块同款 |

## 8. 硬约束落实对照

| 硬约束 | 落实位置 |
| --- | --- |
| 只增不删 / schema_version / `[]` / AppErrorDTO | §3 全部新 DTO 与字段增量；错误形态沿用契约 03 §2.7 |
| ADR-0009/0010 权威、只投影不重定 | §0.2/§3.1（链语义转述 ADR-0010 §1）/§3.3（分类与操作转述 ADR-0009 §4）/§3.2（暂停/不可用转述 §6/§7） |
| 免确认唯一口径零特判 | §3.1.4：停靠判定=契约 06 §4 同口径；merged_clean 随授权批量执行 |
| 冲突与删除永不自动 | §3.1.4：draft 含冲突无决议输入即停；`merged_clean` 之外含块冲突永不进自动面 |
| 横切零 wire 项 | §0.5/§2：SettingsService 零扩容、GetStorageStats 留执行规格（ADR-0011 §8） |
