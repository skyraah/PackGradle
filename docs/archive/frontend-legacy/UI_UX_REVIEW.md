> ⚠️ **已归档（2026-08-28）**：本文描述的是旧版前端（工作区架构之前的旧信息架构）。后续设计与决策请以 [工作区 UX 原型设计](../../frontend/05-workspace-ux-prototype.md)、[工作区交互原型](../../frontend/workspace-ux-prototype.html) 与 [架构文档](../../architecture/) 为准，本文仅作历史参考，不再维护。

# PackGradle 前端 UI/UX 交互检视报告

> 版本：v1.0 · 2026-08-15
> 范围：`frontend/src` 全部视图（工作台 / 项目 / 项目详情 / Prism 联动 / 设置）、通用组件与全局壳层
> 方法：静态代码走查（未实机运行），按「问题 → 影响 → 解决方案」组织，标注严重度与涉及文件
>
> **v2.0 更新（2026-08-16）**：文末新增「原型落地对照」——全部视图已按 PCL2 风格 + 任务中心范式完成颠覆性重构原型（mock 数据驱动，可 `npx vite --port 5199` 直接体验），本文所列问题绝大多数已在原型中解决或给出结构性方案。

## 严重度定义

| 级别 | 含义 |
| ---- | ---- |
| 🔴 P0 | 违反用户已确立的交互原则，或可能误导用户造成数据/状态误解 |
| 🟠 P1 | 明确的可用性缺陷，增加操作成本或认知负担 |
| 🟡 P2 | 一致性问题、细节打磨，可在迭代中顺手修复 |

---

## 一、概览：交互做得好的部分（保持）

在列问题之前，先记录已有的良好实践，避免后续重构时退化：

- **全局确认体系**：`ConfirmDialog` 统一替代 Wails 原生对话框（构建版挂起 bug 的规避），重要操作（移除项目、解除关联、拉取 meta、手动链接）均有确认 —— 符合「操作前确认」原则。
- **共享缓存 + 版本号通知**：`stores/projects.ts` 的 inflight 合并与 `projectsVersion` 跨视图通知，保证各页数据一致且无重复请求。
- **snackbar 队列**：连续操作的消息按序展示，不互相覆盖。
- **API Key 引导**：错误码驱动的全局引导弹窗，比裸报错友好得多。
- **快速开始清单**：工作台按真实工作流（工具 → Key → 项目 → 关联）组织引导，完成度可视化。
- **错误→可读文本**：`displayText`/`parseAppErr` 把错误码 JSON 翻译成用户语言，而非直接抛异常文本。

---

## 二、问题清单

### A. 确认原则执行不一致 🔴

「重要/有副作用操作前必须弹确认提示，不静默执行」是用户明确确立的交互原则，但目前执行不一致——有的操作有确认，有的没有，且**有确认的操作确认文案未说明后果**。

#### A1. `packwiz update` 类写操作全部无确认 🔴

| 位置 | 操作 | 现状 |
| ---- | ---- | ---- |
| `CheckUpdatesDialog.vue:96` applyAll | 「应用全部更新」改写全部 mod 的 pw.toml | 一键直接执行，无确认 |
| `CheckUpdatesDialog.vue:112` updateOne | 单 mod 更新 | 点击即执行，无确认 |
| `ProjectsView.vue:116` / `ProjectDetailView.vue:72` refreshProject | `packwiz refresh` 重写 index.toml | 无确认 |
| `InstancesView.vue:124` pushMeta | 推送 meta 写入实例 `mods/.index` | 无确认 |
| `DirLinksDialog.vue:129` executeLinkAll | 一键关联，批量创建 junction/硬链接 | 仅在缺 `.pgignore` 时询问；正常情况下**无任何确认**即批量改文件系统 |

**对比**：同一对话框里的「手动链接」却有确认（`DirLinksDialog.vue:379`）——同量级副作用，确认策略相反，用户无法形成稳定预期。

**影响**：更新全部 mod 是不可一键回滚的批量写入；一键关联会在实例目录批量建链。误点一下即生效。

