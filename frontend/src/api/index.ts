// 服务访问门面：前端统一从这里导入 EnvService / PackwizService / PrismService，
// 由它在「真实 Wails 绑定」与「src/mocks 模拟层」之间按调用时分发。
//
// 开关持久化在 localStorage（packgradle.mock），供设置页开关与顶栏 MOCK 徽标读写；
// 因各 store 持有已加载数据，切换后需刷新页面（调用方负责 window.location.reload()）。
import * as RealEnvService from '../../bindings/packgradle/internal/service/envservice'
import * as RealPackwizService from '../../bindings/packgradle/internal/service/packwizservice'
import * as RealPrismService from '../../bindings/packgradle/internal/service/prismservice'
import * as MockEnvService from '../mocks/envservice'
import * as MockPackwizService from '../mocks/packwizservice'
import * as MockPrismService from '../mocks/prismservice'
import * as RealProjectService from '../../bindings/packgradle/internal/transport/projectservice'
import * as RealRuntimeService from '../../bindings/packgradle/internal/transport/runtimeservice'
import * as RealSyncService from '../../bindings/packgradle/internal/transport/syncservice'

const MOCK_KEY = 'packgradle.mock'

export function isMockEnabled(): boolean {
    try {
        return localStorage.getItem(MOCK_KEY) === '1'
    } catch {
        return false
    }
}

export function setMockEnabled(v: boolean): void {
    try {
        if (v) localStorage.setItem(MOCK_KEY, '1')
        else localStorage.removeItem(MOCK_KEY)
    } catch {
        // localStorage 不可用时静默降级：本次会话内开关仍生效（内存判断读不到，直接忽略）
    }
}

// proxyService 生成命名空间代理：每次属性访问时按开关挑选实现。
// mock 模式下缺失的方法在访问时抛出明确错误，避免静默打到真实后端。
function proxyService(name: string, real: object, mock: object): unknown {
    return new Proxy(
        {},
        {
            get(_target, prop) {
                if (typeof prop !== 'string') return undefined
                const useMock = isMockEnabled()
                const impl = (useMock ? mock : real) as Record<string, unknown>
                const fn = impl[prop]
                if (typeof fn === 'function') return fn
                if (useMock && typeof (real as Record<string, unknown>)[prop] === 'function') {
                    throw new Error(`[mock] 未实现 ${name}.${prop}，请在 src/mocks 中补充`)
                }
                return fn
            },
        },
    )
}

// 类型与真实绑定一致：调用侧完全无感，mock 返回的普通 Promise 可直接 await
export const EnvService: typeof RealEnvService = proxyService(
    'EnvService',
    RealEnvService,
    MockEnvService,
) as typeof RealEnvService

export const PackwizService: typeof RealPackwizService = proxyService(
    'PackwizService',
    RealPackwizService,
    MockPackwizService,
) as typeof RealPackwizService

export const PrismService: typeof RealPrismService = proxyService(
    'PrismService',
    RealPrismService,
    MockPrismService,
) as typeof RealPrismService

export const ProjectService: typeof RealProjectService = proxyService(
    'ProjectService',
    RealProjectService,
    {},
) as typeof RealProjectService

export const RuntimeService: typeof RealRuntimeService = proxyService(
    'RuntimeService',
    RealRuntimeService,
    {},
) as typeof RealRuntimeService

export const SyncService: typeof RealSyncService = proxyService(
    'SyncService',
    RealSyncService,
    {},
) as typeof RealSyncService

// 端点管理页与工作区页的类型出口（真实绑定模型）
export type {
    EndpointDTO,
    ProjectCandidateDTO,
    RuntimeCandidateDTO,
    EndpointHealthDTO,
    PrepareRelationDTO,
    RelationPreparationDTO,
    PreparationCheckDTO,
    PrepareRebindDTO,
    RebindPreparationDTO,
    PolicyDTO,
    MappingRuleDTO,
    RelationDTO,
    WorkspaceDTO,
    WorkspacePageDTO,
    WorkspaceStateDTO,
    WorkspaceFeaturesDTO,
    ActionAvailabilityDTO,
    TaskDTO,
    TaskPageDTO,
    SnapshotSummaryDTO,
    GetChangesDTO,
    ChangeDTO,
    ChangesSummaryDTO,
    ChangesPageDTO,
    SyncPlanDTO,
    ResolutionDTO,
    RepresentationDTO,
    ConflictDTO,
    DiagnosticDTO,
} from '../../bindings/packgradle/internal/transport/models'
