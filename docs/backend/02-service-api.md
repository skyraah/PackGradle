# 服务方法 API 参考

本文列出 Wails 暴露给前端的全部 Go 服务方法（共 39 个）。
前端调用方式为 `Bindings.<服务名>.<方法名>(...)`，生成的 TS 绑定位于
`frontend/bindings/packgradle/internal/service/`。

> 类型定义统一使用 JSON snake_case；本表中的字段名写 Go 侧字段，括号内注明 JSON 键名。
> 完整 DTO 字段表见 [通信契约：数据结构](../contract/02-data-structures.md)。

## 1. EnvService（7 个方法）

注册对象：`service.NewEnvService(config)`。负责工具检测、PATH、API Key 与首次运行判定。

| 方法 | Go 签名 | 返回 | 说明 |
| --- | --- | --- | --- |
| `Detect` | `Detect() []ToolInfo` | `ToolInfo[]` | 检测 packwiz 与 prism-launcher，始终返回两个元素。查找链：config → 环境变量（PACKWIZ/PRISM）→ PATH → 默认目录。 |
| `Configure` | `Configure() ([]ToolInfo, []string, error)` | `[ToolInfo[], string[]]` | 把检测到的工具目录幂等写入 `HKCU\Environment\Path` 并广播 `WM_SETTINGCHANGE`；更新当前进程 PATH。返回配置后最新检测结果 + 实际新增目录。 |
| `SetToolPath` | `SetToolPath(name, path string) ([]ToolInfo, error)` | `ToolInfo[]` | 保存用户手动指定的工具路径（空串清除），`name` 仅接受 `packwiz` / `prism-launcher`。返回最新检测结果。 |
| `GetApiKey` | `GetApiKey() string` | `string` | 返回已保存的 CurseForge API Key（未配置为空串）。 |
| `SetApiKey` | `SetApiKey(key string) error` | `void` | 保存 API Key（空串清除），两端 trim。 |
| `ConfigExists` | `ConfigExists() bool` | `boolean` | 判断全局 config.toml 是否已在磁盘上（首次运行检测，前端据此弹首次引导）。 |
| `MarkConfigCreated` | `MarkConfigCreated() error` | `void` | 首次引导完成/跳过后落盘 config.toml（已存在时 no-op），下次启动不再引导。 |

### ToolInfo

| Go 字段 | JSON | 类型 | 含义 |
| --- | --- | --- | --- |
| `Name` | `name` | string | `packwiz` / `prism-launcher` |
| `Found` | `found` | bool | 是否已安装 |
| `Path` | `path` | string | 可执行文件或配置目录完整路径 |
| `Source` | `source` | string | 发现来源：`config` / `env` / `path` / `default-dir`（前端按 `tool.source.*` 翻译） |
| `EnvDir` | `env_dir` | string | 需要加入 PATH 的目录（无可加目录时为空） |
| `EnvOK` | `env_ok` | bool | 该目录是否已在用户 PATH 中 |

## 2. PackwizService（8 个方法）

注册对象：`service.NewPackwizService(config)`。负责 packwiz 项目与 CurseForge 更新。

### 2.1 项目 CRUD

| 方法 | Go 签名 | 返回 | 说明 |
| --- | --- | --- | --- |
| `ImportProject` | `ImportProject(packTomlPath string) (packwiz.PackProject, error)` | `PackProject` | 解析指定 `pack.toml`，首次导入时在项目根创建 `.pgignore`；同名项目覆盖路径。导入成功即写入全局项目索引。 |
| `ListProjects` | `ListProjects() []packwiz.PackProject` | `PackProject[]` | 返回所有已导入项目的解析结果。单个项目解析失败不中断：错误码 JSON 落入 `PackProject.Error`。 |
| `RemoveProject` | `RemoveProject(name string) ([]packwiz.PackProject, error)` | `PackProject[]` | 按名称移除项目，返回剩余列表。若存在项目级关联，先清理实例侧 junction/硬链接并删除 `packgradle.toml`；项目目录内用户文件不动。 |
| `RefreshProject` | `RefreshProject(name string) packwiz.RefreshResult` | `RefreshResult` | 在项目目录执行 `packwiz refresh`，返回 `{ok, output}`。未找到工具时 `ok=false` 且 `output` 为 `err.tool.packwiz_not_found`。 |

