// 诊断分区动作钩子（自 SettingsView 拆出）：无服务调用的纯前端动作封装，
// DiagnosticsSection 只管渲染。
// 数据来源：无——复制版本信息读 i18n.language 与 navigator（平台/UA），写前端剪贴板
//（原型 copyDiag 同语义）；导出脱敏诊断包与查看日志目录待后端服务面就绪（绑定未
// 生成，票 #113），点击仅提示占位。
// 行为边界：剪贴板失败呈 error toast（不抛出）；复制载荷固定四行
//（产品/语言/平台/UA），与 Vue 版逐字节一致。
import { useTranslation } from 'react-i18next'
import { showSnackbar } from '@/stores/ui'

/** @description 诊断分区：复制版本信息（四行）与待就绪动作的占位提示 */
export function useDiagnostics() {
    const { t, i18n } = useTranslation()

    async function copyVersionInfo(): Promise<void> {
        const info = [
            'PackGradle P1',
            `locale: ${i18n.language}`,
            `platform: ${navigator.platform}`,
            `userAgent: ${navigator.userAgent}`,
        ].join('\n')
        try {
            await navigator.clipboard.writeText(info)
            showSnackbar(t('settings.diag.copiedToast'), 'success')
        } catch {
            showSnackbar(t('settings.diag.copyFailedToast'), 'error')
        }
    }

    function showPendingToast(): void {
        showSnackbar(t('settings.diag.pendingToast'), 'info')
    }

    return { copyVersionInfo, showPendingToast }
}
