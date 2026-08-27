# AGENT.md — 工作记录

> 每次重大任务完成后追加：日期 / 模型 / 产出 / 验证方式。

## 2026-08-28 · 工作区文档与记忆库清理（grill 两轮确认口径）

- **模型**：GLM-5.3（ZCode builtin:bigmodel-coding-plan）
- **口径**：经两轮问询确认——删除无争议冗余；史料归档不删除；新视觉资产入库；旧架构记忆精简重写。
- **删除**：`Typora_Hook_Log.txt` ×6（docs/ 及 5 个子目录，Typora hook 工具日志误落盘，约 327KB）、`docs/pcl2.png`（视觉基准已切概念风格.pdf）。
- **归档**（git mv 保留历史 + 顶部横幅 + `docs/archive/README.md` 新增「其他归档」表）：`docs/docs.md`（重设计讨论实录）、`docs/CODE_REVIEW_BUGS.md`（27 bug 已全修复的旧审查报告）、根目录 `AGENT History.md`（旧工作日志，本次用户改拍板移入归档）。
- **入库**（此前未跟踪，docs/README.md 已引用）：`docs/概念风格.pdf`、`docs/frontend/workspace-ux-prototype.html`、`docs/frontend/06-shadcn-migration.md`。
- **README 纠偏**：速览「mock 层已移除」改为实际状态（src/api+src/mocks 在，可一键切换）；「3 服务 40 方法」改为 4 服务 52 方法（EnvService 7/PackwizService 8/PrismService 26/SyncService 11，按 bindings 逐一核实）；技术栈补 shadcn-vue+Tailwind v4 迁移中共存；前端索引补 06 迁移指南。**顺手修复上次归档遗留的 archive/README.md 断链**（frontend-legacy 五个链接少了子目录段）。
- **提交**：k3ui 分支 `00addd3`（21 文件，+1665/−3615），仅文档改动，frontend shadcn 代码改动未混入。根 AGENT.md 本身保持未跟踪（由用户决定是否入库）。
- **记忆库**：重写 `project-architecture.md`（删 PCL2 前端叙事与过时方法数，保留 legacy 冻结事实 + junction/pgignore/meta/错误码 i18n 机制）；`feedback-pcl2-style.md` 改名 `feedback-visual-baseline.md`（反向链接与索引同步）；`project-wails-dialog-bug.md` How-to-apply 补 shadcn 迁移后用 shadcn Dialog。
- **验证**：`git log/show-stat` 确认 rename 保留历史；grep 全仓无 pcl2.png/Typora/旧路径残留引用；记忆目录 grep 无断链（仅存「原记忆名」注释）；archive README 链接指向核实存在。
- **未动**：docs/backend、contract、development、architecture、REQUIREMENTS.md（现行文档，其中 backend/development 部分内容仍描述 legacy 栈，属正常——前端仍在调旧服务，全面一致性体检另行安排）。

## 2026-08-28 · 工作区 UX 交互原型（可修改）

- **模型**：GLM-5.3-Flash（ZCode builtin:bigmodel-coding-plan）
- **产出**：`docs/frontend/workspace-ux-prototype.html` —— 把 `docs/frontend/05-workspace-ux-prototype.md`（新架构工作区 UX 规格）物化为单文件可交互原型。零依赖、双击即开（file:// 离线可用）、内存态演示数据。
  - 覆盖 05 文档 §13.1 的 Round 1（P1 主闭环）共 24 个画板入口：壳层 S-01/S-02、工作区列表 W-01..W-03/04、新建向导 N-01..N-05、变化页 C-01..C-06、受管范围 M-01、计划 P-01/P-02/P-05、重绑定 R-01/03、项目源 E-01、运行实例 E-02、设置 SET-01。
  - 底部浮条画板导航（← → 键 / 目录下拉）、深浅双主题（取 vuetify.ts 令牌）、任务模拟推进、演示状态重置。
  - Round 2/3 待补：P-03/P-04 冲突决议与风险确认执行、H-01..H-03 历史与恢复、X-01 恢复详情（文件头有标注）。
- **验证**：node --check 内联脚本语法通过；经内置浏览器（1200×780）实测工作区列表、变化页冲突画板、同步分析、任务中心抽屉、浅色主题 + 向导 N-01，渲染与交互均正常；修复了画板序号回写覆盖、扫描中骨架、任务完成后取消按钮残留、浮条文字竖排等问题。
- **备注**：该文件为抛弃式原型——结论确认后应把设计决策折回真实前端代码，本文件归档抛弃分支，不进 main。

## 2026-08-28 · 归档旧前端交付文档

- **模型**：GLM-5.3-Flash（ZCode builtin:bigmodel-coding-plan）
- **产出**：旧版前端交付文档移入 `docs/archive/frontend-legacy/`（01-overview / 02-routes / 03-components / 04-stores-and-utils / UI_UX_REVIEW），每个文件顶部加归档横幅；新增 `docs/archive/README.md` 说明归档原因与现行设计依据；`docs/README.md` 索引改为以工作区 UX 设计基线 + 交互原型为准。
- **验证**：`git status` 确认 5 个文件以 rename（历史保留）+ 横幅修改入暂存区；全仓 grep 无残留旧路径引用（历史日志 AGENT History.md 除外，属历史记录不动）。
- **备注**：未做 git commit，改动留在暂存区/工作区由用户确认后提交。`docs/CODE_REVIEW_BUGS.md`、`docs/docs.md`、`docs/REQUIREMENTS.md` 不属「前端交付文档」，未动；若也想归档可再处理。