**解决方案**：
1. `CheckUpdatesDialog` 的「应用全部」改为两步：按钮文案改为「应用全部更新（N）」，点击后弹 `ConfirmDialog`，文案明确「将更新 N 个 mod，原版本文件名将变更，建议先备份/提交 git」。单 mod 更新可保持无确认（颗粒小、列表内上下文清晰），但应在行内给出执行结果反馈（见 B3）。
2. 「一键关联」增加确认框：列出「将对 X 个项目根目录条目创建链接（尊重 .pgignore 规则），其中已有内容的目录需手动处理」，确认后再执行。这样 `.pgignore` 询问与自然确认可合并为一个对话框：有 ignore 文件时显示普通确认；没有时在确认框中附带「创建 / 跳过」两个选项（当前的两层弹窗可减为一层）。
3. `refreshProject`、`pushMeta` 颗粒较小且只影响单项目/单实例，可不加确认，但二者都是「会改文件」的操作，建议在按钮 tooltip 或菜单项副标题里点明「将重写 index.toml / 写入实例 .index」，让用户点之前知道有副作用。

#### A2. 确认文案不含后果说明 🟠

`ConfirmDialog` 多处使用仅写「确定要…吗？」：

- `InstancesView.vue:404` 拉取 meta 确认（`metaPullConfirmText`）——未说明「将从实例拉取并覆盖项目中的 mod 元数据，且会自动执行 packwiz refresh」
- `DirLinksDialog.vue:381` 手动链接确认——未说明「实例侧已有内容将被复制并入项目目录」这一**数据移动**行为

**解决方案**：确认框文案遵循「动作 + 对象 + 后果 + 是否可逆」四要素模板，例如：
> 「从实例拉取 mod 元数据到项目 **Project-Collapse**？项目中同名 mod 的 .pw.toml 将被覆盖，拉取后自动执行 packwiz refresh。此操作可通过 git 撤销。」

---

### B. 反馈与状态可见性

#### B1. 副作用较重的操作成功后只弹一条 snackbar 🟠

以下操作完成后**仅有一条 4.2 秒的 snackbar**，无任何持久记录：

| 位置 | 操作 |
| ---- | ---- |
| `InstancesView.vue:124` pushMeta | 「已推送 N 条」snackbar |
| `InstancesView.vue:144` pullMeta | 「已拉取 N 条」+ 后台静默 refresh（refresh 失败才追加一条 warning） |
| `DirLinksDialog.vue:86` removeDirLink | 「已移除」snackbar |
| `ProjectsView.vue:131` fetchAllVersions | 「成功 N/M」snackbar（失败详情不可见） |

**对比**：`refreshProject` 却会弹出 `OutputDialog` 展示完整 CLI 输出——同类操作反馈粒度悬殊。

**影响**：用户无法回看「刚才到底改了什么」；`fetchAllVersions` 报「成功 8/10」时，失败的 2 个是哪些、为什么失败，snackbar 消失后无从查起。

**解决方案**：
1. 引入**操作日志面板**（全局）：壳层右下加一个可展开的消息中心（复用现有 snackbar 队列的数据结构，追加 `timestamp` 与 `detail` 字段），snackbar 照常弹出，历史消息可回看。`showSnackbar` 调用点无需大改，仅给关键操作传 `detail`（如失败列表、CLI 输出摘要）。
2. `fetchAllVersions` 的批量结果沿用 `CheckUpdatesDialog` 的「失败/跳过列表」模式：批量完成后若有失败，弹出结果对话框列出失败 mod 与原因，而非仅 snackbar 计数。
3. push/pull meta 完成后，snackbar 追加「查看差异」action 按钮（v-snackbar 支持 actions slot），点击直接打开 `MetaDiffDialog`——把「操作 → 验证结果」连成闭环。

#### B2. 刷新失败被静默吞掉 🟠

