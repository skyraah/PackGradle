// 适配器与工具分区取数钩子（自 SettingsView 拆出）：工具检测投影与 Packwiz 路径
// 保存的状态与动作封装，ToolsSection 只管渲染。
// 数据来源：EnvService.Detect（name = packwiz / prism-launcher 的健康投影）、
// EnvService.SetToolPath('packwiz', …)（保存回执即最新检测投影，直接回填）。
// 行为边界：前端不判路径对错——Detect 返回的 found 是唯一健康口径；检测失败保持
// 未找到态，可手动填路径后保存；applyTools 收敛检测/保存两个入口的投影回填
//（仅 packwiz 且带 path 时覆写输入框，prism 只读不回填）。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { EnvService } from '@/api'
import type { ToolInfo } from '../../../../bindings/packgradle/internal/service'
import { showSnackbar } from '@/stores/ui'
import { errText } from '@/utils/errors'

/** @description 适配器与工具分区：检测/保存互斥防抖、首装检测一次、失败静默降级 */
export function useTools() {
    const { t } = useTranslation()
    const [loading, setLoading] = useState(true)
    const [detecting, setDetecting] = useState(false)
    const [tools, setTools] = useState<ToolInfo[]>([])
    const [packwizPath, setPackwizPath] = useState('')
    const [saving, setSaving] = useState(false)

    const packwizTool = tools.find(x => x.name === 'packwiz')
    const prismTool = tools.find(x => x.name === 'prism-launcher')

    function applyTools(list: ToolInfo[]): void {
        setTools(list)
        const pw = list.find(x => x.name === 'packwiz')
        if (pw && pw.path) setPackwizPath(pw.path)
    }

    async function detectTools(): Promise<void> {
        if (detecting) return
        setDetecting(true)
        try {
            applyTools((await EnvService.Detect()) ?? [])
            showSnackbar(t('settings.tools.detectOkToast'), 'success')
        } catch (e) {
            showSnackbar(errText(e), 'error')
        } finally {
            setDetecting(false)
        }
    }

    async function savePackwizPath(): Promise<void> {
        if (saving || !packwizPath.trim()) return
        setSaving(true)
        try {
            applyTools((await EnvService.SetToolPath('packwiz', packwizPath.trim())) ?? tools)
            showSnackbar(t('settings.tools.saveOkToast'), 'success')
        } catch (e) {
            showSnackbar(errText(e), 'error')
        } finally {
            setSaving(false)
        }
    }

    useEffect(() => {
        void (async () => {
            try {
                applyTools((await EnvService.Detect()) ?? [])
            } catch {
                // 检测失败保持未找到态，可手动填路径后保存
            } finally {
                setLoading(false)
            }
        })()
        // eslint-disable-next-line react-hooks/exhaustive-deps -- 首装取数一次，与 Vue onMounted 同口径
    }, [])

    return {
        loading,
        detecting,
        saving,
        packwizTool,
        prismTool,
        packwizPath,
        setPackwizPath,
        detectTools,
        savePackwizPath,
    }
}
