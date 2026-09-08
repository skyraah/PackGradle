// 全局通知覆盖层（App.vue 内嵌 snackbar 标记的组件化移植，挂 App 根部）：
// ui store 排队驱动，右下角 toast——中性面板 + 3px 语义色左边框（原型 .toast），
// 约 3200ms 自动消失（计时收口在 stores/ui）；长任务结果以任务中心为权威。
// 语义色用 main.css 既有 --success/--warning/--error/--info token（浅深主题各自
// 定义），info 顺延主色；出入场走 motion（lib/motion 档位）。
import { AnimatePresence, motion } from 'motion/react'
import { CircleAlert, CircleCheck, CircleX, Info, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { dismissSnackbar, useUiStore, type NoticeTone } from '@/stores/ui'
import { duration, ease } from '@/lib/motion'
import { cn } from 'cn'

// 原型 toast（.toast.ok/.err）：3px 语义色左边框（error 与 destructive 同值）
const TONE_ACCENT: Record<NoticeTone, string> = {
    info: 'border-l-primary',
    success: 'border-l-success',
    warning: 'border-l-warning',
    error: 'border-l-error',
}

const TONE_ICON: Record<NoticeTone, typeof Info> = {
    info: Info,
    success: CircleCheck,
    warning: CircleAlert,
    error: CircleX,
}

const TONE_ICON_CLASS: Record<NoticeTone, string> = {
    info: 'text-primary',
    success: 'text-success',
    warning: 'text-warning',
    error: 'text-error',
}

/** @description 全局通知 toast：由 stores/ui 的队列驱动，一次一条顺序展示 */
export function Snackbar() {
    const { t } = useTranslation()
    const open = useUiStore(s => s.snackbarOpen)
    const message = useUiStore(s => s.snackbarMessage)
    const tone = useUiStore(s => s.snackbarTone)
    const ToneIcon = TONE_ICON[tone] ?? Info

    return (
        <AnimatePresence>
            {open && (
                <motion.div
                    initial={{ opacity: 0, y: 12, scale: 0.98 }}
                    animate={{ opacity: 1, y: 0, scale: 1 }}
                    exit={{ opacity: 0, y: 8, scale: 0.98 }}
                    transition={{ duration: duration.normal, ease: ease.enter }}
                    className={cn(
                        'app-no-drag bg-surface-3 text-foreground fixed right-[18px] bottom-[18px] z-[2400]',
                        'flex max-w-[380px] items-start gap-2.5 rounded-lg border border-border border-l-[3px]',
                        'px-3.5 py-[9px] text-[12.5px] leading-relaxed shadow-lg',
                        TONE_ACCENT[tone] ?? TONE_ACCENT.info,
                    )}
                    role="status"
                >
                    <ToneIcon className={cn('mt-0.5 size-4 flex-none', TONE_ICON_CLASS[tone] ?? TONE_ICON_CLASS.info)} />
                    <span className="min-w-0 break-all">{message}</span>
                    <button
                        type="button"
                        className="text-muted-foreground hover:text-foreground ml-1 flex-none"
                        title={t('common.dismiss')}
                        onClick={dismissSnackbar}
                    >
                        <X className="size-4" />
                    </button>
                </motion.div>
            )}
        </AnimatePresence>
    )
}

export default Snackbar
