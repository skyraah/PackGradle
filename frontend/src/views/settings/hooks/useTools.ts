// 适配器与工具分区取数钩子(自 SettingsView 拆出):工具检测投影与双工具
// (packwiz / prism-launcher)路径保存的状态与动作封装,ToolsSection 只管渲染。
// 数据来源:EnvService.Detect(健康投影)、EnvService.SetToolPath(name, …)
// (保存回执即最新检测投影)。行为边界:前端不判路径对错——Detect 返回的 found
// 是唯一健康口径;检测失败保持未找到态,可手动填路径后保存;applyTools 收敛
// 检测/保存两个入口的投影回填(带回 path 的工具各自覆写输入框)。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { EnvService } from '@/api'
import type { ToolInfo } from '../../../../bindings/packgradle/internal/service'
import { showSnackbar } from '@/stores/ui'
import { errText } from '@/utils/errors'

// 保存成功 toast 按工具名分发(文案各自指名)
const SAVE_OK_TOAST: Record<string, string> = {
    packwiz: 'settings.tools.saveOkToast',
    'prism-launcher': 'settings.tools.prismSaveOkToast',
}

/** @description 适配器与工具分区:双工具对称取数,检测/保存互斥防抖、首装检测一次、失败静默降级 */
export function useTools() {
    const { t } = useTranslation()
    const [loading, setLoading] = useState(true)
    const [detecting, setDetecting] = useState(false)
    const [tools, setTools] = useState<ToolInfo[]>([])
    // 各工具输入框值,键 = 工具名(packwiz / prism-launcher)
    const [paths, setPaths] = useState<Record<string, string>>({})
    const [savingName, setSavingName] = useState<string | null>(null)

    const packwizTool = tools.find(x => x.name === 'packwiz')
    const prismTool = tools.find(x => x.name === 'prism-launcher')

    function applyTools(list: ToolInfo[]): void {
        setTools(list)
        setPaths(prev => {
            const next = { ...prev }
            for (const x of list) if (x.path) next[x.name] = x.path
            return next
        })
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

    /** 保存成功回 true(调用方据此退出编辑态),失败/空路径/保存中回 false */
    async function saveToolPath(name: string): Promise<boolean> {
        const v = (paths[name] ?? '').trim()
        if (savingName !== null || !v) return false
        setSavingName(name)
        try {
            applyTools((await EnvService.SetToolPath(name, v)) ?? tools)
            showSnackbar(t(SAVE_OK_TOAST[name] ?? 'settings.tools.saveOkToast'), 'success')
            return true
        } catch (e) {
            showSnackbar(errText(e), 'error')
            return false
        } finally {
            setSavingName(null)
        }
    }

    useEffect(() => {
        void (async () => {
            try {
                applyTools((await EnvService.Detect()) ?? [])
            } catch {
                // 检测失败保持未找到态,可手动填路径后保存
            } finally {
                setLoading(false)
            }
        })()
        // eslint-disable-next-line react-hooks/exhaustive-deps -- 首装取数一次,与 Vue onMounted 同口径
    }, [])

    return {
        loading,
        detecting,
        packwizTool,
        prismTool,
        detectTools,
        /** name 工具的输入框值(检测/保存回执回填) */
        path: (name: string) => paths[name] ?? '',
        setPath: (name: string, v: string) => setPaths(p => ({ ...p, [name]: v })),
        /** 保存互斥按工具名(一行保存不禁用另一行) */
        saving: (name: string) => savingName === name,
        saveToolPath,
    }
}
