// 左侧边栏（壳层）：shadcn/ui Sidebar 组合件（components/ui/sidebar.tsx）的拼装层——
// 头部工作区切换器（WorkspaceSwitcher）+ 导航区（SidebarNav）+ footer（SidebarFooter：
// 折叠行 + GitHub 行），三段各自成文件；本组件只做拼装、分区折叠时钟（SpringDiv 统一
// 内边距几何）与切换器的占位接线（placeholders.ts，TODO(store)）。
// 收起/展开入口：Ctrl+B、footer 折叠行（收起态退化为 PanelLeft 图标）、主区顶条折叠钮；
// 窗口窄于 768px（use-mobile）时退化为 Sheet 抽屉，footer 折叠行仍是入口。
// SidebarRail 边缘热区刻意不挂载（hover 竖线+原生 title 提示+整条边框感三象皆此件），
// 勿照官方示例补回；可插拔件不挂载即不渲染，ui/sidebar 原件不动。
// 折叠几何数值契约与勿动项（--sidebar-width-icon 勿覆写、菜单钮勿传 size=lg）见
// SidebarNav 头注释；折叠弹簧族见 SidebarSpring。
import { useLocation, useNavigate } from 'react-router'
import WorkspaceSwitcher from './WorkspaceSwitcher'
import SidebarNav from './SidebarNav'
import SidebarFooter from './SidebarFooter'
import { SIDEBAR_SECTION_PAD, SpringDiv } from './SidebarSpring'
import { PLACEHOLDER_WORKSPACES } from '../placeholders'
import { workspaceIdOf } from '../routing'
import { Sidebar, SidebarContent, useSidebar } from '@/components/ui/sidebar'

/**
 * @description 左侧边栏：icon 模式可折叠——展开态显示 icon+标签，收起态只剩 icon rail。
 *              高亮逻辑在 SidebarNav（按一级路径归属）；贴近原型的调用处覆写：头部
 *              px-3 py-2.5 + 底边框、footer 顶边框，颜色全部走 --sidebar-* token
 *              （main.css）。变体注：原型定稿是贴边 variant=sidebar（自动右边框），
 *              当前临时切 inset 看效果——两态差异与还原要点见 Sidebar 元素处注释。
 */
export default function AppSidebar() {
    const location = useLocation()
    const navigate = useNavigate()
    const { state: sidebarState } = useSidebar()
    const collapsed = sidebarState === 'collapsed'
    const workspaceId = workspaceIdOf(location.pathname)
    const activeWorkspaceId = PLACEHOLDER_WORKSPACES.some((w) => w.id === workspaceId)
        ? workspaceId
        : undefined

    return (
        // variant=inset 试用：整窗画布变 --sidebar 色（Provider 的 has-data-[variant=inset]）、
        // 主区变 rounded-xl 阴影浮层卡片（SidebarInset 的 peer-data 样式自动生效）；inset 分支
        // 容器无 border-r（那条只配给贴边 sidebar 变体），贴边时代的 border-sidebar-border
        // 修边框色补丁随之移除——回贴边时须连该 className 一起还原，否则 border-r 落
        // currentColor 出亮线（本项目无官方 *{border-color} 兜底）
        <Sidebar collapsible="icon" variant="inset">
            {/* 侧栏头部/footer 的折叠内边距（12→8px）由 SpringDiv 弹簧驱动；基类就地
                内联自 ui/sidebar 原件（data-sidebar 语义保留，升级时对照合并） */}
            <SpringDiv
                data-sidebar="header"
                collapsed={collapsed}
                geometry={SIDEBAR_SECTION_PAD}
                className="flex flex-col gap-2 border-sidebar-border border-b px-3 py-2.5"
            >
                <WorkspaceSwitcher
                    workspaces={PLACEHOLDER_WORKSPACES}
                    activeId={activeWorkspaceId}
                    onSelect={(id) => navigate(`/workspaces/${id}/changes`)}
                    onCreate={() => navigate('/workspaces/new')}
                />
            </SpringDiv>
            <SidebarContent>
                <SidebarNav />
            </SidebarContent>
            <SpringDiv
                data-sidebar="footer"
                collapsed={collapsed}
                geometry={SIDEBAR_SECTION_PAD}
                className="flex flex-col gap-0.5 border-sidebar-border border-t px-3 py-2"
            >
                <SidebarFooter />
            </SpringDiv>
        </Sidebar>
    )
}
