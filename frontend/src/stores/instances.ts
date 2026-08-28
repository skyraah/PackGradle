// Prism 联动总览缓存：工作台与联动页共用 Overview（实例目录 + 实例列表 + 关联视图）。
import { ref } from 'vue'
import { PrismService } from '../api'
import type { PrismOverview } from '../../bindings/packgradle/internal/service'

const overview = ref<PrismOverview | null>(null)
const loaded = ref(false)
let inflight: Promise<PrismOverview> | null = null
let queuedRefresh: Promise<PrismOverview> | null = null

export async function loadOverview(force = false): Promise<PrismOverview> {
    if (!force && loaded.value && overview.value) return overview.value
    if (inflight) {
        if (force) {
            if (!queuedRefresh) {
                const current = inflight
                queuedRefresh = current
                    .catch(() => undefined)
                    .then(() => {
                        if (inflight === current) inflight = null
                        return loadOverview(true)
                    })
                    .finally(() => {
                        queuedRefresh = null
                    })
            }
            return queuedRefresh
        }
        return inflight
    }
    const task = PrismService.Overview().then(ov => {
        overview.value = ov
        loaded.value = true
        return ov
    })
    inflight = task
    try {
        return await task
    } finally {
        if (inflight === task) {
            inflight = null
        }
    }
}

export function invalidateOverview() {
    loaded.value = false
}

// overview 直接导出（顶栏状态指示等只读场景用）
export { overview }

export function useInstances() {
    return { overview, loadOverview, invalidateOverview }
}
