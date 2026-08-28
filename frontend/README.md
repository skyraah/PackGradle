# PackGradle 前端

Vue 3 + TypeScript + Vite + Vuetify 4（dark）+ vue-i18n + vue-router（hash 模式）。
Wails 3 桌面应用前端：Go 服务经 Wails 生成的绑定（frontend/bindings，勿手改）供组件调用，
界面文案唯一来源为 src/locales/zh-CN.json。

## 目录结构

```
src/
├── App.vue               应用外壳：顶栏（含 Wails 拖拽区）+ rail 导航抽屉 + 全局 snackbar/引导弹窗
├── main.ts               入口：Vue + i18n + Vuetify + Router
├── router/index.ts       路由表（hash 历史）；侧栏导航项由路由 meta 驱动生成
├── plugins/vuetify.ts    主题（深色石板底 + 祖母绿主色）与组件默认值
├── assets/main.css       应用级全局样式（滚动条 / 拖拽区 / 文本选择）
├── stores/               模块级共享状态（无 Pinia 依赖）
│   ├── projects.ts       项目列表缓存 + 跨视图数据版本号（projectsVersion）
│   ├── instances.ts      Prism Overview 缓存（实例 + 关联视图一次返回）
│   ├── env.ts            工具检测结果 + API Key 缓存
│   ├── apiKeyGuide.ts    CurseForge API Key 错误分流（应用级引导弹窗）
│   └── ui.ts             全局 snackbar
├── components/
│   ├── common/           通用：PageHeader / ConfirmDialog / OutputDialog / EmptyState
│   ├── projects/         项目域：ModsTable / CheckUpdatesDialog（含单 mod 更新）
│   └── prism/            Prism 域：LinkDialog / DirLinksDialog / FileSelectDialog / MetaDiffDialog
├── views/                页面（路由懒加载）
│   ├── DashboardView.vue      /              工作台
│   ├── ProjectsView.vue       /projects      项目列表（keep-alive 保持状态）
│   ├── ProjectDetailView.vue  /projects/:name 项目详情（mod 管理）
│   ├── InstancesView.vue      /instances     Prism 联动
│   └── SettingsView.vue       /settings      设置（工具检测 / PATH / API Key）
├── utils/                errors.ts（错误码渲染）、cf.ts（CurseForge 展示工具）
└── locales/zh-CN.json    全部文案
```

## 约定

- 错误处理：Go 端只出错误码，utils/errors.ts 双路径解析（e.cause 与数据字段文本）；
  API Key 相关错误码由 stores/apiKeyGuide 弹应用级引导框，其余走全局 snackbar。
- 确认类交互一律用自定义 v-dialog（Wails 原生 Question 在构建版挂起）。
- 跨视图刷新：meta 拉取等变更 bumpProjectsVersion() + invalidateProjects()，相关视图 watch 后重载。
- 新增页面：在 router/index.ts 注册路由并写入 meta（titleKey + icon），侧栏与顶栏标题自动出现。

## 开发

```bash
yarn install   # 依赖（.yarnrc.yml 固定 node-modules 链接器）
yarn build     # vue-tsc 类型检查 + 生产构建
yarn dev       # Vite 开发服务器（配合 wails3 dev）
```
