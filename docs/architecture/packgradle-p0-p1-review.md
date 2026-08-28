# PackGradle P0/P1 实现检视报告

> 检视基线：`docs/architecture/packgradle-architecture-redesign.md`、
> `docs/architecture/packgradle-implementation-roadmap.md`。
>
> 检视日期：2026-08-22
>
> 结论：后端新架构的 P0 工程基座和 P1 只读核心已经形成可运行版本，但完整的 P1 产品闭环尚未完成，暂不具备切入 Phase 2 文件写入的条件。

## 1. 验证结果

- `go test ./...`：通过。
- `go vet ./...`：通过。
- `frontend/node_modules/.bin/vue-tsc.cmd --noEmit`：通过。
- Vite production build：使用 `--configLoader runner` 通过；默认 config loader 因当前 `node_modules/.vite-temp` 权限限制失败。
- `go test -race`：未执行成功；当前环境的 `-race` 要求启用 CGO，而项目选择纯 Go SQLite driver，尚未配置可用的 CGO 工具链。

测试通过说明代码可以编译并通过现有 fixture，不等于架构退出条件全部满足。当前没有完成真实 Wails 窗口、Windows 文件系统边界、断电/强杀、磁盘写满和新前端工作区流程的验收。

### 后端单独里程碑的范围说明

如果当前里程碑明确限定为“只验收后端 P0/P1，不接入新前端”，则：

- 原 **P0-1**（新栈接管前端、隐藏 legacy 写入口）可以延期，不作为后端实现阻塞项；它保留为后续前端切换的发布门槛。
- 原 **P0-2**（locale 文案）可以延期到前端重构，但后端不能延期结构化错误契约：错误必须继续使用稳定的 `err.*` code、args、detail，不得在新 core 中返回面向用户的中文文案，也不能在前端接入前随意改码。
- 后端仍必须优先处理 P0-3、P0-4、P0-5，以及跨 Relation 数据约束、路径安全、Policy collision、Plan exactness 和准备阶段原子性等问题。

## 2. 完成度矩阵

| 范围 | 状态 | 检视结论 |
| --- | --- | --- |
| Step 0 模块树与 legacy 冻结 | 部分完成 | 新目录、bootstrap、冻结说明已存在；legacy 入口仍对用户可见，迁移适配器未实现。 |
| Core model / normalize / diff / plan | 基本完成 | 领域对象、canonical digest、三方 Diff、确定性计划和 resolution 有实现及单测。 |
| SQLite / CAS / 用户数据目录 | 部分完成 | schema、migration、repository、CAS、hash cache 已有；跨 Relation 约束、摘要校验和完整事务边界不足。 |
| Project / Runtime adapter | 基本完成 | Packwiz、Prism scanner 和 fixture 已有；路径安全、binding identity、mapping collision 仍不完整。 |
| Relation 生命周期 | 部分完成 | Prepare/Create 已实现；Rebind、端点发现/登记查询和失败回滚未完成。 |
| Scan / Snapshot / Task | 基本完成 | 双端扫描、快照、任务、取消、僵尸任务恢复已有；hash cache 命中度量和 watcher follow-up 未完成。 |
| PrepareSync / ResolvePlan | 部分完成 | 初始化/三方计划可生成；`requested_exactness` 未进入 plan 持久化，资源级 Diff 查询缺失。 |
| Wails transport / bindings | 部分完成 | SyncService、DTO、事件桥和 bindings 已生成；Features、ActionAvailability、Rebind、Mapping、Changes API 缺失。 |
| Phase 1 前端 | 未完成 | 现有路由仍是 legacy 页面，没有 `/workspaces` 工作区、计划、冲突和新任务 cache。 |
| Phase 1 退出验收 | 未完成 | 后端 headless 链路通过，但产品入口、错误 locale、Windows E2E 和性能/崩溃验收未通过。 |

## 3. 阻塞问题（完整产品口径）

### P0-1：新 P1 栈没有接管产品入口，旧写操作仍可用