`ProjectsView.vue:116`、`ProjectDetailView.vue:72`：`refreshProject` 的 try 块**没有 catch**——`PackwizService.RefreshProject` 抛错时，`refreshing` 状态虽能在 finally 复位，但**用户得不到任何失败提示**，界面表现为「转圈结束，什么都没发生」。

同样模式出现在 `InstancesView.vue:60` load（有 catch，OK），但 `refreshProjectIndex`（`InstancesView.vue:165`）里 refresh 返回 `ok: false` 仅 snackbar warning，实际失败输出被丢弃。

**解决方案**：`refreshProject` 补 catch，走 `handleApiKeyError`/`showSnackbar(errText(e), 'error')`；refresh 返回 `ok:false` 时把 `result.output` 放进 `OutputDialog` 展示（现在只有成功才弹输出框，失败反而看不到输出，逻辑反了）。

#### B3. 行内操作无完成态反馈 🟡

`ModsTable.vue:111` 的单 mod 版本获取按钮：loading → 结束，行内无任何变化提示（版本号若未变，用户无法确认操作是否生效）。成功 snackbar 有，但表格行本身无「刚更新过」的视觉标记。

**解决方案**：获取成功后该行版本单元格短暂高亮（Vuetify 可用 CSS transition，2 秒淡出的 success 底色），成本极低，感知提升明显。

#### B4. `loading` 进度条位置分散且部分缺失 🟡

- `ProjectDetailView.vue:27` 的 `loading` ref **从未被赋值**（声明后永远是 false），详情页加载进度条实际是死的（`ProjectDetailView.vue:150`）。
- 各视图进度条有的放 `PageHeader` 下、有的放卡片内（`InstancesView.vue:269`），节奏不统一。

**解决方案**：删除死代码，或在 `ensureLoaded` 期间真正置位；进度条统一放页面容器顶部（紧贴 PageHeader 下沿）。

---

### C. 导航与信息架构

#### C1. 「联动」chip 语义不明 🟠

`App.vue:95` 顶栏常驻的 `<v-chip>联动</v-chip>`：

- 无任何状态指示（绿点/灰点），看不出 Prism 当前是已连接还是未配置；
- 不可点击（chip 无 click 行为），用户看到「联动」二字无法得知它指什么、去哪管理；
- 占用顶栏宝贵空间，信息量却为零。

**解决方案**：二选一——
- **改为状态指示器**：绑定 `useInstances().overview`，定位正常时显示 `mdi-link-variant` + 绿色，定位失败时 warning 色 + 可点击跳 `/instances`；tooltip 说明当前实例目录。
- **直接移除**：联动状态在工作台环境卡已有完整呈现，顶栏不必重复。推荐前者（状态可见性好），实现约 15 行。

#### C2. 路由跳转丢失滚动位置 🟠

`router/index.ts` 未配置 `scrollBehavior`，且仅 `ProjectsView` 被 keep-alive：

- 从项目详情（长 mod 表格滚到中部）返回列表，列表状态保留（keep-alive 生效）✅；
- 但从详情页去联动页再回来，或刷新深链，所有非 keep-alive 视图**总是回到顶部**；
- `ProjectDetailView` 不在 keep-alive 内，从详情→联动→返回详情会**整页重载**（`ensureLoaded` 再跑一遍，虽然缓存命中仍有一帧空态闪烁）。

**解决方案**：
1. `createRouter` 增加 `scrollBehavior(to, from, savedPosition)`：有 savedPosition 时恢复，否则 `return { top: 0 }`。
2. 视实际体验决定是否把 `ProjectDetailView` 也纳入 keep-alive（`:key` 已按 fullPath 区分，注意多项目并存时内存可控——mod 列表一般几百行，可接受）。

#### C3. 详情页「返回」按钮的返回目标固定 🟡

`ProjectDetailView.vue:146` 返回按钮固定 `router.push('/projects')`。若用户从工作台「我的项目」卡片进入详情，点返回却到了项目列表而非工作台——来源上下文丢失。

