# PackGradle 前端结构与操作逻辑

> 结构文档：记录前端目录结构、路由与各页面操作逻辑，供需求对齐与后续开发参考。
> 更新日期：2026-08-15（路由化重构版）。后端调用均经 Wails 生成的绑定（frontend/bindings/packgradle/internal/，勿手改），
> 文案唯一来源 src/locales/zh-CN.json。关联文档：docs/REQUIREMENTS.md（需求状态与验收标准）。

## 1. 目录结构

| 路径 | 职责 |
| --- | --- |
| src/main.ts | 应用入口：Vue + Vuetify（dark 主题）+ vue-i18n + vue-router |
| src/App.vue | 根布局：顶栏（页面标题 + Wails 拖拽区）+ rail 导航抽屉（路由 meta 驱动）+ 全局 snackbar 与 API Key 引导弹窗 |
| src/router/index.ts | 路由表（hash 历史，适配 Wails 静态资产服务器）；侧栏导航项由路由 meta（titleKey/icon）生成 |
| src/plugins/vuetify.ts | 主题（深色石板底 + 祖母绿主色）与组件默认值（圆角描边卡片、outlined 输入框） |
| src/stores/ | 模块级共享状态（无 Pinia）：projects（项目缓存 + projectsVersion 跨视图版本号）、instances（Prism Overview 缓存）、env（工具检测 + API Key）、apiKeyGuide（API Key 错误分流）、ui（全局 snackbar） |
| src/components/common/ | 通用组件：PageHeader / ConfirmDialog（通用确认，替代 Wails 原生 Question）/ OutputDialog（CLI 输出）/ EmptyState |
| src/components/projects/ | 项目域：ModsTable（mod 表格 + 搜索 + side 过滤）、CheckUpdatesDialog（更新检查 + 应用全部 + 单 mod 更新） |
| src/components/prism/ | Prism 域：LinkDialog（关联 + 程序创建实例）、DirLinksDialog（目录同步 + 一键关联 + .pgignore 引导 + 手动链接 + 模式切换）、FileSelectDialog（文件级同步选择）、MetaDiffDialog（差异三区 + 单 mod 推送/拉取） |
| src/views/ | 页面（路由懒加载）：Dashboard / Projects / ProjectDetail / Instances / Settings |
| src/utils/ | errors.ts（错误码渲染）、cf.ts（loader chip / side 颜色 / 日期与发布类型） |
| src/locales/zh-CN.json | 全部界面文案（错误码 err.* 也在此翻译；缺失键返回键名便于发现遗漏） |
| bindings/packgradle/internal/service/ | Wails 生成的 EnvService / PackwizService / PrismService 绑定（37 方法，本次重构未改动） |

## 2. 路由与共享机制

| 路由 | 页面 | 说明 |
| --- | --- | --- |
| / | 工作台 | 环境健康卡 + 快速开始清单 + 项目/关联概览（数据全部来自共享缓存） |
| /projects | 项目列表 | 卡片 + 搜索 + 溢出菜单；keep-alive 保持存活（返回详情后保留搜索/滚动状态） |
| /projects/:name | 项目详情 | 项目信息 + mod 管理（搜索 / side 过滤 / 版本获取）+ 刷新与更新检查；URL 参数为项目名 |
| /instances | Prism 联动 | 实例目录定位 + 关联列表（meta 推送/拉取/差异 + 目录同步）+ 实例列表 |
| /settings | 设置 | packwiz / Prism Launcher 检测与 PATH 配置 + CurseForge API Key |
| 兜底 | — | 未匹配路径重定向到 / |

