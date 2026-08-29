# 06 · shadcn-vue 全量迁移准备（2026-08-28 就绪）

> 状态：**基础设施已就绪，尚未迁移任何页面**。Vuetify 4 与 shadcn-vue（Tailwind v4）共存，
> 新页面一律直接用 shadcn-vue；旧页面**不逐页迁移**，保持 Vuetify 现状直至切换发布整体删除（见第 5 节，依 [ADR-0001](../adr/0001-p1-frontend-cutover-and-legacy-retirement.md) 修订）。

## 1. 已就绪的设施

| 设施 | 位置 | 说明 |
| --- | --- | --- |
| Tailwind v4 | `@tailwindcss/vite`（vite 插件） | 无 postcss/tailwind.config，配置全在 CSS |
| 组件 CLI | `components.json`（frontend 根） | style=new-york、baseColor=neutral、cssVariables=true、图标库 lucide |
| cn() 工具 | `src/lib/utils.ts` | clsx + tailwind-merge |
| 组件目录 | `src/components/ui/` | 已有 `button/`、`badge/`（管线验证用） |
| 主题令牌 | `src/assets/main.css` 顶部 | 概念风格配色，亮/暗双主题，含原型扩展令牌 |
| 路径别名 | `@/*` → `src/*` | vite `resolve.alias` + tsconfig `paths` 已配 |
| 图标 | `@lucide/vue` | CLI 按需装；`@mdi/font` 保留至 Vuetify 移除 |
| 暗色同步 | `src/App.vue` `syncDarkClass()` | 随 Vuetify 主题（跟随系统）切换 `html.dark` |

添加组件：`cd frontend && npx shadcn-vue@latest add <组件名>`（如 `dialog input select table tabs …`）。

## 2. 主题令牌映射（概念风格 → shadcn）

令牌源：`docs/frontend/workspace-ux-prototype.html`（视觉基准 `docs/概念风格.pdf`：祖母绿 + 平面深墨蓝，**背景无渐变**）。

| 概念风格（原型变量） | 暗色值 | shadcn 变量 | 生成的工具类示例 |
| --- | --- | --- | --- |
| --bg | `#1B212D` | `--background` | `bg-background` |
| --text | `#E9EDF3` | `--foreground` | `text-foreground` |
| --surface | `#222A37` | `--card` / `--popover` | `bg-card` |
| --surface2 / --surface3 | `#2A3240` / `#333C4C` | `--secondary`+`--muted` / `--accent` | `bg-secondary` `bg-accent` |
| --rail | `#121720` | `--rail`（扩展） | `bg-rail` |
| --primary | `#4ADE80`（亮 `#16A34A`） | `--primary` | `bg-primary text-primary-foreground` |
| --muted / --faint | `#9AA6B6` / `#6C7787` | `--muted-foreground` / `--faint`（扩展） | `text-muted-foreground` `text-faint` |
| --tint-primary 等 | `rgba(74,222,128,.13)` 等 | `--tint-*`（扩展） | `bg-tint-primary` |
| --border / --border-weak | `rgba(148,163,184,.14)` | `--border` / `--input` | `border-border` |

亮色整套已在 `:root` 中对应定义（浅色 `#F2F5F9` 平面底 + 绿 `#16A34A`）。圆角 `--radius: 0.625rem`（10px，贴合原型扁平风）。

## 3. 共存规则（迁移期，重要）

1. **优先级**：Vuetify 样式未进 CSS layer，天然压过 Tailwind 的 `base`/`utilities` layer。
   现有页面不受 Tailwind preflight 影响；反过来 **Tailwind 工具类不要套在 Vuetify 组件（v-*）上**，会被 Vuetify 覆盖，两套组件不要混用在同一个元素。
2. **主题切换**：暗色令牌同时挂在 `.dark` 与 `.v-theme--dark` 上，html 层由 `App.vue` 同步 `dark` 类；Vuetify 移除后 `.dark`（html）是唯一开关。
3. **暗色写法**：shadcn 组件内的暗色分支用 `dark:` 前缀（自定义变体已注册），业务样式同理。
4. **新增 UI 一律用 shadcn-vue**（`@/components/ui/*`），不再新写 Vuetify 组件；mdi 图标仅旧组件存量使用。

## 4. 迁移时的组件对应关系（常用）

| Vuetify | shadcn-vue |
| --- | --- |
| v-btn | `Button`（variant: default/secondary/outline/ghost/destructive） |
| v-chip | `Badge` |
| v-text-field / v-select | `Input` / `Select`（配合 `FormField`） |
| v-dialog | `Dialog`（消息确认沿用 Vuetify v-dialog 的坑见记忆库，shadcn Dialog 不受影响） |
| v-navigation-drawer | `Sheet` / `Sidebar` |
| v-snackbar | `Sonner`（toast） |
| v-data-table | `Table` + TanStack（如需） |
| v-tabs | `Tabs` |
| v-tooltip | `Tooltip` |
| v-progress-linear/circular | `Progress` / 手写 |

## 5. 切换发布与拆除（2026-08-28 依 ADR-0001 修订）

1. **新页面/新功能一律 shadcn-vue**（`@/components/ui/*`）：`/workspaces` 系列、新设置页等全部如此，零回归风险。
2. **旧页面保持 Vuetify 现状**：不再逐页迁移 shadcn，仅缺陷修复；通用组件（`src/components/common/*`）只为仍存活的页面按需替换。
3. **切换发布 = 旧栈整体删除 + Vuetify 收尾拆除**（已按此执行）：删除旧路由/页面/旧 store/旧 mocks，同发布卸载 `vuetify`、`@mdi/font`、`@fontsource/roboto`、`plugins/vuetify.ts`、main.css 中 `--pg-*` 与 v-* 覆盖样式、main.ts 的 vuetify 装载、`.v-theme--dark` 令牌选择器，主题经 `stores/theme.ts`（三态偏好 + prefers-color-scheme 监听）落到 `html.dark`。mock 生产裁剪（§6 验收）经 `vite define __DEV__` 静态门 + dev 组件异步门控 + `stripDevMockLocale` locale 剔除插件三件套实现。

## 6. 验证

- 类型 + 构建：`yarn build:dev`（vue-tsc + vite build）。
- 切换发布验收（ADR-0001）：生产构建 dist 中无 mock 痕迹（`packgradle.mock` 等），mock 区块与 MOCK 徽标仅 `import.meta.env.DEV` 渲染。
- 已验证（2026-08-28）：CLI 添加 button/badge 成功；`yarn build:dev` 通过；产物 CSS 含亮/暗两套令牌与 `dark:` 变体规则。
