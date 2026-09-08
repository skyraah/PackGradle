import { LucideIcon } from 'lucide-react'

/**
 * @description 侧栏入口条目。不手工维护——由 sections 区段表投影（见 router.tsx 的 navItems），
 *              在 {@link Section} 注册即自动出现在侧栏，新增一级页面无需改动 App 外壳。
 */
export interface NavItem {
    path: string
    titleKey: string
    icon: LucideIcon
}


/**
 * @description 路由表条目：createHashRouter children 的元素形态，区段子页用相对路径挂在 {@link Section} 下。
 * @description handle 走 react-router 原生 handle 通道，组件内 useMatches() 可取回；
 *              顶栏标题按匹配链自叶向根取第一个 titleKey（main:frontend/src/App.tsx）。
 * @description index 条目渲染区段根路径本身（如 /workspaces），不带 path。
 *
 * @example
 * { index: true, element: <WorkspacesView />, handle: { titleKey: 'nav.workspaces' } }
 * { path: ':id/history', element: <WorkspacesHistoryView />, handle: { titleKey: 'nav.workspacesHistory' } }
 */
export interface RouteHandle {
    path?: string
    index?: boolean
    element: React.ReactNode
    handle?: {
        titleKey?: string
    }
}

/**
 * @description 一级导航区段：侧栏入口与一段嵌套路由的单一数据源（见 router.tsx 的 sections）。
 * @description 区段本体是合法路由对象——children 相对路径以 path 为前缀解析；不带 element 时
 *              react-router 按透传处理，子路由直接渲染在上级 Outlet。将来要加区段级布局
 *              （工作区头部/面包屑/局部边界）只需补 element 字段，子路径一条不动。
 */
export interface Section {
    path: string
    titleKey: string
    icon: LucideIcon
    children: RouteHandle[]
}