| 机制 | 说明 |
| --- | --- |
| 路由 | vue-router hash 历史：Wails 用 AssetFileServerFS 托管静态资源，hash 模式深链/刷新不 404；页面懒加载按视图拆 chunk |
| 共享缓存 | stores/projects（ListProjects 结果缓存，工作台/列表/详情/联动共用，并发共享同一请求）、stores/instances（Prism Overview 一次返回实例+关联）、stores/env（工具检测 + API Key） |
| 跨视图刷新 | projectsVersion ref：meta 拉取改变项目 mods 后 bumpProjectsVersion() + invalidateProjects()；ProjectsView / ProjectDetailView watch 后强制重载 |
| 全局反馈 | stores/ui 单例 snackbar（App.vue 渲染，视图不各自持有一套）；stores/apiKeyGuide 应用级 API Key 引导弹窗，错误码 err.cf.api_key_missing / err.cf.unauthorized 弹引导，其余走 snackbar |
| 错误契约 | Go 端只出错误码；errors.ts 双路径解析（e.cause 与数据字段文本），非结构化文本（packwiz CLI 输出）原样返回 |
| 确认对话框 | 一律自定义 v-dialog（构建版 Wails 原生 Question 会挂起）；Dialogs.OpenFile 可用，取消以异常返回需静默忽略 |

## 3. 工作台（/）

| 区块 | 数据来源 | 交互 |
| --- | --- | --- |
| 快捷操作 | — | 导入 pack.toml（文件对话框 → ImportProject → 跳转详情页）、去关联实例、环境设置 |
| 快速开始清单 | Detect + API Key + 项目缓存 + Overview | 5 步工作流（packwiz → Prism → API Key → 项目 → 关联），未完成项点击跳转对应页，进度条 + n/m 计数 |
| 环境健康卡 | 同上 | packwiz / Prism / API Key / 实例数 四卡，展示检测来源或状态，点击跳转设置或联动页 |
| 我的项目 | projects 缓存 | 前 5 个项目（loader / mod 数），点击进详情，查看全部 → /projects |
| 关联概览 | Overview.links | 前 5 条关联（项目 → 实例 + 有效性），查看全部 → /instances |

数据装载：loadTools / loadApiKey / loadProjects / loadOverview 四个既有调用 Promise.allSettled 并发执行，个别失败不影响其余展示。

## 4. 项目列表（/projects）与项目详情（/projects/:name）

| 操作 | UI 入口 | 前端逻辑 | 后端调用 | 反馈 |
| --- | --- | --- | --- | --- |
| 导入项目 | 页头「导入 pack.toml」 | 文件对话框过滤 pack.toml；成功后跳转详情页 | PackwizService.ImportProject | snackbar projects.imported；失败 projects.importFailed |
| 搜索 | 搜索框 | 按名称过滤（本地） | — | 无匹配显示 projects.noMatch |
| 打开详情 | 卡片点击 /「打开详情」 | 路由跳转 /projects/:name | — | — |
| packwiz refresh | 卡片溢出菜单 / 详情页按钮 | — | PackwizService.RefreshProject | OutputDialog 展示 CLI 输出；随后刷新 |
| 批量获取版本 | 同上 | 单项目互斥 loading | PackwizService.FetchAllModVersions | snackbar 成功数/总数；API Key 错误走应用级引导 |
| 检查更新 | 卡片溢出菜单 / 详情页按钮 | CheckUpdatesDialog 打开即检查 | PackwizService.CheckUpdates | 结果列表（可更新 / 失败跳过 / 全最新）+ CLI 输出 |
| 应用全部更新 | 更新对话框 | 应用后自动重查刷新列表 | PackwizService.UpdateMods(name, '') | 输出内嵌展示；emit changed → 父级重载 |
| 单 mod 更新 | 更新对话框每行「更新」 | 逐行 loading，更新后自动重查 | PackwizService.UpdateMods(name, modName) | snackbar projects.updateOneDone（打通原 GAP-3 死路径） |
| 移除项目 | 溢出菜单 / 详情页 | ConfirmDialog 确认（仅移除注册表，不动磁盘） | PackwizService.RemoveProject | snackbar projects.removed；详情页移除后返回列表 |
| mod 管理 | 详情页 | ModsTable：搜索 + side 过滤（全部/客户端/服务端/通用）+ 版本列（本地优先，CF 缓存回填）+ 单 mod 获取版本 | PackwizService.FetchModVersion | 成功就地 Object.assign 更新行 |

详情页路由参数为项目名：挂载时确保缓存就绪（未命中则强制重载），项目不存在显示 EmptyState 引导返回；projectsVersion 变更自动重载。

## 5. Prism 联动（/instances）

