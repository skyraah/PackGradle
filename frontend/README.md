# PackGradle 前端（React 重写分支 `ui/react`）

React 19 + TypeScript + Vite + Tailwind v4 + shadcn/ui（new-york）+ react-router（hash）+
react-i18next + zustand。ADR-0014：Vue 3 + shadcn-vue 全量二次切换为 React，本分支由
脚手架起步，**14 个页面由维护者本人执笔**；main 上的 Vue 应用保持可用，14 页全齐后
单 PR 合入并删旧树。Wails 3 桌面应用前端：Go 服务经 Wails 生成的绑定（`bindings/`，勿手改，
再生成命令 `wails3 generate bindings -ts -i`）供页面调用，界面文案唯一来源
`src/locales/zh-CN.json`。

## 分工（ADR-0014 §2）

**脚手架已含（代理交付）**

- 构建链：Vite + React + TS + Tailwind v4 + Wails vite 插件；类型门 `tsc --noEmit`
- 应用外壳（`src/App.tsx`）：无边框窗口控制/拖拽区/最大化事件、68px icon rail、路由骨架
- 路由（`src/router.tsx`）：hash 模式，4 主导航 + 10 工作区子页占位 + 旧栈静默重定向；
  每条路由注释保留了 Vue 树的契约引用（契约号/票号/入口位置）
- 14 张占位页（`src/views/*.tsx`）：每页头部注释 = 移植来源（main 分支路径 + 行数）+ 接线要点
- shadcn/ui 基座 + 15 个组件族（alert-dialog/badge/button/card/checkbox/dialog/dropdown-menu/
  input/progress/radio-group/select/sheet/switch/table/tabs）
- react-i18next 接线（`src/lib/i18n.ts`），`zh-CN.json` 原路径原键

**待你移植（源码在 main 分支）**

| 包 | 说明 |
| --- | --- |
| `stores/`（4 个手写单例 → zustand） | `syncCache`（178 行，状态核心）/`endpoints`/`theme`/`ui` |
| `api/`（与 store 是一捆） | `index.ts` 门面 + mock 开关（`__DEV__` + localStorage `packgradle.mock=1`）、`events.ts`（`packgradle://event` 信封解析）、`notificationNav.ts`（`packgradle://notify`）、`utils/dialogs.ts` |
| `mocks/` | dev 构建的假服务（`__DEV__` 门控 + 动态导入裁剪，ADR-0001 §5） |
| `utils/` | `errors.ts`（错误码渲染）/`mergePreview`/`pageState`/`plans` |
| `composables/` → hooks | `useEndpointPage`（Sources/Runtimes 共用）、`useWorkspaceHeadTabs` |
| `components/common/` 7 件 | CandidateCard/DangerConfirmDialog/MergePreviewDrawer/MockBadge/RestoreConfirmDialog/TaskCenterDrawer/WorkspaceObjectHead |
| 14 页本身 | 替换占位页 |

**壳层待接回件**（挂点注释都在 `src/App.tsx`）：主题切换（`html.dark` 先于首帧落位）、
任务中心铃铛 + 抽屉、全局 snackbar 队列、顶栏 MOCK 徽标。

## 移植速查

- 看旧源码：`git show main:frontend/src/api/events.ts`，或 `git worktree add ../pg-vue main`
- **时序契约**：核心事件订阅先于一切查询、先于首帧渲染（契约 04 §2.1）；
  Vue 版证明在 `main:frontend/src/main.ts`——React 版把订阅放在 `src/main.tsx` 的
  `createRoot(...).render(...)` 之前即可
- **受保护 locale 键**（详见 `src/lib/i18n.ts` 头注释）：`notify.pendingPlan.title/body`
  （`main.go` 编译期嵌入读取）、`err.*`/`msg.*` 与 `workspaces.taskKind.*`
  （`internal/errs/locale_conformance_test.go` 双向扫描）；文件路径也是契约，勿移动
- **插值差异**：vue-i18n 位置插值写 `{0}`，i18next 默认写 `{{0}}`——`zh-CN.json`
  有 143 行含 `{0}`，移植 `utils/errors.ts` 时装 `i18next-sprintf-postprocessor`
  保持原样最省
- 加 shadcn 组件：`npx shadcn@latest add <name>`（代理环境下先
  `env -u HTTPS_PROXY -u HTTP_PROXY`）；注意 CLI 会覆盖同路径手修（Input.vue 事故教训）
- 确认类交互一律用 shadcn AlertDialog——Wails 原生 Question 在构建版挂起
- bindings 调用一律经 api 门面（`api/index.ts`），mock 开关才有意义

## 开发

```bash
yarn install   # 依赖
yarn dev       # Vite 开发服务器（配合根目录 wails3 task dev）
yarn build     # tsc --noEmit 类型检查 + 生产构建（B 口径门）
```
