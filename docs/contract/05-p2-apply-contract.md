# Phase 2 Apply 契约（P2-APPLY）

> **状态：已决议（契约草案定稿）。** 2026-08-31 经决策图票 #29 用户拍板定稿（四决策点全按推荐，见 §8）；执行会话按本规格施工，不再做隐式决策。
> 来源：决策图「PackGradle 重构全路线决策图（Phase 1-4）」票 #29。
> 依据：[ADR-0004](../adr/0004-phase2-apply-journal-and-recovery.md)（事实模型权威，本文只定投影与交互）、契约 03 §2.1/§3、契约 04、UX 原型 §4.3/§5.3/§6.6/§7.5/§7.9、`internal/transport/dto.go` 现状。

## 0. 覆盖范围与硬约束

覆盖：**ConfirmPlan → Apply 运行投影 → 恢复投影与人工确认 → SyncCommit 历史读** 的 DTO、方法签名、事件增量与前端交互契约。

硬约束（沿契约 03 §0，全程生效）：

1. `dto.go` 既有 DTO 字段只增不删；顶层 `schema_version`、slice 归一 `[]`、错误用 `AppErrorDTO/ProblemDTO`；
2. 未实现能力不暴露：`restore_preview/restore_apply` 继续 false，`prepare_restore/apply_restore` 不注册 availability（Phase 3）；
3. ADR-0004 是事实模型权威：apply_runs 六阶段、journal 三层、恢复四路裁决、staging 提交后清理均不在本文重定；
4. 普通用户视图**不暴露临时绝对路径与 ownership proof**（ADR-0004 §4）：`ApplyOperationDTO` 不含 `temp_relative_path`/`ownership_proof_json`。

## 1. 能力与可用性增量

`WorkspaceFeaturesDTO`（契约 03 §2.1 形状不变）Phase 2 固定值变更：

| feature | P1 | P2 | 说明 |
| --- | --- | --- | --- |
| `sync_apply` | false | **true** | ConfirmPlan/Apply 全链路点亮 |
| `history_view` | false | **true** | ListCommits/GetCommit + 历史 Tab |
| `materialization_modes` | `[]` | **`["copy"]`** | copy-only Materializer |
| `restore_preview` / `restore_apply` | false | false | 不变（Phase 3） |

availability 新注册 `apply_sync` 动作（后端推导，前端不得自行推断）：

| action | available 条件 | 不可用 reason |
| --- | --- | --- |
| `apply_sync` | 无活跃任务，且 `relation_health` 非 `recovery_required`，且 `scan_state == "ready"`，且该关系存在可应用的计划（status=`resolved`、非 stale、非 expired） | `err.scan.already_running`；`err.recovery.in_progress`；`err.scan.incomplete`；`err.plan.stale` / `err.plan.expired`（既有）；`err.plan.none_ready`（无可用计划，**新增**） |

## 2. 用例签名总览（SyncService 6 个新方法）

| 用例 | application 层 | transport 层（Wails 方法） |
| --- | --- | --- |
| 计划确认 | `ConfirmPlan(ctx, ConfirmPlanInput) (view.TaskView, error)` | `ConfirmPlan(input ConfirmPlanDTO) (TaskDTO, error)` |
| Apply 运行投影 | `GetApplyRun(ctx, relationID) (view.ApplyRunView, error)` | `GetApplyRun(relationID string) (ApplyRunDTO, error)` |
| 逐操作清单 | `ListApplyOperations(ctx, ListApplyOperationsInput) (view.ApplyOperationPage, error)` | `ListApplyOperations(relationID, taskID string, cursor string, limit int) (ApplyOperationPageDTO, error)` |
| 恢复人工确认 | `AcknowledgeRecovery(ctx, taskID) (view.WorkspaceView, error)` | `AcknowledgeRecovery(taskID string) (WorkspaceDTO, error)` |
| 历史列表 | `ListCommits(ctx, relationID, ports.PageRequest) (view.CommitPage, error)` | `ListCommits(relationID string, cursor string, limit int) (CommitPageDTO, error)` |
| 历史详情 | `GetCommit(ctx, relationID, commitID) (view.CommitView, error)` | `GetCommit(relationID, commitID string) (CommitDTO, error)` |

