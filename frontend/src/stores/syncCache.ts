// 受控重查信号（契约 04 §2.3/§2.4 的精简移植，设置页迁移样板阶段）：
// Vue 版（main:frontend/src/stores/syncCache.ts）= 工作区/任务缓存 + 事件驱动重查
// 管线 + 30s 对账；其消费方（任务中心、工作区页、api/events 事件桥）均未迁移，
// 且 bootstrap 接线点在 main.tsx——完整管线随工作区页迁移整体落位，届时把
// queryAndCommit/对账计时器自 main 分支移植并挂回。
//
// 本阶段只落地各页现在就要消费的「重查信号」：triggerRequery() 递增单调序号，
// 已迁移页面（如设置页 RequestGC 建任务后）调它宣告「查询面有新事实」；未来的
// 缓存管线/任务中心订阅 useRequerySignal() 感知变化即可，接口形状与完整版对齐，
// 落位时无需改动调用方。
import { create } from 'zustand'

interface SyncCacheState {
    /** 受控重查信号：单调递增，消费方对其做 diff 触发重查 */
    requerySeq: number
}

export const useSyncCacheStore = create<SyncCacheState>(() => ({
    requerySeq: 0,
}))

// triggerRequery 发起一轮受控重查（事件、页面动作后的刷新共用同一信号）
export function triggerRequery(): void {
    useSyncCacheStore.setState(s => ({ requerySeq: s.requerySeq + 1 }))
}

// useRequerySignal 订阅重查信号（组件内 diff 前值即可判断「来了新一轮」）
export function useRequerySignal(): number {
    return useSyncCacheStore(s => s.requerySeq)
}