### 5.1 实例目录与实例列表

| 操作 | UI 入口 | 前端逻辑 | 后端调用 | 反馈 |
| --- | --- | --- | --- | --- |
| 定位 | 进入页面 / 刷新 | loadOverview（一次返回实例目录 + 实例 + 关联） | PrismService.Overview | 定位失败显示原因；err.prism.not_found 附「去配置」→ /settings |
| 手动指定目录 | 浏览 / 保存 / 恢复自动检测 | 空串 = 自动定位 | PrismService.SetInstancesPath | snackbar；保存后强制重载 Overview |
| 实例列表 | 卡片网格 | 名称/分组/加载器/MC/路径，解析失败实例内嵌错误 | （Overview.instances） | 错误 chip + 原因 |

### 5.2 关联与 meta（关联列表为工作区核心，子组件拆分）

| 操作 | UI 入口 | 前端逻辑 | 后端调用 | 反馈 |
| --- | --- | --- | --- | --- |
| 新建关联 | 页头「关联项目」 | LinkDialog：打开前刷新项目缓存；选项目自动匹配同名实例；可基于项目信息创建实例 | PrismService.LinkProject / CreateInstance | snackbar；emit changed → 强制重载 Overview |
| 推送 meta | 关联行「推送 meta」 | 按项目互斥 loading | PrismService.PushMeta(project, '') | snackbar 数量 |
| 拉取 meta | 关联行「拉取 meta」 | ConfirmDialog 确认（覆盖同名 pw.toml） | PrismService.PullMeta(project, '') | snackbar；成功后自动 packwiz refresh → 重载 Overview → bumpProjectsVersion + invalidateProjects；refresh 失败仅提示 |
| 查看差异 | 关联行「查看差异」 | MetaDiffDialog：打开即重算并刷新缓存；三区列表；单 mod 拉取需确认 | PrismService.MetaDiff / PullMeta / PushMeta | 单拉取后同样走 refresh + 缓存失效链并刷新差异 |
| 解除关联 | 关联行「解除」 | ConfirmDialog（提示目录链接将被删除） | PrismService.UnlinkProject | snackbar；重载 Overview |

### 5.3 目录同步关联（DirLinksDialog / FileSelectDialog）

| 操作 | UI 入口 | 前端逻辑 | 后端调用 | 反馈 |
| --- | --- | --- | --- | --- |
| 打开管理框 | 关联行「同步目录」 | 载入已关联目录 + 候选目录（已添加剔除），两个查询并发 | ListDirLinks + ListProjectDirs | 目录行：模式 chip（整目录/文件级）+ 目标路径 |
| 添加目录 | 候选下拉 + 添加 | — | PrismService.AddDirLink | snackbar；刷新 |
| 移除目录 | 目录行「移除」 | — | PrismService.RemoveDirLink | snackbar；刷新 |
| 一键关联 | 「一键关联全部」 | 先查 .pgignore：缺失弹三选询问（生成并继续 / 不生成直接关联 / 取消） | HasPGIgnore → EnsurePGIgnore（可选）→ CreateAllLinks | 结果列表按状态 chip；snackbar 统计 |
| 手动链接 | 目录行「手动链接」（整目录模式） | ConfirmDialog 确认复制并入后建链 | PrismService.ManualLinkDir | snackbar / detail 错误展示 |
| 模式切换 | 目录行「切换为文件级/整目录同步」 | — | PrismService.SetDirLinkMode | snackbar；刷新 |
| 选择同步文件 | 目录行「选择同步文件」（文件级） | FileSelectDialog：实例侧文件列表 + 当前勾选；全选/清空/逐项勾选 | PrismService.ListInstanceDirFiles | 文件选择弹窗 |
| 保存勾选 | 弹窗「保存」 | 勾选文件移动到项目目录后硬链接回实例侧 | PrismService.SelectInstanceFiles | snackbar 成功/跳过；emit changed → 父级刷新 |

### 5.4 meta 差异三区（MetaDiff）

instance_only（实例有、项目无，可拉取）/ project_only（项目有、实例无，可推送）/ version_diff（双端版本不一致，只读展示）。差异每次查看时重算并写入 .cache/metadiff.cache，不做实时监听。

