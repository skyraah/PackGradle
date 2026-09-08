// 工作区切换器（壳层）：SidebarHeader 里的下拉切换件，结构对齐 shadcn sidebar-07 的
// TeamSwitcher，视觉对齐原型 .ws-btn/.ws-menu（26px 靛蓝头像 + 名/slug 双行 + 右侧单箭头；
// 菜单项 20px mini 头像 + 当前项右侧 Check）。纯展示组件——清单/选中项/回调全部由调用方
// 注入；store 接线缝在 AppSidebar 的占位数据处（ADR-0014 §2 分工：stores 是用户车道，
// 壳层只留挂点）。菜单仍走 ui/dropdown-menu 的 Radix 通道，选中指示用底层
// RadioGroup/RadioItem + ItemIndicator（ui 层现成 RadioItem 的指示器在左侧且是圆点，
// 不合原型，故此处直接用同源 primitive 自绘行内布局，定位/关闭行为不变）。
import { Check, ChevronDown, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { motion } from 'motion/react'
import { DropdownMenu as DropdownMenuPrimitive } from 'radix-ui'
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuRadioGroup,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
    SidebarMenu,
    SidebarMenuButton,
    SidebarMenuItem,
    useSidebar,
} from '@/components/ui/sidebar'
import { spring } from '@/lib/motion'
import { SpringButton, type SpringGeometry } from './SidebarSpring'

/** @description 切换器条目——store 落地后按真实工作区字段（id/name/slug）映射即可。 */
export interface SwitcherWorkspace {
    id: string
    name: string
    slug: string
}

/** @description 头像双字母缩写：取工作区名前两字符（原型 .ws-avatar 11px/590 缩写对）。 */
function initials(name: string): string {
    return name.trim().slice(0, 2).toUpperCase()
}

/** 触发钮折叠几何：高 44→32、内边距 9/7→0（SpringButton 与折叠主时钟同拍插值） */
const TRIGGER_GEOMETRY: SpringGeometry = { height: [44, 32], paddingX: [9, 0], paddingY: [7, 0] }

/**
 * @description 工作区切换器：触发钮（头像块 + 当前工作区名/slug 双行 + 展开箭头）+ 下拉
 *              清单 + 「新建工作区」。触发钮不用 size="lg"（lg 在 icon 态 p-0! 的坑见
 *              SidebarNav 勿动项②）；展开态高度钉 h-[44px]（非 h-auto，显式值才可插值）：
 *              44 = py-7×2 + 13px 行 ×1.25 + mt-px + 10px 行 ×1.25（leading-tight 行高只随
 *              font-size，数值稳定），改字号/行高须连带校准。icon 收起态几何（高 44→32、
 *              内边距 9/7→0、头像 26→32）由 SpringButton 弹簧直驱（spring.sidebar 与折叠
 *              主时钟同拍）；32px 头像块把文字挤到裁切线外的老规矩不变（与 SidebarNav
 *              勿动项同理）。
 */
