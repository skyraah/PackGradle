// 侧栏导航区（壳层，自 AppSidebar 拆出）：navItems 入口清单渲染 + 「运行实例」药丸徽标。
// 入口清单直接消费 router.tsx 投影出的 navItems——sections 是唯一注册点，本组件零路由
// 知识（注册语义见 router.tsx 的 sections JSDoc）。
// 导入环警告（自 AppSidebar 随行）：router → App → AppSidebar → 本组件 → @/router，
// 本模块顶层求值时 navItems 尚在 TDZ（2026-09-08 白屏事故）——图标 motion 包装的缓存
// 禁止模块级，延迟到渲染期 useMemo 内建（壳层单例，等效一次性）。
import { useMemo } from 'react'
import { useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import { motion } from 'motion/react'
import { navItems } from '@/router'
import {
    SidebarGroupContent,
    SidebarMenu,
    SidebarMenuBadge,
    SidebarMenuButton,
    SidebarMenuItem,
    useSidebar,
} from '@/components/ui/sidebar'
import { spring } from '@/lib/motion'
import { SIDEBAR_SECTION_PAD, SpringDiv, SpringLink, type SpringGeometry } from './SidebarSpring'
import { PLACEHOLDER_RUNNING_RUNTIMES } from '../placeholders'
import { activeRootOf } from '../routing'

/** 导航行几何：34px 高/13px 对齐原型 .nav-item；折叠 34→32、内边距 10→6——收起态
    32px 盒 = 20px 图标 + 6px×2，改图标档须联动 (32 − 图标宽)/2 改收起态内边距 */
const NAV_ROW: SpringGeometry = { height: [34, 32], paddingX: [10, 6] }

/**
 * @description 导航菜单：icon 模式可折叠——展开态显示 icon+标签，收起态只剩 icon rail
 *              （宽度与图标对齐均为 shadcn 默认 3rem）。两个勿动项：①勿覆写
 *              --sidebar-width-icon——默认 48px 下 32px 按钮被「图标 20px + 内边距
 *              6px×2」恰好填满、天然居中；②菜单钮勿传 size="lg"——lg 在 icon 态的
 *              p-0! 会把内容盒撑回 32px，span 被 flex-shrink 压剩 8px，恰好露出半个汉字
 *              （例外：WorkspaceSwitcher 触发钮——首个子元素在 icon 态保证是 32px 块）。
 *              高亮按一级路径归属（activeRootOf），/workspaces/:id/... 下所有子页都
 *              高亮「工作区」。
 */
export default function SidebarNav() {
    const { t } = useTranslation()
    const location = useLocation()
    const { state: sidebarState } = useSidebar()
    const collapsed = sidebarState === 'collapsed'
    // 图标 motion 包装的渲染期缓存（模块级会踩 router 导入环的 TDZ，见文件头注释）
    const motionNavIcons = useMemo(
        () => new Map(navItems.map((item) => [item.path, motion.create(item.icon)])),
        []
    )
    const activeRoot = activeRootOf(location.pathname)

    return (
        // 分区容器折叠内边距（12→8px）由 SpringDiv 弹簧驱动；基类就地内联自 ui/sidebar
        // 原件（data-sidebar 语义保留，升级时对照合并）
        <SpringDiv
            data-sidebar="group"
            collapsed={collapsed}
            geometry={SIDEBAR_SECTION_PAD}
            className="relative flex w-full min-w-0 flex-col px-3 py-2"
        >
            <SidebarGroupContent>
                <SidebarMenu className="gap-0.5">
                    {navItems.map((item) => {
                        // 渲染期缓存的图标 motion 组件（每图标仅建一次）
                        const MotionNavIcon = motionNavIcons.get(item.path)!
                        return (
                            <SidebarMenuItem key={item.path}>
                                <SidebarMenuButton
                                    asChild
                                    isActive={activeRoot === item.path}
                                    tooltip={t(item.titleKey)}
                                    // 34px/13px medium 对齐原型 .nav-item；图标色随态迁移
                                    // （muted → hover fg-2 → active fg），描边 1.5 由 lucide 透传；
                                    // 折叠几何（高 34→32、内边距 10→6、图标 16→20——放大档
                                    // 对齐 header 头像 26→32）由 SpringLink/MotionNavIcon 以
                                    // spring.sidebar 驱动；32px 盒 = 20px 图标 + 6px×2，
                                    // 改图标档须联动 (32-图标宽)/2 改内边距目标值
                                    className="h-8.5 gap-2.5 px-2.5 text-[13px] font-medium hover:bg-sidebar-accent/50 [&>svg]:text-sidebar-muted-foreground hover:[&>svg]:text-sidebar-foreground data-[active=true]:[&>svg]:text-sidebar-accent-foreground"
                                >
                                    <SpringLink to={item.path} collapsed={collapsed} geometry={NAV_ROW}>
                                        <MotionNavIcon
                                            strokeWidth={1.5}
                                            initial={false}
                                            transition={spring.sidebar}
                                            animate={{
                                                width: collapsed ? 20 : 16,
                                                height: collapsed ? 20 : 16,
                                            }}
                                        />
                                        <span>{t(item.titleKey)}</span>
                                    </SpringLink>
                                </SidebarMenuButton>
                                {/* 原型 .nav-badge：药丸、11px medium、中性灰底；数值来自占位常量；
                                    基类 icon 态 hidden 以同组 flex 抵消，改 invisible+淡出——
                                    display 不可过渡；药丸绝对定位随行宽滑行，末帧才移出可读域 */}
                                {item.path === '/runtimes' && PLACEHOLDER_RUNNING_RUNTIMES > 0 && (
                                    <SidebarMenuBadge className="h-auto min-w-0 rounded-full bg-[color-mix(in_oklab,var(--foreground)_7%,transparent)] px-1.75 py-px text-[11px] text-sidebar-muted-foreground group-data-[collapsible=icon]:flex group-data-[collapsible=icon]:invisible group-data-[collapsible=icon]:opacity-0 transition-[opacity,visibility] duration-200 ease-linear">
                                        {PLACEHOLDER_RUNNING_RUNTIMES}
                                    </SidebarMenuBadge>
                                )}
                            </SidebarMenuItem>
                        )
                    })}
                </SidebarMenu>
            </SidebarGroupContent>
        </SpringDiv>
    )
}
