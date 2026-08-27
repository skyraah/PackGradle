# 通信契约：数据结构字典（DTO）

Go 结构体定义于 `internal/{packwiz,prism,service}`，JSON 标签即线上字段名。
生成 TS 接口位于 `frontend/bindings/packgradle/internal/**/models.ts`（属性保持 snake_case）。

类型标注：TS 生成类型对 Go slice 统一为 `T[] | null`；`int64` → `number`；其余与 Go 一一对应。

## 1. 服务层类型

### 1.1 ToolInfo（`service/detect.go`）

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `name` | string | `packwiz` / `prism-launcher` |
| `found` | bool | 是否已安装 |
| `path` | string | 可执行文件或配置目录完整路径 |
| `source` | string | `config` / `env` / `path` / `default-dir` |
| `env_dir` | string | 需要加入 PATH 的目录（可为空） |
| `env_ok` | bool | 该目录是否已在用户 PATH |

### 1.2 ModVersionResult（`service/curseforceservice.go`）

批量版本获取的单条结果。

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `id` | string | mod ID |
| `name` | string | mod 显示名 |
| `version` | string | displayName（版本） |
| `ok` | bool | 本条是否成功 |
| `error` | string | 失败原因（`err.*` JSON 文本或普通文本） |

### 1.3 PrismOverview（`service/prismservice.go`）

Prism 页一次性装载的聚合结构。

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `instances_dir` | string | 当前实例根目录；定位失败为空 |
| `locate_error` | string | 定位失败的错误码 JSON 文本；空串 = 成功 |
| `instances` | `Instance[] \| null` | 实例列表；定位失败为空 |
| `links` | `LinkView[] \| null` | 项目↔实例关联视图；定位失败时实例侧为失效态 |

## 2. packwiz 域类型（`internal/packwiz/types.go`）

### 2.1 PackProject

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `name` | string | 项目名（pack.toml 的 name） |
| `path` | string | pack.toml 所在目录 |
| `pack_toml` | string | pack.toml 完整路径 |
| `version` | string | 整合包版本 |
| `author` | string | 作者 |
| `pack_format` | string | packwiz 格式版本（如 `packwiz:2`） |
| `minecraft` | string | MC 版本 |
| `modloader` | string | fabric / forge / neoforge / quilt ... |
| `modloader_version` | string | 加载器版本 |
| `mods` | `ModInfo[] \| null` | mod 列表 |
| `error` | string | 解析失败原因（`err.*` JSON 文本）；空串 = 正常 |

### 2.2 ModInfo

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `id` | string | mod ID（.pw.toml 文件名） |
| `name` | string | 显示名 |
| `side` | string | `client` / `server` / `both`（文案前端翻译） |
| `version` | string | pw.toml 顶层 version（不一定存在） |
| `file` | string | 下载文件名 |
| `path` | string | pw.toml 完整路径 |
| `cf_project_id` | number（int64） | CF project-id；0 = 非 CF 源 |
| `cf_file_id` | number（int64） | CF file-id；0 = 非 CF 源 |
| `cf_version` | string | 缓存回填的 displayName |
| `cf_file_date` | string | 缓存回填的发布日期 |
| `cf_release_type` | number | 1=正式版 2=测试版 3=Alpha；0=未获取 |

### 2.3 RefreshResult

| JSON 字段 | 类型 | 说明 |
| --- | --- | --- |
| `ok` | bool | CLI 是否成功 |
| `output` | string | CLI 输出；超时/未找到工具时为 `err.*` JSON 文本 |

### 2.4 ModUpdateInfo

| JSON 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | mod 显示名 |
| `has_update` | bool | 是否有更新 |
| `current_file` | string | 当前文件名 |
| `latest_file` | string | 最新文件名 |
| `error` | string | 失败/跳过原因（可为 `err.update.pinned` / `err.update.no_updater` JSON） |

### 2.5 UpdateCheckResult

| JSON 字段 | 类型 | 说明 |
| --- | --- | --- |
| `ok` | bool | 检查命令是否执行成功 |
| `output` | string | CLI 原始输出 |
| `updates` | `ModUpdateInfo[] \| null` | 有更新的 mod |
| `errors` | `ModUpdateInfo[] \| null` | 检查失败/跳过/无更新源的 mod |

## 3. prism 域类型（`internal/prism/types.go`）

### 3.1 Instance

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `id` | string | 实例 ID（instances/ 下目录名） |
| `name` | string | instance.cfg 的 name=，缺失回退 ID |
| `path` | string | 实例目录完整路径 |
| `game_dir` | string | 游戏目录 `<实例>/minecraft`（可能尚不存在） |
| `group` | string | instgroups.json 分组（可为空） |
| `minecraft` | string | mmc-pack.json 中 net.minecraft 版本 |
| `modloader` | string | fabric/forge/neoforge/quilt/liteloader/"" |
| `modloader_version` | string | 加载器组件版本 |
| `error` | string | 解析失败原因（`err.*` JSON 文本） |

