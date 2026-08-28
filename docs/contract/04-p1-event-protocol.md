# P1 前端事件协议（P1-EVENT）

> **状态：已决议（执行规格）。** 2026-08-28 经决策图票 #7 用户拍板定稿（七决策点全按推荐）；执行会话按本规格施工，不再做隐式决策。
> 来源：决策图「PackGradle 重构全路线决策图（Phase 1-4）」票 #7。
> 依据：roadmap Step 6.3 / Step 7.5 / §2.1 第 9 条、检视报告 P1-6、`internal/transport/events.go`、`internal/application/task/publisher.go`、`internal/store/sqlite/event_repo.go` 现状。

## 0. 覆盖范围与硬约束

前端接入 `packgradle://event` 的**订阅时序、漏包判定、受控重查、失效合批、断线恢复**协议。

硬约束（roadmap §2.1 第 9 条，规格全程生效）：

1. 事件只做通知，**查询 API 是唯一事实源**；
2. 事件 payload **一概不进前端缓存**，状态只能经查询 API 到达缓存；
3. 不得用页面 loading 状态推测 Task 完成。

## 1. 事实基线（代码现状，规格的前提）

- 单一 Wails topic `packgradle://event`，payload 恒为 `EventEnvelope{schema_version, event_id, event_type, stream_sequence, emitted_at, relation_id?, task_id?, payload}`。
- 三种事件类型：`task_updated`（payload = Task 快照 JSON）、`relation_invalidated`（payload = `{}`）、`watch_failed`（P1 保留常量，**不会发出**）。
- `stream_sequence`：`task_events` 表内事务 MAX+1 持久化分配，事件流内单调递增；与 Task.Sequence（任务内序号）、Relation.Revision（关系修订号）互不相干。
- 发布路径：**先持久化、后 Emit；Emit 失败不回滚、不重试** → 漏包是常态可能。
- P1 **没有事件重放/补拉 API**（`TaskEventRepository` 只有 `Append`）；漏包后无法补拉事件，恢复只能经查询面。
- P1 **没有真实 watcher**（`watch_failed` 未接 fsnotify）；`relation_invalidated` 只在扫描提交后发出——**扫描失败只有 `task_updated`、没有 invalidation**，而工作区可用性依赖任务终态。

## 2. 决议

### 2.1 订阅与启动 bootstrap 时序（Q5）

1. **App mount 前完成订阅**：订阅点是 `api/` 适配器层的唯一 eventSource 模块（单点订阅 `packgradle://event`，页面不得自行订阅）。
2. mount 与 bootstrap 查询**并行**：`ListWorkspaces` + `ListTasks(active=true)`；页面先渲骨架，查询到达填缓存，**不阻塞首帧**。
3. bootstrap **复用受控重查管线**（§2.4），不存在第二套初始化逻辑。
4. `last` 序号（最后见到的 `stream_sequence`）在订阅后**从收到的第一个事件起建立**；此前无基线、不做任何判定。

### 2.2 漏包判定（Q1，严格规则）

对每个到达事件的 `stream_sequence`（下称 seq）：

| 条件 | 判定 | 动作 |
| --- | --- | --- |
| seq < last | 旧包 | 丢弃（去旧），无副作用 |
| seq == last + 1 | 正常 | last 推进到 seq |
| seq > last + 1 | 漏包 | **立即触发受控重查**，last 直接推进到 seq |

未知 `event_type` 或不认识的 `schema_version`：丢弃 + 诊断日志，不触发重查、不崩溃。

### 2.3 统一触发规则与 payload 强度（Q2 / Q3）

- **任何到达的合法事件**（`task_updated` / `relation_invalidated` / `watch_failed`）一律触发同一受控重查管线（§2.4）——不存在按事件类型分级的差异化刷新。
- `task_updated` 额外对该 `task_id` 做 **`GetTask` 重读**（payload 快照一概不进缓存）。
- 任务终态（succeeded/failed/cancelled/recovery_required）经上述管线**天然刷新工作区可用性**——覆盖「扫描失败只有 task_updated、没有 invalidation」的缺口，后端无需为失败补发 invalidation。

