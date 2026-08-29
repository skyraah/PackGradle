# 新架构后端（P0 工程基座 + P1 只读核心）

> 状态：已实施（2026-08-22）。
> 权威设计见 [目标底层架构设计](../architecture/packgradle-architecture-redesign.md) 与
> [重写实施路线](../architecture/packgradle-implementation-roadmap.md)；
> 冻结清单与已决 ADR 见 [重写边界与冻结清单](../architecture/packgradle-rewrite-boundaries.md)。
> 本文是面向开发者的模块导览；代码与本文有出入时以代码为准。

## 与 legacy 的关系

`internal/service`（EnvService / PackwizService / PrismService）已冻结为 legacy，
仍注册在 Wails 中维持现有前端行为；新能力一律落在下列模块树。
新栈 Wails 出口为 `transport.SyncService` + 端点管理的 `transport.ProjectService` /
`transport.RuntimeService`（与 legacy 并存注册，见 main.go）。

## 模块树

```text
internal/
  core/                     # 纯 Go 标准库（禁止 import wails/sqlite/fsnotify）
    model/                  # 领域类型：Relation/LogicalResource/ObservedSnapshot/SyncBaseline/SyncPlan/Task/EventEnvelope...
    ids/                    # 前缀 + 26 位 Crockford base32 标识符（ULID 风格）
    normalize/              # 路径规范化、CanonicalJSON、snapshot/baseline/plan/policy digest、语义摘要
    diff/                   # 三方 Diff 真值表（§6.3）与初始化分类
    plan/                   # 确定性 plan builder（draft/resolved、方向过滤、确认要求）
  application/
    ports/                  # 消费方接口（仓库/扫描器/哈希/指纹/事件/端点发现）+ 仓库哨兵错误
    endpoint/               # Project/Runtime 共享的端点错误码与只读健康评估
    task/                   # 任务生命周期（持久化 Task + task_updated/relation_invalidated 事件）
    policy/                 # MappingPolicy 模板（default-v1 仅 mods；config 等为建议模板）
    view/                   # 用例返回投影
    project/                # 项目源端点用例：DiscoverProjects/RegisterProject/GetProjectHealth/ListProjects
    runtime/                # 运行实例端点用例：DiscoverRuntimes/RegisterRuntime/GetRuntimeHealth/ListRuntimes
    sync/                   # 用例：PrepareRelation/CreateRelation/StartScan/PrepareSync/ResolvePlan/GetPlan/GetWorkspace/GetSnapshotDiagnostics/GetHashCacheStats/GetChanges/GetMappingPolicy/UpdateMappingPolicy/PrepareRebind/ApplyRebind...
  adapters/
    filesystem/             # 流式 sha256、原子写、ResolveWithin 路径安全、卷序列号 binding fingerprint
    packwiz/                # Project 扫描：index.toml 权威 + modrinth/curseforge 身份 + [download] 声明 hash；DiscoverProjects 有限深度发现
    prism/                  # Runtime 扫描：mods/*.jar + mods/.index 元数据 + filename hint 跨侧匹配；Discoverer 实例发现
  store/
    paths.go                # 用户数据目录布局（packgradle.db/objects/staging/logs/exports）
    sqlite/                 # schema v1→v4 前向迁移（VACUUM INTO 备份门禁；v2 补 tasks.plan_id/commit_id 外键；v3 补 sync_plans.requested_exactness；v4 补 rebind_preparations 重绑预检表）+ 完整性守卫 + 各仓库
    objectstore/            # SHA-256 CAS：流式写 + 复核 + 原子落位
  transport/                # Wails DTO/转换/SyncService/事件桥（packgradle://event）
  bootstrap/                # 唯一装配点（main.go 与 headless 工具共用）
cmd/pgheadless/             # headless 验证入口：Prepare→Create→Scan→PrepareSync→GetPlan
```

## 关键语义速查

- **Snapshot digest**（`normalize.SnapshotDigest`）：内容 revision。含 normalization_version、side、policy_digest 与按 resource_id 排序的资源语义表；排除 snapshot_id/captured_at/binding_fingerprint/scanner/diagnostics。
- **Plan digest**：含 relation_revision、kind、resolved 标记、base baseline digest、输入快照 digest、policy digest、expected_bindings、确定性排序后的操作与最小化冲突/resolutions；排除所有 ID、status、expires_at。
- **mod 语义摘要**：高置信度（modrinth/curseforge）= identity + version + side + hash（声明值优先，否则实测 sha256）；低置信度（jar/path）= 小写文件名 + hash。显示名与 [download] url 永不进入 digest。
- **跨侧 mod 匹配**：唯一通道是 application 在扫描时把项目侧 pw.toml 的 `filename`（小写）→ ResourceID 作为 hint 传给 Prism scanner；core/diff 绝不做路径→身份推断，低置信度身份不参与跨侧等价判定。
- **错误协议**：沿用 `internal/errs.AppError`；新错误码命名空间 `err.relation.*`、`err.scan.*`、`err.plan.*`、`err.sync.*`、`err.mapping.*`、`err.endpoint.*`（端点登记/发现/健康，已带 zh-CN locale 键）。
- **任务事件**：统一 topic `packgradle://event`，payload 为 `EventEnvelope`（event_type ∈ task_updated / relation_invalidated / watch_failed）。事件不是事实源；漏包后经 ListTasks/GetWorkspace 查询恢复。

## P1 明确不做

Apply/ConfirmPlan/SyncCommit 写入（Phase 2）、Restore/GC（Phase 3）、watcher 接入、
Junction/hardlink 物化（Phase 4）。`transport.SyncService` 不暴露对应方法。

## 测试入口

```bash
go test ./internal/...                       # 全部单测 + headless 集成测试
go run ./cmd/pgheadless -project <项目根> -instance <实例目录> -data <临时数据目录>
```
