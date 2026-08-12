// CurseForge API Key 缺失/无效时的引导弹窗：统一错误分流，
// 涉及 API Key 的错误码弹窗引导配置，其余交给 snackbar 提示
import { ref } from 'vue'
import { errText, errorCode } from '../utils/errors'
import { navigate } from '../nav'

// 需要引导用户去配置页填写 API Key 的错误码
const API_KEY_CODES = ['err.cf.api_key_missing', 'err.cf.unauthorized']

export function useApiKeyGuide() {
    const apiKeyDialog = ref(false) // 未配置/无效 API Key 的引导弹窗

    // 统一错误处理：API Key 相关错误码弹窗引导配置，其余用 show 提示
    function handleError(e: unknown, show: (msg: string) => void) {
        const code = errorCode(e)
        if (code && API_KEY_CODES.includes(code)) {
            apiKeyDialog.value = true
            return
        }
        show(errText(e))
    }

    function goConfigApiKey() {
        apiKeyDialog.value = false
        navigate('env')
    }

    return { apiKeyDialog, handleError, goConfigApiKey }
}
