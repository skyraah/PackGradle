// 应用路由：hash 历史（Wails 以静态资源服务器托管前端，hash 模式深链/刷新不 404）。
// 一级导航按 UX 原型 §4.1：工作区 / 项目源 / 运行实例 / 设置，默认首页 /workspaces。
// 旧栈路由（/、/projects*、/instances、/dev）与 catch-all 静默重定向 /workspaces（ADR-0001 §4）。
// 导航 rail 由 navRoutes 的 meta 驱动，新增页面只需在此注册路由即可自动出现在侧栏。
import { createRouter, createWebHashHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

// 路由元信息：titleKey 为 i18n 翻译键（rail 与顶栏标题共用），icon 为 lucide 图标名
export interface AppRouteMeta {
    titleKey: string
    icon?: string
}

// 端点管理页（契约 03 Project/Runtime 服务 + UX 原型 §7.x）
const sources: RouteRecordRaw = {
    path: '/sources',
    name: 'sources',
    component: () => import('../views/SourcesView.vue'),
    meta: { titleKey: 'nav.sources', icon: 'Package' },
}

const runtimes: RouteRecordRaw = {
    path: '/runtimes',
    name: 'runtimes',
    component: () => import('../views/RuntimesView.vue'),
    meta: { titleKey: 'nav.runtimes', icon: 'Gamepad2' },
}

// 工作区页（契约 04 缓存骨架 + 列表页；导航 IA 见 docs/frontend/05 §4.1）
const workspaces: RouteRecordRaw = {
    path: '/workspaces',
    name: 'workspaces',
    component: () => import('../views/WorkspacesView.vue'),
    meta: { titleKey: 'nav.workspaces', icon: 'LayoutGrid' },
}

// 创建页（Prepare → Apply 建工作区，UX 原型 §7.2。
// 列表页头部与空态的「新建工作区」入口承接本页，不再占用侧栏导航项）
const workspacesNew: RouteRecordRaw = {
    path: '/workspaces/new',
    name: 'workspaces-new',
    component: () => import('../views/WorkspacesNewView.vue'),
    meta: { titleKey: 'nav.workspacesNew' },
}

// 工作区详情：资源变更页（GetChanges 分页 + 筛选，UX 原型 §7.3。
// 入口在工作区列表行操作，不占侧栏导航项）
const workspacesChanges: RouteRecordRaw = {
    path: '/workspaces/:id/changes',
    name: 'workspaces-changes',
    component: () => import('../views/WorkspacesChangesView.vue'),
    meta: { titleKey: 'nav.workspacesChanges' },
}

// 工作区详情：受管范围页（GetMappingPolicy/UpdateMappingPolicy 读写 +
// 乐观锁编辑，UX 原型 §7.4。入口在工作区列表行操作，不占侧栏导航项）
const workspacesMappings: RouteRecordRaw = {
    path: '/workspaces/:id/mappings',
    name: 'workspaces-mappings',
    component: () => import('../views/WorkspacesMappingsView.vue'),
    meta: { titleKey: 'nav.workspacesMappings' },
}

// 工作区详情：设置页（授权模式开关 SetWorkspaceAuthorized，契约 06 §9，票 #62。
// 入口在工作区列表行操作，不占侧栏导航项）
const workspacesSettings: RouteRecordRaw = {
    path: '/workspaces/:id/settings',
    name: 'workspaces-settings',
    component: () => import('../views/WorkspacesSettingsView.vue'),
    meta: { titleKey: 'nav.workspacesSettings' },
}

// 工作区详情：计划页（PrepareSync/ResolvePlan/GetPlan 只读计划与
// choose_side 冲突解决，UX 原型 §7.5。无 Apply/History/Restore 入口，票 #21。
// 入口在工作区列表行操作与变化页头部，不占侧栏导航项）
const workspacesPlan: RouteRecordRaw = {
    path: '/workspaces/:id/plans/:plan_id',
    name: 'workspaces-plan',
    component: () => import('../views/WorkspacesPlanView.vue'),
    meta: { titleKey: 'nav.workspacesPlan' },
}

// 工作区详情：回滚计划页（PrepareRestore/ResolveRestorePlan/GetRestorePlan/
// StageUserObject/ConfirmRestorePlan，结构 B 单表全列，契约 06 §9，票 #61。
// 字面段 restore 与上一条 :plan_id 参数路由并存时静态段优先匹配；
// 入口唯一落在历史详情页主操作，不占侧栏导航项）
const workspacesRestorePlan: RouteRecordRaw = {
    path: '/workspaces/:id/plans/restore/:plan_id',
    name: 'workspaces-restore-plan',
    component: () => import('../views/WorkspacesRestorePlanView.vue'),
    meta: { titleKey: 'nav.workspacesRestorePlan' },
}

// 工作区详情：重新绑定页（PrepareRebind/ApplyRebind 重绑闭环，
// UX 原型 §7.6，票 #22。入口在工作区列表行操作与变化页头部（rebind_required），
// 不占侧栏导航项）
const workspacesRebind: RouteRecordRaw = {
    path: '/workspaces/:id/rebind',
    name: 'workspaces-rebind',
    component: () => import('../views/WorkspacesRebindView.vue'),
    meta: { titleKey: 'nav.workspacesRebind' },
}

// 工作区详情：同步历史页（ListCommits 分页，UX 原型 §7.7；history_view 门控，
// 契约 05 §5，票 #42。入口在工作区列表行操作由 T11 承接，不占侧栏导航项）
const workspacesHistory: RouteRecordRaw = {
    path: '/workspaces/:id/history',
    name: 'workspaces-history',
    component: () => import('../views/WorkspacesHistoryView.vue'),
    meta: { titleKey: 'nav.workspacesHistory' },
}

// 工作区详情：同步记录详情页（GetCommit 逐资源变更表，UX 原型 §7.8，
// 契约 05 §5。由历史页行进入，不占侧栏导航项）
const workspacesCommit: RouteRecordRaw = {
    path: '/workspaces/:id/history/:commit_id',
    name: 'workspaces-commit',
    component: () => import('../views/WorkspacesCommitView.vue'),
    meta: { titleKey: 'nav.workspacesCommit' },
}

// 工作区详情：恢复详情页（GetApplyRun/ListApplyOperations/AcknowledgeRecovery，
// UX 原型 §7.12；契约 05 §5 D2，run_id=task_id，票 #42。
// 「处理恢复」入口在任务中心/列表行由 T11 承接，不占侧栏导航项）
const workspacesRecovery: RouteRecordRaw = {
    path: '/workspaces/:id/recoveries/:run_id',
    name: 'workspaces-recovery',
    component: () => import('../views/WorkspacesRecoveryView.vue'),
    meta: { titleKey: 'nav.workspacesRecovery' },
}

const settings: RouteRecordRaw = {
    path: '/settings',
    name: 'settings',
    component: () => import('../views/SettingsView.vue'),
    meta: { titleKey: 'nav.settings', icon: 'Settings' },
}

// 侧栏导航项（一级导航仅四项；不含详情页等二级路由）
export const navRoutes: RouteRecordRaw[] = [workspaces, sources, runtimes, settings]

// 旧栈路由静默重定向（ADR-0001 §4：单发布切换后旧入口整体退场，无提示）
const legacyRedirect = { redirect: '/workspaces' } as const

const router = createRouter({
    history: createWebHashHistory(),
    routes: [
        ...navRoutes,
        workspacesNew,
        workspacesChanges,
        workspacesMappings,
        workspacesSettings,
        workspacesPlan,
        workspacesRestorePlan,
        workspacesRebind,
        workspacesHistory,
        workspacesCommit,
        workspacesRecovery,
        { path: '/', ...legacyRedirect },
        // :pathMatch(.*)* 亦可空重复，覆盖裸 /projects
        { path: '/projects/:pathMatch(.*)*', ...legacyRedirect },
        { path: '/instances', ...legacyRedirect },
        { path: '/dev', ...legacyRedirect },
        { path: '/:pathMatch(.*)*', ...legacyRedirect },
    ],
    // 返回时恢复滚动位置
    scrollBehavior(_to, _from, savedPosition) {
        return savedPosition ?? { top: 0 }
    },
})

export default router
