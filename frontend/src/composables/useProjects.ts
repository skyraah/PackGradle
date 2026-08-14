// 项目列表的共享缓存：项目页与 Prism 联动页共用同一份数据与并发请求，
// 避免视图切换/对话框打开时重复解析全部 pack.toml（ListProjects 是最重的后端调用）。
//
// 数据变更方负责使缓存失效：
//   - 服务端返回最新列表时调用 setProjects（如移除项目）
//   - 本地已知数据已过期时调用 invalidateProjects（如 meta 拉取改变 mods）
import { ref } from 'vue'
import { PackwizService } from '../../bindings/packgradle/internal/service'
import type { PackProject } from '../../bindings/packgradle/internal/packwiz'

const projects = ref<PackProject[]>([])
const loaded = ref(false)
let inflight: Promise<PackProject[]> | null = null

// loadProjects 返回项目列表；已加载时直接复用缓存（force 强制重新拉取），
// 并发调用共享同一次请求，避免重复后端往返。
export async function loadProjects(force = false): Promise<PackProject[]> {
    if (!force && loaded.value) return projects.value
    if (!inflight) {
        inflight = PackwizService.ListProjects()
            .then(list => {
                projects.value = list ?? []
                loaded.value = true
                return projects.value
            })
            .finally(() => {
                inflight = null
            })
    }
    return inflight
}

// setProjects 用服务端返回的最新列表更新缓存（如 RemoveProject 的返回值）
export function setProjects(list: PackProject[] | null) {
    projects.value = list ?? []
    loaded.value = true
}

// invalidateProjects 使缓存失效，下次 loadProjects 重新拉取
export function invalidateProjects() {
    loaded.value = false
}

export function useProjects() {
    return { projects, loaded, loadProjects, setProjects, invalidateProjects }
}
