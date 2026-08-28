> ⚠️ **已归档（2026-08-28）**：本文描述的是旧版前端（工作区架构之前的旧信息架构）。后续设计与决策请以 [工作区 UX 原型设计](../../frontend/05-workspace-ux-prototype.md)、[工作区交互原型](../../frontend/workspace-ux-prototype.html) 与 [架构文档](../../architecture/) 为准，本文仅作历史参考，不再维护。

# 前端 Stores、工具函数与系统对话框

## 1. Stores（模块级共享状态）

不使用 Pinia；每个 store 是普通 TS 模块，用 `ref/computed` 导出状态与函数。

### 1.1 projects.ts —— 项目列表缓存

文件：`src/stores/projects.ts`

| 导出 | 类型 | 说明 |
| --- | --- | --- |
| `projects` | `Ref<PackProject[]>` | 项目列表共享缓存（工作台/列表/详情/联动共用） |
| `loaded` | `Ref<boolean>` | 是否已加载 |
| `projectsVersion` | `Ref<number>` | 跨视图数据版本号；数据变更（如 meta 拉取）后 `bumpProjectsVersion()` 递增 |
| `bumpProjectsVersion()` | `() => void` | 版本号 +1，相关视图 watch 后重载 |
| `loadProjects(force?)` | `Promise<PackProject[]>` | 已加载复用缓存；并发共享 `inflight`；`force` 在请求进行中时排期补刷 |
| `setProjects(list)` | `void` | 用服务端返回值（如 Import/Remove 返回的列表）直接更新缓存 |
| `invalidateProjects()` | `void` | 使缓存失效，下次重新拉取 |
| `findProject(name)` | `PackProject \| undefined` | 缓存中按名查找（详情/联动页用） |
| `useProjects()` | 组合式包装 | 返回以上全部 |

### 1.2 instances.ts —— Prism Overview 缓存

文件：`src/stores/instances.ts`

| 导出 | 类型 | 说明 |
| --- | --- | --- |
| `overview` | `Ref<PrismOverview \| null>` | 工作台与联动页共用；一次装载实例目录 + 实例列表 + 关联视图 |
| `loadOverview(force?)` | `Promise<PrismOverview>` | 同上并发共享与 force 排期逻辑 |
| `invalidateOverview()` | `void` | 失效缓存 |
| `useInstances()` | 组合式包装 | `{ overview, loadOverview, invalidateOverview }` |

### 1.3 env.ts —— 工具检测与 API Key 缓存

文件：`src/stores/env.ts`

| 导出 | 类型 | 说明 |
| --- | --- | --- |
| `tools` | `Ref<ToolInfo[]>` | 工具检测结果 |
| `apiKey` | `Ref<string>` | CF API Key |
| `loadTools(force?)` | `Promise<ToolInfo[]>` | 拉取检测结果（并发共享） |
| `setTools(list)` | `void` | 用服务端返回（SetToolPath/Configure）更新 |
| `loadApiKey()` | `Promise<string>` | 拉取 API Key |
| `saveApiKey(key)` | `Promise<void>` | 保存成功后才写缓存（避免脏值） |
| `setApiKeyValue(v)` | `void` | 直接设缓存值 |
| `useEnv()` | 组合式包装 | 返回全部 |

### 1.4 taskCenter.ts —— 任务中心

文件：`src/stores/taskCenter.ts`

核心类型：

```ts
type TaskStatus = 'running' | 'success' | 'warning' | 'error'
type TaskKind = 'refresh' | 'fetch' | 'update' | 'meta' | 'link'
             | 'import' | 'remove' | 'config' | 'other'
interface TaskItem {
    id: number
    title: string
    kind: TaskKind
    status: TaskStatus
    progress: number        // 0~1；单次绑定调用无中间上报，完成 = 1
    stepText: string        // 当前步骤文本（执行方 report 上报；单次调用为空）
    resultText: string      // 完成后的结果摘要
    output: string          // 可展开的详细输出（CLI 输出/错误）
    startedAt: Date
    finishedAt: Date | null
    seen: boolean           // 用户是否已看
}
```

