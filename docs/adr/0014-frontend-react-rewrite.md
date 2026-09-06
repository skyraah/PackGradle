---
status: accepted
date: 2026-09-06
---

# 0014 · 前端框架二次切换（Vue 退场，React 进入）

P1（ADR-0001）为前端选了 Vue 3 + shadcn-vue，#102–#111 随后把界面与原型的画板对照清零。但维护者不擅长 Vue 模板接线（v-model/slot/ref 一类静默失效多次咬人，Input.vue 接线事故是实例），熟练面在 JSX 系（React/Preact）。据此整体二次切换，决议如下：

1. **React 本体，非 Preact**。shadcn/ui 原版与 Radix 底座以 React 为一等公民（shadcn-vue 是移植链）；React 受控组件的取值接线（value 配 onChange）是显式的，结构上消灭「模板指令线没接上但静默失效」这类缺陷；桌面端本地加载不在乎包体，Preact 的兼容层（`preact/compat`）只添风险无收益。Svelte 非 JSX，出局。
2. **分工：代理搭脚手架，维护者本人执笔页面**。脚手架 = 构建链（Vite + React + TS + Tailwind v4 + Wails vite 插件）、应用外壳（无边框窗口控制/拖拽区/最大化事件、icon rail、hash 路由与 14 张带移植注释的占位页）、shadcn/ui 基座与 15 个组件族、react-i18next 接线。维护者负责 14 页、4 个 store 的 zustand 移植、api 层搬迁（含 mock 开关与 DevMock 卡）——目的即维护者能独立维护前端。
3. **口径翻案：不承诺 1:1 对齐现状**。维护者可自由改版，两份原型（`docs/frontend/workspace-ux-prototype.html`、`p3-restore-prototype.html`）从验收规格降为参考资料，#102–#111 的保真成果不再是重写的对齐义务。
4. **长分支单点切换，沿用 ADR-0001 方法论**。`ui/react` 分支上 `frontend/` 原地换血——go:embed 两处路径不变（`frontend/dist`、`frontend/src/locales/zh-CN.json`）；main 上的 Vue 应用保持可用，期间定期 merge main 回分支；14 页全齐后单 PR 合入并删旧树。不设双框架共存期（ADR-0001 §1 同款否决）。
5. **配套栈**：zustand（4 个手写模块单例 store 一一移植）、react-router（hash 模式，Wails 静态托管所需）、shadcn/ui 原版（new-york，Tailwind v4 令牌平移，视觉基线零变化）、react-i18next（`zh-CN.json` 键值与路径原样）。**受保护键族**（删改即破坏后端，详见 `frontend/src/lib/i18n.ts` 头注释）：`notify.pendingPlan.title`/`notify.pendingPlan.body`（main.go `loadNotifyCopy` 编译期嵌入读取）、`err.*`/`msg.*` 与 `workspaces.taskKind.*`（`internal/errs/locale_conformance_test.go` 双向扫描，且该测试硬编码 locale 文件路径）。
6. **测试缓建**：前端不新增测试设施，维护者发令才建。验收 = 维护者自查 + 合入 main 前代理跑一轮全页 L1 走查作为切换门。
7. **排队票挂起**：#69、#113、#70 与重写解耦，等维护者发令再动；#113 的前端接线直接落 React 树，不在 Vue 上做两遍。

## Considered Options

- Preact：拒——组件供应链（Radix 系）需兼容层，省下的包体在桌面端无收益。
- 双框架共存逐页替换：拒——Wails 单页应用挂两套框架两套路由自找复杂度，ADR-0001 对共存已有否定先例。
- 代理全量 1:1 重写：拒——重写的目的之一就是页面由维护者执笔，否则换框架不解决维护面问题。

## Consequences

- ADR-0001 的栈选型段（Vue 3 + shadcn-vue）被本 ADR 取代；其单发布切换/旧入口退场方法论仍有效。
- devdocs 指南站与 api 页 `.vue` 锚点、`frontend/README.md`、CONTEXT.md「切换/退场」词条的同步，留在切换收口时一并做。
- vue-i18n 位置插值（`{0}`）与 i18next（`{{0}}`）语法不同，`zh-CN.json` 含 `{0}` 的 143 行在移植 `utils/errors.ts` 时需选边（sprintf postprocessor 或改传参），见脚手架 README。
