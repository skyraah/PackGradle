> ⚠️ **已归档（2026-08-28）**：旧 AI 工作日志；新记录在项目根 AGENT.md 持续追加，本文件仅作历史参考，不再维护。

# AGENT 工作记录

> 本文件记录 AI 代理在本项目中的主要工作会话，供后续接续与追溯。
> 追加新记录时请包含：日期、模型、任务目标、关键决策、产出物、验证方式。

## 长期记忆约定

- 项目的重大变更必须追加记录到本文件，作为当前工作区的长期记忆。
- 每条重大变更记录必须明确注明执行该变更的模型名称。
- 记录至少包含日期、模型、任务目标、关键决策、产出物和验证方式；不得覆盖或删除既有记录。

## 命令行环境约定

- 在当前工作区需要使用 Bash 时，必须使用 Git Bash。
- 在当前工作区需要使用 PowerShell 时，必须使用 PowerShell 7（`pwsh`）。

---

## 2026-08-15 · 设置工作区命令行环境规则

**模型**：GPT-5（Codex）

**任务目标**：统一当前工作区内 Bash 与 PowerShell 命令的执行环境。

**关键决策**：Bash 命令使用 Git Bash；PowerShell 命令使用 PowerShell 7（`pwsh`）。

**产出物**：在本文件开头新增“命令行环境约定”。

**验证方式**：确认两条环境规则已写入 `AGENT.md`，且本次记录包含模型名称。

---

## 2026-08-15 · 设置工作区长期记忆规则

**模型**：GPT-5（Codex）

**任务目标**：将 `AGENT.md` 设为当前工作区的长期记忆载体。

**关键决策**：后续所有重大变更均追加到本文件，并在记录中明确附上模型名称。

**产出物**：在本文件开头新增“长期记忆约定”。

**验证方式**：确认约定已写入 `AGENT.md`，且本次记录包含模型名称。

---

## 2026-08-15 · 持久化项目级 Codex 子代理

**模型**：GPT-5.6（Codex）

**任务目标**：为项目创建可跨会话复用的前端产品、前后端契约集成和测试质量三个自定义子代理。

**关键决策**：使用项目级 `.codex/config.toml` 注册代理，角色定义保存在 `.codex/agents/*.toml`；三个代理均使用 `gpt-5.6-sol`，推理强度设为 `high`，并按模块明确职责边界与验证要求。

**产出物**：`.codex/config.toml`、`.codex/agents/frontend_product.toml`、`.codex/agents/contract_integrator.toml`、`.codex/agents/test_engineer.toml`。

**验证方式**：检查 TOML 语法、配置文件引用、角色模型与指令字段，并确认文件出现在 Git 工作区状态中。

---

## 2026-08-15 · 注册 project-code-docs 子代理

**模型**：GPT-5.6（Codex）

**任务目标**：将已有的 `.codex/agents/project-code-docs.toml` 纳入项目级 Codex 子代理注册表。

**关键决策**：保持现有角色文件及其 `gpt-5.6-terra`、`low` 配置不变，以 `project_code_docs` 作为注册 ID；该角色用于范围明确的通用代码维护、文档同步和仓库扫描。

**产出物**：更新 `.codex/config.toml`，新增 `agents.project_code_docs` 注册项。

**验证方式**：解析项目级 TOML 配置并检查 `agents/project-code-docs.toml` 引用存在。

---

## 2026-08-15 · 创建 PackGradle 子代理编排 Skill

**模型**：GPT-5.6（Codex）

**任务目标**：创建项目级 Skill，使主代理能够按任务类型正确选择、调度并约束 `frontend_product`、`contract_integrator`、`test_engineer` 和 `project_code_docs`。

**关键决策**：Skill 采用主代理最终负责制，最多并发三个子代理；委派前建立文件所有权表，同一文件不得由两个代理并发写入；跨端功能按“契约稳定 → 前端消费 → 独立测试”分波执行；高风险文件系统逻辑与全局配置保留给主代理；微小或文件重叠任务禁止为委派而委派。

**产出物**：`.agents/skills/packgradle-agent-orchestrator/SKILL.md` 与 `agents/openai.yaml`。

**验证方式**：官方 `quick_validate.py` 通过；`openai.yaml` YAML 解析通过；暂存与最终 `SKILL.md` 哈希一致；两个只读前向测试分别验证跨端三波调度与微小任务拒绝委派行为。

