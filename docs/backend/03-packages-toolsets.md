# internal 包与工具集

本节按包列出后端函数/类型工具集（只列对上层有意义的导出 API；`*_test.go` 不在此列）。
`service` 包本身的方法见 [服务 API](./02-service-api.md)，这里只列其内部辅助函数。

## 1. errs —— 结构化错误

文件：`internal/errs/errs.go`

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `AppError` | `{Code string; Args []string; Detail string}`（JSON: `code/args/detail`） | 结构化错误。`Code` 为前端翻译键（`err.*`），`Args` 为插值参数，`Detail` 为底层错误透传文本。 |
| `New` | `New(code string, args ...any) error` | 构造无 detail 的错误码错误。 |
| `NewDetail` | `NewDetail(code, detail string, args ...any) error` | 构造带底层文本的错误码错误。 |
| `CodeOf` | `CodeOf(err error) string` | 提取错误码；非 AppError 返回空串。 |
| `(*AppError).Error` | `Error() string` | 返回与 MarshalError 一致的 JSON 文本，供数据字段携带与日志。 |

## 2. appconfig —— 全局 / 项目级配置

### 2.1 全局配置（config.go）

| 名称 | 签名 / 类型 | 说明 |
| --- | --- | --- |
| `Config` | `PackwizPath, PrismPath, Projects []ProjectEntry, PrismInstancesPath, PrismInstancesDir, CurseforgeApiKey, LegacyLinks, LegacyDirLinks`（TOML） | 全局配置，文件 `%AppData%\PackGradle\config.toml`。 |
| `ProjectEntry` | `{Name, Path}` | 项目索引条目，Path 为 pack.toml 所在目录。 |
| `ConfigManager` | 结构体（互斥锁保护） | 所有服务共享的配置管理器。 |
| `NewConfigManager` | `() (*ConfigManager, error)` | 创建/读取用户配置目录中的 config.toml。 |
| `NewConfigManagerAt` | `(path string) *ConfigManager` | 指定路径构造（不读盘，测试用）。 |
| `Get` | `() Config` | 返回配置快照。 |
| `SetToolPath` | `(tool, path string) error` | 保存工具路径，`tool` ∈ {packwiz, prism-launcher}。 |
| `AddProject` | `(ProjectEntry) error` | 添加项目，同名覆盖路径。 |
| `RemoveProject` | `(name string) error` | 按名称移除。 |
| `FindProject` | `(name string) (ProjectEntry, bool)` | 按名称查找。 |
| `SetApiKey` | `(key string) error` | 保存 API Key。 |
| `SetPrismInstancesPath` | `(path string) error` | 手动实例根目录（空串清除）。 |
| `SetPrismInstancesDir` | `(dir string) error` | 自动定位结果回写（值未变不写盘）。 |
| `MigrateLegacyProjectConfigs` | `() error` | v1 全局 links/dir_links 迁移到项目级配置（幂等）。 |

### 2.2 项目级配置（projectconfig.go）

| 名称 | 签名 / 类型 | 说明 |
| --- | --- | --- |
| `ProjectConfig` | `{Instance string; DirLinks []ProjectDirLink; FileLinks []string}`（TOML） | 文件 `<项目目录>/packgradle.toml`。 |
| `ProjectDirLink` | `{ProjectDir, InstanceDir, Mode, Files []string}` | 目录关联；`Mode=""` junction / `"files"` 文件级同步。 |
| `ProjectConfigPath` | `(projectPath string) string` | 项目级配置路径。 |
| `LoadProjectConfig` | `(projectPath string) (ProjectConfig, error)` | 读取；不存在返回零值。 |
| `SaveProjectConfig` | `(projectPath string, ProjectConfig) error` | 原子写。 |
| `WithProjectConfigLock` | `(projectPath string, fn func() error) error` | 按项目路径加锁执行读-改-写（Load/Save 自身不加锁，防止死锁）。 |

### 2.3 TOML 原子读写（tomlfile.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `ReadToml` | `(path string, v any) error` | 文件不存在返回 nil 并保持 v 不变（首启场景）；其余错误直接返回底层错误（由调用方包裹 `err.*`）。 |
| `WriteTomlAtomic` | `(path string, v any) error` | 自动建父目录，先写唯一临时文件再 rename；失败分别包 `err.file.mkdir/write/serialize/save`。 |