export default function WorkspaceSwitcher({
    workspaces,
    activeId,
    onSelect,
    onCreate,
}: {
    workspaces: SwitcherWorkspace[]
    activeId?: string
    onSelect: (id: string) => void
    onCreate: () => void
}) {
    const { t } = useTranslation()
    const { isMobile, state: sidebarState } = useSidebar()
    const collapsed = sidebarState === 'collapsed'
    const active = workspaces.find((w) => w.id === activeId)

    return (
        <SidebarMenu>
            <SidebarMenuItem>
                <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                        <SidebarMenuButton
                            asChild
                            className="h-11 gap-2.5 rounded-lg px-2.25 py-1.75 data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground data-[state=open]:[&>svg]:rotate-180"
                        >
                            {/* 折叠几何（高 44→32、内边距 9/7→0、头像 26→32）由 SpringButton
                                弹簧驱动（见文件头 JSDoc）；其内建 motion.button 保住原生按钮
                                语义——asChild 链上 Radix 不再兜底 type/焦点 */}
                            <SpringButton
                                type="button"
                                collapsed={collapsed}
                                geometry={TRIGGER_GEOMETRY}
                            >
                                <motion.div
                                    className="bg-sidebar-primary text-sidebar-primary-foreground flex size-6.5 shrink-0 items-center justify-center rounded-md text-[11px] font-semibold tracking-wide"
                                    initial={false}
                                    transition={spring.sidebar}
                                    animate={{ width: collapsed ? 32 : 26, height: collapsed ? 32 : 26 }}
                                >
                                    {active ? initials(active.name) : 'PG'}
                                </motion.div>
                                <div className="flex min-w-0 flex-1 flex-col text-left leading-tight">
                                    <span className="text-sidebar-accent-foreground truncate text-[13px] font-medium">
                                        {active?.name ?? t('sidebar.workspaceLabel')}
                                    </span>
                                    {active && (
                                        <span className="text-sidebar-muted-foreground mt-px truncate font-mono text-[10px]">
                                            {active.slug}
                                        </span>
                                    )}
                                </div>
                                {/* 12px 对齐原型 .ws-chevron；展开随触发钮 data-state 旋转 180°
                                    （基类注册的是 peer/menu-button 而非 group，故经按钮子代选择器落地） */}
                                <ChevronDown className="text-sidebar-muted-foreground size-3! shrink-0 transition-transform" />
                            </SpringButton>
                        </SidebarMenuButton>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent
                        // 投影分主题：原型 .ws-menu 的重投影是暗色默认值，亮色原型另有
                        // 覆盖 box-shadow:var(--elev-raised)（0 8px 24px 14% + 1px 5% 环），
                        // 硬编码单值会让亮色戴上暗色的 20% 黑环 + 40% 黑投影
                        className="min-w-56 rounded-[10px] p-1.5 shadow-[0_8px_24px_rgba(11,13,15,0.14),0_0_0_1px_rgba(11,13,15,0.05)] dark:shadow-[0_12px_32px_rgba(0,0,0,0.4),0_0_0_1px_rgba(0,0,0,0.2)] w-(--radix-dropdown-menu-trigger-width)"
                        align="start"
                        side={isMobile ? 'bottom' : 'right'}
                        sideOffset={4}
                    >
                        <DropdownMenuRadioGroup value={activeId ?? ''}>
                            {workspaces.map((w) => (
                                <DropdownMenuPrimitive.RadioItem
                                    key={w.id}
                                    value={w.id}
                                    // 行内布局对齐原型 .ws-item（8px 内边距、9px 间距、6px 圆角），
                                    // 选中指示为右侧 14px Check（ItemIndicator 仅当前项渲染）
                                    className="flex cursor-default items-center gap-2.5 rounded-md p-2 text-[13px] outline-hidden select-none data-disabled:pointer-events-none data-disabled:opacity-50 focus:bg-sidebar-accent/60 focus:text-sidebar-accent-foreground [&_svg]:pointer-events-none [&_svg]:shrink-0"
                                    onSelect={() => onSelect(w.id)}
                                >
                                    <span className="flex size-5 shrink-0 items-center justify-center rounded-[5px] bg-[color-mix(in_oklab,var(--foreground)_9%,transparent)] text-[9px] font-semibold text-muted-foreground">
                                        {initials(w.name)}
                                    </span>
                                    <span className="flex min-w-0 flex-col">
                                        <span className="truncate">{w.name}</span>
                                        <span className="truncate font-mono text-[10px] text-muted-foreground">
                                            {w.slug}
                                        </span>
                                    </span>
                                    <DropdownMenuPrimitive.ItemIndicator className="ml-auto flex-none text-foreground">
                                        <Check className="size-3.5" strokeWidth={2} />
                                    </DropdownMenuPrimitive.ItemIndicator>
                                </DropdownMenuPrimitive.RadioItem>
                            ))}
                        </DropdownMenuRadioGroup>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem className="gap-2.5 p-2 text-[13px]" onSelect={onCreate}>
                            <Plus className="text-muted-foreground" />
                            {t('sidebar.newWorkspace')}
                        </DropdownMenuItem>
                    </DropdownMenuContent>
                </DropdownMenu>
            </SidebarMenuItem>
        </SidebarMenu>
    )
}
