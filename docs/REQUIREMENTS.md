# PackGradle 需求文档

> 版本：1.1（2026-08-12）
> 状态标注：✅ 完整实现 ／ ⚠️ 部分实现 ／ ❌ 缺失 ／ ⛔ 不在开发范围内
> 本文档覆盖 README 全部愿景，逐条标注完成状态并给出实现位置与验收要点，作为后续架构设计与实现的依据。
>
> 更新记录：
> - v1.1：修正 REQ-3.1（区分 Prism 安装路径检测 ✅ 与实例路径检测 ❌）；REQ-4.9 CurseForge 搜索标注 ⛔ 不在开发范围内；路线图重排（Prism 联动上调 P0，其余顺延）
> - v1.0：初版

---

## 1. 项目概述

**定位**：便于整合包作者构建 packwiz 与 Prism Launcher 联动开发环境的桌面工具（Windows 优先）。

**技术栈**：

| 层 | 技术 |
|---|---|
| 桌面框架 | Wails 3（`v3.0.0-beta.7`），`go:embed frontend/dist` |
| 后端 | Go 1.25，`BurntSushi/toml`（配置解析），`golang.org/x/sys`（Windows registry） |
| 前端 | Vue 3.5 + TypeScript 5.9 + Vite 8 + Vuetify 4（dark）+ vue-i18n 11 + `@wailsio/runtime`，yarn |
| 运行 | `wails3 dev -config ./build/config.yml -port 9245`；bindings 由 `wails3 generate bindings -ts -i` 生成 |

**核心设计原则**（client/server 分离）：Go 端零用户可见文案，只返回错误码（`errs.AppError`）；前端 i18n 渲染。

---

## 2. 架构总览

### 2.1 后端 `internal/` 六包

| 包 | 职责 | 关键文件 |
|---|---|---|
| `appconfig` | `%AppData%\PackGradle\config.toml` 配置管理（projects / packwiz_path / prism_path / curseforge_api_key），读缺文件容忍、写原子（临时文件+rename） | `config.go`、`tomlfile.go` |
| `envutil` | Windows 环境工具：可执行文件查找链、用户级 PATH 写入（HKCU registry + WM_SETTINGCHANGE 广播）、`%VAR%` 展开 | `path.go` |
| `errs` | 结构化错误 `AppError{code, args, detail}`，`Error()` 返回与 MarshalError 一致的 JSON | `errs.go` |
| `packwiz` | packwiz 交互层（纯函数、无状态）：CLI 子进程（CREATE_NO_WINDOW + HideWindow 隐藏控制台）、pack.toml/index.toml 解析、update 输出正则解析 | `cli.go`、`parse.go`、`update.go`、`types.go` |
| `curseforge` | CurseForge API 客户端（`GET /v1/mods/{id}/files/{id}`，x-api-key，15s 超时）+ 项目内 `.cache/modversion.cache` 缓存（TOML 原子写、并发 Upsert、Prune） | `client.go`、`cache.go` |
| `service` | Wails 绑定服务层（`EnvService` / `PackwizService`，共 13 个方法） | `envservice.go`、`detect.go`、`packwizservice.go`、`curseforceservice.go` |

### 2.2 前端结构

```
frontend/src/
├── App.vue              布局（app bar + 导航抽屉）+ 视图切换（v-if，无路由）
├── nav.ts               跨视图导航状态（currentView + navigate()，无 Pinia）
├── i18n.ts              vue-i18n 初始化，导出全局 t 供非组件模块使用
├── locales/zh-CN.json  唯一语言文件（119 键，含 28 条 err.* 翻译）
├── composables/         useSnackbar.ts、useApiKeyGuide.ts（API Key 缺失引导弹窗）
├── utils/               errors.ts（统一错误解析）、cf.ts（颜色/日期/发布类型）
└── views/               EnvView.vue、ProjectsView.vue
```

### 2.3 关键机制

- **错误契约**：Go 抛 `AppError{code, args, detail}` → `main.go` `MarshalError` 序列化 → 前端 `err.cause` 读取；或经 `Error()` JSON 文本落入数据字段（`PackProject.error` / `RefreshResult.output`）。前端 `utils/errors.ts` 双路径解析：`errText(e)` 读 `e.cause`，`displayText(s)` 对文本尝试 JSON.parse 识别错误码，否则原样返回（packwiz CLI 原生输出）。
- **数据流**：`ProjectsView` → bindings `PackwizService.ListProjects()` → `config.Get().Projects` → `packwiz.ParseProject`（pack.toml + index.toml 权威 mod 扫描）→ `applyCfCache` 回填缓存版本 → JSON 渲染项目卡片 + mod 表格。
- **mod 扫描权威来源**：index.toml 的 `[[files]]` 中 `mods/` 条目（读取 `mods/<id>.pw.toml`）；无 index.toml 时回退旧式 `mods/<name>/pw.toml` 目录扫描；索引有条目但文件缺失时保留条目展示。
- **版本缓存**：每项目独立 `.cache/modversion.cache`，键 `"projectID:fileID"`，原子写防损坏；更新应用后 `refreshCfCacheAfterUpdate` 剪除旧 file-id/孤儿条目并自动重取变化条目。
- **更新检查**：`packwiz update --all` 喂 stdin `"n\n"` 只列不应用；`update.go` 四个正则依次匹配 pinned 跳过 / 无更新源 / 检查失败 / `Name: old -> new` 更新行。