## 3. envutil —— 可执行文件查找与 Windows PATH

文件：`internal/envutil/path.go`（Windows 专用实现）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `FindExecutable` | `(configPath, exeName, envVar string, candidates ...string) (path, source string, ok bool)` | 统一查找链：config → `%envVar%`（支持 `%VAR%` 展开）→ `exec.LookPath` → 候选默认目录。source ∈ {config, env, path, default-dir}。 |
| `InUserPath` | `(dir string) bool` | 判断目录是否已在 `HKCU\Environment\Path`（忽略大小写，先展开 `%VAR%`）。 |
| `AddDirsToUserPath` | `(dirs []string) ([]string, error)` | 去重合并写入用户 PATH（保留 REG_SZ/REG_EXPAND_SZ 类型），广播 `WM_SETTINGCHANGE`；返回实际新增目录。 |
| `JoinPathWith` | `(dirs []string, existing string) string` | 把新增目录拼进当前进程 PATH（会话内即时生效）。 |

内部还有：`mergePathDirs`（大小写不敏感去重）、`expandEnv`（调用 `ExpandEnvironmentStringsW`）、`normalizePathEntry`。

## 4. fsutil —— 文件系统工具

文件：`internal/fsutil/fsutil.go`

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `Exists` | `(path string) bool` | 路径存在（os.Stat）。 |
| `IsDir` | `(path string) bool` | 是否为目录。 |
| `IsFile` | `(path string) bool` | 是否为普通文件。 |
| `MkdirAll` | `(dir string) error` | 递归建目录，失败包 `err.file.mkdir`。 |
| `WriteFile` | `(path string, data []byte) error` | 自动建父目录 + `os.WriteFile`（0o644），失败包 `err.file.write`。 |
| `RemoveEmptyDirs` | `(root string)` | 自底向上清理空目录（保留含内容目录）。 |
| `SamePath` | `(a, b string) bool` | 规范化后比较路径（junction 目标校验）。 |
| `CopyDirMerge` | `(src, dst string) error` | 目录合并复制；同名文件跳过（项目侧权威）。 |
| `ListFilesRelative` | `(root string) ([]string, error)` | 递归列出相对文件路径（排除隐藏项）。 |

## 5. junction —— Windows Junction 管理

文件：`junction.go`（接口）、`windows.go`（生产实现）、`other.go`（非 Windows 桩）、`memory.go`（测试假实现）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `Manager` | 接口 | 链接操作抽象。 |
| `Manager.Create` | `(link, target string) error` | 将 link 建为指向 target 的 junction（target 须为已存在绝对路径）。 |
| `Manager.Remove` | `(link string) error` | 删除链接本身，不动目标。 |
| `Manager.IsJunction` | `(link string) (bool, error)` | 判断是否 junction（不存在/普通目录/文件 false）。 |
| `Manager.TargetOf` | `(link string) (string, error)` | 返回目标绝对路径。 |
| `NewWindowsManager` | `() Manager` | Windows 生产实现：创建用 `cmd /c mklink /J`（参数数组传参，天然处理中文/空格路径，无需管理员），检测/目标解析用 `FSCTL_GET_REPARSE_POINT` 直查重解析点，删除用 `os.Remove`（仅删链接本身）。 |
| `NewMemoryManager` | `() Manager` | 内存实现（测试注入）。 |

## 6. pgignore —— 一键关联忽略规则

文件：`internal/pgignore/pgignore.go`

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `DefaultContent` | 常量 | 默认忽略：`.git .cache index.toml pack.toml packgradle.toml .pgignore`。 |
| `Ensure` | `(projectPath string) (created bool, err error)` | 创建 `.pgignore`，已存在不覆盖。 |
| `CoreExcluded` | `(name string) bool` | 核心文件始终排除（即使 .pgignore 损坏/清空）。 |
| `Matcher` | 结构体 | 封装 gitignore 规则。 |
| `Load` | `(projectPath string) *Matcher` | 解析 `.pgignore`；不存在或解析失败返回空匹配器（不阻断一键关联）。 |
| `(*Matcher).Matches` | `(relPath string) bool` | 相对项目根条目是否命中规则（目录规则补尾斜杠匹配）。 |

