// 项目列表共享缓存：工作台 / 项目列表 / 项目详情 / Prism 联动共用同一份数据与并发请求，
// 避免页面切换时重复解析全部 pack.toml（ListProjects 是最重的后端调用）。
//
// 数据变更方负责使缓存失效：
//   - 服务端返回最新列表时调用 setProjects（如导入/移除项目）
//   - 本地已知数据已过期时调用 invalidateProjects（如 meta 拉取改变 mods）
import { ref } from 'vue'
import { PackwizService } from '../../bindings/packgradle/internal/service'
import type { PackProject } from '../../bindings/packgradle/internal/packwiz'

export const projects = ref<PackProject[]>([])
export const loaded = ref(false)
let inflight: Promise<PackProject[]> | null = null

// 跨视图数据版本号：数据变更（如 meta 拉取改变项目 mods）后递增，
// 相关视图 watch 后重新加载——避免视图间直接耦合
export const projectsVersion = ref(0)

export function bumpProjectsVersion() {
    projectsVersion.value++
}

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

// setProjects 用服务端返回的最新列表更新缓存（如 ImportProject/RemoveProject 的返回值）
export function setProjects(list: PackProject[] | null) {
    projects.value = list ?? []
    loaded.value = true
}

// invalidateProjects 使缓存失效，下次 loadProjects 重新拉取
export function invalidateProjects() {
    loaded.value = false
}

// findProject 按名称在缓存中查找项目（供详情页 / 联动页使用）
export function findProject(name: string): PackProject | undefined {
    return projects.value.find(p => p.name === name)
}

export function useProjects() {
    return { projects, loaded, loadProjects, setProjects, invalidateProjects, findProject }
}
