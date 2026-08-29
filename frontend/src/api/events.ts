// 新架构事件桥（契约 04：P1 前端事件协议）。
// 这里是 packgradle://event 的唯一订阅点——页面与 store 不得自行订阅；
// 本模块只做协议判定（事件流序号基线/跳号判漏/未知丢弃），缓存与重查管线在 stores/syncCache。
//
// 事实基线（契约 04 §1）：后端先持久化后 Emit，Emit 失败不回滚不重试 → 漏包是常态可能；
// P1 无事件重放/补拉 API，恢复只能经查询面（受控重查）。
import { Events } from '@wailsio/runtime'
import type { EventEnvelope } from '../../bindings/packgradle/internal/core/model/models'

// 与后端 CoreEvent（internal/transport/events.go）一致
export const CORE_EVENT_TOPIC = 'packgradle://event'

// 已知信封形态（契约 04 §2.2：未知 schema_version / event_type 一律丢弃 + 诊断日志，
// 不触发重查、不崩溃）
const KNOWN_SCHEMA_VERSION = 1
const KNOWN_EVENT_TYPES = new Set(['task_updated', 'relation_invalidated', 'watch_failed'])

export interface CoreEventHandlers {
    /** 任何到达的合法事件（含漏包）触发受控重查 */
    onRequery(reason: 'event' | 'gap'): void
    /** task_updated：该任务标记 dirty，管线内经 GetTask 重读（payload 不进缓存） */
    onTaskUpdated(taskID: string): void
    /** watch_failed：P1 不会发出（保留常量）；预留语义 = 一次性提示「监听不可用」 */
    onWatchFailed(): void
}

// 最后见到的 stream_sequence（事件流序号）。订阅后从收到的第一个事件建立基线，
// 此前无基线、不做任何判定；窗口 reload / 应用重启后 JS 状态重置，重新建基线（§2.1.4/§2.6）。
let lastSeenSeq: number | null = null
let subscribed = false

// subscribeCoreEvents 订阅核心事件并装配处理器。
// 调用时序约束（契约 04 §2.1）：必须在 App mount 之前、任何查询发起之前调用；
// 重复调用合并为单订阅（单订阅点可证：全前端仅此一处 Events.On 核心事件）。
export function subscribeCoreEvents(h: CoreEventHandlers): void {
    if (subscribed) return
    subscribed = true
    Events.On(CORE_EVENT_TOPIC, ev => {
        const env = ev.data as EventEnvelope | null
        if (!env || typeof env.stream_sequence !== 'number' || typeof env.event_type !== 'string') {
            console.warn('[events] 忽略不完整事件信封', ev?.data)
            return
        }
        if (env.schema_version !== KNOWN_SCHEMA_VERSION) {
            // 不认识的 schema：信封整体不可信，不参与事件流序号记账
            console.warn('[events] 丢弃未知 schema 事件', env.schema_version)
            return
        }

        // 严格事件流序号规则（契约 04 §2.2）：seq ≤ last 视为旧包丢弃（后端 MAX+1 持久化
        // 分配，流内无重复）；seq > last+1 漏包先触发重查；两种情况都推进 last（首个事件只建基线）。
        const seq = env.stream_sequence
        const gap = lastSeenSeq !== null && seq > lastSeenSeq + 1
        if (lastSeenSeq !== null && seq <= lastSeenSeq) return
        lastSeenSeq = seq

        if (!KNOWN_EVENT_TYPES.has(env.event_type)) {
            // 未知类型只丢内容、不触发重查（§2.2）；但序号已记账，避免后续合法事件被误判为漏包
            console.warn('[events] 丢弃未知事件类型', env.event_type)
            return
        }

        // payload（Task 快照 JSON）不落地任何前端状态：只提取 task_id 供 GetTask 重读
        if (env.event_type === 'task_updated' && env.task_id) h.onTaskUpdated(env.task_id)
        if (env.event_type === 'watch_failed') h.onWatchFailed()
        h.onRequery(gap ? 'gap' : 'event')
    })
}
