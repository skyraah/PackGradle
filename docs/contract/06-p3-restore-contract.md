# Phase 3 回滚契约（P3-RESTORE）

> **状态：已决议（契约草案定稿）。** 2026-09-01 经决策图票 #53 用户拍板定稿（十一题全落定，见 §11）；执行会话按本规格施工，不再做隐式决策。
> 来源：决策图「PackGradle Phase 3 决策图（回滚·CAS GC·下载物化）」票 #53。
> 依据：[ADR-0005](../adr/0005-mod-update-channel.md)（下载物化红线）、[ADR-0006](../adr/0006-restore-rollback-semantics.md)（回滚事实模型权威，本文只定投影与交互）、[ADR-0007](../adr/0007-cas-retention-gc.md)（保留/GC 权威）、契约 03 §2、契约 04、契约 05、研究笔记 `docs/research/p3-cf-download-channel.md`（分支 `research/p3-cf-download`）、UX 原型 [p3-restore-prototype.html](../frontend/p3-restore-prototype.html)（结构 B 定稿）。

## 0. 覆盖范围与硬约束

覆盖：**回滚三方法 + 用户对象补全 + 快速更新/授权模式 + download 物化模式 + CF 探测透出 + GC/保留设置服务面** 的 DTO、方法签名、schema v6、事件增量、错误码与前端交互契约。

硬约束（沿契约 03 §0 / 05 §0，全程生效）：

1. `dto.go` 既有 DTO 字段只增不删；顶层 `schema_version`、slice 归一 `[]`、错误用 `AppErrorDTO/ProblemDTO`；
2. ADR-0006/0007 是事实模型权威：四标记矩阵、`exact_infeasible`、降级链、整场失败退出、GC 两层模型均**只投影不重定**；
3. mod 字节不进 CAS（ADR-0005 §7）：`redownload_required` 的下载字节只走 staging；用户提供的字节凭目标 digest 验收进 **staging 绑计划**（§3.5），提交后随计划清理；
4. 免确认判定**全入口唯一口径**：计划 `confirmation_requirements` 为空 ⇔ 授权模式下免确认页（§4）；不新增任何按入口特判的分支；
5. CF 探测是可用性辅助非承诺（ADR-0006 §7）：探测失败不阻塞 prepare，`unknown` 不阻塞确认；下载机制（直链构造/murmur2/频控）**不在本文**，归 ADR-0008（票 #54）。

## 1. 能力与可用性增量

`WorkspaceFeaturesDTO` Phase 3 固定值变更：

| feature | P2 | P3 | 说明 |
| --- | --- | --- | --- |
| `restore_preview` / `restore_apply` | false | **true** | PrepareRestore → ConfirmRestorePlan 全链路点亮 |
| `materialization_modes` | `["copy"]` | **`["copy","download"]`** | download 模式服务面见 §3.7 |
| `sync_apply` / `history_view` | true | true | 不变 |

availability 新注册两个动作（后端推导，前端不得自行推断）：

| action | available 条件 | 不可用 reason |
| --- | --- | --- |
| `prepare_restore` | 无活跃任务，且 `relation_health` 非 `recovery_required`，且 `scan_state == "ready"` | `err.scan.already_running`；`err.recovery.in_progress`；`err.scan.incomplete` |
| `quick_update` | `authorized_apply` 开启，且同上三条 | `err.auth_mode.disabled`（**新增**）；其余同上 |

`apply_sync` 不变。注：回滚目标 head 合法（空差异计划走既有先例，ADR-0006 §1），availability 不含 commit 维度；head 禁选纯属 UI 防误触。

## 2. 用例签名总览（SyncService 5 个新方法 + SettingsService 新服务 3 方法）

