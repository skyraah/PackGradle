// 设置分区外壳（sections 共享件，自 SettingsView 拆出；与分区间共享小件同放
// sections 同级，不提升到全局 src/components）：SET-01 分区骨架的唯一实现——
// <section> + 顶边框分隔（首分区 border-t-0）+ 标题/描述行 + 入场动画；值区与
// 操作区结构由各分区自带（SettingsRow 只占 150px 标签位）。
// 入场：消费 lib/motion 的 fadeUp，由拼装层 SettingsView 持有的 staggerContainer
// 编排（变体标签经上下文传播，跨组件边界不失效）——六分区 delay 递增上浮淡入，
// 0.03s × 5 间隔 + 0.18s 时长 ≈ 0.33s < 400ms 预算；仅动 transform/opacity，
// 不触发 reflow。motion 不自动尊重 prefers-reduced-motion，须 useReducedMotion
// 显式接住（命中时 initial=false 整体跳过入场）。
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { motion, useReducedMotion } from 'motion/react'
import { fadeUp } from '@/lib/motion'

/**
 * @description 设置分区：titleKey/descKey 渲染标题与描述；first=true 用于首个分区
 *              （去掉顶边框、收紧上内边距，对齐 §7.11 全宽分区 + border-t 分隔骨架）。
 *              children 为分区行（SettingsRow 或操作行）。
 */
function SettingsSection({
    titleKey,
    descKey,
    first = false,
    children,
}: {
    titleKey: string
    descKey: string
    first?: boolean
    children: ReactNode
}) {
    const { t } = useTranslation()
    const reduceMotion = useReducedMotion()
    return (
        <motion.section
            variants={fadeUp}
            // initial=false：减动效下跳过入场；正常态留 undefined 以继承拼装层
            // staggerContainer 的 initial/animate 变体标签，六分区才能成编排序列
            initial={reduceMotion ? false : undefined}
            className={first ? 'border-t-0 pb-1.5 pt-1' : 'border-t border-border pb-1.5 pt-4.5'}
        >
            <h2 className="text-[14.5px] font-bold leading-tight">{t(titleKey)}</h2>
            <p className="text-muted-foreground mb-3 mt-0.5 text-xs">{t(descKey)}</p>
            {children}
        </motion.section>
    )
}

/**
 * @description SET-01 标签行：150px 标签 + children（值区/操作区），py-[9px] 行距。
 *              无标签的操作行（保存/回收、诊断按钮组）不套本件，各分区就地排。
 */
function SettingsRow({ labelKey, children }: { labelKey: string; children: ReactNode }) {
    const { t } = useTranslation()
    return (
        <div className="flex flex-wrap items-center gap-3.5 py-2.25">
            <span className="text-muted-foreground w-37.5 flex-none text-[12.5px]">{t(labelKey)}</span>
            {children}
        </div>
    )
}

export {
    SettingsSection,
    SettingsRow
}