---

## 2026-08-15 ~ 2026-08-16 · 前端 UI/UX 检视与颠覆性重构原型

**模型**：kimi k3（kimi-k3）

### 任务一：前端 UI/UX 交互检视报告

**目标**：检视 `frontend/src` 全部前端代码，产出交互优化建议文档。

**产出**：`docs/UI_UX_REVIEW.md`（v1.0）

**核心发现**：
- 🔴 P0：确认原则执行不一致——「应用全部更新」「一键关联」等批量写操作无确认，而同对话框内「手动链接」却有确认；`refreshProject` 成功弹输出框、失败反而静默无提示（try 块无 catch），逻辑反了。
- 🟠 P1：重操作（push/pull meta、fetchAllVersions）只有 4 秒一次性 snackbar，失败详情无从回看；顶栏「联动」chip 无状态不可点击；ModsTable 无排序分页；路由无 scrollBehavior。
- 共 20+ 项问题按 🔴/🟠/🟡 三级标注，每条附「问题 → 影响 → 解决方案 + 文件：行号」，末尾给出三批落地计划。

**方法**：静态代码走查（5 视图 + 10 组件 + stores/路由），未实机运行。

### 任务二：颠覆性交互重构原型（PCL2 风格）

**用户授权**：允许颠覆性重构、允许原型设计（暂不适配后端接口）、参考 `docs/pcl2.png`（PCL2 启动器截图）做视觉设计。

**产出**：全部视图重写，约 18 个前端文件 + 2 个新建组件 + mock 层。

**新交互范式**：
1. **任务中心**（`stores/taskCenter.ts` + `components/common/TaskCenterDrawer.vue`）：顶栏 bell + 未读角标，右侧 380px 抽屉；任务卡片含实时进度条、步骤文本、完成态左色条（成功/警告/失败）、可展开输出、复制按钮；「仅看失败」过滤。取代一次性 snackbar 成为操作历史权威。
2. **操作四段式** `runTask()`：确认（后果四要素：动作/对象/后果/可逆性）→ 执行（进度可见）→ 结果驻留 → 可追溯。
3. **确认框升级**：ConfirmDialog 新增 `danger` 变体（左侧红条）与 `consequences` 列表属性。
4. **PCL2 壳层**：68px 常显 icon rail（选中浅色底块 + 左侧亮条），顶栏 = 品牌 + 页面标题 + 联动状态 chip（可点击、带状态色）+ 任务中心 + 主题切换；路由补 scrollBehavior。

**数据策略**：新建 `stores/mock.ts` 独立 mock 层（假数据 + 模拟异步延迟 + 进度回调），每个函数注释标注对应的真实 bindings 方法（如 `mockCheckUpdates` ↔ `PackwizService.CheckUpdates`），回切时逐函数替换 stores 内引用即可，页面层零改动。mock 不做持久化，刷新重置。

**关键页面改动**：
- Dashboard：欢迎横幅 + 环境健康卡（失败卡整卡红条）+ 项目/关联概览。
- Projects：加载器着色卡片（首字母头像）+ 加载器过滤 chip 组 + hover 浮起。
- ProjectDetail：ModsTable 迁移 v-data-table（排序持久化 + 分页 + 「仅看未获取版本」开关）；CheckUpdatesDialog 页签化 + 应用全部走确认。
- Instances：定位状态横幅（失败变大卡内嵌修复 + 关联区置灰）；关联操作视觉分级（差异主按钮、推送/拉取方向图标 tooltip）；差异对话框版本差异区补操作按钮；实例列表折叠面板。
- Settings：路径/API Key 本地副本编辑 + dirty 判断 + 失败 inline 错误。

**验证**：`vue-tsc + vite build` 通过；dev 服务器浏览器走查（注意：本机 5173 被 Adobe CC 常驻 node 进程占用，验证用 `--port 5199 --strictPort`）。

### 任务三：二次迭代（用户走查反馈）

**反馈 1**：工作台环境卡与下方区块间距过近 → `mb-5` 调 `mb-6`、行间距 `mt-1` 调 `mt-2`。

**反馈 2**：快速开始清单不应常驻工作台，应仅在首次打开（未检测到 config.toml）时作为引导弹出 → 新建 `components/common/OnboardingDialog.vue`：
- 五步步骤条 + 进度条 + 当前待完成提示 +「去完成当前步骤」直达对应页面；
- 判断 config 存在性（原型用 localStorage `packgradle.mock.configExists` 模拟，回切换真实 ConfigService 检查）；
- 跳过/完成均写入标记，之后不再弹出；
- Dashboard 内旧的快速开始卡片已移除。