---

## 3. 功能需求

### F1 一键环境配置

| 编号 | 需求 | 状态 | 实现位置 | 验收要点 |
|---|---|---|---|---|
| REQ-1.1 | 检测 packwiz / Prism Launcher 装载状态（四级查找链：config → 环境变量 → PATH → 默认目录，自动回写 config） | ✅ | `internal/service/detect.go`、`envservice.go:Detect`；前端 `EnvView.vue` | 卡片展示 found / env_ok / source 四类来源 |
| REQ-1.2 | 一键将工具目录写入用户级系统 PATH（幂等、`%VAR%` 展开、广播刷新） | ✅ | `internal/envutil/path.go:AddDirsToUserPath`；`EnvView.vue:configure` | 重复执行不产生重复条目 |
| REQ-1.3 | 工具路径手动编辑/浏览/保存（空串清除） | ✅ | `envservice.go:SetToolPath`；`EnvView.vue`（Dialogs.OpenFile） | 保存后重新检测生效 |
| REQ-1.4 | CurseForge API Key 输入（可显隐）/保存/清除 | ✅ | `envservice.go:GetApiKey/SetApiKey`；`EnvView.vue` | 持久化到 config.toml |

### F2 packwiz 项目管理

| 编号 | 需求 | 状态 | 实现位置 | 验收要点 |
|---|---|---|---|---|
| REQ-2.1 | 导入 pack.toml 路径注册项目（同名覆盖） | ✅ | `packwizservice.go:ImportProject`；`ProjectsView.vue` | 成功后展开新项目卡片 |
| REQ-2.2 | 项目列表（名称/加载器/版本/作者/mod 数），解析失败项目带错误展示 | ✅ | `packwizservice.go:ListProjects`；`ProjectsView.vue` | 失败项目显示错误文本不崩溃 |
| REQ-2.3 | 移除项目（仅注册表，不动磁盘文件，确认弹窗） | ✅ | `packwizservice.go:RemoveProject`；`ProjectsView.vue` | ⚠️ 吞写错误见 GAP-7 |
| REQ-2.4 | 项目解析：pack.toml 元数据 + index.toml 权威 mod 扫描（含旧式回退、缺失条目保留） | ✅ | `internal/packwiz/parse.go` | 单测覆盖（parse_test.go） |
| REQ-2.5 | **创建项目**（packwiz init：名称/版本/加载器/MC 版本引导） | ❌ | 无 | 全链路缺失 |
| REQ-2.6 | **添加 mod**（packwiz add：搜索/指定源 URL） | ❌ | 无 | 全链路缺失 |
| REQ-2.7 | **移除 mod**（packwiz remove） | ❌ | 无 | 全链路缺失 |
| REQ-2.8 | 项目刷新（`packwiz refresh`） | ✅ | `packwizservice.go:RefreshProject` → `packwiz.RunRefresh` | 输出对话框展示 |

### F3 双边同步（Prism Launcher 联动）

| 编号 | 需求 | 状态 | 实现位置 | 验收要点 |
|---|---|---|---|---|
| REQ-3.1 | 检测 Prism Launcher **安装路径** | ✅ | `detect.go:detectPrism`（安装目录扫描） | 查找链检测，自动回写 config.toml |
| REQ-3.2 | 检测 Prism **实例路径**（自动定位实例根 + 扫描 `instances/` 列表） | ✅ | `internal/prism/prism.go`（便携 cfg 解析/APPDATA 回退）、`instance.go`（mmc-pack.json/instance.cfg/instgroups 解析）、`service/prismservice.go:ListInstances`；前端 `PrismView.vue` | 7 个真实实例全部解析正确；坏实例 Error 内嵌不中断；手动指定实例根目录随 Phase 2 提供 |
| REQ-3.3 | 将 packwiz 项目对应到 Prism 实例（关联流程：扫描匹配 → 无匹配时询问 → 手动关联 / **程序创建实例**；关联持久化 `[[links]]`，目录同步关联 `[[dir_links]]`） | ⚠️ | `prismservice.go:LinkProject/UnlinkProject/GetLinks/CreateInstance`、`prism/create.go`（instance.cfg + mmc-pack.json 程序创建）；前端关联对话框（同名自动匹配 + 创建实例按钮）；目录关联管理界面（候选 = 项目顶层目录，排除 mods/隐藏目录） | 关联与目录关联管理完成；junction 建链/状态属 REQ-3.4（Phase 3） |
| REQ-3.4 | 根据配置创建 Junction 同步项目文件更改 | ❌ | 无 | 零实现 |
| REQ-3.5 | prism ↔ packwiz 相互 meta 拉取（更新项目 mod） | ❌ | 无 | 零实现 |

