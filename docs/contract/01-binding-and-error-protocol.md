# 通信契约：Wails 绑定机制与错误协议

## 1. 总览

前后端不是 HTTP REST，而是 **Wails v3 本地 IPC**：

1. Go 侧在 `main.go` 用 `application.NewService(...)` 注册服务实例；
2. Wails 构建时分析 Go 源码，生成 TypeScript 绑定到 `frontend/bindings/`；
3. 前端调用绑定函数，底层为 `@wailsio/runtime` 的 `Call.ByID(callID, ...args)`，返回 `$CancellablePromise<T>`。

```
Go struct 导出方法
      │ wails3 generate bindings（-ts -i）
      ▼
frontend/bindings/packgradle/internal/service/{envservice,packwizservice,prismservice}.ts
      │ import { EnvService, PackwizService, PrismService } from './bindings/packgradle/internal/service'
      ▼
views / stores 调用  →  $Call.ByID →  本地 IPC  →  Go 方法
```

## 2. 生成物结构

| 文件 | 内容 |
| --- | --- |
| `bindings/packgradle/internal/service/index.ts` | 导出三个命名空间 + 服务级类型 |
| `bindings/packgradle/internal/service/{envservice,packwizservice,prismservice}.ts` | 每个服务一个模块，每方法一个导出函数 |
| `bindings/packgradle/internal/service/models.ts` | 服务包内定义的类型（`ToolInfo`、`PrismOverview`、`ModVersionResult`） |
| `bindings/packgradle/internal/packwiz/{index,models}.ts` | packwiz 包模型 |
| `bindings/packgradle/internal/prism/{index,models}.ts` | prism 包模型 |
| `bindings/github.com/wailsapp/wails/v3/internal/*` | Wails 内部事件辅助（自动生成） |

所有生成文件头部有 `DO NOT EDIT` 标识。生成命令（见 build/Taskfile.yml）：

```bash
wails3 generate bindings -f '' -clean=true -ts -i
# 生产/服务模式相应加 -tags server,production；混淆加 -obfuscated
```

## 3. 调用形态

```ts
import { EnvService } from './bindings/packgradle/internal/service'

const tools = await EnvService.Detect()          // Promise<ToolInfo[] | null>
try {
    await PrismService.LinkProject('MyPack', 'inst-1')
} catch (e) {
    errText(e)                                    // 解析 e.cause 的 err.* 码
}
```

- 每个绑定函数返回 `$CancellablePromise<T>`（Wails Runtime 类型，可当 Promise 使用，支持取消）。
- Go slice 序列化为 JSON 数组；生成类型对 slice 全部加 `| null`，前端需 `?? []` 兜底。
- Go `error` 返回值不会出现在 resolve 值里：**有 error 时 Promise reject**。

## 4. 命名与序列化规则

| 维度 | 规则 |
| --- | --- |
| 服务命名空间 | Go 结构体名原样（`EnvService` / `PackwizService` / `PrismService`） |
| 方法名 | Go 导出方法名原样（`Detect` / `ImportProject` / `CreateAllLinks` …） |
| 参数名 | Go 参数名原样（`projectName`、`modID`…），位置参数调用 |
| 字段名 | Go 结构体 `json:"snake_case"` 标签 → JSON/TS 均为 **snake_case** |
| 数字 | `int64`（CF project/file ID）在 JSON 中为 number（当前值域安全） |
| 空 slice | JSON `null`，TS 类型 `T[] \| null` |
| Go 类型 | JS/TS：string→string、bool→boolean、int→number、struct→object、slice→array\|null |

## 5. 错误协议（核心契约）

### 5.1 后端结构化错误

`internal/errs.AppError`：

```go
type AppError struct {
    Code   string   `json:"code"`             // 前端翻译键，如 err.proj.not_found
    Args   []string `json:"args,omitempty"`   // i18n 插值参数
    Detail string   `json:"detail,omitempty"` // 底层错误透传文本
}
```

`AppError.Error()` 返回与 `MarshalError` 一致的 JSON 文本，因此无论错误走哪条路径，结构一致。

### 5.2 前端可见的两条错误路径