**过程中修复**：mock.ts 重写时丢失 `instances` 实例数组声明导致 TS 错误，已补回。

**遗留观察**（走查中发现，待真实版处理）：任务中心抽屉与全局弹窗同开时存在 z-index 层级遮挡（抽屉 overlay 拦截弹窗按钮点击），已在抽屉加 `z-index: 2400` 缓解，真实版建议弹窗打开时自动收起抽屉或统一 overlay 层级管理。

### 文档

- `docs/UI_UX_REVIEW.md` 已追加「四、原型落地对照」章节（v2.0），逐条标注原检视的 20+ 项问题在新范式下的解决方式，可作真实版改造验收清单。

---

## 2026-08-16 · mods 双端目录实时监听（WatchMods + Wails 事件）

**模型**：Claude（当前会话）

**目标**：新增并暴露一个后端方法，监听已关联项目的 `mods` 目录与实例 `mods/.index` 目录；任一侧变化时防抖执行一次双端 `MetaDiff`，并打包推送到前端。

**产出**：
- `internal/service/mods_watch.go`：基于 `fsnotify` 的双端监听器（多项目共用一个 watcher，每个项目一条防抖队列）。目录不存在时回退监听最近存在父目录并按目标路径过滤；目录删除/重建后自动迁移注册。600ms 防抖后调用 `MetaDiff`，通过自定义事件 `packgradle:mods-diff` 推送 `prism.ModsWatchEvent`。
- `PrismService.WatchMods() ([]string, error)`：幂等重同步监听集合并返回当前监听项目名；`ServiceStartup` 启动时自动监听所有已关联项目，`ServiceShutdown` 关闭；`LinkProject`/`UnlinkProject`/`SetInstancesPath` 后自动重同步。
- `prism.ModsWatchEvent` DTO（project/side/diff/error）+ `application.RegisterEvent` 注册，`wails3 generate bindings` 后前端 `Events.On` 获得类型推断。
- 测试 `internal/service/mods_watch_test.go`：目录回退与事件匹配、已关联项目扫描、项目侧/实例侧变化的端到端事件发包。
- 顺手修复 `frontend/src/views/DevView.vue`（未跟踪原型）的 `FocusItem.raw` 类型错误，使 `npm run build` 通过。

**关键决策**：
- 文件系统事件必须防抖（一次编辑器保存/refresh 会产生一串 Write/Create），每个项目独立队列串行化差异计算，避免并发重复发包。
- 不轮询：`fsnotify` 监听目录本身；目标目录尚未创建时退而监听最近存在父目录并在事件后重新对齐注册。
- 事件名 `packgradle:mods-diff`，数据为 `prism.ModsWatchEvent`；错误不入独立错误通道，而是放入包的 `error` 字段（避免链路断裂时静默）。

**验证方式**：`go test ./...` 通过；`wails3 generate bindings -f '' -clean=true -ts -i` 生成 39 方法 + 1 事件；`npm run build`（vue-tsc + vite）通过。文档 `docs/backend/01-overview.md`、`02-service-api.md`、`03-packages-toolsets.md`、契约文档已同步。

---

## 2026-08-16 · 「开发版本」页（focus model，前端原型）

**模型**：kimi k3（kimi-k3）

**背景**：用户反馈目录关联功能藏在 Prism 联动页的对话框里操作不便，要求新增「当前开发版本」页面：存放当前项目与实例同步状态、同步目录/文件列表、mod 差异列表（双边有差异时展示，原型用 mock 暂代，后续接 `packgradle:mods-diff` 监听事件），且几个列表不得同页堆叠。

