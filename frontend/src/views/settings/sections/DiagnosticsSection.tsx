// 设置 · 诊断分区（自 SettingsView 拆出，SET-01 分区五）：复制版本信息即时可用
//（前端剪贴板，四行：产品/语言/平台/UA）；导出脱敏诊断包与查看日志目录待后端
// 服务面就绪（绑定未生成，票 #113），点击提示占位。数据来源：useDiagnostics
//（纯前端动作，无服务调用）。
import { useTranslation } from 'react-i18next'
import { Copy, ExternalLink, Folder } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { SettingsSection } from '../SettingsSection'
import { useDiagnostics } from '../hooks/useDiagnostics'

/** @description 诊断：导出诊断包（占位）/ 查看日志目录（占位）/ 复制版本信息 */
export default function DiagnosticsSection() {
    const { t } = useTranslation()
    const diag = useDiagnostics()

    return (
        <SettingsSection titleKey="settings.diag.title" descKey="settings.diag.desc">
            <div className="flex flex-wrap items-center gap-2 py-[9px]">
                <Button variant="outline" size="sm" onClick={diag.showPendingToast}>
                    <ExternalLink className="size-4" />
                    {t('settings.diag.exportBtn')}
                </Button>
                <Button variant="outline" size="sm" onClick={diag.showPendingToast}>
                    <Folder className="size-4" />
                    {t('settings.diag.logsBtn')}
                </Button>
                <Button variant="ghost" size="sm" onClick={() => void diag.copyVersionInfo()}>
                    <Copy className="size-4" />
                    {t('settings.diag.copyVersionBtn')}
                </Button>
            </div>
        </SettingsSection>
    )
}
