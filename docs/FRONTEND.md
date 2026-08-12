# PackGradle 前端结构与操作逻辑（原型）

> 原型文档：记录前端目录结构与各页面操作逻辑（原型表格），供需求对齐与后续开发参考。
> 生成日期：2026-08-13。后端调用均经 Wails 生成的绑定（`frontend/bindings/packgradle/internal/`，勿手改），文案唯一来源 `src/locales/zh-CN.json`。

## 1. 目录结构

| 路径 | 职责 |
| --- | --- |
| `src/main.ts` | 应用入口：Vue + Vuetify（dark 主题）+ vue-i18n |
| `src/App.vue` | 根布局：顶栏 + 常驻导航抽屉 + `v-main` 三视图切换 |
| `src/nav.ts` | 视图切换状态：`currentView` ref（env/projects/prism）+ `navigate()`（无 vue-router） |
| `src/i18n.ts` | 全局 i18n（zh-CN），导出全局 `t` 供非组件模块使用 |
| `src/locales/zh-CN.json` | 全部界面文案（错误码 `err.*` 也在此翻译） |
| `src/composables/useSnackbar.ts` | 共享 snackbar 状态与 `show(msg)`（各视图自持一套实例） |
| `src/composables/useApiKeyGuide.ts` | CurseForge API Key 错误分流：Key 相关错误码弹引导框，其余走 snackbar |
| `src/utils/errors.ts` | 错误码 → 用户可读文本：解析 `e.cause` 与数据字段中的错误码 JSON 两条路径 |
| `src/utils/cf.ts` | CurseForge 展示工具：loader chip、side 颜色、releaseType/日期格式化、`isCfMod` |
| `src/views/EnvView.vue` | 环境页：packwiz / Prism Launcher 检测与路径配置、CF API Key |
| `src/views/ProjectsView.vue` | 项目页：导入/移除、packwiz refresh、CF 版本获取、更新检查与应用 |
| `src/views/PrismView.vue` | Prism 页：实例目录定位、项目↔实例关联、目录同步关联（junction/文件级） |
| `bindings/packgradle/internal/service/` | Wails 生成的 `EnvService` / `PackwizService` / `PrismService` 绑定 |

## 2. 共享机制

| 机制 | 说明 |
| --- | --- |
| 视图切换 | `nav.ts` 的 `currentView` ref，`navigate(key)` 可跨页跳转（如引导去配置 API Key） |
| 错误提示 | Go 端只返回错误码（`errs.AppError`）；`errors.ts` 统一解析两条路径：① Wails 调用异常的 `e.cause` ② 数据字段文本（`RefreshResult.Output` / `PackProject.Error` / `Errors[].Error`），非结构化文本（packwiz CLI 输出）原样返回 |
| API Key 分流 | `useApiKeyGuide.handleError(e, show)`：错误码为 `err.cf.api_key_missing` / `err.cf.unauthorized` 时弹 `apiKeyDialog`，点「去配置」→ `navigate('env')`；其余错误走 snackbar |
| 确认对话框 | 一律自定义 `v-dialog`（构建版 Wails 原生 `Dialogs.Question` 会挂起）；`Dialogs.OpenFile` 选择文件/目录可用，取消时以异常返回需静默忽略 |
| 加载状态 | 行级/全局 `loading` ref 绑定到按钮与卡片，防止重复提交 |

## 3. EnvView（环境配置）

| 操作 | UI 入口 | 前端逻辑 | 后端调用 | 反馈 |
| --- | --- | --- | --- | --- |
| 加载/刷新 | 进入页面 / 刷新按钮 | `load()`：检测并渲染工具卡片 | `EnvService.Detect()` | 卡片显示来源（`tool.source.*`）与 PATH 状态 chip；有工具未找到时弹引导框（会话内仅一次，`dismissed`） |
| 自动配置 PATH | 「自动配置」按钮 | 仅当存在已找到的工具时可用 | `EnvService.Configure()` | snackbar：新增 PATH 项 `env.pathConfigured` / 无变化 `env.pathNoChange`；返回后刷新卡片 |
| 浏览选择路径 | 输入框旁文件夹图标 | `Dialogs.OpenFile`（可选文件或目录），选中回填 `tool.path` | — | 取消选择静默忽略 |
| 保存工具路径 | 「保存」按钮 / 回车 | — | `EnvService.SetToolPath(name, path)` | snackbar `env.pathSaved`，返回最新工具列表 |
| 缺失工具弹窗保存 | 弹窗底部「保存」 | 逐个 `savePath()` 后关闭弹窗 | 同上（逐工具） | 同 `env.pathSaved` |
| 配置 API Key | API Key 卡片「保存」/ 回车 | 明文/密码切换显示；空值=清除 | `EnvService.SetApiKey(key)` | snackbar：`env.apiKeySaved` / `env.apiKeyCleared` |
| 读取 API Key | 页面加载 | `onMounted` 回填输入框 | `EnvService.GetApiKey()` | 已配置 chip 状态 |

