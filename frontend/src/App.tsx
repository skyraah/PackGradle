// 应用壳层（ADR-0014 脚手架）：拼装层——左侧可折叠边栏（AppSidebar，shadcn/ui Sidebar，
// 头部带工作区切换器）+ 主区顶条（工作区 slug/视图名面包屑靠左、行尾嵌入窗口控制组
// （折叠侧栏钮 + 最小化/最大化/关闭三键，WindowControls），整条兼作无边框窗口拖拽面）
// + 主内容区（Outlet）。无标题栏设计：整壳由 SidebarProvider 的
// 包裹层直接充当（flex 行布局），顶部另有一条 8px 隐形拖拽带兜底侧栏上沿。
// 壳件细节各自住在 components/shell/，本文件只做拼装：折叠主时钟在 useSidebarCollapse
// （open/onOpenChange），路径推导在 shell/routing，占位数据在 shell/placeholders。
// 随 store 移植接回（ADR-0014 §2 分工，挂点见各壳件头注释）：
// - 主题切换已接 stores/theme（WindowControls 消费，View Transitions 换肤收编进 store）
// - 全局通知 snackbar 已接 stores/ui（本文件 SidebarProvider 内尾部挂 Snackbar 覆盖层）
// - 面包屑数据源换真实工作区 store（stores/workspace）
// - 任务中心铃铛（活跃任务数徽标）与抽屉（stores/ui 的 taskDrawerOpen + stores/syncCache 的 tasks）→ 主区顶条右侧
// - 顶栏 MOCK 徽标（仅 dev 构建 __DEV__ 门控 + 动态导入裁剪，ADR-0001 §5）→ 主区顶条右侧
import { Outlet, useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import AppSidebar from './components/shell/sidebar/AppSidebar'
import WindowControls from './components/shell/WindowControls'
import { useSidebarCollapse } from './components/shell/useSidebarCollapse'
import { PLACEHOLDER_WORKSPACES } from './components/shell/placeholders'
import { activeRootOf, workspaceIdOf } from './components/shell/routing'
import { Snackbar } from './components/common/Snackbar'
import { navItems } from '@/router'
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'

/**
 * @description 应用壳：无边框无标题栏——主区顶条常驻（面包屑靠左 + 行尾窗口控制组，
 *              折叠钮在窄窗口时也是侧栏 Sheet 抽屉的入口），内容全幅铺满，顶部一条
 *              8px 隐形拖拽带兜底侧栏上沿。主内容区是唯一页面纵向滚动容器。侧栏开合
 *              受控收口：Ctrl+B / 顶条折叠钮 / 一切 toggleSidebar 都汇到 onOpenChange，
 *              折叠主时钟见 useSidebarCollapse；cookie 仍由 Provider 内建写入。
 */
export default function App() {
    const { t } = useTranslation()
    const location = useLocation()
    // 视图名按一级路径归属匹配（与 SidebarNav 高亮同源 activeRootOf），slug 取工作区占位数据
    const activeNavItem = navItems.find((item) => item.path === activeRootOf(location.pathname))
    // TODO(store): 面包屑 slug 待接用户工作区 store（PLACEHOLDER_WORKSPACES 为壳层占位）
    const workspaceId = workspaceIdOf(location.pathname)
    const activeSlug = (
        PLACEHOLDER_WORKSPACES.find((w) => w.id === workspaceId) ?? PLACEHOLDER_WORKSPACES[0]
    )?.slug

    const { open: sidebarOpen, onOpenChange: handleSidebarOpen } = useSidebarCollapse()

    return (
        <SidebarProvider
            open={sidebarOpen}
            onOpenChange={handleSidebarOpen}
            // h-svh 钉死壳高等于视口（官方默认 min-h-svh 会随内容长高，内层
            // overflow-y-auto 永不激活、超视口内容在应用窗口不可达——body
            // overflow:hidden 档掉文档滚动，第一条超高页 settings 首次暴露）
            className="h-svh overflow-hidden"
            // --sidebar-width 不进 style（折叠动画的驱动变量，见 useSidebarCollapse）；
            // 移动端 Sheet 宽独立覆写（官方默认 18rem）
            style={{ '--sidebar-width-mobile': '20rem' } as React.CSSProperties}
        >
            <AppSidebar />
            {/* inset 折叠态对称修正：官方收起时给主区加 ml-2，以 ! 压回 ml-0（两态皆 0，
                卡片左缘 = gap 逐帧随动，自身无需动画）。
                overflow-hidden：border-radius 不裁子元素，行尾贴角的窗口控件 hover 底色
                会方角戳出卡片圆角——卡片层统一裁切（控件本体方角设计不变，弹层走 portal
                不受影响） */}
            <SidebarInset className="flex min-h-0 flex-col overflow-hidden md:peer-data-[variant=inset]:peer-data-[state=collapsed]:ml-0!">
                {/* 主区顶条：面包屑靠左（原型 .topbar 左端 20px），行尾嵌入窗口控制组
                    （折叠钮 + 三键，自身 ml-auto 贴右、顶条不留右内边距）；整条 app-drag
                    兼作拖拽面，控制组内部自压 app-no-drag */}
                <header className="border-sidebar-border app-drag flex h-11 flex-none items-center border-b pl-5">
                    {/* 面包屑：工作区slug / 视图名（原型 .crumb：13px/510 medium，单行截断；
                        色对齐原型 titlebar muted（侧栏 muted #8a8f98）；全局暗板不动） */}
                    <span className="text-sidebar-muted-foreground min-w-0 truncate text-[13px] font-medium">
                        {activeSlug} / {activeNavItem ? t(activeNavItem.titleKey) : ''}
                    </span>
                    <WindowControls />
                    {/* [挂点] 主题切换/任务中心铃铛/MOCK 徽标——见文件头注释 */}
                </header>
                {/* 主内容区：唯一页面纵向滚动容器 */}
                <div className="min-h-0 flex-1 overflow-y-auto">
                    <Outlet />
                </div>
            </SidebarInset>
            {/* 无边框窗口拖拽带：顶条之外兜底侧栏上沿的整窗拖动面（窗口控制组以 z-30 压过它） */}
            <div className="app-drag fixed inset-x-0 top-0 z-20 h-2" />
            {/* 全局通知 snackbar（stores/ui 排队驱动，右下角 toast）——壳层挂载点 */}
            <Snackbar />
        </SidebarProvider>
    )
}