## 7. packwiz —— 项目解析与 CLI 封装

### 7.1 解析（parse.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `ParseProject` | `(packToml string) (PackProject, error)` | 解析 pack.toml；读取 index.toml（校验 `index.file` 不得越界）；逐 mod 解析 `mods/*.pw.toml`；旧式目录结构回退扫描 mods/。 |
| `UpdateVersion` | `(update map[string]map[string]any) string` | 从 `[update.*]` 取版本号。 |
| `CfIDsFromUpdate` | `(update map[string]map[string]any) (int64, int64)` | 从 `[update.curseforge]` 提取 project-id/file-id。 |

### 7.2 CLI（cli.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `RunRefresh` | `(packwizPath, projectDir string) RefreshResult` | `packwiz refresh`；5 分钟超时，隐藏控制台窗口。 |
| `RunCheckUpdates` | `(packwizPath, projectDir string) (UpdateCheckResult, error)` | `packwiz update --all` + stdin `"n\n"` 只列不应用；15 分钟超时；解析输出。 |
| `RunUpdateMods` | `(packwizPath, projectDir, modName string) RefreshResult` | 单个 `update <name>` / 全部 `update --all -y`；15 分钟超时。 |

超时统一返回 `RefreshResult{OK:false, Output: err.packwiz.timeout}`。

### 7.3 更新输出解析（update.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `ParseUpdateOutput` | `(output string) (updates, errors []ModUpdateInfo)` | 解析 packwiz update 文本输出；识别 `pinned`（固定版本）与 `no_updater`（无更新源）两类跳过项。 |

### 7.4 类型（types.go）

`ModInfo`、`PackProject`、`RefreshResult`、`ModUpdateInfo`、`UpdateCheckResult`，字段详见 [数据结构字典](../contract/02-data-structures.md)。

## 8. prism —— Prism 实例与 meta 转换

### 8.1 定位（prism.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `DataDir` | `() string` | Prism 数据目录：`%APPDATA%\PrismLauncher`（配置文件固定位置）。 |
| `InstancesDir` | `(dataDir string) (string, error)` | 读取 `prismlauncher.cfg` 的 `InstanceDir`；相对路径相对 dataDir 解析，也支持绝对路径；无配置时回退 `instances` 子目录。INI 读取容忍 BOM/CRLF、跳过空行与 `#`/`;` 注释。 |

### 8.2 实例扫描（instance.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `ScanInstances` | `(instancesDir string) ([]Instance, error)` | 扫描实例根目录：有效实例 = 含 `instance.cfg` 的一级子目录（目录名即实例 ID）；解析 instance.cfg / mmc-pack.json / instgroups.json；单项失败错误码 JSON 落入 `Instance.Error`；按名称（不区分大小写）排序。 |

内部映射 `loaderUIDs`：`net.minecraftforge→forge`、`net.neoforged→neoforge`、`net.fabricmc.fabric-loader→fabric`、`org.quiltmc.quilt-loader→quilt`、`com.mumfrey.liteloader→liteloader`。

### 8.3 实例创建（create.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `CreateMinimalInstance` | `(instancesDir string, req CreateRequest) (Instance, error)` | 生成最小 Prism 实例（instance.cfg + mmc-pack.json）；校验名称/版本/加载器；已存在报 `err.prism.instance_exists`。 |

### 8.4 meta 转换（meta.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `PrismMeta` | `{Loaders, MCVersions, ReleaseType, Version string}` | Prism 扩展字段载体。 |
| `ToPrismFormat` | `(content []byte, meta PrismMeta) ([]byte, error)` | pw.toml 在 `side` 后插入 4 个 `x-prismlauncher-*` 字段。 |
| `FromPrismFormat` | `(content []byte) ([]byte, error)` | 剥掉 `x-prismlauncher-*` 与 `[download]` 的 `url`，还原 packwiz 格式。 |

## 9. curseforge —— CurseForge API 与本地缓存