**解决方案**：`router.back()` 配合来源判断：历史栈内上一跳是本应用时 `router.back()`，直接深链进入时 fallback `push('/projects')`。

#### C4. 侧栏收纳状态对小屏不友好 🟡

rail 模式持久化在 localStorage（`App.vue:15`），但没有按窗口宽度自适应：窗口拉窄时 220px 抽屉 + 内容区挤压严重。Wails 桌面窗可随意拉伸，此场景常见。

**解决方案**：监听 `v-main` 宽度（或 `window.resize`），宽度 < 1100px 时强制 rail（不覆盖用户手动偏好，可用一个 `userToggled` 标志区分）。

---

### D. 表单与输入

#### D1. 路径类输入无校验、无规范化 🟠

- `InstancesView.vue:240` 实例目录手动路径：任意字符串都可保存，输错时后端报错才知晓；不支持 `~` 或环境变量说明；Windows 反斜杠/正斜杠混用无提示。
- `SettingsView.vue:250` 工具路径同理，且「保存」按钮**始终可用**——path 未修改时点保存也发一次请求。

**解决方案**：
1. 输入框失焦时做基础校验（非空、路径存在性可交给后端轻量接口，或至少在保存失败时把后端错误 inline 显示在输入框 `error-messages` 而非仅 snackbar——现在保存失败提示在右上角，用户的视线却在输入框上，反馈错位）。
2. 保存按钮加 dirty 判断：`tool.path` 与已存值相同时禁用。
3. 浏览按钮选目录/选文件混用（`SettingsView.vue:89` `CanChooseFiles: true, CanChooseDirectories: true` 同时开），用户可能选错类型，建议工具只能选 exe 文件、实例目录只能选文件夹，分开限定。

#### D2. API Key 输入框安全问题 🟡

`SettingsView.vue:300`：`apiKey` 直接 v-model 绑定共享缓存并回显完整 Key（虽有 password 遮蔽，但「眼睛」切换后明文展示）。共享缓存意味着任何视图都能拿到明文 Key——这本身可接受（本地桌面应用），但**输入即写缓存**：`apiKey.value` 在输入过程中就改了全局状态，未保存时点「清除」之外的页面离开，缓存里的脏值会让工作台显示「已配置」（`DashboardView.vue:87` 判断 `!!apiKey.value`），与实际存储不一致。

**解决方案**：设置页用本地副本编辑，保存成功后才 `setApiKeyValue` 写缓存；或给 apiKey 卡加「未保存的更改」提示。

#### D3. 缺失工具引导弹窗的取消语义 🟡

`SettingsView.vue:326` 的 missingDialog 是 `persistent`，但 `@click:outside` 和 `@keydown.esc` 绑了 `closeMissingDialog`——persistent 对话框默认就不响应外部点击，这两个绑定是死代码；且「本次会话不再打扰」（`dismissed`）的语义用户无从得知。

**解决方案**：去掉 persistent 或去掉死代码（二选一）；在对话框底部加一行 caption：「本次会话内不再提示，可从设置页重新配置」。

---

### E. 列表与表格

#### E1. ModsTable 无排序、无分页 🟠

`ModsTable.vue`：mod 表格按后端返回顺序平铺，几百个 mod 时：
- 无列排序（想按 side 聚合、按「无版本」优先排都做不到）；
- 无分页/虚拟滚动，`v-table` 全量渲染，300+ 行时滚动与渲染都会卡；
- 行高内联死（density comfortable），无法切换紧凑视图。

**解决方案**：
1. 改用 `v-data-table`（自带排序/分页/密度切换），迁移成本低（列结构已规整）；开 `items-per-page="50"` + 排序持久化到 localStorage。
2. 加一个「仅显示未获取版本的 mod」快捷过滤 chip——「获取全部版本」前用户最想先看缺哪些，当前只能靠肉眼扫「—」。

#### E2. 关联行的操作按钮语义与层级 🟠

