// SecretsService 的 Mock 实现（ADR-0015 凭据槽位）：Set / Delete / Status
// 三件，明文不出——Status 只回掩码线索（末四位 + 设置时间），校验与槽位
// 枚举口径镜像后端 internal/secrets。
import { delay, db } from './fixtures'
import type { SecretStatus } from '../../../bindings/packgradle/internal/service'

// 槽位固定枚举（对齐 internal/secrets.Slot）：未知槽位报 slot_unknown
const KNOWN_SLOTS: readonly string[] = [
    'curseforge/api-key',
    'github/token',
    'github/ssh-passphrase',
]

function assertSlot(kind: string): void {
    if (!KNOWN_SLOTS.includes(kind)) {
        throw new Error(`[mock] err.secrets.slot_unknown: ${kind}`)
    }
}

export async function Set(kind: string, value: string): Promise<void> {
    await delay(200)
    assertSlot(kind)
    const v = value.trim()
    if (!v) throw new Error('[mock] err.secrets.value_empty')
    if (v.length > 1024) throw new Error('[mock] err.secrets.value_too_large')
    db.setSlot(kind, v)
}

export async function Delete(kind: string): Promise<void> {
    await delay(150)
    assertSlot(kind)
    db.clearSlot(kind) // 幂等：未设置视作已删除
}

export async function Status(kind: string): Promise<SecretStatus> {
    await delay(120)
    assertSlot(kind)
    const entry = db.getSlot(kind)
    if (!entry) return { set: false, last4: '', updatedAt: '' }
    return {
        set: true,
        last4: entry.value.slice(-4),
        updatedAt: new Date(entry.updatedAt).toISOString(),
    }
}
