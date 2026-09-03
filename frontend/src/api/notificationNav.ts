// 系统通知点击直达（契约 07 §3.5/§6，票 #97）：后端 toast 正文点击（进程内
// OnNotificationResponse 回调 → 窗口前置）发出 packgradle://notify 导航事件，
// 前端在此订阅并直达计划页——与待确认角标同落点（WorkspacesView 角标点击的
// router.push 同形）。
//
// 该 topic 独立于核心事件流 packgradle://event（契约 04「零新事件类型」红线
// 不受影响）：纯 UI 导航信号、零状态语义——待确认事实仍以 pending_plan_id
// 查询投影为准，导航只是代用户点了一下角标。
import { Events } from '@wailsio/runtime'
import router from '../router'

// 与后端 internal/notify/wails_windows.go NavTopic 一致
export const NOTIFY_NAV_TOPIC = 'packgradle://notify'

interface NotifyNavPayload {
    relation_id?: string
    plan_id?: string
}

// subscribeNotificationNav 订阅通知点击导航。时序约束与 subscribeCoreEvents
// 相同：mount 前调用一次（main.ts）；重复调用合并为单订阅。
let subscribed = false

export function subscribeNotificationNav(): void {
    if (subscribed) return
    subscribed = true
    Events.On(NOTIFY_NAV_TOPIC, ev => {
        const d = ev.data as NotifyNavPayload | null
        if (!d || typeof d.relation_id !== 'string' || typeof d.plan_id !== 'string' || !d.relation_id || !d.plan_id) {
            console.warn('[notifyNav] 忽略不完整导航载荷', ev?.data)
            return
        }
        void router.push(`/workspaces/${d.relation_id}/plans/${d.plan_id}`)
    })
}