### F4 meta 优化

| 编号 | 需求 | 状态 | 实现位置 | 验收要点 |
|---|---|---|---|---|
| REQ-4.1 | pw.toml 列表可视化：mod 表格 + side 芯片（client/server/both 中文标签+颜色） | ✅ | `ProjectsView.vue` 表格；`utils/cf.ts` | 只读展示 |
| REQ-4.2 | **side 标识编辑** | ❌ | 无（仅 `normalizeSide` 读取归一化） | 只读，无编辑入口 |
| REQ-4.3 | 单 mod 获取 CurseForge 版本并写缓存 | ✅ | `curseforceservice.go:FetchModVersion`；`ProjectsView.vue:117` | 仅 CF 源 mod 显示按钮 |
| REQ-4.4 | 批量获取全部 CF 版本（并发 8、信号量限流） | ✅ | `curseforceservice.go:FetchAllModVersions`；`ProjectsView.vue:131` | ⚠️ 无进度事件（GAP-14） |
| REQ-4.5 | 版本列展示（本地 version 优先；displayName 与文件名相同时显示发布日期；发布类型正式/测试/Alpha） | ✅ | 版本列渲染逻辑 + `utils/cf.ts` | 避免两列重复 |
| REQ-4.6 | 更新检查（`packwiz update --all` 喂 "n" 只列不应用，解析 4 种输出形态） | ✅ | `packwiz/update.go` + `cli.go:RunCheckUpdates`；`ProjectsView.vue:155` | 依赖 packwiz 英文提示格式（GAP-15） |
| REQ-4.7 | 应用全部更新（`--all -y`）并重建版本缓存 | ✅ | `curseforceservice.go:UpdateMods` + `refreshCfCacheAfterUpdate`；`ProjectsView.vue:174` | 缓存剪除陈旧条目 |
| REQ-4.8 | **应用单个 mod 更新**（`packwiz update <name>`） | ⚠️ | 后端 `UpdateMods(projectName, modName)` 非空分支已实现；**前端无 UI（死路径，GAP-3）** | 单行更新按钮缺失 |
| REQ-4.9 | **CurseForge 搜索**（`/v1/mods/search`） | ⛔ 范围外 | 无 | 明确不纳入开发范围（仅存在 Get Mod File 端点，勿规划） |
| REQ-4.10 | **导出/构建 modpack**（packwiz export/serve/打包） | ❌ | 无 | 零实现 |
| REQ-4.11 | API Key 缺失/无效引导（弹窗跳转配置页） | ✅ | `composables/useApiKeyGuide.ts`；`err.cf.api_key_missing` / `err.cf.unauthorized` 分流 | 引导流程闭环 |

---

## 4. 非功能需求

| 编号 | 需求 | 状态 | 说明 |
|---|---|---|---|
| NFR-1 | 错误处理：client/server 分离、错误码全覆盖 | ✅ | 26 个代码内错误码调用，28 条 `err.*` 翻译键 100% 对齐；`err.cause` 与文本双路径解析 |
| NFR-2 | i18n 多语言 | ⚠️ | 架构按 `{code, args}` 设计可扩展，但仅 zh-CN 一份文件、locale 硬编码、无切换 UI、无 en-US |
| NFR-3 | 健壮性 | ⚠️ | `main.go:37` config.toml 解析失败直接 `log.Fatalf` 启动崩溃，无降级/恢复；更新检查解析依赖 packwiz 英文提示格式 |
| NFR-4 | 性能 | ⚠️ | `FetchAllModVersions` 每 mod 全量缓存文件原子重写（Upsert 读-改-写）IO 放大；`FetchedAt` 只写不读，缓存无 TTL；批量获取无进度事件，长任务只有 loading |
| NFR-5 | 平台 | ⚠️ | `envutil/path.go`、`cli.go` 为纯 Windows 代码，但 Taskfile 仍挂载 darwin/linux/ios/android 构建目标（必然失败）；ios/android 无意义 |
| NFR-6 | 打包配置 | ⚠️ | `build/config.yml` 为模板占位值（companyName "My Company" 等未定制）；`build/windows/info.json` 待核实 |
| NFR-7 | 测试 | ⚠️ | Go 单测覆盖良好（appconfig/envutil/errs/packwiz/curseforge/service 均有 _test.go）；**前端零测试** |
| NFR-8 | 安全 | ⚠️ | `.npmrc` 设 `minimum-release-age=10080` 防供应链攻击；API Key 明文存 config.toml，无加密 |

