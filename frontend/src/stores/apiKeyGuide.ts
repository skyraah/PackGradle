// CurseForge API Key 缺失/无效时的引导：应用级弹窗（App.vue 渲染），
// 涉及 API Key 的错误码弹窗引导配置，其余交给全局 snackbar 提示。
import { ref } from 'vue'
import router from '../router'
import { errText, errorCode } from '../utils/errors'
import { showSnackbar } from './ui'

// 需要引导用户去设置页填写 API Key 的错误码
const API_KEY_CODES = ['err.cf.api_key_missing', 'err.cf.unauthorized']

const apiKeyDialog = ref(false)

// 统一错误处理：API Key 相关错误码弹引导框，其余用全局 snackbar 提示
export function handleApiKeyError(e: unknown) {
    const code = errorCode(e)
    if (code && API_KEY_CODES.includes(code)) {
        apiKeyDialog.value = true
        return
    }
    showSnackbar(errText(e), 'error')
}

export function goConfigApiKey() {
    apiKeyDialog.value = false
    router.push('/settings')
}

export function useApiKeyGuide() {
    return { apiKeyDialog, handleApiKeyError, goConfigApiKey }
}