**产出**（前端原型，未提交）：
- `views/DevView.vue`（新建）：focus 项目选择器（页头下拉，已关联项目置顶，`?project=` query 可深链）+ 同步状态横幅（项目 ↔ 实例信息 + 同步目录数 / 差异数 / meta 一致状态 chips）+ 互斥区块切换（v-btn-toggle，「同步目录/文件」与「mod 差异」同一时间只展示一个）+ meta 推送/拉取快捷按钮。选中未关联项目时横幅显示「去关联实例」引导。
- `composables/useDirLinks.ts`（新建）：从 DirLinksDialog 提炼的目录同步管理逻辑（列表/添加/移除/一键关联/手动链接/整目录↔文件级切换/文件选择），`setProject()` 切换目标项目。
- `components/prism/DirLinksDialog.vue`：改为复用 composable，仅保留对话框壳；目前无引用方（联动页入口已跳转 DevView），保留备用。
- `router/index.ts`：新增 `/dev` 路由（`nav.dev`「开发版本」），侧栏位于「项目管理」与「Prism 联动」之间。
- 入口：InstancesView「同步目录」按钮由弹 DirLinksDialog 改为跳转 `/dev?project=X`；ProjectDetailView 头部、ProjectsView 卡片菜单新增「开发版本」入口。
- i18n：`nav.dev` + `dev.*` 共 23 个键。

**关键决策**：差异列表与目录列表用 btn-toggle 互斥展示（遵守「不要同页堆叠」）；diff 数据现走 `mockMetaDiff`，接真实监听时把 `loadDiff()` 换成订阅 `packgradle:mods-diff` 事件即可，页面层零改动。

**验证**：`npx vue-tsc --noEmit` 与 `npx vite build` 均通过。

### 迭代：mod 差异区块 → mod 列表表格（同日，用户反馈）

**反馈**：mod 列表中差异置顶展示，共有 mod 同样显示在下面，页面参考 mod 管理。

**改动**（`views/DevView.vue`）：
- 「mod 差异」页签改为「mod 列表」，由三段 v-list 改为 **v-data-table**（对齐项目详情 ModsTable 的交互：搜索框、行内筛选 chip「仅看差异」、列排序、50/页分页、`filtered/total` 计数）。
- 行模型 `DevModRow` 合并项目 mods 与 MetaDiff 三组差异：`version_diff`（版本差异）→ `instance_only`（实例独有）→ `project_only`（项目独有）→ `common`（一致），**差异置顶、共有 mod 列于其下**，组内按名称排序（用户点击列头后由表格排序接管）。
- 列：mod（名称+id，差异行加粗）、side chip、状态 chip（版本差异 warning / 实例独有 primary / 项目独有 success / 一致 grey-tonal）、项目版本、实例版本（版本差异行高亮 warning；实例独有显示「仅实例侧存在」）、操作（实例独有→拉取、项目独有→推送、版本差异→拉取+推送，一致无操作）。
- i18n：`dev.tabMods/colStatus/colProjectVersion/colInstanceVersion/statusInstanceOnly/statusProjectOnly/statusVersionDiff/statusCommon/onlyDiff/instanceHasMod`，modsTitle/modsHint 文案同步更新。

**验证**：`vue-tsc --noEmit` 与 `vite build` 通过。

### 迭代 2：同步目录区块 → 同步条目表格（同日，用户反馈）

**反馈**：同步列表同样制作表格；文件级目录同步直接展示出同步了哪些文件，不隐藏在二级菜单；目录同步置顶。

**改动**（`views/DevView.vue`）：
- 「同步目录 / 文件列表」由 v-list 改为 **v-data-table**：行模型 `DevSyncRow` 铺平 dirLinks——目录行在前，其文件级同步（mode='files'）的 `files` 清单逐文件成行紧随其后（目录天然置顶，不藏二级菜单/对话框即可看到同步了哪些文件）。
- 列：条目（目录文件夹图标+加粗，文件行缩进 22px 视觉从属）、类型 chip（目录/文件）、模式（整目录/文件级；文件行显示「硬链接」）、同步目标（→ minecraft/<dir>）、操作（目录行保留全套：选择文件/切文件级/切整目录/手动链接/移除；文件行提供「选择同步文件」入口，按 key 找回所属目录打开 FileSelectDialog）。
- i18n：`dev.colEntry/colType/colMode/colTarget/kindDir/kindFile/modeHardlink`；文件行样式 `.file-row` 缩进 + 次级文字色。

**验证**：`vue-tsc --noEmit` 与 `vite build` 通过。

---

## 2026-08-16 · 前后端契约重建：移除 mock 层，前端全面接入真实数据

**模型**：GLM-5.3（ZCode）

### 任务目标

将原型阶段的前端 mock 数据源全部替换为真实 Wails bindings，重建前后端契约；补齐 mock 时代缺失的真实能力（系统文件对话框、首次引导后端判定、mods 监听事件订阅）。