**证据**：`main.go:66-74` 同时注册 legacy 三服务和新 `SyncService`；`frontend/src/router/index.ts:55-59` 仍只注册 Dashboard、Projects、Dev、Instances、Settings；`frontend/src/api/index.ts` 只代理旧 Env/Packwiz/Prism Service。

**影响**：用户仍可通过旧页面直接执行 Junction、硬链接、meta 拉取/推送等写操作。架构要求 Phase 1 前端只能登记、扫描、查看差异和预览计划，且未实现能力不能继续出现在产品操作面。当前新核心存在于后端，但没有成为用户实际使用的同步入口。

**下一步**：实现新 `SyncService` 的手写前端 API/cache，增加 `/workspaces`、`/workspaces/new`、changes、mappings、plans 页面；新入口达到验收后再替换旧入口。旧写操作必须隐藏、只读或显式标记 legacy 迁移工具。

### P0-2：新错误码没有进入前端 locale

**证据**：新错误码定义在 `internal/application/sync/relation.go:20-36`；`frontend/src/locales/zh-CN.json` 中没有 `err.relation.*`、`err.scan.*`、`err.plan.*`、`err.mapping.*`、新 `err.sync.*` 的完整映射，也没有 `msg.task.scan.*` 文案。

**影响**：新栈失败会显示原始 code/detail 或空文案，违反“后端只返回结构化错误、所有用户可见失败映射到 locale key”的契约。

**下一步**：建立新错误码清单和 locale conformance test；补齐任务阶段文案、预检检查项、计划冲突和 stale/rebind 文案。

### P0-3：SQLite repository 没有执行跨 Relation/side 的完整性约束

**证据**：`internal/store/sqlite/plan_repo.go:28`、`baseline_repo.go:40`、`snapshot_repo.go:32` 主要依赖单列 FK。Plan 没有验证输入 Project/Runtime snapshot 属于同一 Relation 且 side 正确；Baseline 没有验证 parent 属于同一 Relation；Plan/Commit/Baseline digest 也没有在 repository 边界重算校验。

**影响**：直接调用 repository 或未来 Apply/Restore 装配错误对象时，可能把不同 Relation 的快照、基线和计划串在一起，破坏 SQLite 作为元数据权威的审计链。

**下一步**：增加 repository transaction guard，统一验证 Relation 所属、snapshot side、parent/target 所属、digest/schema；新增跨 Relation、跨 side、伪造 digest、错误 parent 的契约测试。`tasks.plan_id/commit_id` 也应在后续 schema 中补齐引用约束。

### P0-4：端点路径和绑定身份还没有达到设计中的安全强度

**证据**：`internal/application/sync/relation.go:59-60` 只做 `filepath.Clean`，没有先绝对化和 real path 校验；`internal/adapters/packwiz/scanner.go:67,157-161`、`internal/adapters/prism/scanner.go:175,278-282` 直接使用 `filepath.Join`/`WalkDir`，没有统一经过 `ResolveWithin`；`internal/adapters/filesystem/fingerprint_windows.go:29` 和 `fingerprint_other.go:20` 主要使用卷/路径材料，未采集端点根目录 file identity。

**影响**：symlink/junction/reparse point、相对输入路径、同路径替换目录和策略前缀越界可能绕过绑定证据或读取 root 外内容。扫描器的路径安全测试覆盖不足，现有 `ResolveWithin` 没有成为所有 adapter 的强制入口。

**下一步**：统一 `NormalizeEndpointPath -> realpath -> binding fingerprint -> ResolveWithin` 流程；Windows 采集卷序列号 + 根目录 file ID，其他平台至少使用真实路径/文件 identity；所有 index、pw.toml、jar、policy prefix 和 managed file 访问都必须通过安全 resolver。

### P0-5：MappingPolicy 没有编译校验和 mapping collision 语义

**证据**：Packwiz/Prism scanner 按规则逐条扫描；同一资源被多条规则命中时，现有逻辑可能产生重复 observation，最后由 `assembleSnapshot` 以通用错误失败。未看到规则优先级/具体路径决议、root-relative glob 编译证明或 `mapping_collision` 诊断的实现与测试。