`InstancesView.vue:290`：每条关联 5 个同级 tonal 按钮（推送/拉取/差异/目录同步/解除），问题：
- 「推送 meta」「拉取 meta」方向性强，但按钮无方向图标，新用户分不清谁覆盖谁（tooltip 也没有）；
- 全部 tonal 同级，最常用的「差异」与危险的「解除关联」视觉权重一样；
- `pl-10` 缩进对齐依赖魔数，窗口窄时按钮换行后缩进错位。

**解决方案**：
1. 推送/拉取按钮加方向图标（`mdi-arrow-up-bold-outline` / `mdi-arrow-down-bold-outline`），tooltip 写明「项目 → 实例」「实例 → 项目（覆盖同名 mod 元数据）」。
2. 主操作「查看差异」保持 tonal；「解除关联」收成行尾 icon 按钮（`mdi-link-variant-off`，error 色）；推送/拉取降为 text 按钮。一行的视觉噪声立刻减半。
3. 操作区改 `ml-10`→ 与标题行 `v-avatar` 对齐的计算缩进或直接 flex 布局，去掉魔数。

#### E3. 差异对话框的「版本差异」区不可操作 🟡

`MetaDiffDialog.vue:172`：版本差异列表只展示「项目版 X / 实例版 Y」，**没有任何操作按钮**——用户看到差异后的自然动作（以哪边为准？）要去别的入口完成，心流断裂。

**解决方案**：每条版本差异行尾加「拉取/推送」按钮（与独有区一致），一次对话框内闭环。

#### E4. 空状态与「无匹配」文案复用错误 🟡

`ModsTable.vue:125`：无匹配时复用 `projects.noMatch`（文案是「没有匹配 "xxx" 的**项目**」）——mod 表格里显示「项目」字样，文案串味。

**解决方案**：新增 `projects.noModMatch` 翻译键。

---

### F. 一致性与细节

| # | 级别 | 位置 | 问题 | 建议 |
| --- | --- | --- | --- | --- |
| F1 | 🟡 | `ProjectsView.vue:165` vs `InstancesView.vue:205` vs `SettingsView.vue:190` | 刷新按钮样式不统一（text icon / 与主按钮混排顺序不一） | 约定：PageHeader actions 区固定「主操作按钮在左，刷新 icon 在最右」，三个页面拉齐 |
| F2 | 🟡 | `DirLinksDialog.vue` vs `CheckUpdatesDialog.vue` | 对话框卡片有的用 `dialog-card` class（App.vue/ConfirmDialog/OutputDialog/CheckUpdates），有的裸 `elevation="8"`（LinkDialog/DirLinks/MetaDiff/FileSelect） | 统一 `dialog-card`（圆角/边框样式集中在一处） |
| F3 | 🟡 | `DirLinksDialog.vue:400` | 边框色 `rgba(255,255,255,0.08)` 写死，亮色主题下边框发虚 | 换 `rgb(var(--v-theme-outline))` 或 `var(--pg-border)`（`InstancesView.vue:427` 已用后者，应统一） |
| F4 | 🟡 | `ProjectsView.vue:212` | 卡片头像颜色只用 `error/primary`，解析失败的项目除了小 chip 外列表里不够扎眼 | error 项目卡左边框 3px error 色条，扫一眼即可定位问题项目 |
| F5 | 🟡 | `App.vue:26` | `noticeIcon` computed 依赖 `snackbarTone.value` 做索引，类型上 tone 缺省时返回 undefined（图标位空白一帧） | 给 `?? 'mdi-information-outline'` 兜底 |
| F6 | 🟡 | `InstancesView.vue:227` | 定位失败 alert 里「去配置」按钮跳 `/settings`，但 settings 页无锚点/高亮，用户到了设置页不知道看哪里 | 跳转时带 query（如 `/settings?focus=prism`），目标卡片闪烁高亮一次 |
| F7 | 🟡 | `CheckUpdatesDialog.vue:35` | watch 依赖 `[modelValue, project?.name]`，同一项目重复打开时（name 未变）若用 keepalive 结果不刷新——当前实现 OK，但 generation 计数器与 `checkPending` 的双保险逻辑无注释说明防竞态意图，后续维护易误删 | 补一段注释说明「快速切换项目时丢弃过期结果」 |