服务归属：全部留在 `internal/application/sync` + `SyncService`（Apply/恢复/历史同为 relation 域用例，不新增 transport 服务）。

## 3. DTO 规格

### 3.1 ConfirmPlan

```go
// ConfirmPlanDTO 是计划确认输入。
type ConfirmPlanDTO struct {
    PlanID string `json:"plan_id"`
}
// 成功 → TaskDTO（kind=apply，status=queued；PlanID 字段回填）。
```

行为语义（单 RunInTx，ADR-0003 doctrine；事件恒在提交后）：

1. 读计划（`err.plan.not_found`）→ 校验 `status=resolved` 且非 stale/expired（`err.plan.stale`/`err.plan.expired`）；
2. **幂等重入（D4）**：该 plan 存在活跃 apply 任务（apply_runs.state ∉ 终局）→ 写 `plan_confirmations` 不新建，返回既有 TaskDTO——双击/双窗口安全；同 plan 的上一运行已 `committed` → `err.plan.apply_not_reentrant`（引导重扫生成新计划）；存在未收口恢复 → `err.recovery.in_progress`；
3. 首次确认：生成 `confirmation_token` 落 `plan_confirmations`（表收口，§7）；建 `tasks(kind=apply, status=queued)`；建 `apply_runs(state=prepared)`——计划 digest、relation_revision、前置条件、恢复对象引用按 ADR-0004 §1 落列；
4. 提交后发布 `task_updated`。

### 3.2 ApplyRunDTO

```go
// ApplyRunDTO 是一次 Apply 的运行头投影（ADR-0004 §1 六阶段）。
type ApplyRunDTO struct {
    SchemaVersion   int    `json:"schema_version"`
    TaskID          string `json:"task_id"`     // 即 run_id（apply_runs 主键）
    RelationID      string `json:"relation_id"`
    PlanID          string `json:"plan_id"`
    PlanDigest      string `json:"plan_digest"`
    State           string `json:"state"` // prepared|staged|applying|verifying|committed|recovery_required
    OperationCount  int    `json:"operation_count"`
    StagingCleared  bool   `json:"staging_cleared"`
    AcknowledgedAt  string `json:"acknowledged_at,omitempty"` // 人工确认时间（recovery_required 收口后）
    CommitID        string `json:"commit_id,omitempty"`       // committed 后回填
    CreatedAt       string `json:"created_at"`
    UpdatedAt       string `json:"updated_at"`
}
```

`GetApplyRun(relationID)` 返回该关系当前/最近一次运行（按 created_at 最新；关系无运行 → `err.apply.no_run`）。前端消费方：恢复详情页、工作区横幅、plans 页进度区。

### 3.3 ApplyOperationDTO（逐操作投影）

```go
// ApplyOperationDTO 是单操作行投影。硬约束 4：不含 temp_relative_path / ownership_proof_json。
type ApplyOperationDTO struct {
    OperationID  string `json:"operation_id"`
    Ordinal      int    `json:"ordinal"`
    Status       string `json:"status"` // pending|running|applied|verified|failed|compensated（ADR-0004 §2 单调路径）
    ResourceID   string `json:"resource_id,omitempty"`
    RelativePath string `json:"relative_path,omitempty"` // root-relative，非临时路径
    ChangeKind   string `json:"change_kind,omitempty"`   // 与计划操作一致（create/modify/delete）
    ResultCode   string `json:"result_code,omitempty"`   // 终局摘要码（成功为空；失败/补偿带说明码）
}

type ApplyOperationPageDTO struct {
    SchemaVersion int                 `json:"schema_version"`
    Items         []ApplyOperationDTO `json:"items"`
    NextCursor    string              `json:"next_cursor,omitempty"`
}
```

`ListApplyOperations(relationID, taskID, cursor, limit)` 按 `ordinal` 升序分页（cursor=上一页末条 operation_id，与 GetChanges 同协议）；task 不存在或跨关系 → `err.apply.run_not_found`。恢复详情页逐资源证据即此清单。

### 3.4 AcknowledgeRecovery

