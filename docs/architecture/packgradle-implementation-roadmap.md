# PackGradle 重写实施路线

> 基于 [PackGradle 目标底层架构设计](./packgradle-architecture-redesign.md)。
>
> 状态：实施任务基线
>
> 日期：2026-08-22

## 1. 当前任务定义

当前仓库仍处于旧架构实现阶段：

- 后端以 `internal/service` 作为 Wails 服务和业务编排边界；
- 关系、目录链接和部分缓存写入全局或项目级 TOML；
- 前端页面、Store 和绑定围绕旧 Service 方法组织；
- Junction/硬链接在现有实现中承担了部分“同步模型”职责。

这些实现不能直接作为新架构的骨架。当前主任务不是继续扩展旧 Service，而是建立新架构的独立实现路径，并优先完成只读核心。

### 当前交付目标

**Phase 0 的设计基线已经完成；下一项工程交付是 Phase 0 工程基座 + Phase 1 MVP 只读核心。** 目标是完成以下闭环：

```text
登记 Project/Runtime
  -> 创建 Relation
  -> 扫描两侧端点
  -> Normalize 为 LogicalResource
  -> 持久化 ObservedSnapshot
  -> 初始化/三方 Diff
  -> 生成确定性的只读 SyncPlan
  -> 前端预览和诊断
```

Phase 1 不允许写入 Project/Runtime 文件，不提供 Apply、自动合并、历史回滚或“同步完成”承诺。

## 2. 需求基线

### 2.1 必须遵守的架构决策

1. **Relation 是聚合根**：Project 和 Runtime 可独立登记；同一 Project 可关联多个 Runtime；每个 pair 默认只能有一条 Relation。
2. **逻辑资源优先**：以 `LogicalResource` 比较 mod、文本文件和二进制文件，不按两个目录树直接覆盖。
3. **共同基线驱动三方 Diff**：没有 `SyncBaseline` 只能生成明确的初始化计划，不能假定双向同步。
4. **写入必须有计划闭环**：后续所有写操作都必须经过 `Scan -> Diff -> Prepare -> Resolve -> Confirm -> Apply -> Verify -> Commit`。
5. **SQLite 是元数据唯一权威**：关系、快照、基线、计划、任务、日志、Commit 和对象引用进入一个全局 `packgradle.db`。
6. **CAS 是可恢复内容唯一权威**：MVP 使用 SHA-256；文本和政策允许的小型二进制可保存，mod JAR 默认只保存 identity/hash/重新获取信息。
7. **本地状态不写入 Project**：数据库、CAS、staging、日志和机器路径放在用户数据目录，不创建项目内 `.packgradle/`。
8. **MVP 只保证 copy**：hardlink/Junction 只能作为后续 capability，并明确共享语义、所有权和降级影响。
9. **事件不是事实源**：事件只通知进度或失效；前端通过查询 API 恢复 Task、Workspace、Plan 和 History 状态。
10. **未实现能力不暴露为可用操作**：Feature 和 ActionAvailability 必须由后端返回，前端不展示永久禁用或模拟成功入口。

### 2.2 领域对象和数据约束

必须实现并保持不可变/可追溯关系的对象：

- `Project`、`Runtime`、`Relation`：登记生成 opaque ID；路径只是定位信息；每次扫描验证 `binding_fingerprint`。
- `MappingPolicy`：显式声明受管范围、方向、排除规则、合并策略和物化策略；修改会递增 `relation_revision`。
- `LogicalResource`：MVP 支持 `mod`、`text_file`、`binary_file`；`directory_manifest` 和 `runtime_local` 只能作为派生诊断。
- `ObservedSnapshot`：单侧、不可变、可频繁生成的扫描事实，包含 scanner version、binding fingerprint、资源和诊断。
- `SyncBaseline`：仅由 Apply 后完整复扫验证成功时创建；保存逐资源逻辑状态和恢复能力。
- `SyncPlan`：不可变 draft/resolved 计划，包含输入快照、Relation revision、操作、冲突、前置条件、风险和 digest。
- `SyncCommit`：成功 Apply/Restore 后的审计记录，引用验证快照、前后 Baseline、计划和对象；失败不得伪造 Commit。
- `Task`、`operation_journal`、`Recovery`：长任务、逐操作结果和崩溃恢复的事实来源。

