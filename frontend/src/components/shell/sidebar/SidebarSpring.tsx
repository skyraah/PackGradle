// 折叠几何弹簧行族（壳层共用件，自 AppSidebar/WorkspaceSwitcher 的多份手搓 animate 收敛）：
// 侧栏折叠/展开的行级几何（行高、水平/垂直内边距）以两态数值对声明一次，弹簧调度
// （lib/motion 的 spring.sidebar，与 App 折叠主时钟同拍）与 initial={false} 只写一份。
// 四种元素壳：div（头部/分组/footer 容器）、button、a、Link（react-router）。模块级
// motion.create 仅涉外部依赖（react-router），不在导入环上——navItems 那条环的 TDZ
// 讨论见 SidebarNav。几何对方向一律 [展开态, 收起态]；只插值声明的字段，未声明字段
// 保持静态类不动。数值联动契约（勿动）：icon 收起态按钮盒 32px，图标档与收起态水平
// 内边距须满足 (32 − 图标宽) / 2 = 收起态水平内边距——改图标尺寸须连带改各调用处的几何对。
import { Link } from 'react-router'
import { motion } from 'motion/react'
import type { ComponentProps, ElementType } from 'react'
import { spring } from '@/lib/motion'

/** @description 两态几何对：[展开态, 收起态]——只声明需要随折叠插值的字段 */
export interface SpringGeometry {
    /** 行高 */
    height?: [number, number]
    /** 水平内边距（左右同值） */
    paddingX?: [number, number]
    /** 垂直内边距（上下同值） */
    paddingY?: [number, number]
}

/** @description 侧栏分区内边距几何：头部/分组/footer 容器共用（原型分区 12px，折叠 8px） */
export const SIDEBAR_SECTION_PAD: SpringGeometry = { paddingX: [12, 8] }

const MotionLink = motion.create(Link)

/** @description 几何对 → motion animate 目标值：按折叠布尔取对应侧数值，未声明字段省略 */
function springTarget(collapsed: boolean, geometry: SpringGeometry) {
    const target: Record<string, number> = {}
    if (geometry.height !== undefined) {
        target.height = collapsed ? geometry.height[1] : geometry.height[0]
    }
    if (geometry.paddingX !== undefined) {
        target.paddingLeft = collapsed ? geometry.paddingX[1] : geometry.paddingX[0]
        target.paddingRight = target.paddingLeft
    }
    if (geometry.paddingY !== undefined) {
        target.paddingTop = collapsed ? geometry.paddingY[1] : geometry.paddingY[0]
        target.paddingBottom = target.paddingTop
    }
    return target
}

type SpringProps<E extends ElementType> = { collapsed: boolean; geometry: SpringGeometry } & ComponentProps<E>

export function SpringDiv({ collapsed, geometry, ...props }: SpringProps<typeof motion.div>) {
    return (
        <motion.div
            initial={false}
            transition={spring.sidebar}
            animate={springTarget(collapsed, geometry)}
            {...props}
        />
    )
}

export function SpringButton({ collapsed, geometry, ...props }: SpringProps<typeof motion.button>) {
    return (
        <motion.button
            initial={false}
            transition={spring.sidebar}
            animate={springTarget(collapsed, geometry)}
            {...props}
        />
    )
}

export function SpringAnchor({ collapsed, geometry, ...props }: SpringProps<typeof motion.a>) {
    return (
        <motion.a
            initial={false}
            transition={spring.sidebar}
            animate={springTarget(collapsed, geometry)}
            {...props}
        />
    )
}

export function SpringLink({ collapsed, geometry, ...props }: SpringProps<typeof MotionLink>) {
    return (
        <MotionLink
            initial={false}
            transition={spring.sidebar}
            animate={springTarget(collapsed, geometry)}
            {...props}
        />
    )
}
