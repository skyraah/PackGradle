// 设置页纯函数库（自 SettingsView 拆出）：SET-01 行结构相关的字节/表单换算——不持
// 状态、不发请求，取数与保存编排在 hooks/*。放 lib.ts 而非 hooks 同级 utils 的理由：
// 与 src/lib/ 的「共享基建」命名口径一致，页内 lib.ts 即「本页共享纯函数」，且只被
// 设置页消费、不提升到全局 src/lib。
import type { RetentionSettingsDTO, UpdateRetentionSettingsDTO } from '@/api'

// formatBytes 字节 → 人类可读（整值按 GiB/MiB/KiB 归一，否则原字节数）；0 显示
// 「0」（= 不限，preserve_max_bytes 专属语义，见键 hint）。
export function formatBytes(bytes: number): string {
    if (bytes === 0) return '0'
    if (bytes % 1024 ** 3 === 0) return `${bytes / 1024 ** 3} GiB`
    if (bytes % 1024 ** 2 === 0) return `${bytes / 1024 ** 2} MiB`
    if (bytes % 1024 === 0) return `${bytes / 1024} KiB`
    return `${bytes} B`
}

// formatBytesHuman 字节 → 只读展示（本地存储三行等）：取能装下的最大单位
//（KiB/MiB/GiB/TiB），1 位小数、整值省略小数；不足 1 KiB 裸显字节数。与
// formatBytes 的分工：本函数四舍五入有损，只作展示，不得回填 retention 表单
//（表单回填须走 formatBytes 保 format→parse 无损往返，见 buildUpdateInput）。
export function formatBytesHuman(bytes: number): string {
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'] as const
    let v = bytes
    let u = 0
    while (v >= 1024 && u < units.length - 1) {
        v /= 1024
        u++
    }
    let disp = u === 0 ? v : Math.round(v * 10) / 10
    // 进位后小数可能顶到 1024（如 1048575 B → 1024.0 KiB），再进一档免出「1024 KiB」
    if (disp >= 1024 && u < units.length - 1) {
        disp /= 1024
        u++
    }
    return `${disp} ${units[u]}`
}

// parseBytes 解析「数值 + 可选单位」为字节数；无法解析返回 null。
export function parseBytes(text: string): number | null {
    const m = /^\s*(\d+(?:\.\d+)?)\s*(GiB|MiB|KiB|B)?\s*$/i.exec(text)
    if (!m) return null
    const n = Number(m[1])
    if (!Number.isFinite(n) || n < 0) return null
    const unit = (m[2] ?? 'B').toUpperCase()
    const mult = unit === 'GIB' ? 1024 ** 3 : unit === 'MIB' ? 1024 ** 2 : unit === 'KIB' ? 1024 : 1
    return Math.round(n * mult)
}

// 表单以文本承载：整数项提交时转整数；字节项支持「数字 + 单位」（GiB/MiB/KiB/B，
// 无单位=字节），与后端 int64 字节数无损互转（读回时归一化为人类可读显示）。
export interface RetentionForm {
    keepCommits: string
    keepDays: string
    relationCapacity: string
    preserveMax: string
    trashDays: string
}

export const EMPTY_RETENTION_FORM: RetentionForm = {
    keepCommits: '',
    keepDays: '',
    relationCapacity: '',
    preserveMax: '',
    trashDays: '',
}

// retentionFormFromDTO 用读投影生成表单（读默认值回填 / 保存成功后归一化重填共用）。
export function retentionFormFromDTO(s: RetentionSettingsDTO): RetentionForm {
    return {
        keepCommits: String(s.keep_commits),
        keepDays: String(s.keep_days),
        relationCapacity: formatBytes(s.relation_capacity_bytes),
        preserveMax: formatBytes(s.preserve_max_bytes),
        trashDays: String(s.trash_days),
    }
}

// buildUpdateInput 汇总表单为写输入；无法上 wire 的输入返回 null（本地拦下不发
// 请求）。范围越界不在此拦——照常提交，由后端 err.settings.retention_invalid
// 整体拒绝并回显含字段名的文案（契约 06 §3.6 唯一校验口径）。
export function buildUpdateInput(form: RetentionForm): UpdateRetentionSettingsDTO | null {
    // 空串视为未填写（Number('') 恒 0，会伪装成合法 0 越界提交），转 NaN 走本地拦下
    const ints = [form.keepCommits, form.keepDays, form.trashDays].map(v =>
        v.trim() === '' ? Number.NaN : Number(v),
    )
    const capacity = parseBytes(form.relationCapacity)
    const preserve = parseBytes(form.preserveMax)
    if (ints.some(n => !Number.isInteger(n) || n < 0) || capacity === null || preserve === null) {
        return null
    }
    return {
        keep_commits: ints[0],
        keep_days: ints[1],
        relation_capacity_bytes: capacity,
        preserve_max_bytes: preserve,
        trash_days: ints[2],
    }
}