## 6. 设置（/settings）

| 操作 | UI 入口 | 前端逻辑 | 后端调用 | 反馈 |
| --- | --- | --- | --- | --- |
| 检测工具 | 进入页面 / 刷新 | 结果走 stores/env 缓存（工作台同源） | EnvService.Detect | 工具卡：状态 chip（已配置/未加入 PATH/未检测到）+ 来源提示；有工具未找到弹引导框（会话内一次） |
| 一键配置 PATH | 页头「一键配置环境变量」 | 仅当存在已找到的工具时可用 | EnvService.Configure | snackbar 新增目录 / 无变化；返回后更新缓存 |
| 手动指定路径 | 工具卡输入框（浏览 / 保存 / 回车） | 空串清除 | EnvService.SetToolPath | snackbar；更新缓存 |
| API Key | API Key 卡（明文切换 / 保存 / 回车） | 空串清除 | EnvService.SetApiKey | snackbar 已保存 / 已清除；工作台健康卡联动 |

## 7. 对话框清单

| 对话框 | 所属 | 触发条件 | 选项与结果 |
| --- | --- | --- | --- |
| 缺失工具引导 | Settings | 检测后存在未找到的工具（会话内一次） | 取消 / 逐个保存路径 |
| API Key 引导 | App（应用级） | CF 版本获取遇 err.cf.api_key_missing / unauthorized | 关闭 / 去设置页 |
| 移除项目确认 | Projects / ProjectDetail | 移除操作 | 取消 / 确认移除（仅注册表） |
| 更新检查结果 | CheckUpdatesDialog | 检查更新 | 单 mod 更新 / 应用全部 / 关闭；输出内嵌展示 |
| 命令输出 | OutputDialog | packwiz refresh | 展示 CLI 输出 |
| 关联项目 | LinkDialog | 点击关联项目 | 选项目 + 实例（自动匹配）/ 程序创建实例 / 关联 |
| .pgignore 询问 | DirLinksDialog | 一键关联时项目无 .pgignore | 取消 / 不生成直接关联 / 生成后关联 |
| 手动链接确认 | DirLinksDialog | 目录行手动链接 | 取消 / 确认复制并入并建链 |
| 文件级同步选择 | FileSelectDialog | 目录行选择同步文件 | 全选/清空/勾选后保存 |
| 拉取 meta 确认 | Instances | 关联行拉取 meta | 取消 / 确认拉取全部 |
| 单拉取确认 | MetaDiffDialog | 差异「实例独有」项拉取 | 取消 / 确认拉取单个 |
| meta 差异 | MetaDiffDialog | 查看差异 | 三区查看 + 逐项推送/拉取 + 刷新 |
| 解除关联确认 | Instances | 关联行解除 | 取消 / 确认解除（删除已建链接） |

## 8. 错误处理路径

| 路径 | 场景 | 处理 |
| --- | --- | --- |
| errText(e) | Wails 调用异常 | 解析 e.cause 错误码 JSON → i18n 翻译；非结构化取 message |
| displayText(s) | 数据字段中的错误文本 | 错误码 JSON → 翻译；packwiz CLI 输出原样展示 |
| handleApiKeyError(e) | CF 版本获取 | API Key 错误码弹应用级引导，其余走全局 snackbar |

## 9. 注意点

- meta 拉取依赖 packwiz CLI 的 refresh 使 index.toml 收录新 pw.toml（差异以 index.toml 为权威）；refresh 失败仅提示不阻断。
- 差异计算为「查看时重算 + 刷新 .cache/metadiff.cache」，无实时监听。
- 单 mod 版本获取成功后就地更新行数据；批量获取整表刷新。
- 前端所有确认类交互已规避 Wails 原生 Dialogs.Question，仅 Dialogs.OpenFile 可用。
- 后端 37 方法契约本次未改动（工作台用四个既有调用并发组装；单 mod 更新复用 UpdateMods 非空分支）。
- 新增页面：在 router/index.ts 注册路由并写 meta（titleKey/icon）即可自动出现在侧栏与顶栏标题。