| 用例 | application 层 | transport 层（Wails 方法） |
| --- | --- | --- |
| 准备回滚 | `PrepareRestore(ctx, PrepareRestoreInput) (view.RestorePlanView, error)` | `PrepareRestore(input RestorePrepareDTO) (RestorePlanDTO, error)` |
| 决议回滚计划 | `ResolveRestorePlan(ctx, ResolveRestorePlanInput) (view.RestorePlanView, error)` | `ResolveRestorePlan(input ResolveRestorePlanDTO) (RestorePlanDTO, error)` |
| 确认回滚 | `ConfirmRestorePlan(ctx, ConfirmRestorePlanInput) (view.TaskView, error)` | `ConfirmRestorePlan(input ConfirmRestorePlanDTO) (TaskDTO, error)` |
| 用户对象补全 | `StageUserObject(ctx, StageUserObjectInput) (view.RestorePlanView, error)` | `StageUserObject(input StageUserObjectDTO) (RestorePlanDTO, error)` |
| 回滚计划读 | `GetRestorePlan(ctx, planID) (view.RestorePlanView, error)` | `GetRestorePlan(planID string) (RestorePlanDTO, error)` |
| 保留设置读 | `GetRetentionSettings(ctx) (view.RetentionSettingsView, error)` | `GetRetentionSettings() (RetentionSettingsDTO, error)` |
| 保留设置写 | `UpdateRetentionSettings(ctx, UpdateRetentionSettingsInput) (view.RetentionSettingsView, error)` | `UpdateRetentionSettings(input UpdateRetentionSettingsDTO) (RetentionSettingsDTO, error)` |
| 授权开关 | `SetWorkspaceAuthorized(ctx, relationID, enabled) (view.WorkspaceView, error)` | `SetWorkspaceAuthorized(relationID string, enabled bool) (WorkspaceDTO, error)` |

服务归属（Q1/Q3）：回滚四用例进 `internal/application/sync` + `SyncService`（relation 域用例）；**新开 transport 服务 `packgradle.core.SettingsService`**（保留设置 + 工作区授权开关——设置/开关域，不与同步执行混装）。`GetRestorePlan` 为对称 `GetPlan`（P1）的读伴随，契约撰写完备性增补，非新决策。服务方法总数 74 → 79（SyncService）+ 3（SettingsService）。

**运行投影零新方法**：回滚运行复用 P2 全套（ADR-0006 §8）——`GetApplyRun`/`ListApplyOperations`/`AcknowledgeRecovery` 签名不变，`kind=restore` 任务与 apply 运行同投影；恢复详情页、恢复四路裁决、`err.recovery.*` 全部照旧。

## 3. DTO 规格

### 3.1 PrepareRestore

```go
// RestorePrepareDTO 是准备回滚输入（Q4：目标 baseline 后端由 commit 推导，不收 baseline id）。
type RestorePrepareDTO struct {
    RelationID string `json:"relation_id"`
    CommitID   string `json:"commit_id"` // 任意历史提交（含 restore 提交=重做）；head 合法（空差异计划）
}
// 成功 → RestorePlanDTO（status=draft）。
```

行为语义（单 RunInTx，ADR-0003 doctrine；探测在事务外，§5）：

1. 读 commit（`err.restore.commit_not_found`：不存在或跨关系）→ 取其 `result_baseline` 为写回目标（双端强一致化，ADR-0006 §1）；
2. 逐资源四标记判定 = ADR-0006 §2 确定函数（prepare 时点）；对 `redownload_required` 行做 CF 尽力探测（§5）；
3. `exact_infeasible` = 存在资源标记 ∉ {`restorable_from_cas`, `redownload_required`}（ADR-0006 §4），附 `blocked_by` 清单（§3.2）；
4. 落 `sync_plans(kind=restore, status=draft)`；**digest/expiry/stale/单活跃计划规则全部沿用 sync 计划既有实现**（Q9 澄清），本文不另定。

### 3.2 RestorePlanDTO 族（Q2：独立族，不复用 SyncPlanDTO）

