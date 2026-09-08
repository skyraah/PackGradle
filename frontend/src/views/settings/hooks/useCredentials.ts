// 凭据分区取数钩子（自 SettingsView 拆出）：CF API Key 的已配置态读取与更换流程，
// CredentialsSection 只管渲染。
// 数据来源：EnvService.GetApiKey（回执为掩码串，仅 trim 后判「已配置/未配置」，
// 不回显完整 secret——原型 §7.11）、EnvService.SetApiKey（更换后仅置已配置态，
// 不回读）。
// 行为边界：更换走编辑态（password 输入每次进入清空，不回填旧值）；空输入禁存；
// 读取失败置 loadFailed 行内降级并禁用更换入口，不弹全局错误。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { EnvService } from '@/api'
import { showSnackbar } from '@/stores/ui'
import { errText } from '@/utils/errors'

/** @description 凭据分区：读取已配置态、编辑/保存/取消动作，首装读取一次 */
export function useCredentials() {
    const { t } = useTranslation()
    const [loading, setLoading] = useState(true)
    const [loadFailed, setLoadFailed] = useState(false)
    const [configured, setConfigured] = useState(false)
    const [editing, setEditing] = useState(false)
    const [input, setInput] = useState('')
    const [saving, setSaving] = useState(false)

    function startReplace(): void {
        setInput('')
        setEditing(true)
    }

    function cancelEdit(): void {
        setEditing(false)
    }

    async function save(): Promise<void> {
        const key = input.trim()
        if (!key || saving) return
        setSaving(true)
        try {
            await EnvService.SetApiKey(key)
            setConfigured(true)
            setEditing(false)
            showSnackbar(t('settings.creds.savedToast'), 'success')
        } catch (e) {
            showSnackbar(errText(e), 'error')
        } finally {
            setSaving(false)
        }
    }

    useEffect(() => {
        void (async () => {
            try {
                setConfigured((await EnvService.GetApiKey()).trim() !== '')
            } catch {
                setLoadFailed(true)
            } finally {
                setLoading(false)
            }
        })()
        // eslint-disable-next-line react-hooks/exhaustive-deps -- 首装取数一次，与 Vue onMounted 同口径
    }, [])

    return { loading, loadFailed, configured, editing, input, setInput, saving, startReplace, cancelEdit, save }
}