### 2.2 CurseForge 版本获取

| 方法 | Go 签名 | 返回 | 说明 |
| --- | --- | --- | --- |
| `FetchModVersion` | `FetchModVersion(projectName, modID string) (packwiz.ModInfo, error)` | `ModInfo` | 调用 CF `GET /v1/mods/{projectID}/files/{fileID}` 获取单个 mod 的文件信息，写入 `.cache/modversion.cache`，返回带 `cf_*` 回填字段的 ModInfo。 |
| `FetchAllModVersions` | `FetchAllModVersions(projectName string) ([]ModVersionResult, error)` | `ModVersionResult[]` | 并发（上限 8）批量获取全部 CF 源 mod，一次性 `UpsertMany` 写缓存。单个失败不中断，结果逐条 `{id,name,version,ok,error}`。项目中无 CF mod 返回 `err.cf.no_cf_mods`。 |

### 2.3 更新检查与应用

| 方法 | Go 签名 | 返回 | 说明 |
| --- | --- | --- | --- |
| `CheckUpdates` | `CheckUpdates(projectName string) (packwiz.UpdateCheckResult, error)` | `UpdateCheckResult` | 运行 `packwiz update --all` 并向确认提示喂入 `n`（只打印列表不应用），解析输出为 `updates[]` 与 `errors[]`。 |
| `UpdateMods` | `UpdateMods(projectName, modName string) (packwiz.RefreshResult, error)` | `RefreshResult` | `modName` 非空：`packwiz update <name>`；为空：`packwiz update --all -y`。成功后重建 CF 版本缓存（清理旧 file-id 孤儿条目 + 自动获取变化项）。 |

## 3. PrismService（26 个方法）

注册对象：`service.NewPrismService(config)`。负责 Prism 实例、项目关联、目录同步与 meta 同步。

### 3.1 实例定位与扫描

| 方法 | Go 签名 | 返回 | 说明 |
| --- | --- | --- | --- |
| `ListInstances` | `ListInstances() ([]prism.Instance, error)` | `Instance[]` | 定位实例根目录并扫描全部实例；单个实例解析失败不中断（错误入 `Instance.Error`）。定位链：`%APPDATA%\PrismLauncher\prismlauncher.cfg` 的 `InstanceDir` → 实例目录。 |
| `InstancesDir` | `InstancesDir() (string, error)` | `string` | 返回当前定位到的实例根目录。自动定位成功后回写全局配置。 |
| `GetInstancesPath` | `GetInstancesPath() string` | `string` | 返回用户手动指定的实例根目录（空串 = 走自动定位）。 |
| `SetInstancesPath` | `SetInstancesPath(path string) error` | `void` | 保存手动实例根目录（空串清除恢复自动定位）；非空时校验目录存在。 |
| `Overview` | `Overview() PrismOverview` | `PrismOverview` | 一次性返回实例目录 + 实例列表 + 关联视图（Prism 页装载唯一入口）。定位失败不抛错：错误码 JSON 落入 `locate_error`，`links` 仍可组装。 |

### 3.2 项目 ↔ 实例关联

| 方法 | Go 签名 | 返回 | 说明 |
| --- | --- | --- | --- |
| `LinkProject` | `LinkProject(projectName, instanceID string) error` | `void` | 建立项目→实例关联（一项目一实例，重复关联覆盖）。实例必须存在；换实例前先清理旧实例侧链接。持久化到项目 `packgradle.toml`。 |
| `UnlinkProject` | `UnlinkProject(projectName string) error` | `void` | 解除关联：先删除实例侧 junction/硬链接（实例目录无法定位时报错并保留配置），再清空关联与目录/文件清单。 |
| `GetLinks` | `GetLinks() []prism.LinkView` | `LinkView[]` | 读取全部项目 `packgradle.toml` + 实时扫描实例，组装关联视图；实例被删时 `instance_valid=false`。按项目名排序。 |
| `CreateInstance` | `CreateInstance(projectName string) (prism.Instance, error)` | `Instance` | 用项目 pack.toml 的 Minecraft/加载器信息创建最小 Prism 实例；创建成功后可再 `LinkProject`。 |

