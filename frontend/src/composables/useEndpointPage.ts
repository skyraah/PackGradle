// 端点管理页（/sources、/runtimes、重绑页候选栏）共享逻辑：已登记列表、幂等登记
// 与健康检查状态。发现语义两侧不同（project 按目录搜索 / runtime 自动发现），
// 留在各自页面实现。
// 健康结果不再挂在组件实例上：统一读写 stores/endpoints 的会话级缓存（票 #109），
// 切页往返已查健康不丢；同一端点的并发检查以 'checking' 哨兵去重。
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { EndpointDTO, EndpointHealthDTO } from '../api'
import {
    beginEndpointHealth,
    clearEndpointHealth,
    endpointHealthMap,
    endpointHealthOf,
    setEndpointHealth,
} from '../stores/endpoints'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { BAD, NEUTRAL, OK, WARN, type BadgeTone } from '../utils/pageState'

// 服务适配面：由各页面用具体 Service（ProjectService / RuntimeService）装配
export interface EndpointPageService {
    list(): Promise<EndpointDTO[] | null>
    register(rootPath: string): Promise<EndpointDTO>
    health(endpointID: string): Promise<EndpointHealthDTO>
}

// 端点健康状态 → 徽标色调（与 pageState HEALTH_TONES 的 healthy/endpoint_missing/
// rebind_required 语义一一对应；键为 EndpointHealthDTO.status）
const ENDPOINT_HEALTH_TONES: Record<string, BadgeTone> = {
    ok: OK,
    missing: BAD,
    identity_mismatch: WARN,
}

export function useEndpointPage(service: EndpointPageService) {
    const { t } = useI18n()

    const registered = ref<EndpointDTO[]>([])
    const loadingList = ref(false)
    const registering = ref(false)

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

    // checkHealth 检查单个端点并写入会话缓存；silent 用于进页自动补查
    // （失败静默回退「未检查」，避免整页端点不可达时弹一片错误）
    async function checkHealth(ep: EndpointDTO, silent = false) {
        beginEndpointHealth(ep.id)
        try {
            setEndpointHealth(ep.id, await service.health(ep.id))
        } catch (e) {
            clearEndpointHealth(ep.id)
            if (!silent) showSnackbar(errText(e), 'error')
        }
    }

    // ensureHealth 批量补查缓存缺失的端点（已查过的命中会话缓存直接跳过，
    // 这是「切页往返不丢已查健康」的消费面）
    function ensureHealth(list: EndpointDTO[]) {
        const health = endpointHealthMap()
        for (const ep of list) {
            if (!health.has(ep.id)) void checkHealth(ep, true)
        }
    }

    // healthOf 取健康结果；'checking' 哨兵视为无结果（供模板收窄）
    function healthOf(id: string): EndpointHealthDTO | undefined {
        return endpointHealthOf(id)
    }

    // healthBadge 健康徽标投影（label + tone）：检查中/未检查回落中性，
    // 结果态取 status 语义色（票 #102 色调单源，票 #109 收敛到组合式函数）
    function healthBadge(id: string): { label: string; tone: BadgeTone } {
        const entry = endpointHealthMap().get(id)
        if (entry === 'checking') return { label: t('endpoints.health.checking'), tone: NEUTRAL }
        if (!entry) return { label: t('endpoints.health.unchecked'), tone: NEUTRAL }
        return {
            label: t('endpoints.health.' + entry.status),
            tone: ENDPOINT_HEALTH_TONES[entry.status] ?? NEUTRAL,
        }
    }

    return {
        registered,
        loadingList,
        registering,
        // health 会话级缓存（stores/endpoints 的共享响应式 Map；模板以
        // health.get(id) === 'checking' 判检查中）
        health: endpointHealthMap(),
        loadRegistered,
        register,
        checkHealth,
        ensureHealth,
        healthOf,
        healthBadge,
    }
}
