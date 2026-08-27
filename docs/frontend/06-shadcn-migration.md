# 06 · shadcn-vue 全量迁移准备（2026-08-28 就绪）

> 状态：**基础设施已就绪，尚未迁移任何页面**。Vuetify 4 与 shadcn-vue（Tailwind v4）共存，
> 从任何新页面/新组件起都可以直接用 shadcn-vue；旧页面按本文第 5 节顺序逐页替换。

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

## 5. 建议迁移顺序

1. **新页面/新功能直接用 shadcn-vue**（零回归风险）。
2. 通用组件：`src/components/common/*`（ConfirmDialog、EmptyState、PageHeader…）→ 逐个替换。
3. 业务视图：Dashboard → Settings → Instances → Projects → ProjectDetail（按复杂度升序，每页迁完跑一遍真实项目验证）。
4. 壳层 App.vue（rail/顶栏）最后替换，替换时移除 `syncDarkClass` 里的 Vuetify 依赖、保留 `html.dark` 机制。
5. **收尾拆除**：卸载 `vuetify`、`@mdi/font`、`@fontsource/roboto`（如不再用）、`plugins/vuetify.ts`、main.css 中 `--pg-*` 与 v-* 覆盖样式、main.ts 的 vuetify 装载；`.v-theme--dark` 令牌选择器一并删除。

## 6. 验证

- 类型 + 构建：`yarn build:dev`（vue-tsc + vite build）。
- 已验证（2026-08-28）：CLI 添加 button/badge 成功；`yarn build:dev` 通过；产物 CSS 含亮/暗两套令牌与 `dark:` 变体规则。