```go
// RestorePlanDTO 是回滚计划投影。
type RestorePlanDTO struct {
    SchemaVersion            int                          `json:"schema_version"`
    PlanID                   string                       `json:"plan_id"`
    RelationID               string                       `json:"relation_id"`
    TargetCommitID           string                       `json:"target_commit_id"`
    Status                   string                       `json:"status"` // draft|resolved|confirmed|applied|expired|stale（沿 sync_plans CHECK）
    ExactFeasible            bool                         `json:"exact_feasible"` // 实时就绪面（§3.5），非 draft 静态标记
    BlockedBy                []RestoreBlockedItemDTO      `json:"blocked_by"`     // draft 时点 exact_infeasible 清单（ADR-0006 §4）
    Items                    []RestorePlanItemDTO         `json:"items"`
    RequestedExactness       string                       `json:"requested_exactness,omitempty"` // resolved 后回填 exact|allow_partial
    ConfirmationRequirements []ConfirmationRequirementDTO `json:"confirmation_requirements"`     // 恒非空（§0.4）
    ExpiresAt                string                       `json:"expires_at"`
    CreatedAt                string                       `json:"created_at"`
}

// RestorePlanItemDTO 是回滚计划单资源行。
type RestorePlanItemDTO struct {
    ResourceID     string `json:"resource_id"`
    RelativePath   string `json:"relative_path"`
    ChangeKind     string `json:"change_kind"`  // create|modify|delete（delete 行不占四标记，ADR-0006 §2/§5）
    Marker         string `json:"marker"`       // restorable_from_cas|redownload_required|user_object_required|unrecoverable
    MarkerReason   string `json:"marker_reason,omitempty"` // user_object_required 行：no_redownload_info|cf_unavailable（§5 降标）
    Skipped        bool   `json:"skipped"`      // resolved 后 skip 决议投影（Q5）
    Staged         bool   `json:"staged"`       // user_object_required 行补全就绪（§3.5）
    DeletionWarn   bool   `json:"deletion_warn,omitempty"` // 手放 mod 删除＝「不可重取」警示（ADR-0006 §5）
    PreserveSkip   bool   `json:"preserve_skip,omitempty"` // >32 MiB 非 mod＝「旧版本不留存」警示（ADR-0007 §7）
    Availability   string `json:"availability,omitempty"`  // ok|unknown，仅 redownload_required 行（§5）
    NewerAvailable bool   `json:"newer_available,omitempty"` // 仅 ok 行；仅提示，版本决策归 packwiz
    ExpectedDigest string `json:"expected_digest,omitempty"` // user_object_required 行：验收入库的目标摘要
}

// RestoreBlockedItemDTO 是 exact 阻塞清单行。
type RestoreBlockedItemDTO struct {
    ResourceID   string `json:"resource_id"`
    RelativePath string `json:"relative_path"`
    Marker       string `json:"marker"`
}
```

确认要求（§0.4 机制的载体）：restore 计划在既有推导（`overwrite`/`delete`/`unrecoverable` 等，§6.5 架构）之上**恒追加一条 `code=restore_acknowledge`**（severity=warning，resource_count=操作行数），保证 `confirmation_requirements` 非空——授权模式零特判而自然不适用回滚（ADR-0006 §6）。

### 3.3 ResolveRestorePlan（Q5：决议面＝partial 逐资源 skip）

```go
// ResolveRestorePlanDTO 是回滚决议输入（ADR-0006 §3：无冲突决议面）。
type ResolveRestorePlanDTO struct {
    PlanID             string   `json:"plan_id"`
    RequestedExactness string   `json:"requested_exactness"` // exact|allow_partial（沿 P2 枚举；空值缺省 allow_partial）
    SkipResourceIDs    []string `json:"skip_resource_ids"`   // 逐资源 skip 决议，固化于 resolved plan
}
```

行为语义（单 RunInTx）：

1. 校验 `status=draft` 且非 stale/expired（`err.plan.stale`/`err.plan.expired` 既有）；
2. exact 决议遇实时就绪面不满（§3.5）→ `err.restore.exact_infeasible`（引导改 `allow_partial` 重 resolve，ADR-0006 §4 前置拦截）；
3. skip 仅对 `user_object_required`（未 staged）与 `unrecoverable` 行合法，其余 → `err.restore.skip_invalid`；
4. 固化 `requested_exactness` 与 skip 清单，status→resolved。

### 3.4 ConfirmRestorePlan（Q1：单调用建 kind=restore 任务，ApplyRestore 不上 wire）

