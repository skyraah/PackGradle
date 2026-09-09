// 服务访问门面（自 main:frontend/src/api/index.ts 移植，设置页迁移样板）：
// 前端统一从这里导入 EnvService / SettingsService 等，由它在「真实 Wails 绑定」
// 与「mocks 模拟层」之间按调用时分发。
//
// mock 仅存在于开发构建（ADR-0001 §5）：isMockEnabled 以 import.meta.env.DEV 为
// 先决门（与 vite.config.ts 注入的 __DEV__ 同语义，静态替换、分支可折叠），生产
// 构建中该常量为 false，mock 分支与 mocks 动态导入整体被裁剪出产物（构建验收：
// dist 无 packgradle.mock 等 mock 痕迹）。mock 实现住在 ./mocks/（main 版在
// src/mocks/，本阶段收敛进 api/ 目录随门面同迁）。
// 开关持久化在 localStorage（packgradle.mock），供设置页 dev 开关与顶栏 DEV MOCK
// 徽标读写；因各 store 持有已加载数据，切换后需刷新页面（调用方负责
// window.location.reload()）。
import * as RealEnvService from '../../bindings/packgradle/internal/service/envservice'
import * as RealPackwizService from '../../bindings/packgradle/internal/service/packwizservice'
import * as RealPrismService from '../../bindings/packgradle/internal/service/prismservice'
import * as RealSecretsService from '../../bindings/packgradle/internal/service/secretsservice'
import * as RealProjectService from '../../bindings/packgradle/internal/transport/projectservice'
import * as RealRuntimeService from '../../bindings/packgradle/internal/transport/runtimeservice'
import * as RealSyncService from '../../bindings/packgradle/internal/transport/syncservice'
import * as RealSettingsService from '../../bindings/packgradle/internal/transport/settingsservice'

const MOCK_KEY = 'packgradle.mock'

export function isMockEnabled(): boolean {
    if (!import.meta.env.DEV) return false
    try {
        return localStorage.getItem(MOCK_KEY) === '1'
    } catch {
        return false
    }
}

export function setMockEnabled(v: boolean): void {
    if (!import.meta.env.DEV) return
    try {
        if (v) localStorage.setItem(MOCK_KEY, '1')
        else localStorage.removeItem(MOCK_KEY)
    } catch {
        // localStorage 不可用时静默降级：本次会话内开关仍生效（内存判断读不到，直接忽略）
    }
}

// mocks 模拟层仅在开发构建按需动态装载：生产构建中 import.meta.env.DEV 恒 false，
// 此分支为死代码，动态导入连同 mocks 模块一并从产物裁剪。
// 本阶段仅实现设置页消费的 env + settings 两服务（./mocks/envservice.ts、
// ./mocks/settingsservice.ts）；其余服务的 mock 随各自页面迁移在 ./mocks/ 下补齐。
type MockImpl = Record<string, unknown>
let mockImpls: { env: MockImpl; settings: MockImpl; secrets: MockImpl } | null = null
if (import.meta.env.DEV && isMockEnabled()) {
    const [env, settings, secrets] = await Promise.all([
        import('./mocks/envservice'),
        import('./mocks/settingsservice'),
        import('./mocks/secretsservice'),
    ])
    mockImpls = { env, settings, secrets }
}

// proxyService 生成命名空间代理：每次属性访问时按开关挑选实现。
// mock 模式下缺失的方法在访问时抛出明确错误，避免静默打到真实后端。
function proxyService(name: string, real: object, mock: MockImpl): unknown {
    return new Proxy(
        {},
        {
            get(_target, prop) {
                if (typeof prop !== 'string') return undefined
                const impl = (mockImpls ? mock : real) as Record<string, unknown>
                const fn = impl[prop]
                if (typeof fn === 'function') return fn
                if (mockImpls && typeof (real as Record<string, unknown>)[prop] === 'function') {
                    throw new Error(`[mock] 未实现 ${name}.${prop}，请在 src/api/mocks 中补充`)
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
    (mockImpls?.env ?? {}) as MockImpl,
) as typeof RealEnvService

// —— mock 扩展桩（真实侧全量直通，mock 实现待各页迁移时补进 ./mocks/）——
export const PackwizService: typeof RealPackwizService = proxyService(
    'PackwizService',
    RealPackwizService,
    {}, // mock 待补：./mocks/packwizservice.ts（项目页迁移时自 main:src/mocks 移植）
) as typeof RealPackwizService

export const PrismService: typeof RealPrismService = proxyService(
    'PrismService',
    RealPrismService,
    {}, // mock 待补：./mocks/prismservice.ts（运行实例页迁移时自 main:src/mocks 移植）
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

// 设置/开关域服务（契约 06 §2，票 #57）：保留设置 + 工作区授权开关（票 #62 消费）
export const SettingsService: typeof RealSettingsService = proxyService(
    'SettingsService',
    RealSettingsService,
    (mockImpls?.settings ?? {}) as MockImpl,
) as typeof RealSettingsService

// 凭据槽位服务（ADR-0015）：Set / Delete / Status 三件，明文永不出后端
export const SecretsService: typeof RealSecretsService = proxyService(
    'SecretsService',
    RealSecretsService,
    (mockImpls?.secrets ?? {}) as MockImpl,
) as typeof RealSecretsService

// 凭据状态投影（ADR-0015；SecretsService.Status 返回型）
export type { SecretStatus } from '../../bindings/packgradle/internal/service/models'

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
    MergedPreviewDTO,
    DiagnosticDTO,
    ApplyRunDTO,
    ApplyOperationDTO,
    ApplyOperationPageDTO,
    CommitSummaryDTO,
    CommitChangeDTO,
    CommitDTO,
    CommitPageDTO,
    RetentionSettingsDTO,
    UpdateRetentionSettingsDTO,
    RestorePlanDTO,
    RestorePlanItemDTO,
    RestoreBlockedItemDTO,
    RestorePrepareDTO,
    ResolveRestorePlanDTO,
    ConfirmRestorePlanDTO,
    StageUserObjectDTO,
} from '../../bindings/packgradle/internal/transport/models'
