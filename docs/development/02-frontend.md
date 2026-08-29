# 前端开发指南

> ADR-0001 切换发布（T16，票 #26）后：旧栈（Dashboard/Projects/Instances/Dev + Vuetify）已整体退场，
> 全部页面为 shadcn-vue + Tailwind v4；mock 仅存在于开发构建。

## 1. 环境与前置

- Node.js（LTS）+ Yarn。仓库固定使用 Yarn：`frontend/.yarnrc.yml` 指定 node-modules 链接器。
- 依赖安装：在 `frontend/` 下 `yarn install`（Wails 的 task 会自动安装）。
- 类型检查/构建：`yarn build`（`vue-tsc && vite build --mode production`）；`yarn build:dev` 为不压缩开发构建。

## 2. 常用开发流程

```bash
# 1) 完整桌面应用热重载开发（推荐）
task dev
# 等价：wails3 dev -config ./build/config.yml -port 9245
# dev_mode 流程：wails3 build DEV=true → 后台启动前端 Vite → 运行应用

# 2) 仅前端 UI 走查（浏览器无 Wails Runtime，绑定调用不可用，仅看布局）
cd frontend
yarn dev --port 5199 --strictPort
# 注意：本机 5173 可能被 Adobe CC 常驻 node 进程占用，故用 5199

# 3) 类型检查 + 生产构建（含 dist 无 mock 痕迹验收）
cd frontend
yarn build
```

Vite dev server 默认 `127.0.0.1:9245`（`vite.config.ts` 读 `WAILS_VITE_PORT`，strictPort 防端口漂移）。

## 3. 目录与新增规则

- **新增页面**：`src/views/XxxView.vue` → 在 `src/router/index.ts` 注册路由并写
  `meta: { titleKey, icon }`（icon 为 lucide 图标名，rail/顶栏标题自动生成）；一级导航只允许
  UX 原型 §4.1 四项（工作区/项目源/运行实例/设置），详情页不占侧栏。文案键加入 `locales/zh-CN.json`。
- **新增 UI 组件**：`npx shadcn-vue@latest add <name>`（`@/components/ui/*`，style=new-york-v4）；
  通用组件放 `src/components/common/`，域组件按域建目录。props 用 `defineProps<T>()`，
  弹窗开关用 `defineModel<boolean>()`。
- **共享状态**：新建 `src/stores/xxx.ts`（组合式模块，不要 Pinia）。新栈数据一律走
  `stores/syncCache`（查询 API 投影 + 受控重查管线，契约 04），页面不做第二处数据获取、不订阅事件。
- **文案**：只加 `zh-CN.json`（扁平键）；模板用 `t('xxx.yyy', [arg])`。
- **确认弹窗**：一律 shadcn `AlertDialog`；不要用 Wails 原生 Question（构建版挂起 bug）。
- **错误处理**：`errText(e)`（AppError code → locale）。

## 4. 真实绑定直连

统一经 `src/api` 门面调用（`EnvService` / `PackwizService` / `PrismService` 旧栈服务与
`SyncService` / `ProjectService` / `RuntimeService` 新栈服务），门面内部直连生成绑定：

```ts
import { SyncService } from '../api'
const plan = await SyncService.GetPlan(planID)
```

约定与注意点：

1. **返回类型**：绑定返回 `T | null` 与 `$CancellablePromise<T>`，`.then(list => list ?? [])` 兜底。
2. **错误**：绑定 reject 的是 Wails RuntimeError（`err.cause` 为 AppError 对象），调用处 `errText(e)` 自动翻译。
3. **文件/目录选择**：统一经 `utils/dialogs.ts` 的 `pickDirectory(title?)`（取消返回 null）。
4. **事件**：核心事件全前端仅 `api/events.ts` 一处订阅（契约 04 §2.1）；页面经 syncCache 管线被动刷新。
5. **后端方法变更**：改 Go 后先跑 `wails3 generate bindings -ts -i` 再写前端。

### 4.1 mock 层（仅开发构建）

- `src/mocks/*` 仅 dev 构建经 `src/api` 的 `__DEV__` 静态门动态装载；生产构建中常量折叠 +
  动态导入裁剪 + `vite.config.ts` 的 `stripDevMockLocale` 插件（剔除 `mock.*` 文案键）保证
  dist 无任何 mock 痕迹（ADR-0001 §5 构建验收：`grep -ri mock dist/` 应零命中）。
- dev 构建可在设置页 dev 卡片开关（切换整页刷新）；顶栏 MOCK 徽标（`MockBadge.vue`）同为 dev 门控。

### 4.2 必测清单（真实数据）

- 工作区全链路：新建（Prepare → Apply）→ 扫描 → 变化浏览 → 只读计划/冲突解决 → 重绑（票 #11-#22）。
- 端点页：自动发现、手动登记路径、健康徽标、目录选择器。
- 任务中心：扫描中取消、失败结果、查看工作区跳转。
- 设置页：主题三态（跟随系统/浅色/深色）、语言切换；dev 构建另有 mock 开关两向切换。
- `yarn build`（vue-tsc + dist 验收）零错误。

## 5. i18n 扩展

1. 在 `zh-CN.json` 加扁平键；插值用 `{0}`（如 `"err.proj.not_found": "未找到项目: {0}"`）。
2. 后端新增错误码：键名必须与 Go `errs.New` 的 code 完全一致（conformance test 自动比对，缺键/死键即红）。
3. 模板/脚本统一 `t(key, [arg])`；非组件模块 `import { t } from '../i18n'`。
4. 产品专名不翻译（Fabric/Forge 等）。

## 6. 样式约定

- 一律 shadcn-vue 组件 + Tailwind v4 工具类；概念风格令牌在 `src/assets/main.css`（亮/暗双套）。
- 主题唯一开关是 `html.dark`（`stores/theme.ts`：跟随系统/浅色/深色三态偏好）；无 Vuetify。
- 全局 CSS 类：`app-drag`（Wails 拖拽区）、`app-no-drag`（交互控件）、`output-pre`（CLI 输出块）。
- 无边框窗口：`app-drag` 区域不要放交互控件；最大化时 App.vue 会给 `html` 加 `window-maximised` 去圆角。

## 7. 已知注意点

- **模板静态门**：dev-only 模板分支不能写 `import.meta.env`（模板编译器不支持）也不可用 setup
  常量（阻断常量折叠）；用 vite `define` 注入的 `__DEV__`，或把 dev-only UI 抽成组件由
  `__DEV__ ? defineAsyncComponent(...) : null` 门控（mock 卡片与 MOCK 徽标的既有做法）。
- **任务中心**：`TaskCenterDrawer.vue` 是 shadcn Sheet，数据全部来自 `stores/syncCache`
  （活跃任务投影）；徽标计数 = 活跃任务数，无本地已读状态。
- **本地存储键**：`packgradle.theme`（主题偏好）、`packgradle.mock`（dev mock 开关，生产恒 false）。
- **构建产物**：`frontend/dist` 会被 `main.go` embed，必须存在于构建前（Wails task 已处理）。