### 2.3 安全与一致性要求

- 所有路径使用 root-relative 形式；拒绝绝对路径、`..`、root 外 symlink/junction/reparse point 和未验证卷边界。
- Plan digest、Snapshot digest、Baseline digest 使用版本化 canonical 编码；资源按 `resource_id` 排序，禁止直接对 Go map 序列化结果做 hash。
- Apply 前必须重验计划 TTL、Relation revision、端点 binding fingerprint、输入 snapshot digest 和资源前置指纹。
- 文件写入使用 staging、临时文件、原子替换和可重放 journal；数据库事务不能被当作文件系统事务。
- 同一 Relation 同时最多一个 Scan，最多一个 Apply/Restore；取消、失败、断电和磁盘写满都不得错误推进 Baseline。
- 结构化错误只返回 `err.*` code/args/detail；用户文案全部由前端 locale 提供。

### 2.4 前端要求

新前端以 Workspace 为产品投影，而不是继续拼接 Project Store、Runtime Store 和事件数据。最低页面范围：

- `/workspaces`：关系列表、健康、扫描状态、dirty/conflict、任务和最近 Commit；
- `/workspaces/new`：登记端点、选择 MappingPolicy、创建 Relation；
- `/workspaces/:id/changes`：扫描结果、资源三态、Diff 筛选和初始化预览；
- `/workspaces/:id/mappings`：受管范围和策略；
- `/workspaces/:id/plans/:plan_id`：不可变计划、冲突选择、风险确认和 stale 处理；
- `/sources`、`/runtimes`：只管理端点登记和健康，不直接执行跨端写入。

前端必须使用手写 `api/` 适配器访问 Wails DTO；生成 bindings 不能成为页面 API。前端只缓存后端权威状态，事件到达后标记失效并重新查询。

## 3. 实施前必须确定的开放决策

以下问题在进入对应实现前必须形成 ADR 或明确选择，不能在代码中隐式决定：

1. SQLite driver：纯 Go 或 CGO，以及三平台打包、迁移和备份验证方式；
2. CAS 保留/锁定策略：按容量、Commit 数量、时间或组合阈值；
3. Packwiz mod identity 优先级，以及无 provider ID、本地 JAR、来源迁移时的低置信度规则；
4. MappingPolicy 默认模板和服务端/客户端专属配置的边界；
5. 后续 TOML/text diff3/Packwiz-aware merge 使用的库与冲突表示；
6. 第二个 Runtime adapter 的验证对象和优先级；
7. 本地日志、Task 事件、staging 和诊断包的保留期限、容量和脱敏规则。（横切保留与脱敏已决：ADR-0011；staging 保留未决，待 #69 回报）

在这些决策完成前，可以搭建接口和 fixture，但不应把临时选择写入稳定数据格式或 UI 契约。

## 4. 分阶段实施步骤

### Step 0：冻结旧架构边界并建立切换面

**产出**：新应用入口/模块树、迁移说明、旧功能冻结清单。

1. 标记 `internal/service`、旧前端路由/Store 和 Junction 同步入口为 legacy；停止向其追加新架构能力。
2. 在 `internal/core`、`internal/application`、`internal/adapters`、`internal/store`、`internal/transport` 建立目标目录和最小接口。
3. 规定新核心只能依赖 Go 标准库；Wails、SQLite driver、fsnotify、Packwiz/Prism 类型留在外围。
4. 增加 headless harness 的测试入口，使后续核心可在不启动 Wails 的情况下验证。
5. 建立旧数据只读 `legacy-import` 适配器的任务边界；不自动删除旧配置、链接或文件。

**退出条件**：新包可以独立编译；旧 Service 不被新 domain/application import；迁移路径和禁止复用边界写入文档。

### Step 1：实现核心领域模型和 canonical 规范化

**产出**：`Relation`、`MappingPolicy`、`LogicalResource`、Snapshot/Baseline/Plan/Commit、错误分类和 digest 工具。

1. 定义资源 ID、资源类型、Representation、ContentRef、Change、Conflict、PlanOperation、Precondition 等纯 Go 类型。
2. 实现 root-relative 路径规范化、case policy、identity 置信度和 binding fingerprint 验证。
3. 实现版本化 canonical 编码和 SHA-256 digest；固定 absent tombstone 规则。
4. 编写三方 Diff 真值表：无变化、单侧变化、同值变化、删除/修改冲突、初始化选择。
5. 实现确定性 Plan builder：相同输入必须得到相同操作顺序、风险列表和 plan digest。