### 3.2 CreateRequest（服务端内部创建入参，未直接暴露为方法参数）

| JSON 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | 实例名 |
| `minecraft` | string | MC 版本 |
| `modloader` | string | 加载器名 |
| `modloader_version` | string | 加载器版本 |

（`PrismService.CreateInstance(projectName)` 在服务端用项目信息组装该结构，前端只传项目名。）

### 3.3 LinkView（项目 ↔ 实例关联视图）

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `project` | string | 项目名 |
| `project_path` | string | 项目目录 |
| `instance_id` | string | 关联实例 ID |
| `instance_name` | string | 实例显示名（实例被删/改名时为空） |
| `instance_path` | string | 实例目录（失效时为空） |
| `instance_valid` | bool | 实例当前是否仍可解析 |

### 3.4 DirLinkView（目录关联视图）

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `project` | string | 项目名 |
| `instance` | string | 实例 ID |
| `project_dir` | string | 项目根下目录名 |
| `instance_dir` | string | 实例游戏目录下相对路径 |
| `mode` | string | `""` = 整目录 junction；`"files"` = 文件级同步 |
| `files` | `string[] \| null` | files 模式同步清单（相对 ProjectDir） |
| `project_exists` | bool | 项目侧目录是否存在 |
| `instance_exists` | bool | 实例侧目录是否存在 |

### 3.5 LinkResult（建链逐条结果）

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `name` | string | 相对项目根的条目名 |
| `is_dir` | bool | 目录（junction）/ 文件（硬链接） |
| `status` | string | `linked` / `existing` / `skipped` / `manual` / `error` |
| `detail` | string | 跳过原因或错误文本（可为 `err.*` JSON） |

### 3.6 VersionDiffItem

| JSON 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | mod ID |
| `project_version` | string | 项目侧版本 |
| `instance_version` | string | 实例侧版本 |

### 3.7 MetaDiff

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `fetched_at` | string | 计算时间（RFC3339） |
| `instance_only` | `string[] \| null` | 实例 `.index` 有、项目 `index.toml` 无（可拉取） |
| `project_only` | `string[] \| null` | 项目有、实例无（可推送） |
| `version_diff` | `VersionDiffItem[] \| null` | 双端版本不一致 |

### 3.8 ModsWatchEvent（后端事件 `packgradle:mods-diff` 数据包）

| JSON 字段 | Go / TS | 说明 |
| --- | --- | --- |
| `project` | string | 变化所属项目名 |
| `side` | string | 触发端：`project` / `instance` / `both` |
| `diff` | MetaDiff | 重算后的双端差异（`error` 非空时为零值） |
| `error` | string? | 比对失败原因（errs JSON 文本；成功时省略） |

## 4. 仅持久化、不直接上线的类型

| 结构 | 文件 | 用途 |
| --- | --- | --- |
| `appconfig.Config` | `%AppData%\PackGradle\config.toml` | TOML 全局配置（字段见后端文档） |
| `appconfig.ProjectConfig` | `<项目>/packgradle.toml` | TOML 项目级关联配置 |
| `curseforge.CfFileCache` | `<项目>/.cache/modversion.cache` | TOML 缓存；服务端读取后以 `cf_*` 字段并入 ModInfo 返回 |
| `MetaDiff` 缓存副本 | `<项目>/.cache/metadiff.cache` | TOML；`MetaDiff` 每次重算刷新 |

## 5. 生成 TS 接口节选（与 Go 定义一一对应）

```ts
// bindings/packgradle/internal/packwiz/models.ts
export interface PackProject {
    "name": string
    "path": string
    "pack_toml": string
    "version": string
    "author": string
    "pack_format": string
    "minecraft": string
    "modloader": string
    "modloader_version": string
    "mods": ModInfo[] | null
    "error": string
}

// bindings/packgradle/internal/service/models.ts
export interface PrismOverview {
    "instances_dir": string
    "locate_error": string
    "instances": prism$0.Instance[] | null
    "links": prism$0.LinkView[] | null
}
```

## 6. 版本兼容与演进规则

1. 改 DTO 前先改 Go 结构体与 `json` 标签，再运行 `wails3 generate bindings`，不要直接手改 `frontend/bindings`。
2. 字段只增不删（向后兼容）；需要废弃时保留字段并返回零值，前端渐进迁移。
3. `json` 标签缺省时 Go 默认字段名首字母大写——本项目约定显式 snake_case 标签，新增字段必须遵守。
4. 返回值优先使用有语义的结构体，避免返回裸 map；聚合查询优先单个 `Overview` 式方法，减少前端往返。