**影响**：用户无法知道哪条规则冲突，也无法在计划页处理该问题；重复规则可能被误报为 adapter failure，违反“不可唯一判定就显式冲突”的要求。

**下一步**：实现 Policy compiler，校验方向、资源类型、prefix、include/exclude、root 边界；按显式优先级/最具体规则决议，无法唯一决议时生成 `mapping_collision`，并在 Snapshot/Plan/DTO 中保留证据。

## 4. P1 重要缺口

### P1-1：Relation 重绑定和端点管理 API 不完整

架构定义的 `PrepareRebind/ApplyRebind`、Project/Runtime discovery/registration/health 用例尚未进入 `internal/application/sync.Application` 和 `internal/transport/SyncService`。当前只有按两个路径直接 `PrepareRelation` 的入口。

### P1-2：Workspace DTO 无法支撑资源级 Changes 页面

`internal/transport/dto.go:97` 的 `WorkspaceDTO` 只有关系、正交状态和两侧 Snapshot 摘要；没有 resource-level Diff、`WorkspaceFeatures`、`ActionAvailability`。`PrepareSync` 也只返回计划操作和冲突，无法展示无操作资源、adopt_equal 和完整三态列表。

### P1-3：`requested_exactness` 被接收但没有进入计划模型和数据库

`internal/application/view/views.go:118`、`internal/transport/dto.go:154` 接收了 `requested_exactness`，但 `SyncPlan` 没有该字段，`sync_plans` 也没有对应列。Phase 2 无法可靠区分 `exact` 与 `allow_partial` 请求。

### P1-4：Prepare/Create 不是原子事务

`internal/application/sync/relation.go:185` 先消费 preparation，之后分步创建 Project、Runtime、Relation 和 Mapping。中途失败会留下已登记的部分端点、已消费 preparation 或无 policy 的 Relation，用户不能安全重试。

### P1-5：Hash cache 没有使用 FileKey，也没有证明热扫描命中

`internal/application/sync/scan.go:261` 构造 `HashCacheKey` 时没有填 `FileKey`；`internal/adapters/filesystem/hash.go` 也明确不采集 FileKey。现有测试只证明删 cache 后 digest 一致，没有命中计数、命中率或性能数据。

### P1-6：事件只接到后端，前端没有新事件 bootstrap/reconnect

`internal/transport/events.go` 已有 `packgradle://event` bridge，但 `frontend/src` 只订阅旧的 `packgradle:mods-diff` 和窗口事件，没有订阅新 Task/Relation 事件，也没有启动时“先订阅、再查询 active/recovery Task、再合并 sequence”的实现。

### P1-7：生产构建仍允许用户切换 mock

`frontend/src/api/index.ts:1-69` 和 `frontend/src/views/SettingsView.vue:199-400` 暴露持久化 mock 开关。架构要求 mock 只能存在于开发构建，生产构建不得让用户切换模拟写操作。当前 mock 与 legacy 写入口叠加，会掩盖真实新契约缺口。

### P1-8：CreateRelation 初始 revision 语义不清晰

`internal/application/sync/relation.go:235-242` 先以 revision=1 创建 Relation，再调用 `SavePolicy` 递增到 2。若“创建时写入初始 policy”不算 policy 修改，首个 Relation 应保持 revision=1；如果算修改，则需要在契约和 UI 中说明。当前只有测试接受 `>=1`，没有固定语义。

## 5. 下一步需求顺序

### 阶段 A：先关闭 P1 退出阻塞