---

## 5. 已知缺口与风险清单（GAP）

| 编号 | 缺口 | 证据 | 影响 |
|---|---|---|---|
| GAP-1 | **Prism 联动整体缺失**（实例路径检测/实例对应/Junction/meta 拉取）——README 核心卖点 | README:9-10 vs 代码零实现（仅安装路径检测） | 产品方向性缺失 |
| GAP-2 | **mod 生命周期闭环断裂**（创建项目/添加/移除/搜索/导出） | 无 packwiz init/add/remove、无 `/v1/mods/search` | 工具是"只读管理器+更新器" |
| GAP-3 | 单 mod 更新为死路径 | `curseforceservice.go:125` 非空分支无前端调用（`ProjectsView.vue:174` 仅传空串） | 后端能力闲置 |
| GAP-4 | `CfCacheStore.Save` 生产零调用 | `cache.go:47` | 死代码 |
| GAP-5 | `FetchedAt` 只写不读 | `client.go:31,94` | 缓存永久有效，无 TTL 策略 |
| GAP-6 | `ModVersionResult.Error` 前端不渲染 | `ProjectsView.vue:131-133` 只统计 ok 数 | 部分 mod 失败对用户不可见 |
| GAP-7 | `RemoveProject` 吞掉 config 写入错误 | `packwizservice.go:62`（`_ =`） | 失败静默 |
| GAP-8 | config.toml 损坏直接崩溃 | `main.go:37` `log.Fatalf` | 启动失败无恢复 |
| GAP-9 | 平台锁定与构建目标脱节 | Taskfile.yml 挂载 darwin/linux/ios/android | 跨平台构建必然失败 |
| GAP-10 | 模板残留未清理 | `public/style.css`（neon-night/greet/拖拽样式）、Wails 事件 bindings 未使用 | 前端包袱 |
| GAP-11 | 测试用导出残留生产代码 | `curseforge.BaseURL()/SetBaseURL`（`client.go:17-24`） | 接口膨胀 |
| GAP-12 | API Key 明文存储 | config.toml 直接保存 | 安全隐患 |
| GAP-13 | 前端零测试 | 无任何前端测试文件 | 回归风险 |
| GAP-14 | 批量获取无进度事件 | `FetchAllModVersions` 无事件推送 | 长任务 UX 差 |
| GAP-15 | 更新检查依赖交互式喂 "n" 与英文提示格式 | `cli.go:33` | 脆弱，packwiz 输出变更即破坏 |

---

## 6. 下一步路线图

按优先级排列候选方向（每轮选择一个方向设计架构并实现）：

- **P0 · Prism Launcher 联动**（README 核心卖点，本轮优先）：实例路径检测（扫描 Prism 安装目录 `instances/`，补 REQ-3.2）+ 实例管理（列表/选择）+ 项目-实例对应 + Junction 同步 + 双向 meta 拉取。工程量最大，需独立设计文件系统同步方案与冲突处理，建议分阶段（先实例检测与对应，再 Junction，最后 meta 拉取）。
- **P1 · mod 生命周期闭环**（原 P0 顺延）：项目创建（packwiz init 引导向导）+ 添加 mod（packwiz add，URL/文件源；CF 搜索不在范围，见 REQ-4.9）+ 移除 mod（packwiz remove）。复用现有 `packwiz/cli.go` 子进程模式、index.toml 权威扫描，打通「创建 → 添加 → 管理 → 更新」闭环。架构提示：CLI 层新增 `RunInit/RunAdd/RunRemove`，前端新增向导/对话框；错误码沿用 `err.*` 契约。
- **P2 · 体验与健壮性补全**（原 P1 顺延）：单 mod 更新按钮（打通 GAP-3）、批量获取进度事件（Wails Events，替换 GAP-14 loading）、缓存批量 Upsert 合并写（GAP-4 顺带消除）、config 损坏降级（GAP-8）、en-US 语言文件与切换器（NFR-2）。
- **P3 · 收尾项**：导出/构建 modpack、side 编辑（REQ-4.2）、打包配置定制（NFR-6）、清理模板残留（GAP-10）与死代码（GAP-4/11）。