### 产出

**后端（Go）**：
- `appconfig.ConfigManager` 新增 `Exists()`（stat config.toml）/ `EnsureCreated()`（当前内存态落盘）。
- `EnvService` 新增 `ConfigExists()` / `MarkConfigCreated()` 两个绑定方法——mock 时代无后端对应的首次引导判定落地。注意 `Detect()` 检测到工具路径时会回写 config（同样终结首次状态）。
- `internal/service/service_test.go` 补 `TestConfigExistsAndMarkConfigCreated`。
- 重新生成 bindings（`wails3 generate bindings -f '' -clean=true -ts -i`，41 方法）。

**前端（Vue）**：
- 删除 `stores/mock.ts`（约 540 行）；3 个 store、6 个视图、5 个组件、1 个 composable 全部改为直接调用 `PackwizService` / `PrismService` / `EnvService`。
- 新增 `utils/dialogs.ts`：封装 `Dialogs.OpenFile`（pickPackToml / pickToolPath / pickDirectory，取消返回 null）。导入项目（Dashboard/Projects）与设置页工具路径、联动页实例目录的「浏览」均接真实系统对话框。
- OnboardingDialog 接 `EnvService.ConfigExists/MarkConfigCreated`：完成/跳过落盘；goStep 不落盘（未完成配置的用户下次启动继续引导）。
- DevView 订阅 `packgradle:mods-diff` 自定义事件（后端 ServiceStartup 自动开启 fsnotify 监听）：项目/实例 mods 目录变化实时刷新差异。
- 任务中心适配：真实绑定为单次调用无中间进度，抽屉对 `progress <= 0` 的运行中任务显示不确定进度条；去除各调用点 mock 的 `report` 进度回调。
- CF 相关调用失败接 `handleApiKeyError` 分流（Key 缺失/无效 → 全局引导弹窗）；LinkDialog 创建实例补错误提示。
- i18n：新增 `dialogs.*` 三键；移除废弃的 `env.browsePrototypeHint`。

**文档**：契约文档（系统对话框条目）、后端 API 参考（EnvService 7 方法）、前端 overview/routes/components/stores 文档、开发指南 §4（回切任务改写为直连约定）、docs/README 同步为「真实数据直连」状态。

### 验证

- `npx vue-tsc` 零错误；`vite build`（production）成功。
- `go build ./...` + `go vet` 通过；`go test ./internal/...` 全绿（含新增用例）。
- grep 确认 `frontend/src` 无任何 mock 残留引用。
- 未实机跑 GUI（需真人交互文件对话框）；建议按 docs/development/02-frontend.md §4.1 必测清单用真实项目（C:\project\Project-Collapse）过一遍。

---

## 2026-08-15 · 持久化项目代码与文档子 Agent

**模型**：GPT-5.6 Terra（medium）

**任务目标**：将项目代码与文档专用子 Agent 保存为仓库级 Codex 配置，供后续会话复用。

**关键决策**：使用 `.codex/agents/project-code-docs.toml` 定义 `project_code_docs`；限定为 `workspace-write`，只处理本仓库代码与文档，并固化生成文件、错误码、命令行环境和验证约定。

**产出物**：`.codex/agents/project-code-docs.toml`。

**验证方式**：检查 TOML 必需字段、模型与推理强度，并确认文件位于 Codex 支持的项目级 `.codex/agents/` 目录。

---

## 2026-08-15 · 前端状态与异步操作一致性修复

**模型**：GPT-5（Codex）

**任务目标**：按 `oil-frontend` 检查当前 Vue/Vuetify 前端，修复共享数据刷新、异步确认、错误反馈和页面遮挡问题。

**关键决策**：
- 强制刷新在已有请求进行中时排队，并让调用方等待最新结果，不再静默后台刷新。
- 确认操作在请求完成并刷新可见状态前保持弹窗、对象和输入；失败信息留在原确认框内，可直接重试。
- 任务中心统一使用结构化错误翻译，并在失败时保留 CLI 输出。
- 批量结果使用结构化计数判断 warning，不解析本地化文案；更新检查用请求代次阻止旧响应覆盖新上下文。
- 初次加载失败显示可重试错误态，不再把失败解释为空集合；开发页按项目隔离请求代次并保留同项目旧快照。