### 3.3 目录同步（junction / 硬链接 / files 模式）

| 方法 | Go 签名 | 返回 | 说明 |
| --- | --- | --- | --- |
| `AddDirLink` | `AddDirLink(projectName, projectDir string) error` | `void` | 添加目录关联对（实例侧默认同名）；要求项目已关联且项目侧目录存在。 |
| `RemoveDirLink` | `RemoveDirLink(projectName, projectDir string) error` | `void` | 移除目录关联，并删除已建链接（仅链接本身；实例目录无法定位时报错保留配置）。 |
| `ListDirLinks` | `ListDirLinks(projectName string) []prism.DirLinkView` | `DirLinkView[]` | 返回项目全部目录关联视图（含两侧目录是否存在、mode、files 清单）。 |
| `ListProjectDirs` | `ListProjectDirs(projectName string) ([]string, error)` | `string[]` | 项目根顶层目录名（排除 `mods` 与 `.` 开头隐藏目录），作为目录关联候选。 |
| `CreateAllLinks` | `CreateAllLinks(projectName string) ([]prism.LinkResult, error)` | `LinkResult[]` | 一键关联：遍历项目根顶层条目，目录→junction、文件→硬链接；排除 `mods`、`CoreExcluded` 与 `.pgignore` 命中项；单条失败不中断。 |
| `ManualLinkDir` | `ManualLinkDir(projectName, dir string) (prism.LinkResult, error)` | `LinkResult` | 手动链接单个目录。实例侧非空时先把内容合并复制到项目目录（同名文件跳过，项目侧权威），再删实例侧建 junction；已是 junction 幂等返回 `existing`。 |
| `HasPGIgnore` | `HasPGIgnore(projectName string) (bool, error)` | `boolean` | 项目是否已有 `.pgignore`（一键关联前询问用）。 |
| `EnsurePGIgnore` | `EnsurePGIgnore(projectName string) (bool, error)` | `boolean` | 确保存在 `.pgignore`（已存在不覆盖），返回是否新建。 |
| `SetDirLinkMode` | `SetDirLinkMode(projectName, dir, mode string) error` | `void` | 切换同步模式：`""`=整目录 junction，`"files"`=文件级；自动清理旧链接并重建；切回 junction 遇实例侧有内容返回 `err.sync.manual_required`。 |
| `SetDirLinkFiles` | `SetDirLinkFiles(projectName, dir string, files []string) error` | `void` | 设置 files 模式清单并重建链接；空清单 = 退出 files 模式回到 junction。清单经严格路径校验。 |
| `SelectInstanceFiles` | `SelectInstanceFiles(projectName, dir string, files []string) ([]prism.LinkResult, error)` | `LinkResult[]` | 将实例侧选中文件纳入文件级同步：移动到项目目录（同名跳过、跨卷报错）→ 硬链接回实例侧（失败回滚移动）→ 仅成功项写入清单。 |
| `ListDirFiles` | `ListDirFiles(projectName, dir string) ([]string, error)` | `string[]` | 递归列出项目目录下全部文件（相对 `dir`，排除隐藏项）。 |
| `ListInstanceDirFiles` | `ListInstanceDirFiles(projectName, dir string) ([]string, error)` | `string[]` | 递归列出实例侧 `minecraft/<dir>` 文件；整目录 junction 时返回空（两侧同一物理目录）；files 模式刚切换后回退列项目侧。 |

### 3.4 meta 同步（mods 元数据，mods 目录不建 junction）