## 4. ProjectsView（项目管理）

| 操作 | UI 入口 | 前端逻辑 | 后端调用 | 反馈 |
| --- | --- | --- | --- | --- |
| 加载列表 | 进入页面 / 刷新按钮 | — | `PackwizService.ListProjects()` | 项目卡片：loader / MC 版本 / mod 数；解析失败显示错误 chip 与原因 |
| 导入项目 | 「导入项目」按钮 | `Dialogs.OpenFile` 过滤 `pack.toml`；成功后展开该卡片 | `PackwizService.ImportProject(packTomlPath)` | snackbar `projects.imported`；失败 `projects.importFailed` |
| 移除项目 | 行内删除图标 | 先弹自定义确认框 | `PackwizService.RemoveProject(name)` | 刷新列表；失败 `projects.removeFailed` |
| packwiz refresh | 行内刷新图标 | — | `PackwizService.RefreshProject(name)` | 命令输出弹窗（成功显示 `packwiz refresh` 成功文案）；随后刷新列表 |
| 获取单个 mod 版本 | mod 行下载图标（仅 CF 源 mod 显示） | 成功后就地 `Object.assign` 更新该行（不整表刷新） | `PackwizService.FetchModVersion(project, modId)` | snackbar `projects.versionFetched`；失败走 `handleError` 分流 |
| 批量获取版本 | 行内云下载图标 | 单行获取按钮在此期间禁用 | `PackwizService.FetchAllModVersions(project)` | snackbar `projects.versionsFetched`（成功数/总数）；随后刷新列表 |
| 检查更新 | 行内更新图标 | 检查期间该行按钮 loading | `PackwizService.CheckUpdates(project)` | 检查结果弹窗：失败 alert / 全最新 / 可更新列表 / 失败跳过列表 / CLI 输出；snackbar `projects.checkDone(WithErrors)` |
| 应用全部更新 | 检查结果弹窗「应用全部更新」 | 仅在存在可更新项时显示 | `PackwizService.UpdateMods(project, '')` | 命令输出弹窗，关闭检查弹窗，刷新列表 |

## 5. PrismView（Prism 联动）

