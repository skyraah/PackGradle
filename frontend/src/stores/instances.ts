// Prism 联动总览缓存：工作台与联动页共用 Overview（实例目录 + 实例列表 + 关联视图），
// 后端只扫描一次实例目录，页面切换零开销。
import { ref } from 'vue'
import { PrismService } from '../../bindings/packgradle/internal/service'
import type { PrismOverview } from '../../bindings/packgradle/internal/service'

const overview = ref<PrismOverview | null>(null)
const loaded = ref(false)
let inflight: Promise<PrismOverview> | null = null

export async function loadOverview(force = false): Promise<PrismOverview> {
    if (!force && loaded.value && overview.value) return overview.value
    if (!inflight) {
        inflight = PrismService.Overview()
            .then(ov => {
                overview.value = ov
                loaded.value = true
                return ov
            })
            .finally(() => {
                inflight = null
            })
    }
    return inflight
}

export function invalidateOverview() {
    loaded.value = false
}

export function useInstances() {
    return { overview, loadOverview, invalidateOverview }
}
