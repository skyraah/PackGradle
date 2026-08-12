// 全局消息提示：两视图共用同一套 snackbar 状态与弹出逻辑
import { ref } from 'vue'

export function useSnackbar() {
    const snackbar = ref(false)
    const snackbarMsg = ref('')

    function show(msg: string) {
        snackbarMsg.value = msg
        snackbar.value = true
    }

    return { snackbar, snackbarMsg, show }
}
