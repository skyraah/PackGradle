import { ref } from 'vue'

export type ViewKey = 'env' | 'projects' | 'prism'

// 共享的当前视图状态：各页面可通过 navigate() 跳转（如引导用户去配置 API Key）
export const currentView = ref<ViewKey>('env')

export function navigate(key: ViewKey) {
    currentView.value = key
}

// 跨视图数据版本号：数据变更（如 meta 拉取改变项目 mods）后递增，
// 相关视图 watch 后重新加载——避免视图间直接耦合
export const projectsVersion = ref(0)

export function bumpProjectsVersion() {
    projectsVersion.value++
}