## 2026-08-28 · 视觉基准切换为概念风格 PDF

- **模型**：GLM-5.3-Flash（ZCode builtin:bigmodel-coding-plan）
- **背景**：用户提供 `docs/概念风格.pdf` 作为后续前端视觉指导文件，明确「不再以 PCL 为视觉指导」。
- **色值提取**：环境无可用 Python，用临时 HTTP 服务 + Chromium PDF 查看器查看，再经 pdf.js（CDN）把页面渲染到 canvas 逐像素采样：内容区纵向渐变 #262D3D→#10141C、壳层（标题栏+rail）#121720、强调色祖母绿 #4ADE80、未激活 rail 图标低饱和灰绿。
- **产出**：`docs/frontend/workspace-ux-prototype.html` 全量换肤——深浅双主题令牌重写（浅色对应 #16A34A 绿）、主区渐变背景、标题栏/rail 用 #121720 深色壳层、rail 选中态改绿色调圆角容器（去掉左亮条）、logo/按钮/进度/页签全部跟令牌走；文件头注明视觉基准变更。
- **文档**：`docs/frontend/05-workspace-ux-prototype.md` §11.1 顶部加标注——视觉方向被概念风格.pdf 取代，密度/窗口规格继续有效。
- **验证**：node --check 通过；浏览器实测深色（工作区列表 + 任务中心）与浅色主题渲染正常。
- **备注**：概念板无浅色稿，浅色主题为同色系推导（绿 600 + 冷灰白），如后续有浅色概念稿需再对齐。

## 2026-08-28 · 背景取消渐变（用户反馈）

- **模型**：GLM-5.3-Flash（ZCode builtin:bigmodel-coding-plan）
- **反馈**：渐变背景视觉效果不算很好，背景不用渐变。
- **产出**：原型主内容区改平面底——深色 `--bg:#1B212D`（概念板渐变中位采样色），surface 阶梯随之上移（#222A37 / #2A3240 / #333C4C）保持层级，rail/标题栏 #121720 不变；浅色主题去掉 --bg-top 用平面 #F2F5F9。同步更新原型文件头注释、05 规格文档 §11.1 标注、记忆库。
- **验证**：node --check 通过；浏览器截图确认平面背景深色主题正常。

## 2026-08-28 · shadcn-vue 安装与全量迁移准备

- **模型**：GLM-5.3（ZCode builtin:bigmodel-coding-plan）
- **产出**：前端（frontend/）落地 shadcn-vue 基础设施，Vuetify 4 与之共存，零页面迁移、零 UI 变更：
  - 依赖：`tailwindcss@4 + @tailwindcss/vite`（vite 插件方式，无 postcss/config 文件）、`reka-ui`、`class-variance-authority`、`clsx`、`tailwind-merge`、`tw-animate-css`、`@lucide/vue`；移除了误装的 `lucide-vue-next`（CLI 实际用 @lucide/vue）。
  - 配置：`components.json`（new-york/neutral/cssVariables）；`@/*` 别名进 vite + tsconfig；`src/lib/utils.ts` cn()。
  - 主题令牌：`src/assets/main.css` 顶部新增 Tailwind v4 块——令牌取自概念风格原型（暗：底 #1B212D/面 #222A37/#2A3240/#333C4C/rail #121720/祖母绿 #4ADE80；亮：#F2F5F9 + #16A34A），含 shadcn 标准名 + 原型扩展令牌（`bg-rail` `text-faint` `bg-tint-primary` 等）与 `dark:` 自定义变体。
  - 主题联动：App.vue 新增 `syncDarkClass()` 随 Vuetify 主题（跟随系统）切换 `html.dark`；令牌同时挂 `.v-theme--dark` 兜底。
  - 组件管线验证：CLI 添加 `button`/`badge` 至 `src/components/ui/`。
  - 文档：`docs/frontend/06-shadcn-migration.md`（令牌映射表、共存规则、组件对应关系、建议迁移顺序、Vuetify 拆除清单）。
- **共存原理**：Vuetify 样式未进 CSS layer，优先级天然高于 Tailwind 的 base/utilities layer，现有页面不受 preflight/工具类影响；反向规则——Tailwind 类不套 v-* 组件。
- **验证**：`yarn build:dev`（vue-tsc + vite build）通过；产物 CSS 实测含亮/暗两套令牌（tint-primary 亮 #16a34a1a / 暗 #4ade8021）与 `dark:` 变体规则；CLI add 全流程跑通。
- **备注**：未 commit；button/badge 仅为管线验证产物，迁移开始后按 06 文档第 5 节顺序执行，收尾时按拆除清单卸载 vuetify/@mdi/font 并删除 main.css 中 --pg-* 覆盖层。