```go
// AcknowledgeRecovery(taskID) → WorkspaceDTO（确认后工作区投影）
```

行为语义（单 RunInTx）：

- 前置：`apply_runs.state=recovery_required`（否则 `err.recovery.not_required`）；已 acknowledged → 幂等返回当前投影不报错；
- 效果：`acknowledged_at=now`；`relation.health=healthy`；**头基线不动、不建 SyncCommit**（ADR-0004 §5：恢复路径不推进 Baseline）；发布 `relation_invalidated`（引导重扫）；
- 落点投影：工作区回到「健康但需重扫」（baseline/diff_state 由既有投影自然收敛），恢复页与列表行恢复态解除；
- 四路裁决中的 `mark-applied` 不经本动作：probe 通过后 run 走 verifying→committed 正常收口（ADR-0004 §4），`redo/compensate` 为恢复器自动路径，均不产生 acknowledge。

### 3.5 CommitDTO（历史读）

```go
// CommitSummaryDTO 是历史列表行。
type CommitSummaryDTO struct {
    CommitID            string `json:"commit_id"`
    Kind                string `json:"kind"` // initialize|sync|restore
    Completeness        string `json:"completeness"` // exact|partial
    RemainingChangeCnt  int    `json:"remaining_change_count"`
    CreatedAt           string `json:"created_at"`
}

// CommitChangeDTO 是单资源变更行（源：commit_changes；relative_path 经资源身份联取，施工票落地）。
type CommitChangeDTO struct {
    ResourceID    string  `json:"resource_id"`
    ChangeKind    string  `json:"change_kind"`
    ProjectBefore *string `json:"project_before,omitempty"` // 表示摘要（联表），缺省 null
    ProjectAfter  *string `json:"project_after,omitempty"`
    RuntimeBefore *string `json:"runtime_before,omitempty"`
    RuntimeAfter  *string `json:"runtime_after,omitempty"`
}

type CommitDTO struct {
    SchemaVersion int               `json:"schema_version"`
    Summary       CommitSummaryDTO  `json:"summary"`
    PlanID        string            `json:"plan_id"`
    Changes       []CommitChangeDTO `json:"changes"`
}

type CommitPageDTO struct {
    SchemaVersion int                `json:"schema_version"`
    Items         []CommitSummaryDTO `json:"items"`
    NextCursor    string             `json:"next_cursor,omitempty"`
}
```

`ListCommits` 按 `created_at` DESC 分页；`GetCommit` 附 changes 全量（单 commit 资源数有限，不分页）；commit 不属于该关系 → `err.commit.not_found`（**新增**）。审计用户故事（原型 §3「一次同步改变了什么」）由此承载。

## 4. 事件协议增量（契约 04 增补，管线零改动）

**D1：不新增 event_type。** 三类型信封与受控重查管线完全不变：

- Apply 进度 = `task_updated`：phase 进度短语（`msg.task.apply.*`，runner 构造的动态键，locale conformance msg.* 矩阵自动纳入）+ `completed/total` 推进；
- `relation_invalidated` **新增两个发射点**：apply committed 事务提交后；恢复收口（acknowledge 或 probe 裁决完成）后。既有发射点（扫描提交、重绑）不变；
- `recovery_required` 任务终态已被契约 04 §2.3 覆盖（终态天然全量刷新工作区可用性），前端无需新协议。

## 5. 前端投影与交互

- **plans 页**（§7.5 增量）：resolved 计划主操作「应用同步」（`apply_sync` availability 门控）→ `ConfirmPlan` → 长任务移交任务中心（§7.9：可离开页面，任务中心追踪）；committed 后计划投影 `status=applied`。
- **任务中心**（§5.3 回填完成）：活跃 apply 任务由既有任务投影自动覆盖；**「处理恢复」动作点亮**（T16 deferred 项收口）：`task.status=recovery_required` 或工作区 `relation_health=recovery_required` → 导航恢复详情页。
- **工作区列表行**：`recovery_required` 徽标 + 「处理恢复」行内入口（与重绑同款双入口模式：任务中心 + 列表行/横幅）。
- **新路由**：
  - `/workspaces/:id/history`——页签，`history_view` 门控，`ListCommits` 数据源；
  - `/workspaces/:id/history/:commit_id`——记录详情，`GetCommit`，渲染 exact/partial、剩余差异数、逐资源变更；
  - `/workspaces/:id/recoveries/:run_id`——恢复详情（**D2**；`run_id` = `task_id`）：run 摘要（六阶段 state、acknowledged、commit_id）+ 操作清单分页（`ListApplyOperations`）+「确认人工处理」动作（`AcknowledgeRecovery`，内联确认条，沿 mappings 页先例）+ 收口后重扫引导。普通视图无临时路径（硬约束 4）。