```go
// ConfirmRestorePlanDTO 是回滚确认输入。
type ConfirmRestorePlanDTO struct {
    PlanID string `json:"plan_id"`
}
// 成功 → TaskDTO（kind=restore，status=queued；PlanID 字段回填）。
```

行为语义（对齐 P2 ConfirmPlan 幂等口径，单 RunInTx）：

1. 校验 `status=resolved` 且非 stale/expired；
2. **幂等重入**：存在活跃 restore 运行（apply_runs.state ∉ 终局）→ 返回既有 TaskDTO（双击/双窗口安全）；存在未收口恢复 → `err.recovery.in_progress`；
3. **failed 可重入（Q8）**：同 plan 上一运行 `state=failed`（staging 期下载失败终局）→ 允许再次确认、建**新**运行；上一运行已 `committed` → `err.plan.apply_not_reentrant`（引导重新 prepare）；
4. 建 `tasks(kind=restore, status=queued)` + `apply_runs(state=prepared)`，提交后发布 `task_updated`。

### 3.5 StageUserObject 与 exact 就绪面（Q10）

```go
// StageUserObjectDTO 是用户对象补全输入。
type StageUserObjectDTO struct {
    PlanID     string `json:"plan_id"`
    ResourceID string `json:"resource_id"`
    SourcePath string `json:"source_path"` // 本地绝对路径；读字节→校验→暂存，暂存路径不透出
}
// 成功 → 更新后的 RestorePlanDTO（该行 staged=true，前端重渲就绪面）。
```

行为语义：

- 仅对 `marker=user_object_required` 行合法（否则 `err.userobject.not_required`）；`status ∈ {draft, resolved}` 均可补全（confirm 前补齐）；
- 按目标 `expected_digest` 验收：不符 → `err.userobject.hash_mismatch`（{0}=期望摘要，可重试）；通过 → 字节进 **staging 绑 plan（plan_id+resource_id），不进 CAS**（ADR-0005 §7），提交后随计划清理；
- staging 不参与 plan_digest（计划不可变性不破）；marker 是 prepare 时点确定函数，补全**不改标记**，只改就绪面。

**exact 就绪面（本契约细化口径）**：就绪 = `restorable_from_cas` ∪ `redownload_required` ∪ (`user_object_required` ∧ staged)。`ExactFeasible` 投影实时就绪面（skip 后剩余行全部就绪）；`exact_infeasible`/`blocked_by` 是 draft 时点（staging 空）的静态评估。依据：ADR-0006 §9 公式只把「user_object **未提供**」计入 partial 剩余 ⇒ 提供即不剩余 ⇒ 可达成与目标一致的结果；§4 的「非 cas/redownload 即 infeasible」按 draft 时点读。事实模型（标记矩阵/降级链/整场失败）零改动，已录 §11。

### 3.6 WorkspaceDTO 增量与 RetentionSettingsDTO（Q3/Q6）

```go
// WorkspaceDTO 增量（只增不删）：
//   AuthorizedApply bool `json:"authorized_apply"` —— 投影 relations.authorized_apply

// RetentionSettingsDTO 是保留策略设置（ADR-0007 §2/§7/§8；config.toml [retention] 承载）。
type RetentionSettingsDTO struct {
    SchemaVersion         int   `json:"schema_version"`
    KeepCommits           int   `json:"keep_commits"`            // 默认 20，范围 5–200
    KeepDays              int   `json:"keep_days"`               // 默认 90，范围 7–365
    RelationCapacityBytes int64 `json:"relation_capacity_bytes"` // 默认 1 GiB，范围 128 MiB–20 GiB
    PreserveMaxBytes      int64 `json:"preserve_max_bytes"`      // 默认 32 MiB，范围 1 MiB–512 MiB；0＝不限
    TrashDays             int   `json:"trash_days"`              // 默认 7，范围 1–90（范围本契约定，ADR 未锁）
}
```

