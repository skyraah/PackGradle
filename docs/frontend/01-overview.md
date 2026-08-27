# 前端总览与目录结构

## 1. 技术栈

| 层 | 技术 | 版本 |
| --- | --- | --- |
| 框架 | Vue 3（`<script setup>` + TS） | ^3.5 |
| 构建 | Vite + @vitejs/plugin-vue + Wails Vite 插件 | ^8.2 / ^6.0 / wails beta.7 |
| UI | Vuetify 4（深色为主，支持系统主题） | ^4.1 |
| 路由 | vue-router（**hash 模式**） | 4 |
| 国际化 | vue-i18n（Composition，zh-CN 唯一语言包） | ^11 |
| 图标 | @mdi/font | ^7 |
| 类型检查 | vue-tsc | ^3 |
| Wails | @wailsio/runtime（Call / Events / Window） | latest |

`frontend/package.json` 脚本：

```bash
yarn dev         # Vite dev server（端口 9245，strictPort）
yarn build:dev   # vue-tsc + vite build（不压缩，development 模式）
yarn build       # vue-tsc + vite build（production）
yarn preview     # 预览 dist
```

## 2. 目录结构

```
frontend/
├── index.html                入口 HTML（#app）
├── vite.config.ts            Vite 配置：127.0.0.1:9245 + vue + wails("./bindings") 插件
├── tsconfig.json             strict TS；include 覆盖 src 与 bindings
├── bindings/                 Wails 自动生成的 Go↔TS 绑定（勿手改）
│   ├── github.com/wailsapp/wails/v3/internal/
│   └── packgradle/internal/
│       ├── packwiz/          PackProject 等模型 + 索引
│       ├── prism/            Instance 等模型 + 索引
│       └── service/          EnvService / PackwizService / PrismService 调用函数
└── src/
    ├── main.ts               createApp(...).use(i18n).use(vuetify).use(router)
    ├── App.vue               应用壳层：顶栏 + 68px icon rail + 路由出口 + 全局弹层
    ├── i18n.ts               vue-i18n 实例；导出全局 t() 供非组件模块使用
    ├── vite-env.d.ts
    ├── assets/main.css       全局样式（拖拽区 / 滚动条 / 文本选择 / 主题变量）
    ├── plugins/vuetify.ts    Vuetify 主题与默认值
    ├── router/index.ts       路由表 + navRoutes + scrollBehavior
    ├── views/                5 个页面（路由懒加载）
    ├── components/
    │   ├── common/           7 个通用组件
    │   ├── projects/         2 个项目域组件
    │   └── prism/            4 个 Prism 域组件
    ├── stores/               模块级共享状态（无 Pinia，直接用 ref 模块）
    ├── utils/                errors.ts / dialogs.ts / cf.ts
    └── locales/zh-CN.json    唯一文案来源（414 个键）
```

## 3. 架构与数据流

```
views/components ──调用──▶ stores（共享缓存）──调用──▶ frontend/bindings/.../service/*.ts
      │                                                            │ $Call.ByID
      │ 读缓存 / watch version                                     ▼
      └──▶ utils/errors.ts ◀── 结构化错误（err.cause）        Go service 方法
```

**当前状态**：mock 层（`stores/mock.ts`）已移除，前后端按 [通信契约](../contract/01-binding-and-error-protocol.md) 直连——stores/views/components 直接调用生成的 bindings，错误走 `utils/errors.ts`，长任务走任务中心（单次调用无中间进度，抽屉对运行中任务显示不确定进度条）。文件/目录选择经 `utils/dialogs.ts`（`Dialogs.OpenFile`）。

## 4. 应用壳层（App.vue）

- **无边框窗口自绘标题栏**：`@wailsio/runtime` 的 `Window.IsMaximised/Minimise/ToggleMaximise/Close`；`Events.On(WindowMaximise / WindowUnMaximise)` 跟踪最大化状态；顶栏中段 `app-drag` 区域为 Wails 拖拽区。
- **左侧 icon rail**：由 `navRoutes` 的 `meta` 自动生成（titleKey + icon），项目详情页归属 `/projects` 高亮。
- **顶栏状态**：
  - 联动状态 chip：`instances.overview.locate_error` 为空 → 绿色「联动正常」，否则灰色「未连接」，点击跳 `/instances`。
  - 任务中心 bell：`taskCenter.runningCount / unseenCount` 角标。
  - 主题切换按钮：在 Vuetify `dark/light` 间切换；挂载时读取 `prefers-color-scheme`。
- **路由出口**：`<keep-alive include="ProjectsView">` 保持项目列表搜索/滚动状态。
- **全局弹层**：`TaskCenterDrawer`、`OnboardingDialog`、API Key 引导 dialog、`ui` snackbar（队列式）。

## 5. 全局约定

| 约定 | 说明 |
| --- | --- |
| 文案 | 只放 `src/locales/zh-CN.json`；服务端只回错误码，由 `utils/errors.ts` 渲染。 |
| 错误处理 | `errText(e)` / `displayText(s)` 双路径解析；API Key 错误码弹全局引导框（`stores/apiKeyGuide.ts`），其余 snackbar。 |
| 确认交互 | 一律用自定义 `ConfirmDialog`（Wails 原生 Question 在构建版会挂起）。 |
| 写操作 | 统一 `runTask()` 走任务中心：确认 → 执行（进度）→ 结果驻留 → 可追溯。snackbar 只做轻量提示。 |
| 跨视图刷新 | `projectsVersion`（`bumpProjectsVersion()`）通知 + `invalidateProjects()` / `invalidateOverview()` 失效缓存，视图 watch 后重载。 |
| 并发请求 | stores 内部用 `inflight` 共享同一次请求；`force` 在请求进行中会排期再刷一次。 |
| 路径安全 | 前端发送相对路径/文件清单时，后端会严格校验（见后端文档）。 |
| 新增页面 | 只需在 `router/index.ts` 注册路由 + `meta.titleKey/icon`，侧栏与顶栏标题自动出现。 |

## 6. 主题与样式

- `plugins/vuetify.ts`：深色石板底（background `#1E1F24`）+ 高饱和青色主色（primary `#4CC2FF`），含 dark/light 两套主题；`assets/main.css` 提供 `--pg-*` 层叠变量（layer/hover/border/nav-active）。
- 组件普遍使用 `surface-tile`、`dialog-card`、`hover-card`、`primary-action` 等全局 class。