**退出条件**：core 单元测试覆盖 identity、规范化、digest、Diff、冲突分类和计划确定性；core 不依赖外部框架。

### Step 2：实现 SQLite 元数据存储和 CAS

**产出**：schema migration、repository、用户数据目录、CAS object store、hash cache。

1. 实现跨平台用户数据根目录和目录创建策略。
2. 创建 `packgradle.db` 初始 schema，启用外键、WAL、FULL synchronous、busy timeout 和 `user_version` migration。
3. 落地 projects、runtimes、relations、mappings、snapshots、representations、baselines、plans、tasks、journal、commits、objects、object_refs 等表。
4. 在 repository 层验证跨表 Relation 所属关系、snapshot side、Plan/Commit/Baseline 关联和 ready object 引用。
5. 实现 CAS 原子写入、hash 复核、去重、引用图 GC 和 staging 保护；mod JAR 默认不写入 CAS。
6. 实现 SQLite Online Backup 或 `VACUUM INTO` 备份流程；迁移失败时禁止启动写操作。

**退出条件**：重复写入幂等；事务回滚不留下悬空引用；删除可丢弃 hash cache 后可以从真实端点重建状态；CAS/SQLite fixture 测试通过。

### Step 3：实现 Project/Runtime Adapter 和 Relation 生命周期

**产出**：Packwiz scanner、Prism scanner、端点登记、Relation Prepare/Create/Rebind。

1. 以现有 `internal/packwiz`、`internal/prism` 的解析知识和 fixture 为输入，适配到新 adapter port，不直接复用旧 Service 方法。
2. 实现 Project/Runtime 独立登记、重复 pair 检查和稳定 opaque ID。
3. 实现 binding fingerprint、路径包含关系、legacy materialization 检查和重绑定预检。
4. Relation 创建和重绑定均采用 Prepare/Apply；重绑定不自动继承旧 Baseline，除非完整扫描证明逻辑等价。
5. 为 `mods`、`config`、`kubejs`、`scripts`、`defaultconfigs` 建立 MappingPolicy 模板，但默认只建议，不在用户确认前纳入受管范围。

**退出条件**：端点路径变化可识别为 `rebind_required`；重复关系被阻止；创建/重绑定不绕过 preparation；adapter 契约测试通过。

### Step 4：实现 Scan -> Normalize -> ObservedSnapshot

**产出**：双端扫描任务、hash cache、snapshot 持久化和诊断。

1. 应用层锁定 Relation，最多创建一个活动 Scan；Watcher 只产生失效信号。
2. Project Adapter 将 Packwiz 元数据归一为 mod LogicalResource；Runtime Adapter 将 JAR/meta 归一为对应逻辑资源。
3. 扫描受 MappingPolicy 约束，明确标记 ignored、unsupported、runtime_local 和 mapping_collision。
4. 在 `size + mtime + file identity` 未变时复用 hash cache，但所有事实仍以端点重建为准。
5. 持久化不可变 ObservedSnapshot、resource representations、scanner version、binding fingerprint、digest 和诊断。
6. 为 3,000 资源 fixture 分别记录 Project、Runtime、Normalize、DB 写入耗时。

**退出条件**：重复扫描得到等价 snapshot digest；端点绑定异常、路径逃逸、未知格式和重复 mapping 都形成结构化诊断；冷/热扫描指标可测。

### Step 5：实现初始化 Diff、三方 Diff 和只读 Plan

**产出**：`PrepareSync`、初始化计划、普通同步分析、计划查询 DTO。

1. 无 Baseline 时只允许 `adopt_equal`、`initialize_from_project`、`initialize_from_runtime`、`skip` 或人工处理。
2. 有 Baseline 时按 base/project/runtime 三方状态生成 Change、Conflict 和候选操作。
3. 生成 draft plan；冲突未解决时禁止进入 Apply 语义。
4. `ResolvePlan` 创建新不可变 resolved plan，记录所有 resolution 并重新计算 digest；旧 plan 不修改。
5. 计算 overwrite、delete、write_project、unrecoverable、shared_materialization 等 confirmation requirements。
6. Phase 1 只暴露预览和诊断，不能生成可执行 Apply 入口。