`UpdateRetentionSettings` 单键范围校验，越界 → `err.settings.retention_invalid`（{0}=字段名），整体拒绝；写入 config.toml `[retention]`，加载层同校验（ADR-0007 §8）。K=3 硬保底固定不可调，不设键。`SetWorkspaceAuthorized(relationID, enabled)` 切换 `relations.authorized_apply` 并返回更新后 `WorkspaceDTO`；恢复期开关值保留，入口由既有 `err.recovery.in_progress` 门禁挡（CONTEXT.md 授权模式词条）。

### 3.7 物化模式 download 的服务面（ADR-0005 落地面）

- `materialization_modes=["copy","download"]` 仅透出能力；**v1 模式选用为后端推导，无用户选择面**：有重取信息的 mod 操作 → download，其余 → copy；推导规则细节归 ADR-0008（票 #54）；
- `OperationDTO`（sync 计划操作行）增量（只增不删）：
  - `Materialization string \`json:"materialization,omitempty"\``（copy|download；P3 起填充，既有行空值＝copy 兼容）；
  - `PreserveSkip bool \`json:"preserve_skip,omitempty"\``（「旧版本不留存」警示行标记，ADR-0007 §7）；
- restore 计划行不设该字段：`RestorePlanItemDTO.marker` 已承载等价信息（cas=本地字节写回、redownload=download、user=用户提供）；
- 下载执行失败语义见 §6（Q8）：网络失败 ≠ 恢复面。

### 3.8 历史读增量（#52 移交：墓碑行）

```go
// CommitPageDTO 增量（只增不删）：
//   PrunedBeforeCount int `json:"pruned_before_count"` —— 按保留策略已清理的更早提交数
```

被裁提交行自然消失；前端在列表尾渲染墓碑行「更早 N 条提交已按保留策略清理」（原型 H-01 先例），N=0 不渲染。

## 4. 快速更新与授权模式（Q5/Q6/Q7）

- **开关**：`relations.authorized_apply`（schema v6）→ `WorkspaceDTO.authorized_apply` 投影 → `SetWorkspaceAuthorized` 切换；per-workspace、随 relation 共生（§3.6）。
- **免确认判定唯一口径（Q7，全入口一致）**：`confirmation_requirements` 为空 ⇔ 授权模式下前端跳过确认页、resolve 后直接 `ConfirmPlan`；非空必人工。不自动触发：授权模式不引入任何后台/watcher 自动执行（ADR-0005 触发分期，watcher 留 P4）；快速更新始终由用户单次点击发起。
- **快速更新＝纯前端编排既有方法（Q5）**：`StartScan` → `PrepareSync` → `ResolvePlan` →（requirements 空 ∧ authorized → `ConfirmPlan`；否则转待确认计划页走 P2 既有确认流）。零新方法零新 DTO。
- **冲突与删除永不自动**（ADR-0005 §4）：冲突转待确认计划，用户不处理则下轮扫描自然重现（Q5）；快速更新 apply 含 skip ⇒ commit=`partial`、relation 保持 dirty（既有语义，不谎报 clean）。

## 5. CF 探测透出（Q9）

- **时机**：仅 `PrepareRestore` 对「有重取信息」的 redownload 候选行探测（免 key 直链，机制归 ADR-0008）；预算内尽力，预算耗尽/离线/超时**不阻塞 prepare**；
- **结果投影**（行内，跟着用户视线走）：
  - 可获取 → `availability="ok"`（+ `newer_available` 探测所得）；
  - 不可获取（404/下架）→ **prepare 时点降标** `user_object_required` + `marker_reason="cf_unavailable"`（ADR-0006 §7「不可用提前降标」的投影；降标原因透出，原型 waystones 行先例）；
  - 超时/预算耗尽 → 保持乐观标记，`availability="unknown"`（按可重新下载执行，不阻塞确认）；
  - 即行内枚举 = `ok|unknown`；`unavailable` 不是行内态而是降标——对 Q9 拍板口径的落地细化，已录 §11；