| 方法 | Go 签名 | 返回 | 说明 |
| --- | --- | --- | --- |
| `PushMeta` | `PushMeta(projectName, modID string) (int, error)` | `number` | 项目 mod 元数据 → 实例 `mods/.index/*.pw.toml`（插入 `x-prismlauncher-loaders/mc-versions/release-type/version-number`）。`modID` 空串 = 全部。返回写入数量。 |
| `PullMeta` | `PullMeta(projectName, modID string) (int, error)` | `number` | 实例 `.index` 元数据 → 项目 `mods/*.pw.toml`（剥离 Prism 扩展字段与 download.url）。拉回后前端应执行 `packwiz refresh` 收录。返回数量。 |
| `MetaDiff` | `MetaDiff(projectName string) (prism.MetaDiff, error)` | `MetaDiff` | 对比项目 `index.toml` 权威列表 vs 实例 `mods/.index`，产出 instance_only / project_only / version_diff 三类差异；每次调用重算并写 `metadiff.cache`。 |
| `WatchMods` | `WatchMods() ([]string, error)` | `string[]` | 幂等重同步 mods 目录监听：覆盖当前全部已关联项目，监听项目 `<project>/mods` 与实例 `<instance>/minecraft/mods/.index`。任一侧变化后 600ms 防抖执行一次 `MetaDiff`，并通过事件 `packgradle:mods-diff` 将 `ModsWatchEvent` 发到前端。返回当前监听的项目名。应用启动时自动调用；关联/解除关联/修改实例目录后也会自动重同步。 |

### 3.5 mods 目录实时监听事件

后端已注册 Wails 自定义事件 `packgradle:mods-diff`，数据为 `prism.ModsWatchEvent`。
生成绑定已把该事件的数据类型写入 `@wailsio/runtime` 的 `Events.CustomEvents`，
前端可直接获得类型推断：

```ts
import { Events } from '@wailsio/runtime'
import { PrismService } from '../../bindings/packgradle/internal/service'

// 启动/重同步监听（应用启动时后端已自动执行，关联变化后建议再调用一次）
await PrismService.WatchMods()

// 接收后端推送的差异包
const off = Events.On('packgradle:mods-diff', event => {
    const pkt = event.data // ModsWatchEvent
    if (pkt.error) return
    // pkt.project + pkt.side + pkt.diff 更新差异视图
})
// 组件卸载时 off()
```

| Go 字段 | JSON | 类型 | 含义 |
| --- | --- | --- | --- |
| `Project` | `project` | string | 变化所属项目名 |
| `Side` | `side` | string | 触发端：`project` / `instance` / `both`（防抖窗口内两侧都变） |
| `Diff` | `diff` | MetaDiff | 重算后的双端差异 |
| `Error` | `error` | string? | 比对失败原因（errs JSON 文本；成功时省略） |

## 4. 返回约定小结

| 约定 | 说明 |
| --- | --- |
| 预期内失败 | 多数方法以 `error` 抛出（前端 Promise reject，`err.cause` 为结构化 AppError）。 |
| 容错数据 | `ListProjects`、`ListInstances`、`GetLinks`、`Overview` 把单项失败放进字段（`PackProject.Error` / `Instance.Error` / `PrismOverview.locate_error`），列表本身不抛错。 |
| CLI 结果 | `RefreshProject` / `UpdateMods` 总是返回 `RefreshResult{ok,output}`，不抛错；`output` 可能是 `err.*` JSON 文本，前端用 `displayText()` 统一解析。 |
| 批量逐条结果 | `CreateAllLinks`、`SelectInstanceFiles`、`FetchAllModVersions` 返回逐条目状态数组，调用方可统计 linked/skipped/error 等。 |
| 空列表 | 生成的 TS 类型中 Go slice 对应 `T[] | null`，前端需 `?? []` 兜底。 |

## 5. 前端生成绑定签名示例

以 `PrismService.SelectInstanceFiles` 为例，生成绑定：

```ts
// frontend/bindings/packgradle/internal/service/prismservice.ts
export function SelectInstanceFiles(projectName: string, dir: string, files: string[] | null):
  $CancellablePromise<prism$0.LinkResult[] | null>
```

其他方法同理，参数名与 Go 参数一致、返回类型为 `$CancellablePromise<T>`，通过 `$Call.ByID(...)` 调用后端。
