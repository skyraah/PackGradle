> ⚠️ **已归档（2026-08-28）**：本文描述的是旧版前端（工作区架构之前的旧信息架构）。后续设计与决策请以 [工作区 UX 原型设计](../../frontend/05-workspace-ux-prototype.md)、[工作区交互原型](../../frontend/workspace-ux-prototype.html) 与 [架构文档](../../architecture/) 为准，本文仅作历史参考，不再维护。

# 前端组件清单

组件分三组：`common/`（7 个）、`projects/`（2 个）、`prism/`（4 个），另有根组件 `App.vue`。
所有组件均为 `<script setup lang="ts">` 单文件组件，文案经 `useI18n()` 取翻译键。

## 1. common —— 通用组件

### 1.1 ConfirmDialog.vue（通用确认框）

替代 Wails 原生 Question（构建版会挂起）的统一确认框；支持危险操作视觉与「后果四要素」列表。

Props（均带默认值）：

| prop | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `modelValue` | boolean | —（必填） | 对话框开合 |
| `title` | string | —（必填） | 标题 |
| `text` | string | `''` | 说明文字，可用 `#text` slot 覆盖 |
| `consequences` | string[] | `[]` | 后果列表，逐条渲染 |
| `confirmText` | string | `''` | 确认按钮文案，空则 `common.confirm` |
| `cancelText` | string | `''` | 取消按钮文案，空则 `common.cancel` |
| `confirmColor` | string | `'primary'` | 确认按钮颜色 |
| `loading` | boolean | `false` | 确认中 loading / 禁用 |
| `icon` | string | `mdi-help-circle-outline` | 图标 |
| `iconColor` | string | `'warning'` | 图标底色 |
| `persistent` | boolean | `false` | 禁止点击外部关闭 |
| `danger` | boolean | `false` | 危险变体：左侧红条 + 图标/按钮变 error |

Emits：`update:modelValue`、`confirm`。

### 1.2 EmptyState.vue（空状态占位）

Props：`icon`（string，必填）、`title`（string，必填）、`text`（string，可选）。
Slots：`actions`（有内容时渲染操作区）。

### 1.3 PageHeader.vue（页面统一头部）

Props：`title`（string，必填）、`subtitle`（string，可选）。
Slots：`actions`（右侧操作区）。窄屏（≤700px）自动纵向排列。

### 1.4 PageSteps.vue（横向步骤条）

- 导出接口：`StepItem { key, label, done, to }`。
- Props：`steps: StepItem[]`。
- Emits：`go(step: StepItem)`。
- 视觉：已完成绿勾、当前步（第一个未完成）主色描边、点击跳转。

### 1.5 OutputDialog.vue（命令输出查看器）

Props：`modelValue`（boolean）、`title`（string）、`output?`（string）。
Emits：`update:modelValue`。
行为：`<pre>` 显示输出；复制按钮写剪贴板，成功 `common.copied`、失败 `common.copyFailed`。

### 1.6 OnboardingDialog.vue（首次引导）

无 props / emits；由 `App.vue` 常驻挂载，自行决定是否弹出。

行为：
1. 挂载时 `EnvService.ConfigExists()`；config.toml 已存在（非首次）→ 不弹。
2. 并行装载 tools / apiKey / projects / overview 后弹出。
3. 五步：packwiz、prism、apiKey、project、link，进度条 + 当前待完成步骤提示。
4. 跳过/完成时 `EnvService.MarkConfigCreated()` 落盘 config.toml，之后不再弹；「去完成当前步骤」只跳路由不落盘（用户尚未配置时下次启动继续引导）。

### 1.7 TaskCenterDrawer.vue（任务中心抽屉）

- `const open = defineModel<boolean>({ default: false })`（v-model 控制）。
- 数据源：`stores/taskCenter`。
- 行为：右侧 380px 临时抽屉；`running` 任务显示进度条与步骤文本；完成项显示结果摘要、可展开/复制 output；「仅看失败」过滤；打开时 `markAllSeen()`；清理已完成任务。
- 视觉：状态左色条（success/warning/error）；`z-index: 2400` 覆盖普通 dialog。

## 2. projects —— 项目域组件

### 2.1 ModsTable.vue（mod 表格）

Props：

| prop | 类型 | 说明 |
| --- | --- | --- |
| `mods` | `ModInfo[]` | 表格数据 |
| `fetching?` | `string \| null` | 正在获取版本的 mod id（行内 loading） |
| `fetchDisabled?` | `boolean` | 批量获取中禁用单行按钮 |
| `flashed?` | `string \| null` | 刚获取成功的 mod id（行高亮，父级负责清除） |

