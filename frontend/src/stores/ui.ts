// 全局消息提示（snackbar）：应用级单例，App.vue 渲染唯一实例，
// 各视图通过 showSnackbar() 直接弹出，不再自持一套 snackbar 状态。
import { ref } from 'vue'

const snackbar = ref(false)
const snackbarMsg = ref('')

export function showSnackbar(msg: string) {
    snackbarMsg.value = msg
    snackbar.value = true
}

export function useUi() {
    return { snackbar, snackbarMsg, showSnackbar }
}
