// 端点管理页（/sources、/runtimes）共享逻辑：已登记列表、幂等登记与健康检查状态。
// 发现语义两侧不同（project 按目录搜索 / runtime 自动发现），留在各自页面实现。
import { reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { EndpointDTO, EndpointHealthDTO } from '../api'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { endpointHealthTone } from '../utils/pageState'

// 服务适配面：由各页面用具体 Service（ProjectService / RuntimeService）装配
export interface EndpointPageService {
    list(): Promise<EndpointDTO[] | null>
    register(rootPath: string): Promise<EndpointDTO>
    health(endpointID: string): Promise<EndpointHealthDTO>
}

export function useEndpointPage(service: EndpointPageService) {
    const { t } = useI18n()

    const registered = ref<EndpointDTO[]>([])
    const loadingList = ref(false)
    const registering = ref(false)
    // endpoint_id -> 健康结果；'checking' 表示检查进行中
    const health = reactive(new Map<string, EndpointHealthDTO | 'checking'>())

    async function loadRegistered() {
        loadingList.value = true
        try {
            registered.value = (await service.list()) ?? []
        } catch (e) {
            showSnackbar(errText(e), 'error')
        } finally {
            loadingList.value = false
        }
    }

    // register 幂等登记；成功返回端点投影（调用方再做页面侧联动，如重发现），
    // 失败返回 undefined（错误提示已统一处理）
    async function register(rootPath: string): Promise<EndpointDTO | undefined> {
        registering.value = true
        try {
            const ep = await service.register(rootPath)
            showSnackbar(t('endpoints.registerOk', [ep.display_name]), 'success')
            await loadRegistered()
            return ep
        } catch (e) {
            showSnackbar(errText(e), 'error')
            return undefined
        } finally {
            registering.value = false
        }
    }

    async function checkHealth(ep: EndpointDTO) {
        health.set(ep.id, 'checking')
        try {
            health.set(ep.id, await service.health(ep.id))
        } catch (e) {
            health.delete(ep.id)
            showSnackbar(errText(e), 'error')
        }
    }

    // healthOf 取健康结果；'checking' 哨兵视为无结果（供模板收窄）
    function healthOf(id: string): EndpointHealthDTO | undefined {
        const h = health.get(id)
        return h && h !== 'checking' ? h : undefined
    }

    // 健康徽标色调收敛于 utils/pageState 的 endpointHealthTone（票 #102）
    const healthBadgeTone = endpointHealthTone

    return {
        registered,
        loadingList,
        registering,
        health,
        loadRegistered,
        register,
        checkHealth,
        healthOf,
        healthBadgeTone,
    }
}