| 路径 | 后端动作 | 前端位置 | 前端处理 |
| --- | --- | --- | --- |
| **Promise reject** | 方法返回 `error`；`main.go` 的 `marshalError` 把 AppError 序列化后交 Wails | `e.cause`（对象） | `errorCode(e)` / `errText(e)` |
| **数据字段** | 容错结果或 CLI 结果把 `AppError.Error()` 文本放进字段（`PackProject.Error`、`Instance.Error`、`RefreshResult.Output`、`PrismOverview.locate_error`、`ModUpdateInfo.Error`、`LinkResult.Detail` 等） | 字段（JSON 字符串） | `displayText(s)` / `parseAppErr(s)` |

前端判定规则：解析后对象含 `code` 且 `code` 以 `err.` 开头才视为结构化错误；否则原样显示（如 packwiz CLI 原生输出）。

### 5.3 文案渲染

- `t(code, args)` → 语言文件 `locales/zh-CN.json` 中扁平键；有 `detail` 时追加 `: {detail}`。
- 后端 **不得** 产出用户可见中文文案（除 CLI 原生输出与日志）。
- 新增后端错误码的完整链路：Go `errs.New(...)` → 前端语言文件补 `err.*` 键 → `utils/errors.ts` 自动可渲染。

### 5.4 错误码字典（65 个）

全部在 `frontend/src/locales/zh-CN.json` 中。

**配置 / 文件（`err.config.*`, `err.file.*`, `err.toml.*`）**

| code | 含义（参数） |
| --- | --- |
| `err.config.user_dir` | 无法获取用户配置目录 |
| `err.config.mkdir` | 无法创建配置目录 `{0}` |
| `err.config.read` | 读取配置文件失败 |
| `err.config.unknown_tool` | 未知工具 `{0}` |
| `err.file.mkdir` | 创建目录 `{0}` 失败 |
| `err.file.write` | 写入 `{0}` 失败 |
| `err.file.serialize` | 序列化 `{0}` 失败 |
| `err.file.save` | 保存 `{0}` 失败 |
| `err.toml.parse` | 解析 `{0}` 失败 |
| `err.toml.missing_name` | `{0}` 缺少 name 字段 |
| `err.toml.read` | 读取 `{0}` 失败 |
| `err.toml.mods_dir` | 读取 mods 目录失败 |
| `err.toml.invalid_index` | pack.toml 中 index.file 路径不合法 `{0}` |

**CurseForge（`err.cf.*`）**

| code | 含义（参数） |
| --- | --- |
| `err.cf.request` | 请求 CurseForge 失败 |
| `err.cf.unauthorized` | API Key 无效/未授权（HTTP `{0}`）→ 触发全局引导框 |
| `err.cf.not_found` | 文件不存在（HTTP `{0}`） |
| `err.cf.http` | 其他 HTTP 错误（`{0}`） |
| `err.cf.parse_response` | 解析响应失败 |
| `err.cf.api_key_missing` | 未配置 API Key → 触发全局引导框 |
| `err.cf.not_cf_source` | `{0}` 非 CurseForge 源 |
| `err.cf.no_cf_mods` | 项目无 CF 源 mod |

**环境 / 工具（`err.env.*`, `err.tool.*`, `err.packwiz.*`, `err.update.*`）**

| code | 含义（参数） |
| --- | --- |
| `err.env.read_user_path` | 读取用户 PATH 失败 |
| `err.env.write_user_path` | 写入用户 PATH 失败 |
| `err.tool.packwiz_not_found` | 未找到 packwiz |
| `err.packwiz.timeout` | packwiz 执行超时 |
| `err.update.pinned` | 版本已固定，跳过更新 |
| `err.update.no_updater` | 无支持的更新源 |

**项目 / mod（`err.proj.*`, `err.mod.*`）**

| code | 含义（参数） |
| --- | --- |
| `err.proj.not_found` | 未找到项目 `{0}` |
| `err.proj.invalid_path` | 无法解析路径 `{0}` |
| `err.mod.not_found` | 未找到 mod `{0}` |

**Prism（`err.prism.*`）**