- `newer_available`：仅提示「目标非该 mod 最新版」，版本决策归 packwiz CLI（图 Out of scope：不做独立更新检查）；
- **普通 sync 计划 v1 不预探测**（高频调用 + CF 频控 403 风险，#50 笔记）；
- 探测证据（方式/原始结果/耗时）为内部诊断不透出 DTO；「依据」抽屉渲染 `availability`/`expected_digest` 与静态失败语义文案（原型先例）。

## 6. schema v6 迁移（Q6/Q8）

| 变更 | 内容 |
| --- | --- |
| `relations` | 增列 `authorized_apply INTEGER NOT NULL DEFAULT 0`（授权开关，§4） |
| `apply_runs` | state CHECK 增 `'failed'`（终局；表重建迁移） |
| 零新表 | `tasks`/`sync_plans` 的 kind CHECK v1 起已含 `'restore'`/`'gc'`（预留兑现） |

**failed 终局语义（Q8）**：staging 相位下载/物化失败 ⇒ run=`failed` + task=`failed` + `problem_json` 承载 `err.download.*`——**网络失败 ≠ 恢复面**，不进 `recovery_required`（ADR-0006 §7 整场失败退出、零部分提交）；applying 及之后的失败仍走既有恢复路径（ADR-0004 不变）。`failed` 后同 plan 可重新 Confirm（新运行，§3.4.3）。

## 7. 事件增量（契约 04/05 口径，管线零改动）

- **不新增 event_type**；
- `relation_invalidated` **新增一个发射点**：restore committed 事务提交后（gc 任务完成不新增发射点，任务终态全量刷新已覆盖）；
- 进度短语：`msg.task.restore.*` / `msg.task.gc.*`（runner 动态键，locale conformance 矩阵自动纳入）；gc 排队文案＝「等待空闲时段（安全窗口未开 · 自动续排）」（ADR-0007 §3）。

## 8. GC/保留移交面（#52 决议评论 → 契约形状）

语义细节一律见 ADR-0007，本契约只定形状：

- 设置读写：`SettingsService.Get/UpdateRetentionSettings`（§3.6）；
- 历史墓碑行：`CommitPageDTO.pruned_before_count`（§3.8）；
- 「旧版本不留存」警示：restore 行 `preserve_skip` + sync 操作行 `preserve_skip`（§3.2/§3.7，同「不可重取」警示先例，确认页损失面可见）；
- CLI：`pgheadless gc` 子命令（建 `kind=gc` 任务；安全窗口不开 → pending 排队自动续，**不拒绝**）；restore 无新子命令（prepare/resolve/confirm 方法面直用，离线恢复语义由既有恢复协议覆盖）。

## 9. 前端投影与交互（结构 B 定稿）

- **回滚入口（唯一）**：历史记录详情页 `/workspaces/:id/history/:commit_id`（P2 已有路由）主操作「回滚到此状态」，`restore_preview` 门控；head 详情禁选（横幅说明）；历史**列表行不放入口**（防误触；原型 H-01 已按此修正）；
- **新路由** `/workspaces/:id/plans/restore/:plan_id`：draft 只读预览 → 决策（exact/allow_partial + skip）→ resolved 确认。信息结构＝**结构 B 单表全列**（资源/判定/CF 可用性/处理说明四列 + 顶部计数条，与 P2 plans 页操作表同构；原型 H-04 定稿，H-03/05 抛弃归档）；
- **用户对象补全**：行内「提供文件」对话框（本地路径 + 浏览）→ `StageUserObject` → busy（校验中）→ ready（「已校验 · 字节就绪」）/ miss（`err.userobject.hash_mismatch` + 重选重试）；全部就绪且无 unrecoverable ⇒ exact 解锁、横幅转绿（§3.5 就绪面）；
- **确认框四要素**（`confirmation_requirements` 投影）：①删除损失面（N 项将删除，含不可找回/不留存警示行）②CF 重取失败＝整场退出、可重试、不进崩溃恢复 ③回滚永远人工确认（授权模式不适用）④成功产生新回滚记录、历史不改写；
- **快速更新入口**：工作区概览主操作区，`quick_update` availability 门控；授权模式开关在工作区详情设置区（`SetWorkspaceAuthorized`）；
- **设置页**新增「保留策略」节（原型 SET-02）：五参数编辑 + 范围校验演示 + 「立即回收空间」（建 gc 任务）；
- **任务中心**：restore/gc 任务由既有任务投影自动覆盖；恢复详情页复用 P2（`run_id = task_id`）；
- **locale**：§10 全部新码 + `msg.task.restore.*`/`msg.task.gc.*` + `req.restore_acknowledge` 文案入 zh-CN（conformance 正向覆盖）。