**产出物**：修改共享任务中心与缓存 store、`ConfirmDialog`、项目/开发版本/Prism 联动相关页面和组件；恢复表格分页，固定异步操作对象；删除重复导航 tooltip 与全局 `.mt-2` 覆盖。

**验证方式**：`vue-tsc --noEmit` 通过；Vite production build 通过；`git diff --check -- frontend/src AGENT.md` 通过；Mock 模式下走查工作台、项目页、项目详情、开发版本页、Prism 联动、设置页与确认框，覆盖 1440×900、820×760、600×760、360×720，未发现页面横向溢出；快速关闭并重开更新检查、快速往返切换开发项目后，最新请求结果与当前项目一致；实例目录编辑器能从现有路径初始化。

---

## 2026-08-16 · 重新启用 Mock 层 + 一键切换真实/模拟后端

**模型**：GLM-5.3（ZCode，builtin:bigmodel-start-plan）

**任务目标**：重新启用前端 mock 层，并提供手动一键切换 mock 与实际后端业务的能力。

**关键决策**：
- 采用**门面 + Proxy 运行时分发**而非改生成文件或 import 分支：新建 `frontend/src/api/index.ts`，导出与真实绑定同类型的 `EnvService`/`PackwizService`/`PrismService`，每次属性访问时按 localStorage `packgradle.mock` 开关挑选实现；mock 模式下缺失方法访问即抛 `[mock] 未实现 X.Y`，不会静默打到真实后端。
- 前端 15 个文件（3 store + 1 composable + 4 view + 6 组件 + utils）的 service 值导入统一改到 `src/api`；`import type` 仍指向 bindings（类型零改动）。
- `frontend/src/mocks/`：`fixtures.ts` 内存数据库（2 个正常项目 + 1 个解析失败项目、3 个实例、2 条关联、5 条目录同步含文件级模式、5 处 meta 差异、更新检查结果）+ 三服务全部 42 个方法的模拟（含模拟延迟）；**写操作真实变更内存库**（添加关联/拉取 meta/应用更新在后续读取中立即可见），错误以错误码 JSON 文本返回（displayText 可翻译）。
- 一键切换入口：设置页新增「开发者选项」卡片（v-switch + ConfirmDialog 确认后果 → 写 localStorage → 刷新页面，因各 store 持有缓存）；顶栏新增紫色 MOCK 徽标（仅启用时显示，点击一键确认切回）。`utils/dialogs.ts` 在 mock 模式跳过系统文件对话框返回模拟路径（配合 mock ImportProject）。

**产出物**：`frontend/src/api/index.ts`、`frontend/src/mocks/{fixtures,envservice,packwizservice,prismservice}.ts`；修改 15 个 service 导入文件、SettingsView（开发者选项卡片）、App.vue（MOCK 徽标）、utils/dialogs.ts、locales/zh-CN.json（settings.devTitle + mock.* + app.mockBadgeTip 共 12 键）。

**验证方式**：`vue-tsc --noEmit` 零错误；`npm run build`（production）成功。浏览器实机走查（vite 直开 9245）：设置页开关 → 确认框 → 刷新后 mock 生效（顶栏 MOCK 徽标、工具检测/API Key 显示模拟值）；项目管理页三个 mock 项目含解析失败态与 loader chips 正常；开发版本页状态横幅（同步目录 4 / 5 处差异）、目录同步表（文件级铺平 + 整目录混合）全部渲染正确；截图视觉确认。纯浏览器下 `Events.On` 为纯内存注册不抛错，mock 模式可脱离 Wails 运行。

---

## 2026-08-22 · PackGradle 目标底层架构设计

**模型**：GPT-5.6（Codex）

**任务目标**：根据重新设计讨论记录，产出一份不受旧 Service、Store、路由和 Junction 同步模型路径依赖影响的前后端重写架构设计，不实施生产代码重构。

**关键决策**：Relation 作为聚合根；Project/Runtime 使用稳定 opaque ID；同步核心采用 LogicalResource、ObservedSnapshot、SyncBaseline、SyncPlan、SyncCommit；写入遵循 Scan、Diff、Prepare、Resolve、Confirm、Apply、Verify、Commit；SQLite 为唯一元数据权威，SHA-256 CAS 为可恢复内容权威，全部本机状态位于用户数据目录；MVP 只保证 copy-based 隔离同步，Junction/硬链接延后为可选共享物化能力；新架构采用重写式演进，旧实现只作为格式知识、fixture 和风险样本。