| 导出 | 类型 | 说明 |
| --- | --- | --- |
| `taskList` | `ComputedRef<TaskItem[]>` | 任务列表（新任务在前） |
| `runningCount` | `ComputedRef<number>` | 进行中数量 |
| `unseenCount` | `ComputedRef<number>` | 未查看的已完成数量（bell 角标） |
| `runTask(opts)` | `Promise<string \| null>` | 执行任务；自动完成/失败置状态，失败返回 null |
| `markAllSeen()` | `void` | 全部标已读（抽屉打开时调用） |
| `clearFinished()` | `void` | 清理非 running 任务 |
| `useTaskCenter()` | 组合式包装 | 返回全部 |

`RunTaskOptions`：

```ts
{
    title: string
    kind: TaskKind
    run: (report: (fraction: number, stepText: string) => void) => Promise<string>
    output?: () => string                       // 完成后追加详细输出
    warn?: (resultText: string) => boolean      // true → warning 状态
}
```

### 1.5 ui.ts —— 全局通知队列

| 导出 | 类型 | 说明 |
| --- | --- | --- |
| `showSnackbar(message, tone?, timeout?)` | `void` | tone ∈ info/success/warning/error，默认 info/4200ms；连续消息排队顺序展示 |
| `dismissSnackbar()` | `void` | 关闭当前 |
| `snackbar / snackbarMsg / snackbarTone / snackbarTimeout` | `Ref` | App.vue 渲染用 |
| `useUi()` | 组合式包装 | 返回全部 |

### 1.6 apiKeyGuide.ts —— API Key 错误分流

| 导出 | 类型 | 说明 |
| --- | --- | --- |
| `apiKeyDialog` | `Ref<boolean>` | 应用级引导弹窗开关（App.vue 渲染） |
| `handleApiKeyError(e)` | `void` | `err.cf.api_key_missing` / `err.cf.unauthorized` → 弹引导框；其余错误 snackbar |
| `goConfigApiKey()` | `void` | 关闭弹窗并跳 `/settings` |
| `useApiKeyGuide()` | 组合式包装 | 返回全部 |

## 2. 数据层：bindings 直连（mock 层已移除）

原型阶段的 `src/stores/mock.ts` 已删除。stores/views/components 直接调用
`frontend/bindings` 生成的服务函数（`PackwizService` / `PrismService` / `EnvService`），
错误经 `utils/errors.ts` 渲染、写操作经任务中心。

调用约定（与契约文档一致）：

- Go slice → `T[] | null`，调用处统一 `?? []` 兜底；
- 方法返回 `error` 时 Promise reject，`e.cause` 为 AppError 对象，`errText(e)` 渲染；
- 长任务（refresh/update/meta/link 等）没有中间进度上报：`runTask` 的 `report` 不必传，
  任务抽屉对 `progress <= 0` 的运行中任务显示不确定进度条；
- 涉及 CurseForge 的调用（`FetchModVersion` / `FetchAllModVersions` / `CheckUpdates`）
  失败时用 `handleApiKeyError(e)` 分流：Key 缺失/无效弹全局引导，其余 snackbar。

原「mock ↔ 绑定映射表」中无后端对应的两项已落地：

| 原 mock 函数 | 现真实绑定 | 说明 |
| --- | --- | --- |
| `mockConfigExists` | `EnvService.ConfigExists` | 判断 `%AppData%\PackGradle\config.toml` 是否已存在（首次运行检测） |
| `mockMarkConfigCreated` | `EnvService.MarkConfigCreated` | 引导完成/跳过后落盘 config.toml；注意 `Detect()` 检测到工具路径时也会写盘（同样终结首次状态） |