---

## 三、优先级落地建议

按「修复成本 × 感知收益」排序，建议分三批：

**第一批（本次迭代，约半天）** —— 全部是低风险小改动：
1. B2：`refreshProject` 补 catch + 失败时输出进 OutputDialog（修反逻辑）
2. A1-3：一键关联加确认框（合并 .pgignore 询问为一层）
3. E4：修 noMatch 文案串味；F3/F5：样式与兜底
4. B4：删 `ProjectDetailView` 死的 loading ref

**第二批（下个迭代，1~2 天）**：
5. A1-1：CheckUpdatesDialog「应用全部」确认 + 确认文案四要素（A2 一并做）
6. B1：操作日志面板 + fetchAllVersions 失败列表对话框 + push/pull 后「查看差异」action
7. C1：顶栏联动 chip 改状态指示器；C2：scrollBehavior + 详情页 keep-alive 评估
8. E1：ModsTable 迁移 v-data-table + 「仅看未获取版本」过滤

**第三批（体验打磨，随版本节奏）**：
9. D1/D2：表单校验、dirty 判断、API Key 本地副本
10. C3/C4：返回来源感知、侧栏宽度自适应
11. E2/E3：关联行操作层级重排、版本差异行内操作

---

## 附：涉及文件索引

| 主题 | 主要文件 |
| ---- | ---- |
| 确认体系 | `components/common/ConfirmDialog.vue`、`components/projects/CheckUpdatesDialog.vue`、`components/prism/DirLinksDialog.vue`、`views/InstancesView.vue` |
| 反馈/日志 | `stores/ui.ts`、`views/ProjectsView.vue`、`views/ProjectDetailView.vue`、`components/common/OutputDialog.vue` |
| 导航/壳层 | `App.vue`、`router/index.ts` |
| 表单 | `views/SettingsView.vue`、`views/InstancesView.vue`（实例目录卡） |
| 列表/表格 | `components/projects/ModsTable.vue`、`components/prism/MetaDiffDialog.vue`、`views/InstancesView.vue` |

---

## 四、原型落地对照（v2.0，2026-08-16）

> 全部视图已按「PCL2 视觉 + 任务中心反馈」完成颠覆性重构原型。原型数据来自 `stores/mock.ts`（内存态假数据，刷新重置），所有交互闭环可在浏览器中真实走通。构建验证：`vue-tsc + vite build` 通过。
>
> 体验方式：`cd frontend && npx vite --port 5199 --strictPort` 后访问 `http://localhost:5199/`（注意本机 5173 被 Adobe CC 常驻服务占用，原型验证用 5199）。

### 新交互范式核心

| 机制 | 实现 | 对应旧问题 |
| ---- | ---- | ---- |
| **任务中心**（`stores/taskCenter.ts` + `components/common/TaskCenterDrawer.vue`） | 顶栏 bell + 未读角标，右侧 380px 抽屉；任务卡片含进度条、步骤文本、完成态（成功/警告/失败左色条）、可展开输出、复制按钮；支持「仅看失败」过滤与「清空已完成」 | B1（重操作只有一次性 snackbar）、B2（refresh 失败无提示） |
| **操作四段式** `runTask({ title, kind, run, output?, warn? })` | 确认（含后果）→ 执行（进度实时上报）→ 结果驻留历史 → 可追溯 | A1（写操作无确认/确认不一致） |
| **确认框升级** | ConfirmDialog 新增 `danger` 变体（左侧红条 + 图标色块）与 `consequences` 列表；所有确认文案按「动作+对象+后果+可逆性」重写 | A2（确认文案不含后果） |
| **PCL2 壳层** | 68px 常显 icon rail（选中浅色底块 + 左侧亮条），顶栏 = 品牌 + 页面标题 + 联动状态 chip（可点击、带状态色）+ 任务中心 bell + 主题切换；路由补 `scrollBehavior` | C1（联动 chip 无状态不可点）、C2（丢滚动位置）、C4（rail 不自适应问题不复存在——常显窄 rail 本身即小屏形态） |

