// 占位页（ADR-0014 脚手架）：React 重写期间每页由维护者本人执笔（ADR-0014 §2 分工），
// 本组件只提供「此页待移植」的统一空态。页面实现后删除对该组件的引用即可，
// 每页的移植要点写在各自 view 文件的头部注释里。
import { useTranslation } from 'react-i18next'

export default function PlaceholderPage({ titleKey }: { titleKey: string }) {
    const { t } = useTranslation()
    return (
        <div className="flex h-full flex-col items-center justify-center gap-2 p-8">
            <h1 className="page-title">{t(titleKey)}</h1>
            <p className="text-muted-foreground text-sm">此页待移植——要点见本文件所在 view 的头部注释</p>
        </div>
    )
}
