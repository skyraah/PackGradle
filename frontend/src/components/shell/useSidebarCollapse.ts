// 折叠主时钟（壳层，自 App.tsx 拆出）：侧栏开合态的持有与 --sidebar-width 逐帧驱动——
// App 只消费 { open, onOpenChange } 喂给 SidebarProvider，宽度换算/弹簧调度细节住在
// 本件，不再摊在应用壳文件里。
import { useLayoutEffect, useState } from 'react'
import { animate } from 'motion/react'
import { spring } from '@/lib/motion'

/**
 * @description 侧栏开合态从 sidebar_state cookie 还原——SidebarProvider 每次 toggle 都会
 *              写该 cookie（shadcn 内建），Wails webview 档案落盘，重启后保持上次状态。
 */
function readSidebarOpen(): boolean {
    const saved = document.cookie
        .split('; ')
        .find((c) => c.startsWith('sidebar_state='))
        ?.slice('sidebar_state='.length)
    return saved !== 'false'
}

/** @description rem→px：运行时读根字号（跟随系统缩放/无障碍字号）——motion 目标值须用 px，
 *              与逐帧插值通约（rem 字符串不参与数值插值） */
function remToPx(rem: number): number {
    return rem * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16)
}

/** @description 读宿主元素上的 CSS 长度变量并折算 px（本项目变量均以 rem 书写） */
function cssVarPx(host: HTMLElement, name: string): number {
    const raw = getComputedStyle(host).getPropertyValue(name).trim()
    const value = parseFloat(raw)
    return raw.endsWith('rem') ? remToPx(value) : value
}

/** 展开态宽（rem）：官方默认 16rem 的原型覆写——侧栏内容 192px + 容器 p-2×2 */
const SIDEBAR_EXPANDED_REM = 12

/** @description 侧栏占位宽：展开 = SIDEBAR_EXPANDED_REM；收起 = --sidebar-width-icon
 *              (3rem=48px) + 容器 p-2×2(16px) = 64px。gap/容器宽度类恒挂 --sidebar-width
 *              （见 ui/sidebar 的 [FORK: motion] 注） */
function sidebarWidthPx(open: boolean, host: HTMLElement): number {
    return open ? remToPx(SIDEBAR_EXPANDED_REM) : cssVarPx(host, '--sidebar-width-icon') + 16
}

/**
 * @description 折叠宽度动画：motion animate() 弹簧驱动 Provider 包裹层上的 --sidebar-width，
 *              gap/容器/主区卡片逐帧随动；侧栏内各元素几何共用 lib/motion 的
 *              spring.sidebar——弹簧收敛时长与幅度无关，全树天然同拍。经 data-slot 查询
 *              宿主而非穿 ref（stock data-slot 即稳定契约）。该变量不进 React style——
 *              重渲会把逐帧值打回静态值。
 */
function animateSidebarWidth(open: boolean) {
    const host = document.querySelector<HTMLElement>('[data-slot="sidebar-wrapper"]')
    if (!host) return
    animate(host, { '--sidebar-width': `${sidebarWidthPx(open, host)}px` }, spring.sidebar)
}

/**
 * @description 折叠主时钟 hook：受控侧栏开合——状态、弹簧动画、初挂宽度还原一次封装；
 *              返回值直接喂 SidebarProvider 的 open/onOpenChange。Ctrl+B / 顶条折叠钮 /
 *              一切 toggleSidebar 都汇到 onOpenChange，宽度在此逐帧驱动；cookie 仍由
 *              Provider 内建写入。
 */
export function useSidebarCollapse() {
    const [sidebarOpen, setSidebarOpen] = useState(readSidebarOpen)
    const handleSidebarOpen = (next: boolean) => {
        setSidebarOpen(next)
        animateSidebarWidth(next)
    }
    // 初挂按 cookie 还原态直设宽度（不动画；Provider 内建默认 16rem 在此覆盖，
    // layout 阶段先于绘制无闪）——此后宽度一律由 animateSidebarWidth 逐帧驱动
    useLayoutEffect(() => {
        const host = document.querySelector<HTMLElement>('[data-slot="sidebar-wrapper"]')
        if (host) host.style.setProperty('--sidebar-width', `${sidebarWidthPx(sidebarOpen, host)}px`)
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [])
    return { open: sidebarOpen, onOpenChange: handleSidebarOpen }
}