后端存在但前端暂未使用的绑定：`PrismService.ListDirFiles`、`SetDirLinkFiles`、
`InstancesDir`、`ListInstances`、`GetLinks`（后三个功能被 `Overview` 聚合替代）、`WatchMods`
（监听由后端 `ServiceStartup` 自动开启，前端只订阅 `packgradle:mods-diff` 事件，见 DevView）。

## 3. utils 工具函数

### 3.1 errors.ts —— 错误码渲染

| 导出 | 签名 | 说明 |
| --- | --- | --- |
| `AppErrorCause` | `{code?, args?, detail?}` | Go `errs.AppError` 的前端形态 |
| `errorCode(e)` | `string \| undefined` | 从 Wails 调用错误 `e.cause` 提取 `err.*` 码 |
| `errText(e)` | `string` | 错误对象 → 用户可读文本；结构化错误按 i18n 渲染，非结构化原文 |
| `displayText(s)` | `string` | 数据字段中的文本（`RefreshResult.Output`、`PackProject.Error` 等）：`err.*` JSON → 翻译；其余原样 |
| `parseAppErr(v)` | `AppErrorCause \| undefined` | 解析 `locate_error` 等 JSON 文本字段 |

解析规则：`cause` 为对象或 JSON 字符串均可；有 `code` 且以 `err.` 开头才视为结构化错误。
渲染规则：`t(code, args)`，有 detail 时追加 `: detail`；缺失翻译键时 vue-i18n 返回键名本身（便于发现遗漏）。

### 3.2 dialogs.ts —— 系统文件/目录选择

封装 `@wailsio/runtime` 的 `Dialogs.OpenFile`（消息类对话框有挂起 bug，确认/询问一律用 Vuetify 对话框，见开发指南）。

| 导出 | 签名 | 说明 |
| --- | --- | --- |
| `pickPackToml()` | `Promise<string \| null>` | 选 pack.toml（标题 + 过滤器）；取消返回 null |
| `pickToolPath()` | `Promise<string \| null>` | 选工具路径（可执行文件或所在目录均可） |
| `pickDirectory(title?)` | `Promise<string \| null>` | 选目录 |

### 3.3 cf.ts —— CurseForge/加载器展示工具

| 导出 | 类型 | 说明 |
| --- | --- | --- |
| `loaderChips` | `Record<string, {label,color}>` | fabric/forge/neoforge/quilt/liteloader 标签与颜色 |
| `sideColors` | `Record<string,string>` | client/server/both 颜色（文案由 `side.*` 键渲染） |
| `isCfMod(mod)` | `boolean` | `cf_project_id>0 && cf_file_id>0` |
| `cfReleaseKey(t)` | `string` | 1→`cf.release.stable`，2→`cf.release.beta`，3→`cf.release.alpha` |
| `cfDateText(iso)` | `string` | ISO 时间 → `yyyy-MM-dd` |

## 4. i18n（src/i18n.ts + locales/zh-CN.json）

- Composition API：`legacy: false`，locale/fallback 均为 `zh-CN`。
- JSON 为 **扁平键**（键名含 `.`，如 `err.proj.not_found`），共 414 个键。
- 非组件模块通过 `import { t } from '../i18n'` 使用（`t(code, args)` 支持 `{0}` 插值）。
- 服务端只返回 `err.*` 码；新增后端错误码必须同步在 `zh-CN.json` 增加 `err.*` 文案。
- 产品专名不翻译：Fabric/Forge/NeoForge/Quilt/LiteLoader 在 `cf.ts` 硬编码。

## 5. 插件与全局样式

- `plugins/vuetify.ts`：dark/light 两套主题；默认 dark；组件默认 `VBtn rounded=lg`、`VCard rounded=xl elevation=0 border=sm`、`VChip rounded=md`、`VTextField/VSelect outlined + rounded=lg`。
- `assets/main.css`：应用级全局样式与 `--pg-*` 变量（拖拽区 `app-drag`/`app-no-drag`、滚动条、文本选择等）。