### 2.4 受控重查管线（Q4，合批模式）

- 触发后**无延迟立即发起**；**inflight 单飞**：进行中再收到触发只标 dirty，本轮结束后立刻再刷一轮，直到干净。风暴场景最多两轮，无任何人为延迟常数。
- 单轮重查内容（**P1 全量**）：`ListWorkspaces` + `ListTasks(active=true)`，外加本轮标 dirty 的 `GetTask`。
- **UI 静默**：重查期间不显示全屏 loading，页面数据原地更新；骨架只在 bootstrap 首次填充时出现。
- **周期对账兜底（Q7）**：仅窗口可见时，每 **30 秒**把缓存标 stale 走一轮管线；与事件触发的重查共用单飞合批、不叠加。保证缓存陈旧时间有上界，覆盖 emit 静默丢失（无后续事件时 seq 检测永远看不到跳号）的盲区。

### 2.5 失效语义分类

| 事件 | P1 是否发出 | 前端语义 |
| --- | --- | --- |
| `task_updated` | 是 | 管线 + `GetTask` 重读（§2.3） |
| `relation_invalidated` | 是（扫描提交后） | 管线（P1 全量刷） |
| `watch_failed` | 否（保留常量） | 预留语义 = 按 invalidation 处理 + 一次性提示「监听不可用」 |

按需（只刷单关系）重查优化明确**推迟 Phase 2**（关系规模变大时再议）。

### 2.6 断线与恢复路径（Q6）

- **不设独立断线协议**。任何（重新）连接——窗口 reload、关窗重开、应用重启——一律**完整重走启动 bootstrap**（§2.1 同一条管线）。
- **不使用** localStorage 等前端持久化事件状态（无重放 API，`last` 跨会话无用）。
- bootstrap 查询失败：统一错误态（`err.*` code → locale 文案），可重试（走同一管线），不引入独立重试协议。

## 3. 决议对照表

| 决策点 | 决议 |
| --- | --- |
| Q1 漏包判定 | 严格跳号即漏包（seq > last+1 → 重查，last 推进）；seq < last 丢弃；首个事件建立基线 |
| Q2 重查范围 | P1 全量（`ListWorkspaces` + `ListTasks(active=true)`）；按需优化推迟 Phase 2 |
| Q3 payload 强度 | 严格：payload 一概不进缓存，`task_updated` 经 `GetTask` 重读 |
| Q4 合批模式 | inflight 单飞 + 二段刷新至干净；零人为延迟 |
| Q5 启动时序 | mount 前订阅；bootstrap 与渲染并行、骨架屏；复用同一管线 |
| Q6 恢复路径 | 无独立断线协议，恢复 = 重新 bootstrap；不持久化前端事件状态 |
| Q7 周期对账 | 30 秒、仅窗口可见、共用单飞合批 |

## 4. 施工检查单（验收会话逐条对照）

1. 订阅先于一切查询：eventSource 模块在 App mount 前完成订阅（代码顺序可证）；
2. 页面无第二处事件订阅（单订阅点可证）；
3. payload 不进缓存：全前端无「事件 payload 直接写入 store」路径（grep 可证）；
4. 漏包注入测试：人为跳过一个 seq → 触发一轮重查；
5. 合批测试：事件风暴下管线最多两轮（inflight + dirty 二段）；
6. reload 恢复测试：刷新窗口后状态经 bootstrap 完整恢复；
7. 周期对账仅在窗口可见时运行（不可见时无查询流量）；
8. 重查期间无全屏 loading（静默原地更新）。

## 5. 术语（入 CONTEXT.md）

- **受控重查（Controlled Re-query）**
- **事件流序号（Stream Sequence）**
