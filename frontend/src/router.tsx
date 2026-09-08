import { createHashRouter, Navigate } from 'react-router'
import { Gamepad2, LayoutGrid, LucideIcon, Package, Settings as SettingsIcon } from 'lucide-react'
import App from './App'
import RouteErrorView from './components/common/RouteErrorView'
import {
    ChangesView,
    CommitView,
    HistoryView,
    MappingsView,
    NewView,
    PlanView,
    RecoveryView,
    RebindView,
    RestorePlanView,
    WorkspacesView,

    RuntimesView,
    SettingsView,
    SourcesView,
} from './views'

import type { NavItem, Section, RouteHandle} from './interface'

const routeHandle = (pathOrIndex: true | string, element: React.ReactNode, titleKey?: string): RouteHandle => {
    return pathOrIndex === true
        ? { index: true, element, handle: titleKey ? { titleKey } : undefined }
        : { path: pathOrIndex, element, handle: titleKey ? { titleKey } : undefined }
}

const sectionHandle = (path: string, titleKey: string, icon: LucideIcon, children: RouteHandle[]): Section => {
    return {
        path: path,
        titleKey: titleKey,
        icon: icon,
        children: children,
    }
}

/**
 * @description 一级导航区段表——侧栏与路由表的单一数据源。每个区段注册同时决定两件事：
 *              icon rail 上的一个入口（path/titleKey/icon）与路由树里的一段嵌套路由（children，
 *              子页一律相对路径）。新增一级页面只在此追加一个对象，双端同时生效，
 *              不会出现「侧栏有、路由无」的单边遗漏。
 */
const sections: Section[] = [
    sectionHandle('/workspaces', 'nav.workspaces', LayoutGrid, [
        routeHandle(true, <WorkspacesView />, 'nav.workspaces'),
        routeHandle("new", <NewView />, 'nav.workspacesChanges'),
        routeHandle(":id/changes", <ChangesView />, 'nav.workspacesChanges'),
        routeHandle(":id/mappings", <MappingsView />, 'nav.workspacesMappings'),
        routeHandle(":id/plan/:plan_id", <PlanView />, 'nav.workspacesPlan'),
        routeHandle(":id/restore-plan/:plan_id", <RestorePlanView />, 'nav.workspacesRestorePlan'),
        routeHandle(":id/rebind", <RebindView />, 'nav.workspacesRebind'),
        routeHandle(":id/history", <HistoryView />, 'nav.workspacesHistory'),
        routeHandle(":id/history/:commit_id", <CommitView />, 'nav.workspacesCommit'),
        routeHandle(":id/recoveries/:run_id", <RecoveryView />, 'nav.workspacesRecovery'),
    ]),
    sectionHandle('/sources', 'nav.sources', Package, [
        routeHandle(true, <SourcesView />, 'nav.sources')
    ]),
    sectionHandle('/runtimes', 'nav.runtimes', Gamepad2, [
        routeHandle(true, <RuntimesView />, 'nav.runtimes')
    ]),
    sectionHandle('/settings', 'nav.settings', SettingsIcon, [
        routeHandle(true, <SettingsView />, 'nav.settings')
    ]),
]

/**
 * @description 侧栏入口清单：由 {@link sections} 投影而来，App 外壳据此渲染 icon rail
 *              （main:frontend/src/App.tsx），不维护第二份路径表。
 */
export const navItems: NavItem[] = sections.map(({ path, titleKey, icon }) => ({ path, titleKey, icon }))

/**
 * @description catch-all 重定向：未匹配路径一律弹回 /workspaces；
 *              replace 避免在历史栈里留下死地址。
 */
const redirectToWorkspaces = <Navigate to="/workspaces" replace />

/**
 * @description 应用路由器（hash 模式——Wails webview 无服务端路由可依托）。
 *              children 由 {@link sections} 各区段摊平而成 + 一条 catch-all；
 *              根 errorElement 兜住任意路由渲染期抛错并接管整壳（见 RouteErrorView），
 *              子路由无需各自挂错误边界。
 */
export const router = createHashRouter([
    {
        element: <App />,
        errorElement: <RouteErrorView />,
        children: [...sections, { path: '*', element: redirectToWorkspaces }],
    },
])
