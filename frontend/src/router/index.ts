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

const instances: RouteRecordRaw = {
    path: '/instances',
    name: 'instances',
    component: () => import('../views/InstancesView.vue'),
    meta: { titleKey: 'nav.prism', icon: 'mdi-link-variant' },
}

const settings: RouteRecordRaw = {
    path: '/settings',
    name: 'settings',
    component: () => import('../views/SettingsView.vue'),
    meta: { titleKey: 'nav.settings', icon: 'mdi-cog-outline' },
}

// 侧栏导航项（不含详情页等二级路由）
export const navRoutes: RouteRecordRaw[] = [dashboard, projects, instances, settings]

const router = createRouter({
    history: createWebHashHistory(),
    routes: [...navRoutes, projectDetail, { path: '/:pathMatch(.*)*', redirect: '/' }],
})

export default router