Emits：`fetch(mod: ModInfo)`、`fetchAll()`。

行为：
- `v-data-table`：列 mod/side/file/version/actions；排序持久化到 `localStorage['packgradle.modsTable.sortBy']`；每页 50（可选 25/50/100/全部），隐藏内建 footer。
- 过滤：搜索（name/id）、side 按钮组、`onlyMissing`（version 与 cf_version 均为空）。
- 版本列优先级：本地 `version` → `cf_version`（≠ 文件名时）→ `cf_version` 与文件名相同则显示发布日期 + release type。
- 行内获取按钮仅对 `isCfMod(mod)` 显示。

### 2.2 CheckUpdatesDialog.vue（更新检查对话框）

Props：`modelValue`（boolean）、`project`（`PackProject | null`）。
Emits：`update:modelValue`、`changed`（应用更新后父级刷新项目）。

行为：
- 打开即检查：`PackwizService.CheckUpdates` → 结果页签化（updates / errors）；更新数为 0 且有错误时自动切到 errors。
- 单 mod 更新：显示名 `u.name` 反查 `ModInfo.id`（后端要求 `.pw.toml` 文件名）；找不到给 warning snackbar。
- 「应用全部」先弹 ConfirmDialog（后果四要素），再走 `runTask`；成功后 emit `changed` + 自动重查。
- CLI 输出收在可展开折叠区（有输出时显示）；操作期间 `persistent` 防止误关。

## 3. prism —— Prism 域组件

### 3.1 LinkDialog.vue（项目 ↔ 实例关联）

Props：`modelValue`（boolean）、`instances`（`Instance[]`）。
Emits：`update:modelValue`、`changed`。

行为：
- 打开时 `loadProjects(true)`；无可关联项目（全部解析失败）自动关闭并提示。
- 选择项目后按 id/name 不区分大小写匹配同名实例，自动预选并提示。
- 支持「创建实例」（`PrismService.CreateInstance`，取项目 MC/加载器信息），创建后自动选中；失败（如实例已存在）snackbar 结构化错误。
- 确认关联走 `runTask`，成功后 emit `changed`（父级刷新 Overview）。

### 3.2 DirLinksDialog.vue（目录同步关联管理）

Props：`modelValue`（boolean）、`project`（string）。
Emits：`update:modelValue`。

行为：
- 打开时并行 `ListDirLinks` + `ListProjectDirs`，候选目录剔除已关联项。
- 添加/移除目录关联；一键关联先查 `.pgignore`：已有 → 普通确认；没有 → 同框三选（跳过 / 创建并关联）。
- 一键关联结果列表按 `LinkResult.status` 着色（linked/existing/skipped/manual/error）；含 error 时任务以 warning 收尾。
- 手动链接（实例侧非空 → 复制并入）有 ConfirmDialog 后果说明。
- 模式切换：junction ↔ files；files 模式可打开 `FileSelectDialog` 选择同步文件。
- 所有操作后 `refreshDirLinks()` 重载。

### 3.3 FileSelectDialog.vue（文件级同步文件选择）

Props：`modelValue`（boolean）、`project`（string）、`dir`（string）、`files`（string[]，当前清单初始勾选）。
Emits：`update:modelValue`、`changed`。

行为：
- 打开时 `ListInstanceDirFiles` 拉实例侧候选文件；全选/清空；保存调 `SelectInstanceFiles`。
- 结果统计 linked/skipped 数量提示；成功 emit `changed` 由父级刷新。

### 3.4 MetaDiffDialog.vue（meta 差异）

Props：`modelValue`（boolean）、`project`（string）。
Emits：`update:modelValue`。

行为：
- 打开即 `MetaDiff` 重算差异；右上角显示 `fetched_at` 并可手动刷新。
- 三区展示：
  - `instance_only`（实例独有）→ 可单 mod 拉取（先确认）；
  - `project_only`（项目独有）→ 可单 mod 推送；
  - `version_diff`（版本不一致）→ 拉取（以实例为准）/ 推送（以项目为准）双向按钮。
- 拉取后自动 `packwiz refresh` + `loadOverview(true)` + `bumpProjectsVersion()` + `invalidateProjects()` + 重算差异。

## 4. 根组件 App.vue

见 [前端总览](./01-overview.md) 第 4 节。要点：自绘无边框窗口控件、Wails 拖拽区、rail 导航、任务中心入口、全局 API Key 引导与 snackbar。
