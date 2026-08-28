# 前端开发指南

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

# 3) 类型检查 + 生产构建
cd frontend
yarn build
```

Vite dev server 默认 `127.0.0.1:9245`（`vite.config.ts` 读 `WAILS_VITE_PORT`，strictPort 防端口漂移）。

## 3. 目录与新增规则

- **新增页面**：`src/views/XxxView.vue` → 在 `src/router/index.ts` 注册路由并写 `meta: { titleKey, icon }`；侧栏与顶栏标题自动生成。文案键加入 `locales/zh-CN.json`。
- **新增通用组件**：放 `src/components/common/`；域组件放 `projects/` 或 `prism/`。组件 props 用 `defineProps<T>()`，弹窗类用 `defineModel<boolean>()` 或 `modelValue + update:modelValue`。
- **共享状态**：新建 `src/stores/xxx.ts`，按 `projects.ts` 的缓存模式（`loaded` + `inflight` + force 排期）实现；不要新建 Pinia。
- **文案**：只加 `zh-CN.json`（扁平键，键名可带 `.`）；模板用 `t('xxx.yyy', [arg])`。
- **确认弹窗**：一律 `ConfirmDialog`（后果四要素）；不要用 Wails 原生 Question。
- **写操作**：一律 `runTask()`。真实绑定为单次调用、无中间进度上报：`report` 可不传，任务抽屉对运行中且无进度的任务显示不确定进度条；有 CLI 输出的用 `runTask` 的 `output` 展示。
- **错误处理**：`errText(e)`（异常）/ `displayText(s)`（数据字段）；CF API Key 错误用 `handleApiKeyError(e)` 分流。

## 4. 真实绑定直连（mock 回切已完成）

mock 层（`stores/mock.ts`）已删除，stores/views/components 直接调用生成的 bindings。新增前端功能时遵循同一模式：

```ts
import { EnvService } from '../../bindings/packgradle/internal/service'
const task = EnvService.Detect().then(list => { ... })
```

约定与注意点：

1. **返回类型**：绑定返回 `T | null` 与 `$CancellablePromise<T>`，`.then(list => list ?? [])` 兜底；`await` 用法不变。
2. **错误**：绑定 reject 的是 Wails RuntimeError（`err.cause` 为 AppError 对象），调用处 `errText(e)` 自动翻译；CF 相关错误用 `handleApiKeyError(e)` 分流（Key 缺失/无效弹全局引导）。
3. **文件/目录选择**：统一经 `utils/dialogs.ts`（`pickPackToml` / `pickToolPath` / `pickDirectory`，取消返回 null）；消息类对话框（Question 等）有挂起 bug，确认一律 `ConfirmDialog`。
4. **进度上报**：真实绑定没有 `onProgress` 参数——单次调用完成即 progress=1，任务中心保留结果历史。
5. **后端方法变更**：改 Go 后先跑 `wails3 generate bindings -f '' -clean=true -ts -i` 再写前端。

### 4.1 必测清单（真实数据）

- 工作台四卡（工具检测、API Key、Prism 定位、项目数）。
- 项目导入（真实文件对话框）、删除（后端会清理链接）、refresh 输出。
- 更新检查页签 + 应用全部/单个（注意 `modIDForUpdate` 的 name→id 反查与真实输出解析）。
- Prism：定位失败引导、手动路径、关联/创建实例、一键关联 `.pgignore` 三分支、manual link、junction↔files 切换、文件选择、meta push/pull/diff。
- 错误场景：无 API Key、无 packwiz、实例被删、跨卷硬链接、实例侧非空目录。
- `yarn build`（vue-tsc）零错误。

## 5. i18n 扩展

1. 在 `zh-CN.json` 加扁平键；插值用 `{0}`（如 `"err.proj.not_found": "未找到项目: {0}"`）。
2. 后端新增错误码：键名必须与 Go `errs.New` 的 code 完全一致。
3. 模板/脚本统一 `t(key, [arg])`；非组件模块 `import { t } from '../i18n'`。
4. 产品专名不翻译（Fabric/Forge 等），展示映射在 `utils/cf.ts`。

## 6. 样式约定

- 优先 Vuetify 组件 + utility class；主题色经 `plugins/vuetify.ts`。
- 全局 CSS 类：`surface-tile`、`dialog-card`、`hover-card`、`primary-action`、`card-error`、`app-drag`（窗口拖拽）、`app-no-drag`（交互控件）。
- 无边框窗口：`app-drag` 区域不要放交互控件；最大化时 App.vue 会给 `html` 加 `window-maximised` 去圆角。

## 7. 已知注意点

- **任务中心 z-index**：TaskCenterDrawer 使用 `z-index: 2400` 缓解与全局 dialog 层级遮挡；真实版若仍冲突，建议弹窗打开时自动收起抽屉或统一 overlay 管理。
- **路由 keep-alive**：`include="ProjectsView"` 依赖组件 `name`；重命名组件时同步 `defineOptions({ name })`。
- **本地存储键**：`packgradle.modsTable.sortBy` 是当前前端直接使用的 localStorage 键（mock 时代的 `packgradle.mock.configExists` 已随 mock 层移除）。
- **构建产物**：`frontend/dist` 会被 `main.go` embed，必须存在于构建前（Wails task 已处理）。
