// 应用路由（ADR-0014 脚手架）：hash 历史（Wails 以静态资源服务器托管前端，hash 模式深链/刷新不 404）。
// 一级导航按 UX 原型 §4.1：工作区 / 项目源 / 运行实例 / 设置，默认首页 /workspaces。
// 旧栈路由（/、/projects*、/instances、/dev）与 catch-all 静默重定向 /workspaces（ADR-0001 §4）。
// 每条路由的注释 = Vue 树（main:frontend/src/router/index.ts）的契约引用，移植页面时照此接线。
import { createHashRouter, Navigate } from 'react-router'
import type { LucideIcon } from 'lucide-react'
import { Gamepad2, LayoutGrid, Package, Settings as SettingsIcon } from 'lucide-react'
import App from './App'
import RuntimesView from './views/RuntimesView'
import SettingsView from './views/SettingsView'
import SourcesView from './views/SourcesView'
import WorkspacesChangesView from './views/WorkspacesChangesView'
import WorkspacesCommitView from './views/WorkspacesCommitView'
import WorkspacesHistoryView from './views/WorkspacesHistoryView'
import WorkspacesMappingsView from './views/WorkspacesMappingsView'
import WorkspacesNewView from './views/WorkspacesNewView'
import WorkspacesPlanView from './views/WorkspacesPlanView'
import WorkspacesRebindView from './views/WorkspacesRebindView'
import WorkspacesRecoveryView from './views/WorkspacesRecoveryView'
import WorkspacesRestorePlanView from './views/WorkspacesRestorePlanView'
import WorkspacesSettingsView from './views/WorkspacesSettingsView'
import WorkspacesView from './views/WorkspacesView'

export interface NavItem {
    path: string
    titleKey: string
    icon: LucideIcon
}

// 侧栏导航项（一级导航仅四项，UX 原型 §5.1 常显不可收起；不含详情页等二级路由）。
// 新增页面只需在此注册即可自动出现在侧栏。
export const navItems: NavItem[] = [
    // 工作区页（契约 04 缓存骨架 + 列表页；导航 IA 见 docs/frontend/05 §4.1）
    { path: '/workspaces', titleKey: 'nav.workspaces', icon: LayoutGrid },
    // 端点管理页（契约 03 Project/Runtime 服务 + UX 原型 §7.x）
    { path: '/sources', titleKey: 'nav.sources', icon: Package },
    { path: '/runtimes', titleKey: 'nav.runtimes', icon: Gamepad2 },
    { path: '/settings', titleKey: 'nav.settings', icon: SettingsIcon },
]

// 旧栈路由静默重定向（ADR-0001 §4：单发布切换后旧入口整体退场，无提示）
const redirectToWorkspaces = <Navigate to="/workspaces" replace />

export const router = createHashRouter([
    {
        element: <App />,
        children: [
            { path: '/workspaces', element: <WorkspacesView />, handle: { titleKey: 'nav.workspaces' } },
            // 创建页（Prepare → Apply 建工作区，UX 原型 §7.2。列表页头部与空态的
            // 「新建工作区」入口承接本页，不占侧栏导航项）
            { path: '/workspaces/new', element: <WorkspacesNewView />, handle: { titleKey: 'nav.workspacesNew' } },
            // 工作区详情：资源变更页（GetChanges 分页 + 筛选，UX 原型 §7.3。入口在工作区列表行操作）
            { path: '/workspaces/:id/changes', element: <WorkspacesChangesView />, handle: { titleKey: 'nav.workspacesChanges' } },
            // 工作区详情：受管范围页（GetMappingPolicy/UpdateMappingPolicy 读写 + 乐观锁编辑，
            // UX 原型 §7.4。入口在工作区列表行操作）
            { path: '/workspaces/:id/mappings', element: <WorkspacesMappingsView />, handle: { titleKey: 'nav.workspacesMappings' } },
            // 工作区详情：设置页（授权模式开关 SetWorkspaceAuthorized，契约 06 §9，票 #62）
            { path: '/workspaces/:id/settings', element: <WorkspacesSettingsView />, handle: { titleKey: 'nav.workspacesSettings' } },
            // 工作区详情：计划页（PrepareSync/ResolvePlan/GetPlan 只读计划与 choose_side 冲突解决，
            // UX 原型 §7.5。无 Apply/History/Restore 入口，票 #21）
            { path: '/workspaces/:id/plans/:plan_id', element: <WorkspacesPlanView />, handle: { titleKey: 'nav.workspacesPlan' } },
            // 工作区详情：回滚计划页（PrepareRestore/ResolveRestorePlan/GetRestorePlan/StageUserObject/
            // ConfirmRestorePlan，结构 B 单表全列，契约 06 §9，票 #61。字面段 restore 与上一条
            // :plan_id 参数路由并存时静态段优先匹配——react-router 同样按静态段加权；
            // 入口唯一落在历史详情页主操作）
            { path: '/workspaces/:id/plans/restore/:plan_id', element: <WorkspacesRestorePlanView />, handle: { titleKey: 'nav.workspacesRestorePlan' } },
            // 工作区详情：重新绑定页（PrepareRebind/ApplyRebind 重绑闭环，UX 原型 §7.6，票 #22。
            // 入口在工作区列表行操作与变化页头部（rebind_required））
            { path: '/workspaces/:id/rebind', element: <WorkspacesRebindView />, handle: { titleKey: 'nav.workspacesRebind' } },
            // 工作区详情：同步历史页（ListCommits 分页，UX 原型 §7.7；history_view 门控，契约 05 §5，票 #42）
            { path: '/workspaces/:id/history', element: <WorkspacesHistoryView />, handle: { titleKey: 'nav.workspacesHistory' } },
            // 工作区详情：同步记录详情页（GetCommit 逐资源变更表，UX 原型 §7.8，契约 05 §5。由历史页行进入）
            { path: '/workspaces/:id/history/:commit_id', element: <WorkspacesCommitView />, handle: { titleKey: 'nav.workspacesCommit' } },
            // 工作区详情：恢复详情页（GetApplyRun/ListApplyOperations/AcknowledgeRecovery，UX 原型 §7.12；
            // 契约 05 §5 D2，run_id=task_id，票 #42。「处理恢复」入口在任务中心/列表行）
            { path: '/workspaces/:id/recoveries/:run_id', element: <WorkspacesRecoveryView />, handle: { titleKey: 'nav.workspacesRecovery' } },
            { path: '/settings', element: <SettingsView />, handle: { titleKey: 'nav.settings' } },
            { path: '/', element: redirectToWorkspaces },
            { path: '/projects/*', element: redirectToWorkspaces },
            { path: '/instances', element: redirectToWorkspaces },
            { path: '/dev', element: redirectToWorkspaces },
            { path: '*', element: redirectToWorkspaces },
        ],
    },
])
