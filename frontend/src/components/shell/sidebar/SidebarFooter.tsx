// 侧栏 footer（壳层，自 AppSidebar 拆出；原型 .side-foot）：折叠行（收起/展开入口之一，
// 与主区顶条主题钮对调而来）+ GitHub 行，icon 收起态各自退化为单图标。两行同构——
// SidebarMenuButton 挂行文本类 + SpringButton/SpringAnchor 挂折叠几何，仅图标/回调/
// 尾随件不同；行宽 w-full 随 --sidebar-width 浮动无需动画。
import { useTranslation } from 'react-i18next'
import { ArrowUpRight, PanelLeft } from 'lucide-react'
import { SidebarMenuButton, useSidebar } from '@/components/ui/sidebar'
import { SpringAnchor, SpringButton, type SpringGeometry } from './SidebarSpring'

/** footer 行几何：高 34→32、内边距 10→8；行宽 w-full 随容器浮动，收起态容器 64px −
    p-2×2 − footer 8×2 = 32px 自然到位、只剩图标（此时折叠行语义为展开） */
const FOOTER_ROW: SpringGeometry = { height: [34, 32], paddingX: [10, 8] }

/** 行文本类：13px medium、图标 muted 随 hover 回前景（与导航行同族，色档略异） */
const ROW_CLASS =
    'h-[34px] gap-2.5 px-2.5 text-[13px] font-medium hover:bg-sidebar-accent/50 hover:text-sidebar-accent-foreground [&>svg]:text-sidebar-muted-foreground hover:[&>svg]:text-sidebar-foreground'

/**
 * @description GitHub 市标（原型 .side-foot 同款填充式 GitHub mark）：lucide 已移除品牌
 *              图标（v1.x 无 Github 导出），故按原型 SVG 原样内联，15px、fill=currentColor。
 */
function GithubMark({ className }: { className?: string }) {
    return (
        <svg
            className={className}
            width="15"
            height="15"
            viewBox="0 0 16 16"
            fill="currentColor"
            aria-hidden="true"
        >
            <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
        </svg>
    )
}

/** @description footer 两行：折叠行（整行即按钮）+ GitHub 外链行 */
export default function SidebarFooter() {
    const { t } = useTranslation()
    const { state: sidebarState, toggleSidebar } = useSidebar()
    const collapsed = sidebarState === 'collapsed'

    return (
        <>
            <SidebarMenuButton asChild className={ROW_CLASS}>
                <SpringButton
                    type="button"
                    onClick={toggleSidebar}
                    collapsed={collapsed}
                    geometry={FOOTER_ROW}
                >
                    <PanelLeft className="size-3.75! shrink-0" strokeWidth={1.5} />
                    <span className="truncate font-normal">{t('app.toggleSidebar')}</span>
                </SpringButton>
            </SidebarMenuButton>
            <SidebarMenuButton asChild className={ROW_CLASS}>
                <SpringAnchor
                    href="https://github.com/skyraah/PackGradle"
                    target="_blank"
                    rel="noreferrer"
                    collapsed={collapsed}
                    geometry={FOOTER_ROW}
                >
                    <GithubMark className="size-3.75! shrink-0" />
                    <span>GitHub</span>
                    <ArrowUpRight className="ml-auto size-3! text-muted-foreground" />
                </SpringAnchor>
            </SidebarMenuButton>
        </>
    )
}