## 10. 新增错误码清单

| code | args | 场景 |
| --- | --- | --- |
| `err.restore.commit_not_found` | {0}=commit_id | PrepareRestore：提交不存在或跨关系 |
| `err.restore.exact_infeasible` | {0}=plan_id | ResolveRestorePlan：exact 决议遇未就绪面（引导 allow_partial） |
| `err.restore.skip_invalid` | {0}=resource_id | skip 决议作用于非阻塞行 |
| `err.userobject.hash_mismatch` | {0}=期望摘要 | StageUserObject：文件内容与目标摘要不符 |
| `err.userobject.not_required` | {0}=resource_id | StageUserObject 作用于非 user_object_required 行 |
| `err.download.unavailable` | {0}=文件名 | staging 期下载：CF 资源不可获取（404/下架） |
| `err.download.rate_limited` | — | staging 期下载：CF 频控（403 体嗅探，#50 笔记） |
| `err.download.hash_mismatch` | {0}=文件名 | staging 期下载：字节校验失败 |
| `err.download.network` | — | staging 期下载：网络超时/连接失败 |
| `err.auth_mode.disabled` | — | quick_update availability：授权模式未开启 |
| `err.settings.retention_invalid` | {0}=字段名 | 保留设置越界（整体拒绝） |

既有复用：`err.plan.not_found/stale/expired/apply_not_reentrant`（restore 计划同在 sync_plans）、`err.recovery.in_progress/not_required`、`err.scan.*`（契约 03 §3）、`err.recovery.*`（契约 05 §6）。

## 11. 决议对照表（2026-09-01，票 #53 十一题 + 原型结构）

| 题 | 决议 | 理由要点 |
| --- | --- | --- |
| Q1 方法面 | PrepareRestore/ResolveRestorePlan/ConfirmRestorePlan 三方法上 wire；ApplyRestore 不上 wire（确认即建 kind=restore 任务） | 与 P2 ConfirmPlan 同构；运行事实全在 apply_runs，无第二执行入口 |
| Q2 DTO 族 | 独立 RestorePlanDTO 族 | 四标记/可用性/补全态与 sync 操作行形状差异大，复用即污染 |
| Q3 服务归属 | 回滚方法进 SyncService；新开 SettingsService | relation 域用例不新增 SyncService 之外的家；设置/开关域独立成服务 |
| Q4 Prepare 输入 | 只收 relation_id + commit_id，目标 baseline 后端推导 | 「回到该提交完成后的状态」语义单一，不暴露 baseline 概念 |
| Q5 skip 与快速更新 | 决议面＝partial 逐资源 skip；快速更新＝纯前端编排；冲突下轮扫描自然重现；partial ⇒ commit=partial/dirty | 回滚无冲突决议面（ADR-0006 §3）；编排零后端新增 |
| Q6 授权存储 | `relations.authorized_apply` DB 列 + WorkspaceDTO 投影 + SetWorkspaceAuthorized | per-workspace 运行状态与 relation 共生，非全局数值偏好；config 会有两处真相 |
| Q7 免确认判定 | 唯一口径＝confirmation_requirements 为空；restore 计划恒带 `restore_acknowledge`；扫描后不自动触发 | 全入口一致零特判；ADR-0006 §6 由恒非空自然成立 |
| Q8 failed 终局 | apply_runs 增 failed（v6）；staging 期下载失败＝task failed+err.download.*；failed 后同 plan 可重 Confirm；applying 后失败仍走 recovery | 网络失败≠恢复面；failed 是终局才能重入 |
| Q9 CF 探测 | 内嵌计划行 availability+newer_available；仅 restore prepare 探测；sync 计划不预探测。**细化：unavailable 不是行内态＝prepare 时点降标 user_object_required**（ADR-0006 §7 权威口径） | 透出跟着用户视线走；降标行带原因（原型先例）；频控风险留在低频面 |
| Q10 用户对象 | StageUserObject(plan_id, resource_id, source_path)；staging 绑 plan 不进 CAS；hash 不符报错；**staged 即入 exact 就绪面** | ADR-0006 §9 公式自洽（未提供才计剩余）；计划不可变性不破 |
| Q11 杂项 | 事件零新类型+invalidated 一新发射点、features 点亮、墓碑行、双警示、pgheadless gc、retention 范围校验 | 照单收口，明细在 §3/§6/§7/§8 |
| 原型结构 | 回滚计划页＝**结构 B 单表全列**（H-04 定稿） | 与 P2 plans 页同构、一屏扫读、阻塞项不藏；A/C 抛弃归档 |