**产出物**：`docs/architecture/packgradle-architecture-redesign.md`，包含领域模型、分层与端口、MappingPolicy、三方 Diff、计划/执行 journal、冲突与恢复、SQLite/CAS schema、任务与事件契约、前端信息架构、阶段验收、迁移弃用和 ADR 基线。

**验证方式**：后端契约与前端产品两个项目子代理完成两轮只读复核；修正 verified snapshot、partial Commit、Plan/Restore 确认闭环、崩溃恢复、binding/content fingerprint、事件重连、Feature/availability 与跨 Relation 存储不变量；Markdown 代码围栏数量成对，未修改任何生产代码或生成文件。

## 2026-08-22 · PackGradle 重写实施路线

**模型**：GPT-5（Codex）

**任务目标**：根据目标底层架构设计，明确当前工程任务、需求基线、开放决策、分阶段实施步骤、依赖关系和验收条件。

**关键决策**：将当前工程目标定义为“Phase 0 工程基座 + Phase 1 MVP 只读核心”；采用新 core/application/adapters/store/transport 边界，旧 `internal/service`、旧前端路由/Store 和 Junction 同步语义进入冻结与迁移阶段；Phase 2 之前不开放文件写入、Apply、Restore 或自动合并。

**产出物**：`docs/architecture/packgradle-implementation-roadmap.md`，并更新 `docs/README.md` 架构文档索引。

**验证方式**：核对路线文档覆盖架构固定决策、领域对象、SQLite/CAS、Scan/Diff/Plan、任务事件、前端信息架构、Phase 1-4 步骤、退出条件和禁止事项；未修改生产代码或生成 bindings。

## 2026-08-22 · PackGradle P0/P1 实现检视

**模型**：GPT-5（Codex）

**任务目标**：检视 GLM5.3 完成的 P0/P1 新架构代码，按 Phase 0/Phase 1 退出条件确认实际完成度，并列出下一步需求。

**关键决策**：确认后端新栈已具备可运行的 core/store/adapter/application/transport 基础，但完整 P1 尚未完成；阻塞项包括新栈未接管前端、旧写入口仍可用、跨 Relation 存储约束不足、路径/绑定安全不足、新错误缺少 locale、Changes/Features/Rebind 等契约缺失。Phase 2 文件写入暂不应启动。

**产出物**：`docs/architecture/packgradle-p0-p1-review.md`，并更新 `docs/README.md` 架构索引。

**验证方式**：`go test ./...`、`go vet ./...`、`vue-tsc --noEmit` 和 Vite production build（runner config loader）通过；`go test -race` 因环境未启用 CGO 未执行；静态检视覆盖新 core/application/adapters/store/transport、main 装配、前端路由/API/locale 和 P1 headless 测试。

## 2026-08-22 · 后端单独验收范围确认

**模型**：GPT-5（Codex）

**任务目标**：确认在前端延后重构的前提下，P0-1/P0-2 是否仍属于后端实现阻塞项。

**关键决策**：P0-1（新栈接管前端、隐藏 legacy 写入口）延期为前端切换门槛，不阻塞后端 P0/P1；P0-2 拆分处理，locale 文案延期，但后端结构化 `err.*` code/args/detail 契约必须现在固定，禁止返回面向用户的中文文案或随意改码。后端仍优先处理数据完整性、路径/绑定安全、Policy collision、Plan exactness 和 preparation 原子性。

**产出物**：更新 `docs/architecture/packgradle-p0-p1-review.md`，新增后端单独验收口径。

**验证方式**：确认新增范围与目标架构的 transport/application/core 边界一致；未修改生产代码。

## 2026-08-22 · 后端重构 P0+P1（新架构只读核心落地）

**模型**：GLM-5.3（ZCode，builtin:bigmodel-start-plan；feature-dev 子代理探索/蓝图/审查 + 三个 general-purpose 子代理并行实施）

**任务目标**：按 `docs/architecture/packgradle-architecture-redesign.md` 与 `packgradle-implementation-roadmap.md`，执行后端重构 P0（Step 0 工程基座）+ P1（Step 1~6 只读核心），旧 `internal/service` 冻结为 legacy，新栈与 legacy 并存注册。