1. `P1-CUTOVER`：完成新 SyncService 前端 API、workspace/plan/task cache 和 `/workspaces*` 路由；冻结或隐藏旧跨端写入口。
2. `P1-CONTRACT`：补 `WorkspaceFeatures`、`ActionAvailability`、resource-level Diff/Changes、Mapping、Rebind、endpoint discovery DTO 和用例。
3. `P1-I18N`：补新错误码、任务消息和预检/冲突文案，并加入 code-to-locale 自动测试。
4. `P1-STORE-GUARD`：补跨 Relation/side/parent/digest/schema 校验，保证 repository 不能写入非法历史链。
5. `P1-PATH`：统一 endpoint canonicalization、realpath/file identity、ResolveWithin 和 adapter 路径安全测试。
6. `P1-POLICY`：实现 policy compiler、规则优先级、collision 诊断、glob root proof 和策略更新 revision 事务。
7. `P1-PLAN`：把 requested exactness、normalization version 和 policy digest 固化到 Plan/SQLite/DTO；锁定初始 revision 语义。
8. `P1-PREP`：将 preparation consume、endpoint registration、Relation、Mapping 保存合并为一个事务或可恢复的应用层提交协议。
9. `P1-EVENT`：接入新事件订阅、sequence 去旧、漏包重查和启动 bootstrap；事件只做通知，查询 API 继续作为事实源。
10. `P1-E2E`：补真实 Wails bindings、Windows path/reparse、跨卷/权限、冷热扫描性能和重启恢复验收。

### 阶段 B：进入 Phase 2 可控同步前置

- `ConfirmPlan` 单次 token 绑定 plan/digest/revision；
- copy-only Materializer、staging、ownership proof、原子替换和 operation journal；
- Apply 前重新扫描/验证 binding、snapshot digest 和每项 precondition；
- 完整复扫后再创建 Baseline/SyncCommit；
- exact/partial/skip 语义和失败时 recovery_required；
- 进程强杀、磁盘写满、取消、部分执行和补偿恢复测试。

### 阶段 C：Phase 3/4

- RestorePlan/ApplyRestore、CAS/重新下载/用户对象/不可恢复分类；
- 对象引用图、保留锁定和 GC；
- TOML/text diff3/Packwiz-aware merge；
- watcher、hardlink/Junction capability 和第二个 Runtime adapter。

## 6. P1 重新验收条件

只有同时满足以下条件，才能将 P1 标记为完成并开始 Phase 2：

- 新前端默认进入 `/workspaces`，不存在绕过 Plan 的 legacy 跨端写入口；
- 所有新错误和任务消息都有 locale 映射；
- 任意跨 Relation/side/错误 digest 的直接 repository 写入都会失败；
- 所有端点/资源路径经过 realpath、root containment 和 binding identity 校验；
- mapping collision、unknown format、low-confidence identity 都能在 Workspace/Plan 中被查询和解释；
- Resource-level Diff、Features、ActionAvailability、Rebind 和 Mapping API 可被前端消费；
- requested exactness 在 Plan、SQLite、DTO 和测试中保持一致；
- 事件漏包、窗口重开和应用重启后，Task/Workspace 能从查询 API 恢复；
- headless、adapter、store、application、frontend、Windows E2E 和性能基线全部有自动化或可重复验收记录。

## 7. 后端单独验收口径

后端在前端延期期间可以先以 headless/transport 为交付边界，但必须满足：

- 新 core/application/store/adapters 不依赖 legacy Service、旧 TOML 状态或项目内 `.packgradle/`；
- Register -> Scan -> Snapshot -> PrepareSync -> ResolvePlan -> GetPlan headless 链路可重复执行；
- repository 拒绝跨 Relation、跨 side、错误 parent 和伪造 digest 的对象引用；
- 所有端点和资源路径经过绝对化、realpath、root containment 和 binding identity 校验；
- MappingPolicy 有编译校验，冲突规则输出 `mapping_collision` 诊断；
- `requested_exactness`、Relation revision、policy digest、snapshot digest 和 plan digest 在 model/SQLite/transport 中一致；
- preparation 消费和 Relation 创建具备事务或可恢复提交语义；
- Task、错误码和事件 envelope 稳定，前端 locale 可以在后续直接接入；
- P0-1/P0-2 的前端部分明确标记 deferred，不被误报为后端已完成。
