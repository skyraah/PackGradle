// 全局通知队列：连续操作产生的消息按顺序展示，避免后一个提示覆盖前一个。
import { ref, watch } from 'vue'

export type NoticeTone = 'info' | 'success' | 'warning' | 'error'

interface Notice {
    message: string
    tone: NoticeTone
    timeout: number
}

const snackbar = ref(false)
const snackbarMsg = ref('')
const snackbarTone = ref<NoticeTone>('info')
const snackbarTimeout = ref(4200)
const current = ref<Notice | null>(null)
const queue: Notice[] = []
let transitionTimer: ReturnType<typeof setTimeout> | undefined

function showNext() {
    if (snackbar.value || current.value || queue.length === 0) return
    current.value = queue.shift() ?? null
    if (!current.value) return

    snackbarMsg.value = current.value.message
    snackbarTone.value = current.value.tone
    snackbarTimeout.value = current.value.timeout
    snackbar.value = true
}

export function showSnackbar(message: string, tone: NoticeTone = 'info', timeout = 4200) {
    const normalized = message.trim()
    if (!normalized) return
    queue.push({ message: normalized, tone, timeout })
    showNext()
}

export function dismissSnackbar() {
    snackbar.value = false
}

watch(snackbar, open => {
    if (open) return
    if (transitionTimer) clearTimeout(transitionTimer)
    transitionTimer = setTimeout(() => {
        current.value = null
        showNext()
    }, 160)
})

export function useUi() {
    return {
        snackbar,
        snackbarMsg,
        snackbarTone,
        snackbarTimeout,
        showSnackbar,
        dismissSnackbar,
    }
}