## 12. 硬约束落实对照

| 硬约束 | 落实位置 |
| --- | --- |
| 只增不删 / schema_version / `[]` / AppErrorDTO | §3 全部新 DTO 与字段增量；错误形态沿用契约 03 §2.7 |
| ADR-0006/0007 权威、只投影不重定 | §0.2/§3.1/§3.3/§3.5/§8：标记矩阵与 GC 模型零重定；§3.5 就绪面与 §5 降标投影为对 ADR 字面的自洽化细化，已录 §11 |
| mod 字节不进 CAS / staging 绑计划 | §0.3/§3.5：StageUserObject 字节只进 staging；下载字节同（ADR-0005 §7） |
| 免确认唯一口径零特判 | §0.4/§4：requirements 恒非空机制（restore_acknowledge） |
| 探测辅助非承诺 | §0.5/§5：unknown 不阻塞；机制归 ADR-0008 |

## 13. 修订项注记（实现与字面偏差，2026-09-01 收口）

执行会话对本文的三处实现与字面偏差，均已裁决落码；本节按 ADR-0008「契约 06 修订项汇总」同一体例归档，后续票面以本节为准（§2/§10 字面保留历史，不再回改）：

1. **SettingsService 3→4 方法（+`RequestGC`）**：§2 方法表漏列 §9 设置页「立即回收空间」的 wire 载体——`RequestGC(ctx) (view.TaskView, error)` 建 kind=gc 任务（全局单飞幂等，已有活跃 gc 任务时复用不重建）。票 #64 落 GC 任务面、票 #65 把该 transport 面接上 wire。理由：§9 交互必须有后端调用面，且任务创建必须走同一单飞语义（并发点击/收口触发共用，不另设通道）；§2 末句「服务方法总数 74 → 79 + 3」随之为 79 + 4。
2. **新增错误码 11→13（+`err.config.download_concurrency_invalid`、`err.file.read`）**：前者是 ADR-0008 修订项 5（`[download] concurrency` 配置读写）的行为必要载体——下载引擎并发度越界必须可报（域 C 配置面）；后者承载「源文件读取失败」（StageUserObject 读用户文件 I/O 失败、下载引擎读成品/续传文件失败），§10 原清单只有校验失败面、缺读取失败面。理由：两码均为既有错误分桶纪律（`errs.NewDetail` 带 detail 与 args）下的必要载体，不扩语义只补缺口。
3. **GC 级联 `previous_baseline_id` 置空连带 `parent_id=NULL`**：ADR-0007 §1 字面只提「首个存活提交 `previous_baseline_id` 置空（仅元数据重连，内容不改）」，实现同批把该提交 `parent_id` 一并置空。理由：`sync_commits.parent_id` 外键 REFERENCES 被裁提交行，SQLite 立即外键下指向被裁行的引用必须同批解除，否则修剪事务整体失败；语义仍是「元数据重连，内容不改」——历史页从 head 向根遍历至 NULL 即止（被裁的是最旧连续前缀），parent 链不出现悬空断链，ADR-0007 §1「连续前缀、无断链遍历」不变。