| 模块 | 操作 | UI 入口 | 前端逻辑 | 后端调用 | 反馈 |
| --- | --- | --- | --- | --- | --- |
| 实例目录 | 定位 | 进入页面 | `load()`：先取实例目录，再列实例 | `PrismService.InstancesDir()` / `ListInstances()` | 定位失败显示手动输入引导；错误码 `err.prism.not_found` 时附「去配置」按钮 → `navigate('env')` |
| 实例目录 | 手动指定目录 | 浏览按钮 / 保存 / 清除 | 空串=恢复自动定位 | `PrismService.SetInstancesPath(path)` | snackbar `prism.manualPathSaved` / `prism.manualPathCleared`；保存后重试定位 |
| 实例列表 | 查看 | — | 渲染实例名/路径/分组/loader/MC 版本 | `PrismService.ListInstances()` | 解析失败实例显示错误 chip 与原因 |
| 项目↔实例关联 | 新建关联 | 「关联项目」按钮 | 打开前刷新项目列表（过滤解析失败项）；选项目自动匹配同名实例（不区分大小写） | `PrismService.LinkProject(project, instanceId)` | snackbar `prism.linkCreated`；刷新关联列表 |
| 项目↔实例关联 | 程序创建实例 | 关联弹窗「程序创建」 | 创建后重扫实例列表并选中新实例 | `PrismService.CreateInstance(project)` | snackbar `prism.instanceCreated` |
| 项目↔实例关联 | 解除关联 | 关联行「解除」 | — | `PrismService.UnlinkProject(project)` | snackbar `prism.linkRemoved` |
| 目录同步关联 | 打开管理框 | 关联行「目录关联」 | 载入已关联目录与候选目录（已添加的剔除） | `PrismService.ListDirLinks` / `ListProjectDirs` | 目录行显示模式 chip（junction/文件级）与目标 `→ minecraft/<instance_dir>` |
| 目录同步关联 | 添加目录 | 候选下拉 + 「添加」 | — | `PrismService.AddDirLink(project, dir)` | snackbar `prism.dirLinkAdded`；刷新 |
| 目录同步关联 | 移除目录 | 目录行「移除」 | — | `PrismService.RemoveDirLink(project, dir)` | snackbar `prism.dirLinkRemoved`；刷新 |
| 目录同步关联 | 一键关联 | 「一键关联」按钮 | 先查 `.pgignore`：缺失则弹自定义询问框（生成默认规则或跳过） | `HasPGIgnore` → `EnsurePGIgnore`（可选）→ `CreateAllLinks(project)` | 结果列表按状态 chip（linked/existing/manual/skipped/error）；snackbar `prism.linkAllDone(WithManual)` 统计 |
| 目录同步关联 | 手动链接 | 目录行「手动链接」 | 弹确认框：实例侧已有内容时复制并入项目目录再建链 | `PrismService.ManualLinkDir(project, dir)` | snackbar `prism.manualLinkDone`；`status=error` 时展示 detail |
| 目录同步关联 | 切文件级同步 | 目录行「切文件级」 | — | `PrismService.SetDirLinkMode(project, dir, 'files')` | snackbar `prism.dirLinkModeFiles` |
| 目录同步关联 | 切回整目录 | 目录行「切回整目录」 | — | `PrismService.SetDirLinkMode(project, dir, '')` | snackbar `prism.dirLinkModeJunction` |
| 文件级同步 | 选择文件 | 目录行「选文件」 | 从实例侧读取文件列表 + 当前已勾选；支持全选/清空/逐项勾选 | `PrismService.ListInstanceDirFiles(project, dir)` | 文件选择弹窗 |
| 文件级同步 | 保存勾选 | 弹窗「保存」 | 勾选文件移动到项目目录后硬链接同步 | `PrismService.SelectInstanceFiles(project, dir, files)` | snackbar `prism.dirLinkSelectDone`（成功/跳过）；关闭弹窗并刷新 |

## 6. 确认/引导对话框清单

| 对话框 | 所属视图 | 触发条件 | 选项与结果 |
| --- | --- | --- | --- |
| 缺失工具引导 | EnvView | 检测后存在未找到的工具（会话内仅一次） | 取消：本次会话不再提示；保存：逐个保存路径 |
| 移除项目确认 | ProjectsView | 点击删除图标 | 取消 / 确认删除并刷新列表 |
| 更新检查结果 | ProjectsView | 点击检查更新图标 | 查看结果；有可更新项时「应用全部更新」 |
| 命令输出 | ProjectsView | refresh / 应用更新完成 | 展示 packwiz CLI 输出（可滚动 `<pre>`） |
| API Key 引导 | ProjectsView（useApiKeyGuide） | CF 版本获取遇 `err.cf.api_key_missing` / `err.cf.unauthorized` | 关闭 / 「去配置」跳转环境页 |
| 关联项目 | PrismView | 点击「关联项目」 | 选择项目+实例；支持程序创建实例 |
| .pgignore 询问 | PrismView | 一键关联时项目无 `.pgignore` | 取消 / 跳过直接关联 / 生成默认规则后关联 |
| 手动链接确认 | PrismView | 目录行点击「手动链接」 | 取消 / 确认复制并入并建链 |
| 文件级同步选择 | PrismView | 目录行点击「选文件」 | 全选/清空/勾选后保存 |

## 7. 错误处理路径

| 路径 | 场景 | 处理 |
| --- | --- | --- |
| `errText(e)` | Wails 调用异常 | 解析 `e.cause` 中的错误码 JSON → i18n 翻译（缺失键返回键名便于发现遗漏）；非结构化错误取 message |
| `displayText(s)` | 数据字段中的错误文本 | 错误码 JSON → 翻译；packwiz CLI 输出等原样展示 |
| `handleError(e, show)` | CF 版本获取 | API Key 错误码弹引导框，其余 snackbar |

## 8. 注意点（原型阶段）

- `refreshProject` 与 `openFileSelect`、`openDirLinks`、`doLinkAll` 的 `HasPGIgnore` 查询缺少 `catch`：异常时无用户提示（unhandled rejection），原型阶段待补。
- 单 mod 版本获取成功后为就地更新行数据，不触发整表刷新；批量获取则整表刷新。
- 前端所有确认类交互已规避 Wails 原生 `Dialogs.Question`（构建版挂起），仅 `Dialogs.OpenFile` 可用。