**退出条件**：相同输入生成相同 plan digest；计划能列出资源数、创建/修改/删除、冲突、前置条件和风险；输入 snapshot、Relation revision 变化可判定 stale。

### Step 6：实现 Application、Task、事件和 Wails Transport

**产出**：新应用用例、DTO、结构化错误、任务查询和事件契约。

1. 实现 `SyncApplication`、`HistoryApplication`、Project/Runtime application 的最小 Phase 1 方法集。
2. 长操作统一创建持久化 Task；支持取消、失败、诊断和任务序号。
3. 事件只发送 `task_updated`、`relation_invalidated`、`watch_failed`；前端启动先订阅，再 bootstrap 查询 active/recovery Task 和 Workspace。
4. transport 显式转换 domain -> DTO；顶层带 `schema_version`，列表为空使用 `[]`，错误使用 `AppErrorDTO/ProblemDTO`。
5. 生成并提交 bindings 作为构建产物；前端页面不得直接依赖 domain model 或生成绑定细节。

**退出条件**：headless application test 可以完成 Register -> Scan -> PrepareSync -> GetPlan；断线/漏事件后查询可以恢复事实状态；错误码可被 locale 映射。

### Step 7：实现 Phase 1 前端只读工作区

**产出**：新 Workspace 信息架构和只读流程。

1. 增加 `api/` 手写适配器、workspace/plan/task/history 领域 cache；每类后端对象只保留一个权威前端 cache。
2. 实现 `/workspaces`、`/workspaces/new`、`/workspaces/:id/changes`、`/mappings`、`/plans/:plan_id` 的 Phase 1 子集。
3. 展示 `WorkspaceStateDTO` 的 scan/baseline/diff/relation health 正交状态；不能用差异数量推断 clean。
4. 展示初始化方向、资源三态、冲突证据、计划风险和 stale 原因；不显示 Apply、Restore 或自动合并成功入口。
5. 适配任务事件和重连 bootstrap；Watcher invalidation 只标记 cache stale 并触发受控重查。
6. 全部文案和错误翻译进入 `frontend/src/locales/zh-CN.json`。

**退出条件**：前端只能登记、扫描、查看差异和预览计划；刷新/重启后从后端查询恢复状态；生产构建不存在模拟写操作。

### Step 8：Phase 1 集成验收和旧入口切换

**产出**：Phase 1 验收报告、新旧入口切换方案、迁移预览。

1. 执行 Core、Store、Adapter、Application、前端流程和 Windows E2E 测试。
2. 验证数据库和可丢弃缓存删除后可从 Project/Runtime 重建快照。
3. 验证旧 Relation、TOML、Junction/硬链接只被识别为 legacy 输入，不自动覆盖。
4. 新 Workspace 达到退出条件后替换对应旧入口；旧入口进入只读/冻结状态。
5. 保留旧配置导入和解除 legacy link 工具，删除旧数据必须独立确认。

## 5. 后续阶段步骤

### Phase 2：MVP 可控同步

- 实现 `ConfirmPlan` 单次短期 token，并绑定 `plan_id + plan_digest + relation_revision`；
- 实现 copy-only Materializer、staging、原子写、ownership proof 和 operation journal；
- Apply 前重验 binding、revision、snapshot 和资源指纹；
- 完整复扫后才更新 Baseline 和写入首个 `SyncCommit`；
- 支持 `take_project`、`take_runtime`、`skip`、`manual`，不提供自动 merge；
- 允许用户预先 skip 产生 `completeness=partial` Commit；执行/验证失败一律 `recovery_required`，不得产生 partial Commit；
- 实现 Task 取消、磁盘写满、进程强杀、staging 保留和恢复探测。

### Phase 3：历史恢复

- 以目标 Baseline/Commit 生成新的 RestorePlan，不直接反转数据库；
- 逐资源标记 CAS、重新下载、用户提供和不可恢复；
- 实现 exact/partial restore 选择、对象引用保护和 GC；
- 实现崩溃恢复工具、恢复可用性说明和 History/Restore UI。

### Phase 4：增强适配

- TOML、文本 diff3、Packwiz-aware merge adapter；
- watcher 扩展和增量扫描协议；
- 第二个 Runtime adapter，用于验证 core/application port 没有被 Prism 特性污染。