**关键决策**：
- 开放 ADR 落定（docs/architecture/packgradle-rewrite-boundaries.md §5）：SQLite driver = modernc.org/sqlite（纯 Go 无 CGO）；CAS P1 不做 GC；mod identity = modrinth mod-id > curseforge project-id > 路径回退（低置信），跨侧匹配唯一通道 = filename hint；default-v1 仅 mods 规则，config/kubejs 等为建议模板。
- 核心不变量实现：core 仅标准库；snapshot/plan/baseline digest 用自研 CanonicalJSON（map 键排序、无浮点），排除 id/时间/scanner/诊断/绑定指纹；低置信度身份不参与跨侧等价判定；P1 无 Apply/Restore 入口；本机状态全部在用户数据目录（ADR-009）。
- 任务终态写库用 context.WithoutCancel（审查修复）：用户取消后 ctx 已失效，直接用它落 cancelled/failed 会留下永久 running 僵尸任务并锁死该 Relation 的扫描；另加启动 reaper（RecoverInterruptedTasks）清扫中断遗留任务。
- 已认可偏离：relations.head_* 两列无 FK（SQLite 循环依赖，repository 层校验）；plan.Resolve 经 Conflict.Detail 携带方向过滤标记（不参与 digest）；Task.Update 乐观锁用 sequence<? 语义。

**产出物**：
- 新模块树：internal/{core(model/ids/normalize/diff/plan), application(ports/task/policy/view/sync), adapters(filesystem/packwiz/prism), store(paths/sqlite/objectstore), transport(dto/convert/syncservice/events), bootstrap}；cmd/pgheadless；internal/service/legacy.go 冻结标记。
- SQLite schema v1（21 表，user_version 迁移 + VACUUM INTO 备份门禁）+ SHA-256 CAS + 可丢弃 hash cache。
- transport.SyncService（12 方法）注册进 main.go 与 legacy 并存；事件统一 topic `packgradle://event`（EventEnvelope）；bindings 再生成（4 服务 52 方法 2 事件）。
- 文档：docs/architecture/packgradle-rewrite-boundaries.md（Step 0 交付物）、docs/backend/04-new-core-architecture.md、docs/README 索引更新。

**验证方式**：`go build ./...`、`go vet ./...`、`go test ./...` 全绿（22 个测试包）；headless 集成测试覆盖全链路 PrepareRelation→CreateRelation→StartScan→PrepareSync→ResolvePlan→GetPlan 与 §12.2 不变量（重复扫描同 digest、删 hash cache 重建、重复 Prepare 不写端点、相同输入同 plan digest、rebind/revision/side 错误契约、僵尸任务恢复）；code-reviewer 审查发现 2🔴2🟠 已全部修复并补回归测试；`wails3 generate bindings` 通过。审查遗留 🟡 5 项（workspace diff_state 不感知 ignore、规则重叠 mapping_collision 语义、.index 版本优先级不对称、端点缺失检测弱、resolvePartial 单级回退）记录待 Phase 2 前处理。

## 2026-08-22 · 工作区 UX 交互原型设计

**模型**：GPT-5（Codex）

**任务目标**：依据目标底层架构设计一份可直接用于制作桌面 UX 原型稿的 Markdown，覆盖新 Workspace 信息架构、核心任务流、页面布局、状态、文案、能力门控和原型画板清单。

**关键决策**：以工作区作为唯一跨端操作入口；Phase 1 只呈现登记、扫描、变化和同步分析，不为 Apply、History、Restore 等未来能力保留禁用占位；Phase 2/3 作为独立能力变体；Plan 保持不可变，冲突草稿按 `plan_id` 隔离；事件只触发刷新，Task/Workspace 查询继续作为权威；恢复处理建议采用完整页面，但正式路由仍需架构决策。

**产出物**：新增 `docs/frontend/05-workspace-ux-prototype.md`，包含用户任务与术语、导航、桌面壳层、创建/扫描/计划/重绑/历史/恢复流程、12 类页面规格、共享组件、状态矩阵、示例数据、34 个必做画板和应用用例映射；更新 `docs/README.md` 前端文档索引。

**验证方式**：逐项核对目标架构 §3、§5-§10、§13 与实施路线 Phase 1-3；检查全部目标路由、正交 Workspace 状态、Plan/Task 状态、Feature/ActionAvailability 和阶段隐藏规则均有对应原型说明；执行 Markdown 结构、链接、代码围栏和 `git diff --check` 检查；未修改生产代码或生成 bindings。
