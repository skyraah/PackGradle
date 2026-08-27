// 环境配置缓存：工具检测结果与 CurseForge API Key。
import { ref } from 'vue'
import { EnvService } from '../api'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'

const tools = ref<ToolInfo[]>([])
const loaded = ref(false)
let inflight: Promise<ToolInfo[]> | null = null
let queuedRefresh: Promise<ToolInfo[]> | null = null

export async function loadTools(force = false): Promise<ToolInfo[]> {
    if (!force && loaded.value) return tools.value
    if (inflight) {
        if (force) {
            if (!queuedRefresh) {
                const current = inflight
                queuedRefresh = current
                    .catch(() => tools.value)
                    .then(() => {
                        if (inflight === current) inflight = null
                        return loadTools(true)
                    })
                    .finally(() => {
                        queuedRefresh = null
                    })
            }
            return queuedRefresh
        }
        return inflight
    }
    const task = EnvService.Detect().then(list => {
        tools.value = list ?? []
        loaded.value = true
        return tools.value
    })
    inflight = task
    try {
        return await task
    } finally {
        if (inflight === task) {
            inflight = null
        }
    }
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

// 保存 API Key：成功后才写缓存，避免脏值污染全局状态
export async function saveApiKey(key: string): Promise<void> {
    await EnvService.SetApiKey(key)
    apiKey.value = key
}

export function setApiKeyValue(v: string) {
    apiKey.value = v
}

export function useEnv() {
    return { tools, apiKey, loaded, loadTools, setTools, loadApiKey, saveApiKey, setApiKeyValue }
}
