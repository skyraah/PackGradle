// 设置页 · Mock 数据层分区（自 main:frontend/src/views/settings/DevMockCard.vue 移植）：
// 仅开发构建由 SettingsView 以 import.meta.env.DEV 门控 + React.lazy 动态导入装载
// （生产构建该组件连同 mock.* 文案引用整体裁剪，vite 插件同时从 zh-CN.json 剔除
// mock.* 键）。切换需确认——确认后整页刷新重载缓存（api 门面与各 store 持有已
// 加载数据，reload 是唯一可靠重置方式）。
// 票 #107：全宽分区行语言（150px 标签），与画板 SET-01 同构，不使用卡片嵌套。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { isMockEnabled, setMockEnabled } from '@/api'
import { Switch } from '@/components/ui/switch'
import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
} from '@/components/ui/alert-dialog'

/** @description Mock 数据层开关：Switch 触发 AlertDialog 确认，确认后 300ms 延时整页刷新 */
export default function DevMockCard() {
    const { t } = useTranslation()
    const [mockOn, setMockOn] = useState(isMockEnabled)
    const [dialogOpen, setDialogOpen] = useState(false)
    const [mockPending, setMockPending] = useState(false)

    const askMock = (v: boolean) => {
        setMockPending(v)
        setDialogOpen(true)
    }

    const confirmMock = () => {
        setDialogOpen(false)
        setMockOn(mockPending)
        setMockEnabled(mockPending)
        setTimeout(() => window.location.reload(), 300)
    }

    return (
        <>
            <section className="border-t border-border pb-1.5 pt-[18px]">
                <h2 className="text-[14.5px] font-bold leading-tight">{t('settings.devTitle')}</h2>
                <p className="text-muted-foreground mb-3 mt-0.5 text-xs">{t('mock.hint')}</p>
                <div className="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span className="text-muted-foreground w-[150px] flex-none text-[12.5px]">
                        {t('mock.switchLabel')}
                    </span>
                    <div className="flex flex-none items-center gap-2">
                        <Switch checked={mockOn} onCheckedChange={askMock} />
                    </div>
                </div>
            </section>

            {/* Mock 切换确认（切换后整页刷新） */}
            <AlertDialog open={dialogOpen} onOpenChange={setDialogOpen}>
                <AlertDialogContent>
                    <AlertDialogHeader>
                        <AlertDialogTitle>{t('mock.confirmTitle')}</AlertDialogTitle>
                        <AlertDialogDescription>{t('mock.confirmText')}</AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                        <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
                        <AlertDialogAction onClick={confirmMock}>
                            {mockPending ? t('mock.enable') : t('mock.disable')}
                        </AlertDialogAction>
                    </AlertDialogFooter>
                </AlertDialogContent>
            </AlertDialog>
        </>
    )
}
