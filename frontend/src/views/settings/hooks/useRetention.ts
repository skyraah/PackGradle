// 保留策略分区取数钩子（自 SettingsView 拆出，契约 06 §9 原型 SET-02，票 #65）：
// 五参数表单的读取/编辑/保存与「立即回收空间」，RetentionSection 只管渲染。
// 数据来源：SettingsService.GetRetentionSettings（读回经 lib.retentionFormFromDTO
// 归一化为人类可读）、UpdateRetentionSettings（五键整体替换，成功回执重填表单）、
// RequestGC（建 kind=gc 任务）。
// 行为边界：范围校验唯一口径在后端 err.settings.retention_invalid——前端只经
// lib.buildUpdateInput 拦「无法上 wire 的输入」（非整数/负数/空串/坏单位），
// 越界值照常提交以呈现后端整体拒绝文案；RequestGC 全局单飞（重复点击复用既有
// 活跃任务），排队可见性靠 triggerRequery 补一轮重查（任务中心消费 syncCache 投影）。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SettingsService } from '@/api'
import { triggerRequery } from '@/stores/syncCache'
import { showSnackbar } from '@/stores/ui'
import { errText } from '@/utils/errors'
import { EMPTY_RETENTION_FORM, buildUpdateInput, retentionFormFromDTO, type RetentionForm } from '../lib'

/** @description 保留策略分区：表单状态 + 保存/GC 动作，首装读取一次 */
export function useRetention() {
    const { t } = useTranslation()
    const [loading, setLoading] = useState(true)
    const [saving, setSaving] = useState(false)
    const [gcing, setGcing] = useState(false)
    const [error, setError] = useState('')
    const [form, setForm] = useState<RetentionForm>(EMPTY_RETENTION_FORM)

    const setField = (field: keyof RetentionForm, value: string) => {
        setForm(prev => ({ ...prev, [field]: value }))
    }

    async function save(): Promise<void> {
        if (saving || loading) return
        const input = buildUpdateInput(form)
        if (input === null) {
            showSnackbar(t('settings.retention.invalidInput'), 'error')
            return
        }
        setSaving(true)
        try {
            // 五键整体替换；越界被后端整体拒绝（文案含字段名），既有值不受影响
            setForm(retentionFormFromDTO(await SettingsService.UpdateRetentionSettings(input)))
            showSnackbar(t('settings.retention.savedToast'), 'success')
        } catch (e) {
            showSnackbar(errText(e), 'error')
        } finally {
            setSaving(false)
        }
    }

    async function requestGC(): Promise<void> {
        if (gcing) return
        setGcing(true)
        try {
            // 安全窗口未开时任务停排队态（msg.task.gc.waiting 文案），开窗自动续排
            await SettingsService.RequestGC()
            // 主动补一轮重查让排队任务立刻可见
            triggerRequery()
            showSnackbar(t('settings.retention.gcQueuedToast'), 'success')
        } catch (e) {
            showSnackbar(errText(e), 'error')
        } finally {
            setGcing(false)
        }
    }

    useEffect(() => {
        void (async () => {
            try {
                setForm(retentionFormFromDTO(await SettingsService.GetRetentionSettings()))
            } catch (e) {
                setError(errText(e))
            } finally {
                setLoading(false)
            }
        })()
        // eslint-disable-next-line react-hooks/exhaustive-deps -- 首装取数一次，与 Vue onMounted 同口径
    }, [])

    return { loading, saving, gcing, error, form, setField, save, requestGC }
}