- **locale**：`err.apply.*`/`err.recovery.*`/`err.plan.none_ready`/`err.commit.not_found`/`msg.task.apply.*` 按清单入 zh-CN（conformance test 正向覆盖）。

## 6. 新增错误码清单

| code | args | 场景 |
| --- | --- | --- |
| `err.apply.no_run` | {0}=relation_id | GetApplyRun：关系无任何 Apply 运行记录 |
| `err.apply.run_not_found` | {0}=task_id | ListApplyOperations：task 不存在或跨关系 |
| `err.recovery.not_required` | {0}=task_id | AcknowledgeRecovery 于非 recovery_required 运行 |
| `err.plan.none_ready` | — | apply_sync availability：关系无可应用的 resolved 计划 |
| `err.plan.apply_not_reentrant` | {0}=plan_id | ConfirmPlan：同计划上一运行已 committed（引导重扫生成新计划） |
| `err.commit.not_found` | {0}=commit_id | GetCommit：记录不存在或跨关系 |

既有复用：`err.plan.not_found/stale/expired`（T11 起即有，expired 若施工时缺键随本票补）、`err.recovery.in_progress`、`err.scan.*`（契约 03 §3）。

## 7. 零消费表收口（schema v1 冻结表的 Phase 2 去向）

| 表 | 去向 |
| --- | --- |
| `plan_confirmations` | **ConfirmPlan 消费**（confirmation_token 幂等键，D4） |
| `sync_commits` / `commit_changes` | **ListCommits/GetCommit 消费**（D3） |
| `objects` / `object_refs` | 保持内部：CAS 权威存储，不透出 DTO（ADR-0004 §6） |
| `operation_journal`（+追加历史表） | 内部恢复事实源；恢复页只读投影经 ListApplyOperations，不直接暴露表 |

## 8. 决议对照表（2026-08-31，票 #29）

| 决策点 | 决议 | 理由 |
| --- | --- | --- |
| D1 Apply 生命周期事件 | 不新增 event_type；task_updated 承载进度 + relation_invalidated 增两发射点 | 契约 04 Q2「无差异化刷新」管线零改动；事件只做通知、任务投影已是权威进度面 |
| D2 恢复状态前端形态 | 建恢复详情页 `/workspaces/:id/recoveries/:run_id` | 原型 §6.6 要求恢复页展示 journal 阶段与资源结果，横幅承载不了；且是 Phase 3 Restore 页面的地基 |
| D3 SyncCommit 历史读 | 同票落 ListCommits/GetCommit（history_view=true） | SyncCommit（exact/partial、剩余差异）是 Apply 的验收事实，原型 §4.3 Phase 2 明确显示；两张零消费表顺势收口 |
| D4 ConfirmPlan 幂等 | confirmation_token 幂等重入同任务 | Apply 是长任务，双击/双窗口返回同一任务优于报错；过期/已应用拆码引导重扫 |

## 9. 硬约束落实对照

| 硬约束 | 落实位置 |
| --- | --- |
| 只增不删 / schema_version / `[]` / AppErrorDTO | §3 全部新 DTO；错误形态沿用契约 03 §2.7 |
| 未实现能力不暴露 | §1：restore 全家 false 不注册；`prepare_restore/apply_restore` 不出现在 availability |
| ADR-0004 权威 | §0.3/§3.4：六阶段与四路裁决只投影不重定；acknowledge 是唯一人工出口 |
| 无临时路径/ownership proof 透出 | §0.4/§3.3：ApplyOperationDTO 字段白名单 |
