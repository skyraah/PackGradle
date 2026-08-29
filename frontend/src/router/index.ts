// 应用路由：hash 历史（Wails 以静态资源服务器托管前端，hash 模式深链/刷新不 404）。
// 导航抽屉由 navRoutes 的 meta 驱动，新增页面只需在此注册路由即可自动出现在侧栏。
import { createRouter, createWebHashHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

// 路由元信息：titleKey 为 i18n 翻译键（侧栏与顶栏标题共用），icon 为 MDI 图标名
export interface AppRouteMeta {
    titleKey: string
    icon?: string
}

const dashboard: RouteRecordRaw = {
    path: '/',
    name: 'dashboard',
    component: () => import('../views/DashboardView.vue'),
    meta: { titleKey: 'nav.dashboard', icon: 'mdi-view-dashboard-outline' },
}

const projects: RouteRecordRaw = {
    path: '/projects',
    name: 'projects',
    component: () => import('../views/ProjectsView.vue'),
    meta: { titleKey: 'nav.projects', icon: 'mdi-package-variant-closed' },
}

const projectDetail: RouteRecordRaw = {
    path: '/projects/:name',
    name: 'project-detail',
    component: () => import('../views/ProjectDetailView.vue'),
    meta: { titleKey: 'nav.projectDetail' },
}

const dev: RouteRecordRaw = {
    path: '/dev',
    name: 'dev',
    component: () => import('../views/DevView.vue'),
    meta: { titleKey: 'nav.dev', icon: 'mdi-rocket-launch-outline' },
}

const instances: RouteRecordRaw = {
    path: '/instances',
    name: 'instances',
    component: () => import('../views/InstancesView.vue'),
    meta: { titleKey: 'nav.prism', icon: 'mdi-link-variant' },
}

// 新栈端点管理页（shadcn-vue；新导航 IA 见 docs/frontend/05-workspace-ux-prototype.md §4.1）
const sources: RouteRecordRaw = {
    path: '/sources',
    name: 'sources',
    component: () => import('../views/SourcesView.vue'),
    meta: { titleKey: 'nav.sources', icon: 'mdi-package-variant' },
}

const runtimes: RouteRecordRaw = {
    path: '/runtimes',
    name: 'runtimes',
    component: () => import('../views/RuntimesView.vue'),
    meta: { titleKey: 'nav.runtimes', icon: 'mdi-gamepad-variant-outline' },
}

// 新栈工作区页（shadcn-vue；契约 04 缓存骨架 + 列表页；导航 IA 见 docs/frontend/05 §4.1）
const workspaces: RouteRecordRaw = {
    path: '/workspaces',
    name: 'workspaces',
    component: () => import('../views/WorkspacesView.vue'),
    meta: { titleKey: 'nav.workspaces', icon: 'mdi-view-grid-outline' },
}

// 新栈创建页（shadcn-vue；Prepare → Apply 建工作区，UX 原型 §7.2。
// 列表页头部与空态的「新建工作区」入口承接本页，不再占用侧栏导航项）
const workspacesNew: RouteRecordRaw = {
    path: '/workspaces/new',
    name: 'workspaces-new',
    component: () => import('../views/WorkspacesNewView.vue'),
    meta: { titleKey: 'nav.workspacesNew' },
}

// 新栈工作区详情：资源变更页（shadcn-vue；GetChanges 分页 + 筛选，UX 原型 §7.3。
// 入口在工作区列表行操作，不占侧栏导航项）
const workspacesChanges: RouteRecordRaw = {
    path: '/workspaces/:id/changes',
    name: 'workspaces-changes',
    component: () => import('../views/WorkspacesChangesView.vue'),
    meta: { titleKey: 'nav.workspacesChanges' },
}

// 新栈工作区详情：受管范围页（shadcn-vue；GetMappingPolicy/UpdateMappingPolicy 读写 +
// 乐观锁编辑，UX 原型 §7.4。入口在工作区列表行操作，不占侧栏导航项）
const workspacesMappings: RouteRecordRaw = {
    path: '/workspaces/:id/mappings',
    name: 'workspaces-mappings',
    component: () => import('../views/WorkspacesMappingsView.vue'),
    meta: { titleKey: 'nav.workspacesMappings' },
}

const settings: RouteRecordRaw = {
    path: '/settings',
    name: 'settings',
    component: () => import('../views/SettingsView.vue'),
    meta: { titleKey: 'nav.settings', icon: 'mdi-cog-outline' },
}

// 侧栏导航项（不含详情页等二级路由）
export const navRoutes: RouteRecordRaw[] = [workspaces, dashboard, projects, dev, instances, sources, runtimes, settings]

const router = createRouter({
    history: createWebHashHistory(),
    routes: [...navRoutes, projectDetail, workspacesNew, workspacesChanges, workspacesMappings, { path: '/:pathMatch(.*)*', redirect: '/' }],
    // 返回时恢复滚动位置（keep-alive 视图由组件自身状态保留）
    scrollBehavior(_to, _from, savedPosition) {
        return savedPosition ?? { top: 0 }
    },
})

export default router
