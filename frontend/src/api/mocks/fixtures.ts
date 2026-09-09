// Mock 层内存数据库（自 main:frontend/src/mocks/fixtures.ts 裁剪移植）：
// 仅保留 env + settings 两服务的模拟状态——packwiz/prism 项目与实例夹具随各自
// 页面迁移时再从 main 分支补回（api 门面 mock 侧的扩展桩见 ../index.ts）。
// 数据只存在于内存（刷新页面即重置），写操作真实变更状态，让设置页的
// 工具检测 / 凭据更换 / 保留策略保存能在无后端环境下完整走通。
import type { ToolInfo } from '../../../bindings/packgradle/internal/service'
import type { RetentionSettingsDTO, StorageStatsDTO } from '../../../bindings/packgradle/internal/transport/models'

// 模拟网络/磁盘延迟：读快写慢，让 loading 状态可见
export function delay(ms = 200): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms))
}

// 读取时深拷贝，避免视图意外改动 fixture
export function clone<T>(v: T): T {
    return structuredClone(v)
}

// —— 环境工具 ——
const tools: ToolInfo[] = [
    {
        name: 'packwiz',
        found: true,
        path: 'C:\\Users\\SkyeJ\\go\\bin\\packwiz.exe',
        source: 'env',
        env_dir: 'C:\\Users\\SkyeJ\\go\\bin',
        env_ok: true,
    },
    {
        name: 'prism-launcher',
        found: true,
        path: 'C:\\Users\\SkyeJ\\AppData\\Roaming\\PrismLauncher',
        source: 'default-dir',
        env_dir: '',
        env_ok: true,
    },
]

// —— 环境杂项 ——
let configExists = true

// —— 凭据槽位（ADR-0015）——
// mock 侧只存明文值与设置时间（内存态，刷新即重置）；掩码/校验/槽位枚举
// 语义由各服务 mock 镜像后端口径。默认预置 CF 钥匙，让设置页凭据区开箱
// 即见「已配置」状态。
interface SecretEntry {
    value: string
    updatedAt: number
}
const secretSlots = new Map<string, SecretEntry>([
    ['curseforge/api-key', { value: 'mock$cf-api-key-0000', updatedAt: Date.now() - 86_400_000 }],
])

// —— 保留策略（默认值对齐 internal/core/model/retention.go DefaultRetention）——
const retention: RetentionSettingsDTO = {
    schema_version: 1,
    keep_commits: 20,
    keep_days: 90,
    relation_capacity_bytes: 1024 ** 3,
    preserve_max_bytes: 32 * 1024 ** 2,
    trash_days: 7,
}

// —— 存储占用概览（量级示意：CAS ~ GiB、DB ~ MiB、剩余 ~ GiB）——
const storage: StorageStatsDTO = {
    schema_version: 1,
    cas_total_bytes: 3 * 1024 ** 3 + 612 * 1024 ** 2,
    cas_object_count: 1284,
    cas_tmp_leftovers: 2,
    task_events_count: 5321,
    db_size_bytes: 46 * 1024 ** 2,
    free_disk_bytes: 118 * 1024 ** 3 + 302 * 1024 ** 2,
}

// —— GC 任务单飞状态（RequestGC 幂等复用既有活跃任务）——
let gcTaskSeq = 0

export const db = {
    tools,
    getSlot(kind: string): SecretEntry | undefined {
        return secretSlots.get(kind)
    },
    setSlot(kind: string, value: string): void {
        secretSlots.set(kind, { value, updatedAt: Date.now() })
    },
    clearSlot(kind: string): void {
        secretSlots.delete(kind)
    },
    get configExists() {
        return configExists
    },
    set configExists(v: boolean) {
        configExists = v
    },
    retention,
    storage,
    nextGcTaskId() {
        gcTaskSeq += 1
        return `mock-gc-${gcTaskSeq}`
    },
}