| code | 含义（参数） |
| --- | --- |
| `err.prism.not_found` | 未找到 Prism Launcher |
| `err.prism.cfg_read` | 读取 prismlauncher.cfg 失败 |
| `err.prism.mmcpack_read` | 读取 mmc-pack.json 失败 |
| `err.prism.mmcpack_parse` | 解析 mmc-pack.json 失败 |
| `err.prism.instances_dir_not_found` | 无法定位实例目录 `{0}` |
| `err.prism.scan_failed` | 扫描实例目录 `{0}` 失败 |
| `err.prism.path_invalid` | 路径不存在或不是目录 `{0}` |
| `err.prism.instance_not_found` | 未找到实例 `{0}` |
| `err.prism.create_invalid_name` | 实例名不合法 `{0}` |
| `err.prism.create_invalid_version` | 未填写 Minecraft 版本 |
| `err.prism.create_invalid_loader_version` | 未填写加载器 `{0}` 版本 |
| `err.prism.instance_exists` | 实例已存在 `{0}` |
| `err.prism.create_failed` | 创建实例失败 |
| `err.prism.loader_unsupported` | 不支持的加载器 `{0}` |

**链接 / 同步（`err.link.*`, `err.junction.*`, `err.sync.*`）**

| code | 含义（参数） |
| --- | --- |
| `err.link.not_found` | 未找到关联 `{0}` |
| `err.link.file_exists` | 实例侧已存在文件 `{0}` |
| `err.link.hardlink_failed` | 创建硬链接失败 |
| `err.junction.create` | 创建 Junction 失败 |
| `err.junction.link_occupied` | 实例侧已存在真实内容 `{0}` |
| `err.junction.wrong_target` | 实例侧链接指向其他目标 `{0}` |
| `err.junction.target_missing` | 目标目录不存在 `{0}` |
| `err.sync.manual_required` | 实例侧已有内容，需手动链接 `{0}` |
| `err.sync.copy_failed` | 复制实例侧内容到项目目录失败 |
| `err.sync.remove_failed` | 删除实例侧目录失败 |
| `err.sync.dir_conflict` | 实例侧已有同名文件（非目录）`{0}` |
| `err.sync.invalid_mode` | 不支持的同步模式 `{0}` |
| `err.sync.dir_not_linked` | 目录未关联 `{0}` |
| `err.sync.dir_is_junction` | 实例侧目录已是整目录链接 `{0}` |
| `err.sync.file_conflict` | 项目侧已有同名文件，跳过 `{0}` |
| `err.sync.move_failed` | 移动文件到项目目录失败 |
| `err.sync.cross_volume` | 跨卷无法建硬链接 `{0}` |
| `err.sync.empty_dir` | 目录名不能为空 |
| `err.sync.invalid_dir` | 目录参数不合法 `{0}` |
| `err.sync.invalid_file` | 文件参数不合法 `{0}` |
| `err.sync.dir_not_exists` | 项目目录不存在 `{0}` |

## 6. Wails Runtime 直接调用（非服务绑定）

除服务绑定外，前端还直接使用 `@wailsio/runtime`：

| 能力 | 用法 |
| --- | --- |
| 窗口控制 | `Window.IsMaximised()/Minimise()/ToggleMaximise()/Close()` |
| 窗口事件 | `Events.On(Events.Types.Windows.WindowMaximise, cb)`、`WindowUnMaximise` |
| mods 监听事件 | `Events.On('packgradle:mods-diff', cb)`，数据为 `prism.ModsWatchEvent`（类型已由生成绑定写入 `Events.CustomEvents`） |
| 系统对话框 | `Dialogs.OpenFile`（经 `src/utils/dialogs.ts` 封装：pickPackToml / pickToolPath / pickDirectory，取消返回 null）；消息类对话框（Question 等）在构建版有挂起 bug，确认/询问一律用 Vuetify 对话框 |
| 底层调用 | 生成绑定内部 `Call.ByID(callID, ...args)` |

`Events.On` 返回取消订阅函数，组件卸载时应调用（App.vue 已在 `onBeforeUnmount` 处理）。
