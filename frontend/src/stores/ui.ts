// 全局 UI 状态（自 main:frontend/src/stores/ui.ts 移植，Vue ref → zustand）：
// 通知队列——连续操作产生的消息按顺序展示，避免后一个提示覆盖前一个；
// 计时自动关闭（Vue 版由 App.vue 的 watch 承担，本版收口进 store 模块，App 根
// 只负责渲染 Snackbar 覆盖层）。
import { create } from 'zustand'

export type NoticeTone = 'info' | 'success' | 'warning' | 'error'

interface Notice {
    message: string
    tone: NoticeTone
    timeout: number
}

interface UiState {
    /** 当前通知是否可见（关闭后有 160ms 出场过渡间隔再取下一条） */
    snackbarOpen: boolean
    snackbarMessage: string
    snackbarTone: NoticeTone
    snackbarTimeout: number
    /** 任务中心抽屉开合（壳层铃铛与视图内「查看任务」入口共用；抽屉本体待任务中心迁移挂载） */
    taskDrawerOpen: boolean
    setTaskDrawerOpen: (v: boolean) => void
}

export const useUiStore = create<UiState>(set => ({
    snackbarOpen: false,
    snackbarMessage: '',
    snackbarTone: 'info',
    snackbarTimeout: 3200,
    taskDrawerOpen: false,
    setTaskDrawerOpen: v => set({ taskDrawerOpen: v }),
}))

// 队列与计时器为模块私有：可见态经 store 广播，消费方（common/Snackbar）只读渲染
const queue: Notice[] = []
let current: Notice | null = null
let transitionTimer: ReturnType<typeof setTimeout> | undefined
let autoTimer: ReturnType<typeof setTimeout> | undefined

function showNext() {
    if (useUiStore.getState().snackbarOpen || current || queue.length === 0) return
    current = queue.shift() ?? null
    if (!current) return
    useUiStore.setState({
        snackbarMessage: current.message,
        snackbarTone: current.tone,
        snackbarTimeout: current.timeout,
        snackbarOpen: true,
    })
    if (autoTimer) clearTimeout(autoTimer)
    // 计时自动关闭（App.vue 职责的迁移落点；长任务结果以任务中心为权威）
    autoTimer = setTimeout(dismissSnackbar, current.timeout)
}

export function showSnackbar(message: string, tone: NoticeTone = 'info', timeout = 3200) {
    const normalized = message.trim()
    if (!normalized) return
    queue.push({ message: normalized, tone, timeout })
    showNext()
}

export function dismissSnackbar() {
    if (!useUiStore.getState().snackbarOpen) return
    if (autoTimer) clearTimeout(autoTimer)
    useUiStore.setState({ snackbarOpen: false })
    // 出场过渡间隔：等动画收完再清 current 取下一条，连续提示不叠影
    if (transitionTimer) clearTimeout(transitionTimer)
    transitionTimer = setTimeout(() => {
        current = null
        showNext()
    }, 160)
}