### 9.1 客户端（client.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `CfFileCache` | `{DisplayName, FileDate, ReleaseType, FetchedAt}`（TOML: display_name/file_date/release_type/fetched_at） | 缓存条目。 |
| `CacheKey` | `(projectID, fileID int64) string` | 缓存键 `"projectID:fileID"`。 |
| `FetchFile` | `(apiKey string, projectID, fileID int64) (CfFileCache, error)` | 调官方 `GET /v1/mods/{id}/files/{fileId}`；401/403→`err.cf.unauthorized`，404→`err.cf.not_found`，其他→`err.cf.http`，网络失败→`err.cf.request`，解析失败→`err.cf.parse_response`。HTTP 超时 15s。 |
| `BaseURL` / `SetBaseURL` | 读写 API 基地址 | 测试注入 httptest 用。 |

### 9.2 缓存（cache.go）

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `CfCacheStore` | 结构体（mutex） | 项目 `.cache/modversion.cache`。 |
| `NewCfCacheStore` | `(root string) *CfCacheStore` | root 为 `.cache` 目录。 |
| `Load` | `() (map[string]CfFileCache, error)` | 读缓存（不存在/为空 → 空 map）。 |
| `Save` | `(map[string]CfFileCache) error` | 覆盖写。 |
| `Upsert` | `(key string, CfFileCache) error` | 单条读改写。 |
| `UpsertMany` | `(map[string]CfFileCache) error` | 批量合并，只读一次写一次（避免 O(n²)）。 |
| `Prune` | `(keep func(key string) bool) error` | 删除不满足 keep 的条目，仅在有删除时写盘。 |

## 10. singleinstance —— 单实例防护

| 名称 | 签名 | 说明 |
| --- | --- | --- |
| `Acquire` | `(name string) bool` | Windows 命名 Mutex `Local\PackGradle_SingleInstance`。 |
| `NotifyAlreadyRunning` | `()` | 弹系统提示「应用已在运行」。 |

非 Windows 构建使用 `singleinstance_other.go` 桩实现。

## 11. service 内部共享辅助函数（不直接暴露）

| 函数 | 位置 | 作用 |
| --- | --- | --- |
| `findProjectByName` | helpers.go | 按名称查项目并解析 pack.toml（PackwizService/PrismService 共用）。 |
| `findPackwizExecutable` | helpers.go | config → `PACKWIZ` → PATH → `%USERPROFILE%\go\bin` 查找链。 |
| `resolveInstancesDir` | helpers.go | 手动路径优先，失效静默回退自动定位；都失败报 `err.prism.not_found`。 |
| `indexInstances` / `findInstanceByID` | helpers.go | 实例列表 → map / 查找。 |
| `removeProjectLinkTargets` | linked.go | 清理项目在实例侧的全部链接（junction/硬链接）。 |
| `junctionPointsTo` / `hardlinkPointsTo` | linked.go / helpers.go | 删除链接前的身份校验（仅删仍指向项目侧的链接）。 |
| `validateRelDir` / `validRelPath` | helpers.go | 客户端相对目录参数校验（拒绝绝对路径/盘符/`.`/`..`）。 |
| `normalizeFileListStrict` | helpers.go | 客户端文件清单校验 + 规范化（trim、转斜杠、去重、排序）。 |
| `hardlinkFile` | helpers.go | 项目侧文件硬链接到实例侧，返回 LinkResult。 |
| `inspectDirSide` | helpers.go | 实例侧位置实态：absent / isFile / emptyDir / nonEmptyDir。 |
| `linkDir` / `linkFile` / `linkDirFiles` | links.go | 单条目建链（junction/硬链接/files 模式）。 |
| `removeDirLinkTargets` | links.go | 删除某目录关联的链接。 |
| `isCrossDeviceError` | links.go | 判断跨卷错误（`ERROR_NOT_SAME_DEVICE`）。 |
| `loaderMeta` / `releaseTypeMeta` / `parseMetaVersion` / `projectMetaVersion` | meta.go | meta 推送/差异的版本与加载器字段组装。 |
| `WatchMods` / `ServiceStartup` / `ServiceShutdown` | mods_watch.go | 双端目录监听入口、启动自动监听与关闭清理。 |
| `currentModsWatchPairs` / `sync` / `refreshProject` | mods_watch.go | 扫描已关联项目、对齐 fsnotify 注册与防抖任务、跟随目录删除/重建迁移注册。 |