## 6. 任务依赖和建议并行方式

主依赖链：

```text
开放 ADR
  -> core model/digest
  -> SQLite/CAS
  -> adapter scan
  -> snapshot/diff/plan
  -> application/transport
  -> Phase 1 frontend
  -> integration acceptance
```

可并行但必须共享契约的工作：

- Core 模型/diff 与 SQLite schema：先固定对象字段和 digest，再分别实现；
- Packwiz/Prism adapter fixture 与路径安全测试；
- Wails DTO/错误契约与前端 `api/`、cache 骨架；
- CAS 存储测试与 headless application 测试。

不可并行或需要串行评审的工作：

- Relation、Snapshot、Baseline、Plan 的字段和所有权变更；
- SQLite schema migration 和生成 bindings；
- Apply/Restore 文件系统事务、恢复和任何 Junction/hardlink 能力。

## 7. 阶段验收清单

### Phase 1 必须满足

- Core 三方 Diff 真值表、identity fixture、canonical digest 和路径安全测试通过；
- 重复 Scan 产生等价 ObservedSnapshot；删除 hash cache 后可重建正确状态；
- 重复 Prepare 不写 Project/Runtime；Plan digest 和操作顺序确定；
- 前端只有登记、扫描、差异和预览，没有 Apply/History/Restore 可用入口；
- Relation、Snapshot、Plan、Task 和错误均可通过新应用 API 查询；
- 旧配置/Junction 只作为迁移输入，不改变新 Relation 语义。

### Phase 2 必须满足

- 覆盖/删除前按 recoverability policy 保存旧内容；
- plan stale、取消、断电、写满磁盘和部分操作失败不推进 Baseline；
- exact/partial Commit 语义符合架构文档；
- 刷新、重启或事件漏包后，Task/Workspace 查询可恢复真实状态。

### Phase 3 必须满足

- 所有声称可恢复的文本对象都能从 CAS 校验并恢复；
- Restore 对不可恢复 mod 明确阻止 exact 或要求用户选择 partial；
- GC 不删除活跃 Commit、Baseline、Plan、staging 引用；
- 降级恢复不会被标记为 exact success。

## 8. 明确禁止的实现方式

- 在旧 `internal/service` 中直接新增 Relation/Plan/Commit 语义；
- 让前端传入源路径、目标路径、删除列表或临时冲突 resolution 给 Apply；
- 用目录树 diff、文件名新旧、数量多少或 mtime 推断 mod identity/同步方向；
- 维护需要与 SQLite 双向同步的 `current-snapshot.json` 权威副本；
- 把 Junction/hardlink 当成默认同步模型；
- 把事件 payload 当成权威状态，或用页面 loading 状态推测 Task 完成；
- 在 Phase 1 暴露 Apply、自动合并、Restore 或模拟成功按钮；
- 在没有完整复扫验证时推进 Baseline 或创建成功 Commit。

## 9. 当前最小任务单

进入开发时建议按以下顺序建立第一批 issue/任务：

1. `ARCH-001`：确定 SQLite driver、CAS retention、mod identity 等开放 ADR；
2. `CORE-001`：建立纯 Go domain model、错误和 canonical digest；
3. `CORE-002`：实现三方 Diff 真值表和确定性 Plan builder；
4. `STORE-001`：实现用户数据目录、SQLite migration 和 repository 约束；
5. `STORE-002`：实现 SHA-256 CAS、对象引用和 GC 保护；
6. `ADAPTER-001`：定义 Packwiz/Prism scanner port，迁移现有解析 fixture；
7. `REL-001`：实现端点登记、Relation 创建/重绑定 preparation；
8. `SCAN-001`：实现 Scan/Normalize/ObservedSnapshot/hash cache；
9. `SYNC-001`：实现初始化/三方只读 PrepareSync 与 Plan DTO；
10. `TASK-001`：实现 Task、事件 envelope、bootstrap 查询和结构化错误；
11. `FE-001`：实现 Workspace/Plan/Task 新 API 适配和 Phase 1 只读页面；
12. `QA-001`：执行 Phase 1 集成、Windows E2E 和验收报告。

这些任务完成后，才能进入 Phase 2 的文件写入和 Commit；任何旧功能迁移都应以这些新端口为目标，而不是反向扩展旧 Service。
