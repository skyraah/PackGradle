/**
 * @description 根级路由错误页（router.tsx 根路由的 errorElement）：任一路由渲染期抛错即接管
 *              整壳——rail/顶栏一并让位，避免「半壳 + react-router 默认英文错误屏」的中间态。
 * @description 错误对象经 useRouteError() 取回，展示 message 供排查；重载按钮整页恢复
 *              （hash 路由不丢路径，reload 后回到原地）。
 */
import { useTranslation } from 'react-i18next'
import { useRouteError } from 'react-router'
import { Button } from '@/components/ui/button'

export default function RouteErrorView() {
    const { t } = useTranslation()
    const error = useRouteError()
    const detail = error instanceof Error ? error.message : typeof error === 'string' ? error : undefined

    return (
        <div className="bg-background text-foreground flex h-full w-full flex-col items-center justify-center gap-3 p-8">
            <h1 className="text-lg font-semibold">{t('app.routeError.title')}</h1>
            <p className="text-muted-foreground text-sm">{t('app.routeError.message')}</p>
            {detail !== undefined && (
                <pre className="bg-card border-border text-muted-foreground max-w-xl overflow-x-auto rounded-md border p-3 text-xs whitespace-pre-wrap">
                    {detail}
                </pre>
            )}
            <Button onClick={() => window.location.reload()}>{t('app.routeError.reload')}</Button>
        </div>
    )
}
