# 后端总览与启动流程

## 1. 技术栈与定位

PackGradle 后端是一个 **Wails v3 桌面应用服务端**，语言为 Go（`go.mod` 声明 Go 1.25）。

- Wails 版本：`github.com/wailsapp/wails/v3 v3.0.0-beta.7`
- 核心功能：packwiz 项目管理、CurseForge 版本查询、Prism Launcher 实例定位/关联/元数据同步、目录 junction 与文件硬链接。
- 模块名：`packgradle`，业务代码全部位于 `internal/`，入口 `main.go`。

## 2. 目录结构

```
main.go                    应用入口：单实例、配置加载、服务注册、窗口创建
internal/
├── service/               Wails 暴露的服务层（前后端通信契约的实现）
│   ├── envservice.go        EnvService：工具检测 / PATH / API Key
│   ├── packwizservice.go    PackwizService：项目导入 / 列表 / 移除 / refresh
│   ├── curseforceservice.go PackwizService：CF 版本获取 / 检查更新 / 应用更新
│   ├── prismservice.go      PrismService：实例定位 / 关联 / 概览
│   ├── links.go             PrismService：一键建链 / 手动建链 / 文件级同步
│   ├── meta.go              PrismService：meta 推送 / 拉取 / 差异
│   ├── mods_watch.go        PrismService：mods 双端目录监听 → 差异事件
│   ├── linked.go            关联操作上下文与链接清理
│   ├── helpers.go           共享查找、校验、硬链接辅助函数
│   └── detect.go            工具检测实现与 ToolInfo
├── appconfig/             全局 config.toml 与项目级 packgradle.toml 读写
├── packwiz/               pack.toml/index.toml 解析、packwiz CLI 封装
├── prism/                 Prism 实例扫描/创建、pw.toml ↔ Prism meta 转换
├── curseforge/            CurseForge API 客户端与项目本地缓存
├── junction/              Windows NTFS Junction 管理（接口 + 实现）
├── envutil/               Windows PATH/注册表/可执行文件查找
├── fsutil/                文件系统工具（原子写、复制合并、相对文件列表）
├── pgignore/              .pgignore 忽略规则
├── errs/                  结构化错误 AppError
└── singleinstance/        单实例互斥
```

## 3. 启动流程（main.go）

1. **单实例防护**：`singleinstance.Acquire("Local\\PackGradle_SingleInstance")`。失败时调用 `NotifyAlreadyRunning()` 提示并退出，避免多实例并发写坏配置。
2. **加载全局配置**：`appconfig.NewConfigManager()`，读取 `%AppData%\PackGradle\config.toml`。
3. **旧配置迁移**：`MigrateLegacyProjectConfigs()` 把 v1 的全局 `[[links]]` / `[[dir_links]]` 一次性迁移到各项目的 `packgradle.toml`，随后清空旧字段（幂等）。
4. **创建 Wails 应用**并注册 3 个服务：

   ```go
   application.NewService(service.NewEnvService(config))
   application.NewService(service.NewPackwizService(config))
   application.NewService(service.NewPrismService(config))
   ```

   这三个服务实例共享同一个 `*appconfig.ConfigManager`。
5. **错误序列化钩子**：`MarshalError: marshalError`——把 `*errs.AppError` 序列化为 `{code,args,detail}` JSON，前端从 `err.cause` 读取。
6. **服务启动钩子**：`PrismService.ServiceStartup` 自动调用 `WatchMods()`，开始监听所有已关联项目的 `mods` 与实例 `mods/.index`（服务关闭时自动停止）。
7. **前端资源**：`//go:embed all:frontend/dist` 嵌入 Vite 构建产物，由 `AssetFileServerFS` 托管，窗口 URL 为 `/`。
8. **窗口参数**：1200×780（最小 940×620）、无边框（Frameless）、背景色 RGB(18,18,24)，标题栏由前端自绘。
9. `app.Run()` 进入 Wails 主循环。

## 4. 服务层职责边界

| 服务 | 职责 | 方法数 | 源码 |
| --- | --- | --- | --- |
| `EnvService` | packwiz / Prism 工具检测、用户 PATH 写入、CurseForge API Key | 5 | envservice.go, detect.go |
| `PackwizService` | packwiz 项目导入/解析/移除、refresh、CF 版本、更新检查/应用 | 8 | packwizservice.go, curseforceservice.go |
| `PrismService` | Prism 实例定位/扫描/创建、项目关联、目录同步与 meta 同步、mods 双端目录监听 | 26 | prismservice.go, links.go, meta.go, mods_watch.go, linked.go |

**命名与绑定规则**：

- Go 结构体上的导出方法（大写开头）会被 Wails 自动绑定为前端 `Bindings.<Service>.<Method>()`。
- Go 结构体字段使用 `json:"snake_case"` 标签，前端收到 **snake_case** 键名；TS 绑定也保留 snake_case 属性。
- 方法签名约束：入参与返回值必须可序列化；`error` 作为最后一个返回值时转为前端 Promise rejection（详见 [通信契约](../contract/01-binding-and-error-protocol.md)）。

## 5. 数据持久化布局

| 文件 | 位置 | 内容 | 读写方式 |
| --- | --- | --- | --- |
| 全局配置 | `%AppData%\PackGradle\config.toml` | 工具路径、项目索引、Prism 实例目录、API Key | `ConfigManager`（互斥锁 + 原子写） |
| 项目级配置 | `<项目目录>\packgradle.toml` | 关联实例 ID、目录关联对、文件链接清单 | `WithProjectConfigLock`（按项目路径加锁）+ 原子写 |
| CF 版本缓存 | `<项目目录>\.cache\modversion.cache` | `"projectID:fileID" → {display_name,file_date,release_type,fetched_at}` | `curseforge.CfCacheStore` |
| meta 差异缓存 | `<项目目录>\.cache\metadiff.cache` | 上次差异计算结果（MetaDiff TOML） | `PrismService.MetaDiff` 计算时刷新 |

原子写实现：`appconfig.WriteTomlAtomic`（临时文件 + rename）。

## 6. 关键跨层约定

- **错误码唯一**：Go 端不产出用户可见文案，错误只携带 `err.*` 码 + 参数 + 底层 detail；文案由前端 i18n 渲染（65 个错误码见契约文档）。
- **容错哲学**：批量扫描/解析不因单项失败中断——`PackProject.Error`、`Instance.Error` 携带错误码 JSON 文本，列表照常返回。
- **并发安全**：全局配置 `sync.Mutex`；项目配置使用 `sync.Map` 按项目路径分锁；`CfCacheStore` 自带锁；批量 CF 请求并发上限 8。
- **路径安全**：所有来自前端的相对路径（目录/文件清单）必须经过 `validateRelDir` / `normalizeFileListStrict`，拒绝绝对路径、盘符与 `..` 越界。
- **子进程策略**：packwiz 以隐藏窗口执行，`refresh` 超时 5 分钟、`update/check` 超时 15 分钟，超时返回 `err.packwiz.timeout`。
