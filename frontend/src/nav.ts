import { ref } from 'vue'

export type ViewKey = 'env' | 'projects' | 'prism'

// 共享的当前视图状态：各页面可通过 navigate() 跳转（如引导用户去配置 API Key）
export const currentView = ref<ViewKey>('env')

export function navigate(key: ViewKey) {
    currentView.value = key
}
