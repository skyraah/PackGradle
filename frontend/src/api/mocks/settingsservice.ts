// SettingsService 的 Mock 实现（main 分支无此模块——Vue 版门面给 SettingsService
// 挂的是空 mock 对象，mock 模式下访问即抛未实现；本阶段设置页迁 React 后补齐其
// 所需四方法，方法名与参数严格对齐 bindings/packgradle/internal/transport/settingsservice.ts）。
// SetWorkspaceAuthorized 暂不实现：消费方是工作区页（未迁移），mock 模式下经门面
// 代理抛「[mock] 未实现」错误，待该页迁移时在此补充。
import type {
    RetentionSettingsDTO,
    StorageStatsDTO,
    TaskDTO,
    UpdateRetentionSettingsDTO,
} from '../../../bindings/packgradle/internal/transport/models'
import { clone, delay, db } from './fixtures'

// 范围口径对齐 internal/core/model/retention.go（ValidateRetention）——越界抛与
// 后端同形的错误码 JSON（errors.ts 的 displayText 会解析并翻译，{0}=字段名），
// 保证「范围越界照常提交由 err.settings.retention_invalid 拒绝」的链路在 mock
// 模式下同样可验证。
const RANGES = {
    keep_commits: [5, 200],
    keep_days: [7, 365],
    relation_capacity_bytes: [128 * 1024 ** 2, 20 * 1024 ** 3],
    trash_days: [1, 90],
} as const

function invalid(field: string): never {
    throw new Error(JSON.stringify({ code: 'err.settings.retention_invalid', args: [field] }))
}

export async function GetRetentionSettings(): Promise<RetentionSettingsDTO> {
    await delay(150)
    return clone(db.retention)
}

export async function GetStorageStats(): Promise<StorageStatsDTO> {
    await delay(200)
    return clone(db.storage)
}

// RequestGC 建「立即回收空间」的 GC 任务：全局单飞，既有活跃任务幂等返回
// （任务中心未迁移，mock 任务只做排队态投影，不推进阶段）。
let activeGc: TaskDTO | null = null

export async function RequestGC(): Promise<TaskDTO> {
    await delay(300)
    if (!activeGc) {
        const now = new Date().toISOString()
        activeGc = {
            schema_version: 1,
            task_id: db.nextGcTaskId(),
            relation_id: undefined,
            task_sequence: 0,
            kind: 'gc',
            status: 'queued',
            outcome: undefined,
            phase: '',
            completed: 0,
            total: 0,
            message_key: 'msg.task.gc.queued',
            message_args: null,
            can_cancel: true,
            created_at: now,
            updated_at: now,
            problem: null,
        }
    }
    return clone(activeGc)
}

export async function UpdateRetentionSettings(input: UpdateRetentionSettingsDTO): Promise<RetentionSettingsDTO> {
    await delay(300)
    const c = input.keep_commits
    const d = input.keep_days
    const cap = input.relation_capacity_bytes
    const p = input.preserve_max_bytes
    const t = input.trash_days
    if (!Number.isInteger(c) || c < RANGES.keep_commits[0] || c > RANGES.keep_commits[1]) invalid('keep_commits')
    if (!Number.isInteger(d) || d < RANGES.keep_days[0] || d > RANGES.keep_days[1]) invalid('keep_days')
    if (!Number.isInteger(cap) || cap < RANGES.relation_capacity_bytes[0] || cap > RANGES.relation_capacity_bytes[1]) {
        invalid('relation_capacity_bytes')
    }
    // preserve_max_bytes：显式 0＝不限；否则 1 MiB–512 MiB
    if (
        !Number.isInteger(p) ||
        (p !== 0 && (p < 1024 ** 2 || p > 512 * 1024 ** 2))
    ) {
        invalid('preserve_max_bytes')
    }
    if (!Number.isInteger(t) || t < RANGES.trash_days[0] || t > RANGES.trash_days[1]) invalid('trash_days')
    Object.assign(db.retention, input)
    return clone(db.retention)
}
