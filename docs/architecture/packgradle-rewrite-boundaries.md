# PackGradle 重写边界与冻结清单（Step 0 交付物）

> 依据：[目标底层架构设计](./packgradle-architecture-redesign.md) §0.1、[重写实施路线](./packgradle-implementation-roadmap.md) Step 0。
>
> 状态：生效中　日期：2026-08-22

## 1. 已建立的新架构模块树

```text
internal/
  core/            # 纯 Go 标准库：model / ids / normalize / diff / plan
  application/     # 用例编排：ports / task / policy / view / sync
  adapters/        # ports 实现：filesystem / packwiz / prism（后续 watcher）
  store/           # 本地状态：paths / sqlite / objectstore
  transport/       # Wails 出口：DTO / SyncService / 事件桥
```

依赖约束（架构文档 §4.3）：

```text
transport -> application -> core
application -> adapters(接口), store(接口)   # 具体实现只在装配处 import
adapters -> core model / normalize
store -> core model
core -> Go standard library only
```

## 2. 冻结清单（legacy，禁止追加新能力）

| 冻结对象 | 位置 | 迁移去向 |
| --- | --- | --- |
| 三个 Wails 服务 | `internal/service`（已加 legacy 包注释） | `internal/application/sync` + `internal/transport` |
| 项目/实例关联模型 | `appconfig/projectconfig.go`（packgradle.toml dir_links） | Relation + MappingPolicy（SQLite） |
| Junction/硬链接同步语义 | `internal/service/links.go`、`internal/junction` | 不迁移：已取消（2026-08-31 用户决议，非推迟；copy 为唯一物化方式）——决议见 [P3 决策图 #49](https://github.com/skyraah/PackGradle/issues/49) Out of scope |
| mods 目录监听 | `internal/service/mods_watch.go` | Phase 4 watcher（仅发 relation_invalidated） |
| CF 更新检查/缓存 | `internal/curseforge` + service | 后续阶段按需重新接入 |

允许的 legacy 改动：缺陷修复（保持行为）、只读迁移输入（legacy-import 适配器，后续任务）。

## 3. 允许复用（以重新实现 + 通过新契约测试为准）

- `internal/packwiz` 的解析知识：`isSafeProjectRelative`、`UpdateVersion`、`CfIDsFromUpdate`、`normalizeSide`（新 `adapters/packwiz` 重写，补 modrinth mod-id 与 [download] hash 提取）。
- `internal/prism` 的实例发现知识：instance.cfg / mmc-pack.json 解析、游戏目录 = `<实例>/minecraft`（新 `adapters/prism` 重写）。
- `internal/errs.AppError` 错误协议与 main.go MarshalError（直接沿用）。
- 隐藏窗口子进程属性（`CREATE_NO_WINDOW`）、junction 检测手段（FSCTL_GET_REPARSE_POINT）等平台知识。
- 测试 fixture 构造思路（内联 temp 目录）。

## 4. 禁止事项（重申）

- 在旧 `internal/service` 中新增 Relation/Plan/Commit 语义；
- 新包 import `internal/appconfig` 或读写旧 config.toml / 项目内 `.packgradle/`；
- 新栈任何状态写入 Project 工作树（ADR-009：本机状态只在用户数据目录）；
- core import Wails/SQLite driver/fsnotify（架构文档 §4.3）；
- 前端传入源/目标路径、删除列表或临时 resolution 给 Apply（Phase 2 实现 Apply 时同样禁止）。

## 5. 已决 ADR 补充（ARCH-001 开放决策中阻塞 P0/P1 的部分）

| 决策 | 结论 |
| --- | --- |
| SQLite driver | `modernc.org/sqlite`（纯 Go 无 CGO，利于 Wails 三平台打包）；DSN 挂 pragma（WAL/FULL/FK/busy_timeout），`SetMaxOpenConns(1)`；迁移前 `VACUUM INTO` 备份。 |
| CAS 保留策略 | P1 不做 GC（对象只增不减）；保留/锁定策略随 Phase 3 Restore 需求再定 ADR。 |
| Packwiz mod identity | `mod:modrinth:<mod-id>` > `mod:curseforge:<project-id>` > `mod:path:<小写规范化路径>`（低置信度）；runtime-only JAR 为 `mod:jar:<小写文件名>`（低置信度）；跨侧匹配唯一通道 = application 传入的 filename hint。 |
| MappingPolicy 默认模板 | `default-v1` 仅含 mods 语义规则；config/kubejs/scripts/defaultconfigs 为建议模板，用户确认后才写入 Relation 的 policy。 |
| 时间与 ID | 时间统一 RFC3339 UTC 字符串；ID 为自实现 ULID 风格（前缀 + 26 位 Crockford base32），不引外部依赖。 |