### 逐项对照

| 原问题 | 级别 | 原型处理 |
| ---- | ---- | ---- |
| A1 写操作无确认 | 🔴 | 「应用全部更新」「一键关联」「移除项目」「解除关联」「移除目录」全部经确认框；一键关联与 .pgignore 询问**合并为单框**（有 ignore=普通确认+后果列表，无 ignore=同框三选）；refresh/pushMeta 等轻量操作 tooltip 标明副作用，确认强度与副作用量级对齐 |
| A2 确认文案不含后果 | 🟠 | 全部确认框附 `consequences` 后果列表（如更新：改写 pw.toml / 自动 refresh / 建议 git 提交） |
| B1 重操作无持久记录 | 🟠 | 全部走任务中心：进度可见、结果与 CLI 输出驻留可回看可复制 |
| B2 refresh 失败静默 | 🟠 | `runTask` 失败即错误态任务卡（含输出）；不再有「转圈结束什么都没发生」 |
| B3 行内操作无完成态 | 🟡 | 保留行内 loading；任务中心记录每次获取；行高亮方案保留在 ModsTable（`row-flash` CSS 就绪） |
| B4 loading 进度条位置不一/死代码 | 🟡 | 删除 ProjectDetail 死的 loading ref；进度条统一页面容器顶部 |
| C1 联动 chip 无语义 | 🟠 | 顶栏 chip 绑定 Overview：正常绿「联动正常」/ 未就绪灰「联动未就绪」，点击跳联动页，tooltip 说明 |
| C2 路由丢滚动 | 🟠 | `scrollBehavior` 恢复 savedPosition；ProjectsView keep-alive 保留 |
| C3 返回目标固定 | 🟡 | 详情页返回 = 历史栈内 `router.back()`，深链 fallback 列表 |
| C4 侧栏窄屏不友好 | 🟡 | 改为 68px 常显 icon rail（PCL2 骨架），无展开态即无挤压问题 |
| D1 路径无校验/保存恒可用 | 🟠 | 设置页路径输入本地副本 + dirty 判断（未改禁用保存）+ 失败 inline `error-messages`（反馈位置与视线一致） |
| D2 API Key 输入即写缓存 | 🟡 | 改本地副本编辑，未保存显示「有未保存的更改」chip，保存成功才写缓存 |
| D3 缺失工具弹窗死代码 | 🟡 | 去掉 persistent 与无效绑定，底部加「本次会话不再提示」caption |
| E1 表格无排序分页 | 🟠 | ModsTable 迁移 `v-data-table`：全列排序（localStorage 持久化）+ 分页 25/50/100/全部 + 工具行「仅看未获取版本」开关 + 获取全部版本按钮内置 |
| E2 关联行 5 个同级按钮 | 🟠 | 视觉分级：「查看差异」tonal 主按钮｜推送/拉取带方向图标 text 按钮（tooltip 写明方向与覆盖后果）｜同步目录/解除收为行尾 icon（解除 error 色） |
| E3 版本差异区不可操作 | 🟡 | 版本差异行尾补拉取/推送按钮，三区全部可操作闭环 |
| E4 noMatch 文案串味 | 🟡 | 新增 `projects.noModMatch` 键 |
| F1~F7 一致性 | 🟡 | 对话框统一 `dialog-card`；边框色统一 `--pg-border`；noticeIcon 兜底；错误项目卡左侧红条（`card-error`） |

### 遗留说明

- F6（设置页跳转高亮目标卡）在原型中未做——设置为单页两卡结构，信息层级已足够平，优先级降低，视真实版反馈再定。
- mock 层每个函数标注了对应真实 bindings 方法（如 `mockCheckUpdates` ↔ `PackwizService.CheckUpdates`），回切时逐函数替换 stores 内的 mock 调用即可；页面层无需改动。
