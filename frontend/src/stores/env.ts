// 环境配置缓存：工具检测结果与 CurseForge API Key。
// 工作台与设置页共用，设置页保存后直接更新缓存，工作台立即可见。
import { ref } from 'vue'
import { EnvService } from '../../bindings/packgradle/internal/service'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'

const tools = ref<ToolInfo[]>([])
const loaded = ref(false)
let inflight: Promise<ToolInfo[]> | null = null

export async function loadTools(force = false): Promise<ToolInfo[]> {
    if (!force && loaded.value) return tools.value
    if (!inflight) {
        inflight = EnvService.Detect()
            .then(list => {
                tools.value = list ?? []
                loaded.value = true
                return tools.value
            })
            .finally(() => {
                inflight = null
            })
    }
    return inflight
}

// setTools 用服务端返回的最新检测结果更新缓存（如 SetToolPath/Configure 的返回值）
export function setTools(list: ToolInfo[] | null) {
    tools.value = list ?? []
    loaded.value = true
}

const apiKey = ref('')

export async function loadApiKey(): Promise<string> {
    apiKey.value = (await EnvService.GetApiKey()) ?? ''
    return apiKey.value
}

export function setApiKeyValue(v: string) {
    apiKey.value = v
}

export function useEnv() {
    return { tools, apiKey, loaded, loadTools, setTools, loadApiKey, setApiKeyValue }
}
