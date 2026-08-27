# 前端路由表

文件：`frontend/src/router/index.ts`。

## 1. 路由配置

| path | name | 组件 | meta.titleKey | meta.icon | 是否侧栏 |
| --- | --- | --- | --- | --- | --- |
| `/` | `dashboard` | `views/DashboardView.vue` | `nav.dashboard` | `mdi-view-dashboard-outline` | ✅ |
| `/projects` | `projects` | `views/ProjectsView.vue` | `nav.projects` | `mdi-package-variant-closed` | ✅ |
| `/projects/:name` | `project-detail` | `views/ProjectDetailView.vue` | `nav.projectDetail` | —（隐藏） | ❌ |
| `/instances` | `instances` | `views/InstancesView.vue` | `nav.prism` | `mdi-link-variant` | ✅ |
| `/settings` | `settings` | `views/SettingsView.vue` | `nav.settings` | `mdi-cog-outline` | ✅ |
| `/:pathMatch(.*)*` | — | redirect `/` | — | — | — |

## 2. 关键设计

- **hash 历史**：`createWebHashHistory()`。Wails 以静态资源服务器托管前端，hash 模式深链/刷新不 404。
- **懒加载**：所有视图 `() => import(...)`，按路由分包。
- **navRoutes**：导出 `[dashboard, projects, instances, settings]`，供 `App.vue` 自动生成左侧 rail（titleKey 文案 + icon）；详情页等二级路由不进入侧栏。
- **scrollBehavior**：返回时恢复 `savedPosition`，否则回顶部；keep-alive 视图由组件自身保留状态。
- **keep-alive**：`App.vue` 中 `<keep-alive include="ProjectsView">`；`ProjectsView` 通过 `defineOptions({ name: 'ProjectsView' })` 与 include 匹配。
- **App.vue 高亮归属**：`route.path.startsWith('/projects')` → 高亮 `/projects`；顶栏标题在详情页显示 `项目详情 · <name>`。
- **兜底**：未匹配路由重定向 `/`。

## 3. 路由 meta 接口

```ts
export interface AppRouteMeta {
    titleKey: string   // i18n 翻译键（侧栏与顶栏标题共用）
    icon?: string      // MDI 图标名；缺省时 App.vue 使用 mdi-circle-outline
}
```

## 4. 页面职责速览

| 视图 | 职责 | 主要数据源（bindings） | 关键行为 |
| --- | --- | --- | --- |
| `DashboardView` | 欢迎横幅、环境健康 4 卡、项目/关联概览 | `loadTools` / `loadApiKey` / `loadProjects` / `loadOverview` | 并行 `Promise.allSettled` 装载；卡片点击直达对应页；支持导入项目 |
| `ProjectsView` | 项目列表：搜索、加载器过滤、行内 refresh/更新检查/批量版本/移除 | `stores/projects` | keep-alive；`watch(projectsVersion)` 强制重载；删除走 danger ConfirmDialog |
| `ProjectDetailView` | 项目信息 + ModsTable + 更新检查/刷新/删除 | 路由参数 `:name` → `findProject` | 深链找不到时 `ensureLoaded(force)` 二次拉取；`fetch` 后行闪烁 2s |
| `InstancesView` | Prism 定位状态、关联工作台、meta 推送/拉取、实例折叠面板 | `stores/instances`（`PrismOverview`） | 定位失败置灰关联区并提供手动路径修复；拉取 meta 有后果确认 |
| `SettingsView` | 工具检测/PATH 配置、CurseForge API Key | `stores/env` | 路径本地副本 + dirty 判断 + inline 错误；缺失工具引导弹窗；API Key 保存才写缓存 |

## 5. 编程导航约定

- 进入项目详情：`router.push({ name: 'project-detail', params: { name } })`。
- 返回列表：`window.history.length > 1 ? router.back() : router.push('/projects')`（来源感知，深链回退安全）。
- 跨页跳转常用 `router.push('/settings')`、`router.push('/instances')` 等常量路径。
