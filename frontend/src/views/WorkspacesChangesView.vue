<script setup lang="ts">
// /workspaces/:id/changes：资源级变更浏览（契约 03 §2.2 GetChanges；UX 原型 §7.3
// 画板 C-01..C-06，票 #105）。
// 头部走共享工作区对象头 WorkspaceObjectHead（变化/受管范围页签、主操作、「更多」
// 五项菜单、指标条以下为变化页自身）；状态横幅同屏最多一条，优先级
// 恢复 > 重绑定 > 扫描失败 > 扫描中 > 过期 > 冲突 > 有变化 > clean（§7.3）。
// 数据模式：工作区上下文读 stores/syncCache 投影；变更快照仅 scan_state=ready
// 时查询（首查/扫描收口触发），刷新失败保留旧快照（契约 04 §2.4）。
// 筛选（胶囊/类型/搜索）为已加载行的客户端过滤——6 枚胶囊是复合谓词，后端
// 单值 classification 参数表达不了；计数行随筛选联动，不影响计划范围。
// 证据入口不画（拍板 Q9-b：后端无资源级三栏内容 API，证据抽屉整体后置）。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
    CircleAlert,
    CircleCheck,
    Clock,
    Play,
    RefreshCw,
    Scan,
    Search,
    TriangleAlert,
    Unlink,
} from '@lucide/vue'
import type { Component } from 'vue'
import { Browser, Clipboard } from '@wailsio/runtime'
import { SyncService } from '../api'
import type { ChangeDTO, ChangesSummaryDTO, DiagnosticDTO, WorkspaceDTO } from '../api'
import { bootstrapped, bootstrapError, retryBootstrap, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar, taskDrawerOpen } from '../stores/ui'
import { errText } from '../utils/errors'
import {
    BASELINE_TONES,
    DIFF_TONES,
    HEALTH_TONES,
    PAGE_LIMIT,
    formatTime,
    latestScanText,
    toneOf,
    verdictClass,
    verdictKeyOf,
} from '../utils/pageState'
import { availabilityReasonText, canPrepareSync, canQuickUpdate, prepareSync } from '../utils/plans'
import { useWorkspaceHeadTabs } from '../composables/useWorkspaceHeadTabs'
import WorkspaceObjectHead from '../components/common/WorkspaceObjectHead.vue'
import type { HeadBadge, HeadMenuItem } from '../components/common/WorkspaceObjectHead.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))
const rebindRoute = computed(() => '/workspaces/' + relationID.value + '/rebind')

// —— 工作区上下文（读 syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)
const scanReady = computed(() => wsRow.value?.state.scan_state === 'ready')
const scanRunning = computed(
    () => wsRow.value?.state.scan_state === 'scanning' || wsRow.value?.state.scan_state === 'queued',
)

// 行内未完结任务（当前关系上的活跃任务，来自 syncCache）：扫描中横幅的阶段文案
const activeTask = computed(() => {
    const id = wsRow.value?.state.active_task_id
    return id ? (tasks.value.get(id) ?? null) : null
})

// —— 变更查询快照（仅 scan ready 时拉取；刷新失败保留旧数据）——
const items = ref<ChangeDTO[]>([])
const summary = ref<ChangesSummaryDTO | null>(null)
const nextCursor = ref('')
const phase = ref<'loading' | 'error' | 'ready'>('loading')
const inflight = ref(false)
const errorMsg = ref('')

let querySeq = 0

async function queryPage(cursor: string): Promise<void> {
    const seq = ++querySeq
    inflight.value = true
    try {
        const page = await SyncService.GetChanges({
            relation_id: relationID.value,
            cursor: cursor || undefined,
            limit: PAGE_LIMIT,
        })
        if (seq !== querySeq) return // 已被更新的查询作废
        if (cursor) {
            items.value = [...items.value, ...(page.items ?? [])]
        } else {
            items.value = page.items ?? []
            summary.value = page.summary
        }
        nextCursor.value = page.next_cursor ?? ''
        phase.value = 'ready'
        errorMsg.value = ''
    } catch (e) {
        if (seq !== querySeq) return
        if (phase.value === 'ready') {
            // 已有查询快照：保留旧数据，仅提示（契约 04 受控重查的失败语义）
            showSnackbar(t('changes.refreshFailed') + '：' + errText(e), 'error')
        } else {
            phase.value = 'error'
            errorMsg.value = errText(e)
        }
    } finally {
        if (seq === querySeq) inflight.value = false
    }
}

const reload = () => void queryPage('')
const loadMore = () => void queryPage(nextCursor.value)

// scan_ready 翻转驱动查询：扫描收口（never/scanning → ready）或开始新一轮扫描
// （ready → scanning 清空快照）都经 syncCache 刷新投影到 scanReady
watch(
    [relationID, scanReady],
    ([, ready]) => {
        if (ready) void queryPage('')
        else {
            items.value = []
            summary.value = null
            nextCursor.value = ''
            phase.value = 'loading'
        }
    },
    { immediate: true },
)
// 扫描外任务（快速更新应用等）收口 → 补一轮变更重查（读时计算，完成后才变）
watch(
    () => wsRow.value?.state.active_task_id ?? '',
    (now, prev) => {
        if (prev && !now && scanReady.value) reload()
    },
)

// —— 客户端筛选：6 枚胶囊 + 资源类型 + 实时搜索（谓词沿原型 resourcesFiltered）——
const CHIPS = [
    { id: 'all', key: 'changes.filter.chipAll' },
    { id: 'changed', key: 'changes.filter.chipChanged' },
    { id: 'conflict', key: 'changes.filter.chipConflict' },
    { id: 'projectChanged', key: 'changes.filter.chipProjectChanged' },
    { id: 'runtimeChanged', key: 'changes.filter.chipRuntimeChanged' },
    { id: 'deleted', key: 'changes.filter.chipDeleted' },
] as const
type ChipID = (typeof CHIPS)[number]['id']

const chip = ref<ChipID>('all')
const kind = ref('all')
const search = ref('')

// 分类谓词集合：变更分类 → 原型判断键（utils/pageState CHANGE_VERDICTS 同一映射口径）
const UNCHANGED_CLASSES = new Set(['noop', 'converged', 'adopt_equal', 'merged_clean'])
const CONFLICT_CLASSES = new Set(['conflict_modify', 'conflict_delete_modify'])
const PROJECT_TOUCHED = new Set([
    'project_to_runtime',
    'remove_project_candidate',
    'remove_runtime_candidate',
    'conflict_modify',
    'conflict_delete_modify',
])
const RUNTIME_TOUCHED = new Set(['runtime_to_project', 'conflict_modify', 'conflict_delete_modify'])
const DELETION_CLASSES = new Set(['remove_runtime_candidate', 'remove_project_candidate', 'conflict_delete_modify'])

function matchChip(classification: string): boolean {
    switch (chip.value) {
        case 'changed':
            return !UNCHANGED_CLASSES.has(classification)
        case 'conflict':
            return CONFLICT_CLASSES.has(classification)
        case 'projectChanged':
            return PROJECT_TOUCHED.has(classification)
        case 'runtimeChanged':
            return RUNTIME_TOUCHED.has(classification)
        case 'deleted':
            return DELETION_CLASSES.has(classification)
        default:
            return true
    }
}

const hasFilters = computed(() => chip.value !== 'all' || kind.value !== 'all' || search.value.trim() !== '')

const displayRows = computed<ChangeDTO[]>(() => {
    const q = search.value.trim().toLowerCase()
    return items.value.filter(r => {
        if (!matchChip(r.classification)) return false
        if (kind.value !== 'all' && r.resource_kind !== kind.value) return false
        if (q && !(r.relative_path || r.resource_id).toLowerCase().includes(q)) return false
        return true
    })
})

function clearFilters(): void {
    chip.value = 'all'
    kind.value = 'all'
    search.value = ''
}

const kindOptions = ['mod', 'text_file', 'binary_file']

// —— 对象头视图模型（副行徽章 / 适配器 / 最近扫描）——
const healthBadge = computed<HeadBadge | null>(() => {
    const w = wsRow.value
    if (!w) return null
    return { label: t('workspaces.health.' + w.relation.health), tone: toneOf(HEALTH_TONES, w.relation.health) }
})
const diffBadge = computed<HeadBadge | null>(() => {
    const w = wsRow.value
    if (!w) return null
    return { label: t('workspaces.diffState.' + w.state.diff_state), tone: toneOf(DIFF_TONES, w.state.diff_state) }
})
const adaptersText = computed(() => {
    const w = wsRow.value
    return w ? `${w.relation.project.adapter} · ${w.relation.runtime.adapter}` : ''
})
const lastScanText = computed(() => {
    const w = wsRow.value
    if (!w) return ''
    return latestScanText(w.latest_project_snapshot?.captured_at, w.latest_runtime_snapshot?.captured_at)
})

// 页签「变化 | 受管范围 | 历史」三常驻（拍板 Q8-a）：跨页导航，活动态各页自持（本页恒 changes）
const tabs = useWorkspaceHeadTabs(() => relationID.value, 'changes')

// —— 主操作（§7.1 行操作优先级，唯一主操作随工作区状态切换）——
const preparing = ref(false)
const canPrepareSyncNow = computed(() => canPrepareSync(wsRow.value))
const canQuickUpdateNow = computed(() => canQuickUpdate(wsRow.value))
const quickUpdateReason = computed(() => availabilityReasonText(wsRow.value, 'quick_update'))

const scanning = ref(false)
const recovering = ref(false)
const updating = ref(false)

async function startScan(): Promise<void> {
    if (!wsRow.value || scanning.value) return
    scanning.value = true
    try {
        await SyncService.StartScan(relationID.value)
        triggerRequery()
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        scanning.value = false
    }
}

// 处理恢复（契约 05 §5 双入口之对象头）：任务中心缓存持有恢复任务时直接取其
// task_id，否则 GetApplyRun 取最近运行 → 导航恢复详情页
async function openRecovery(): Promise<void> {
    if (recovering.value) return
    recovering.value = true
    try {
        const cached = [...tasks.value.values()].find(
            k => k.relation_id === relationID.value && k.status === 'recovery_required',
        )
        const runID = cached?.task_id ?? (await SyncService.GetApplyRun(relationID.value)).task_id
        await router.push('/workspaces/' + relationID.value + '/recoveries/' + runID)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        recovering.value = false
    }
}

function openTasks(): void {
    taskDrawerOpen.value = true
}

// 生成同步计划（T11 计划页承接）：PrepareSync 不发事件，补一轮受控重查让计划页
// 拿到新鲜 availability
async function prepareSyncPlan(): Promise<void> {
    const ws = wsRow.value
    if (!ws || preparing.value) return
    preparing.value = true
    try {
        const plan = await prepareSync(ws)
        triggerRequery()
        await router.push('/workspaces/' + relationID.value + '/plans/' + plan.plan_id)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        preparing.value = false
    }
}

const primaryAction = computed(() => {
    const w = wsRow.value
    if (!w) return null
    if (w.relation.health === 'recovery_required')
        return { label: t('changes.bannerAction.recover'), disabled: recovering.value }
    if (w.relation.health === 'rebind_required') return { label: t('changes.bannerAction.rebind') }
    if (w.state.scan_state === 'never_scanned') return { label: t('changes.startScan'), disabled: scanning.value }
    if (w.state.scan_state === 'failed') return { label: t('changes.bannerAction.retryScan'), disabled: scanning.value }
    if (scanRunning.value) return { label: t('changes.bannerAction.viewTasks'), tonal: true }
    if (canPrepareSyncNow.value) return { label: t('changes.planAction'), disabled: preparing.value }
    return null
})

function onPrimary(): void {
    const w = wsRow.value
    if (!w) return
    if (w.relation.health === 'recovery_required') void openRecovery()
    else if (w.relation.health === 'rebind_required') void router.push(rebindRoute.value)
    else if (w.state.scan_state === 'never_scanned' || w.state.scan_state === 'failed') void startScan()
    else if (scanRunning.value) openTasks()
    else if (canPrepareSyncNow.value) void prepareSyncPlan()
}

// —— 「更多」菜单（规范五项，票 #105 拍板）——

// 快速更新（契约 07 §3.1/§6，票 #86）：一次点击单调用——链在后端（扫描 → 计划 →
// 停靠判定 → 免确认执行或停待确认）。三态承接：no_diff → 「已是最新」；apply_started
// → 任务移交任务中心（本页即变化页，无需跳转）；awaiting_confirmation → 待确认计划页
async function runQuickUpdate(): Promise<void> {
    if (updating.value) return
    updating.value = true
    try {
        const res = await SyncService.QuickUpdate(relationID.value)
        triggerRequery()
        if (res.outcome === 'no_diff') {
            showSnackbar(t('workspaces.quickUpdate.upToDate'), 'success')
        } else if (res.outcome === 'apply_started') {
            showSnackbar(t('workspaces.quickUpdate.directToast'), 'success')
        } else {
            showSnackbar(t('workspaces.quickUpdate.manualToast'), 'warning')
            await router.push('/workspaces/' + relationID.value + '/plans/' + res.plan_id)
        }
    } catch (e) {
        showSnackbar(errText(e), 'error')
        triggerRequery()
    } finally {
        updating.value = false
    }
}

// 打开端点位置：项目源根目录交系统打开（Wails v3 Browser.OpenURL 在 Windows 走
// FileProtocolHandler，目录路径即资源管理器定位该文件夹）
async function openEndpoint(): Promise<void> {
    const path = wsRow.value?.relation.project.root_path
    if (!path) return
    try {
        await Browser.OpenURL(path)
    } catch (e) {
        showSnackbar(t('changes.openEndpoint.failed') + '：' + errText(e), 'error')
    }
}

// 复制诊断信息：读双端最新快照的持久化诊断（GetSnapshotDiagnostics，与受管范围页
// 同源），连同工作区状态摘要写入系统剪贴板（Wails Clipboard.SetText）
function buildDiagnosticText(w: WorkspaceDTO, diags: DiagnosticDTO[]): string {
    const lines = [
        t('changes.diag.header'),
        t('changes.diag.workspace', [w.relation.project.display_name, w.relation.runtime.display_name]),
        t('changes.diag.relation', [w.relation.relation_id]),
        t('changes.diag.state', [
            t('workspaces.health.' + w.relation.health),
            t('workspaces.scanState.' + w.state.scan_state),
            t('workspaces.baselineState.' + w.state.baseline_state),
            t('workspaces.diffState.' + w.state.diff_state),
        ]),
    ]
    if (!diags.length) {
        lines.push(t('changes.diag.clean'))
        return lines.join('\n')
    }
    lines.push(t('changes.diag.list', [diags.length]))
    for (const d of diags) {
        lines.push(`- [${d.severity}] ${d.code}${d.detail ? ' · ' + d.detail : ''}`)
    }
    return lines.join('\n')
}

async function copyDiagnostics(): Promise<void> {
    const w = wsRow.value
    if (!w) return
    try {
        const ids = [w.latest_project_snapshot?.snapshot_id, w.latest_runtime_snapshot?.snapshot_id].filter(
            (s): s is string => !!s,
        )
        let diags: DiagnosticDTO[] = []
        if (ids.length) {
            const lists = await Promise.all(ids.map(id => SyncService.GetSnapshotDiagnostics(relationID.value, id)))
            diags = lists.flat().filter((d): d is DiagnosticDTO => !!d)
        }
        await Clipboard.SetText(buildDiagnosticText(w, diags))
        showSnackbar(t('changes.copyDiagnostics.ok'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    }
}

const menuItems = computed<HeadMenuItem[]>(() => [
    {
        id: 'quick-update',
        label: updating.value ? t('workspaces.quickUpdate.busy') : t('objHead.menu.quickUpdate'),
        disabled: !canQuickUpdateNow.value || updating.value,
        title: quickUpdateReason.value || undefined,
    },
    { id: 'rebind', label: t('objHead.menu.rebind') },
    { id: 'settings', label: t('objHead.menu.settings') },
    { id: 'open-endpoint', label: t('objHead.menu.openEndpoint') },
    { id: 'copy-diagnostics', label: t('objHead.menu.copyDiagnostics') },
])

function onMenu(id: string): void {
    if (id === 'quick-update') void runQuickUpdate()
    else if (id === 'rebind') void router.push(rebindRoute.value)
    else if (id === 'settings') void router.push('/workspaces/' + relationID.value + '/settings')
    else if (id === 'open-endpoint') void openEndpoint()
    else if (id === 'copy-diagnostics') void copyDiagnostics()
}

// —— 状态横幅（§7.3：同屏最多一条，只表达当前最需要处理的状态）——
interface BannerVM {
    tone: 'warn' | 'err' | 'info' | 'ok'
    icon: Component
    title: string
    text: string
    action?: { label: string; variant: 'default' | 'secondary' | 'destructive'; run: () => void }
}

function bannerPlanAction(): BannerVM['action'] {
    if (!canPrepareSyncNow.value) return undefined
    return { label: t('changes.planAction'), variant: 'default', run: () => void prepareSyncPlan() }
}

const banner = computed<BannerVM | null>(() => {
    const w = wsRow.value
    if (!w || w.state.scan_state === 'never_scanned') return null // C-01：空态面板即状态
    if (w.relation.health === 'recovery_required') {
        return {
            tone: 'err',
            icon: CircleAlert,
            title: t('changes.banner.recoveryTitle'),
            text: t('changes.banner.recoveryText'),
            action: { label: t('changes.bannerAction.recover'), variant: 'destructive', run: () => void openRecovery() },
        }
    }
    if (w.relation.health === 'rebind_required') {
        return {
            tone: 'warn',
            icon: Unlink,
            title: t('changes.banner.rebindTitle'),
            text: t('changes.banner.rebindText'),
            action: {
                label: t('changes.bannerAction.rebind'),
                variant: 'secondary',
                run: () => void router.push(rebindRoute.value),
            },
        }
    }
    if (w.state.scan_state === 'failed') {
        return {
            tone: 'err',
            icon: CircleAlert,
            title: t('changes.banner.scanFailedTitle'),
            text: t('changes.banner.scanFailedText'),
            action: { label: t('changes.bannerAction.retryScan'), variant: 'secondary', run: () => void startScan() },
        }
    }
    if (scanRunning.value) {
        const phaseText = activeTask.value ? t(activeTask.value.message_key, activeTask.value.message_args ?? []) : ''
        return {
            tone: 'info',
            icon: RefreshCw,
            title: t('changes.banner.scanningTitle'),
            text: phaseText
                ? t('changes.banner.scanningTextPhase', [phaseText])
                : t('changes.banner.scanningText'),
            action: { label: t('changes.bannerAction.viewTasks'), variant: 'secondary', run: openTasks },
        }
    }
    if (w.state.baseline_state === 'stale') {
        return {
            tone: 'warn',
            icon: Clock,
            title: t('changes.banner.staleTitle'),
            text: t('changes.banner.staleText'),
            action: { label: t('changes.bannerAction.rescan'), variant: 'secondary', run: () => void startScan() },
        }
    }
    if (w.state.diff_state === 'conflicted') {
        return {
            tone: 'warn',
            icon: TriangleAlert,
            title: t('changes.banner.conflictTitle'),
            text: t('changes.banner.conflictText', [summary.value?.conflict_count ?? 0]),
            action: bannerPlanAction(),
        }
    }
    if (w.state.diff_state === 'dirty') {
        return {
            tone: 'info',
            icon: RefreshCw,
            title: t('changes.banner.dirtyTitle'),
            text: t('changes.banner.dirtyText'),
            action: bannerPlanAction(),
        }
    }
    if (w.state.diff_state === 'clean') {
        return {
            tone: 'ok',
            icon: CircleCheck,
            title: t('changes.banner.cleanTitle'),
            text: t('changes.banner.cleanText'),
        }
    }
    // 其余 diff 态（unknown / initialization_required）：就绪可分析即给计划入口（原型兜底横幅）
    if (canPrepareSyncNow.value) {
        return {
            tone: 'info',
            icon: RefreshCw,
            title: t('changes.banner.planAvailableTitle'),
            text: '',
            action: bannerPlanAction(),
        }
    }
    return null
})

const bannerClass: Record<BannerVM['tone'], string> = {
    warn: 'bg-tint-warning border-tint-warning',
    err: 'bg-tint-error border-tint-error',
    info: 'bg-tint-primary border-tint-primary',
    ok: 'bg-tint-success border-tint-success',
}
const bannerIconClass: Record<BannerVM['tone'], string> = {
    warn: 'text-warning',
    err: 'text-error',
    info: 'text-primary',
    ok: 'text-success',
}

// —— 指标条（原型 wsMetrics；扫描 ready/failed 展示，扫描中/未扫描不展示）——
const showMetrics = computed(() => !!wsRow.value && (scanReady.value || wsRow.value?.state.scan_state === 'failed'))

function sideScanTime(at?: string): string {
    if (scanRunning.value) return t('changes.metrics.inProgress')
    return formatTime(at)
}
const projectScanTime = computed(() => sideScanTime(wsRow.value?.latest_project_snapshot?.captured_at))
const runtimeScanTime = computed(() => sideScanTime(wsRow.value?.latest_runtime_snapshot?.captured_at))
const baselineBadge = computed<HeadBadge | null>(() => {
    const w = wsRow.value
    if (!w) return null
    return { label: t('workspaces.baselineState.' + w.state.baseline_state), tone: toneOf(BASELINE_TONES, w.state.baseline_state) }
})
const resourceCount = computed(() => {
    const s = wsRow.value
    const n = s?.latest_project_snapshot?.resource_count ?? s?.latest_runtime_snapshot?.resource_count
    return typeof n === 'number' ? n.toLocaleString() : '—'
})

// —— 表区状态（仅 scan ready；互斥主状态沿页面级状态机先例）——
const tableState = computed<'loading' | 'error' | 'empty' | 'filteredEmpty' | 'ready'>(() => {
    if (phase.value === 'loading') return 'loading'
    if (phase.value === 'error') return 'error'
    if (!displayRows.value.length) return hasFilters.value ? 'filteredEmpty' : 'empty'
    return 'ready'
})

// —— 展示辅助 ——
function humanSize(bytes?: number): string {
    if (!bytes || bytes <= 0) return ''
    const units = ['B', 'KB', 'MB', 'GB']
    let v = bytes
    let i = 0
    while (v >= 1024 && i < units.length - 1) {
        v /= 1024
        i++
    }
    return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

function diagTitle(row: ChangeDTO): string {
    return (row.diagnostics ?? [])
        .map(d => {
            const text = t(d.code, d.args ?? [])
            return d.detail ? `${text}（${d.detail}）` : text
        })
        .join('\n')
}

const cols = [
    'changes.colResource',
    'changes.colProject',
    'changes.colBaseline',
    'changes.colRuntime',
    'changes.colVerdict',
    'changes.colEnd',
]

// 筛选胶囊形态（原型 .fchip/.fchip.on：26px 胶囊，选中 tint 底 + 主色字）
const chipBaseClass = 'h-[26px] rounded-full border px-[11px] text-[11.5px] font-semibold transition-colors'
const chipOnClass = 'border-transparent bg-tint-primary text-primary'
const chipOffClass = 'border-border bg-transparent text-muted-foreground hover:text-foreground'
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- syncCache 引导失败：统一错误态可重试（否则卡骨架无出口） -->
        <Card v-if="!bootstrapped && bootstrapError">
            <CardContent class="flex items-center justify-between gap-3 py-4">
                <span class="text-destructive text-sm">{{ t('workspaces.errorTitle') }}：{{ bootstrapError }}</span>
                <Button variant="outline" size="sm" @click="retryBootstrap">{{ t('workspaces.retry') }}</Button>
            </CardContent>
        </Card>

        <!-- syncCache 引导未完成：对象头位给同构占位 -->
        <Card v-else-if="!bootstrapped">
            <CardContent class="flex flex-col gap-2 py-3">
                <div v-for="i in 2" :key="i" class="h-4 w-64 animate-pulse rounded bg-muted"></div>
            </CardContent>
        </Card>

        <!-- 工作区不存在：syncCache 引导完成后仍找不到该关系 -->
        <Card v-else-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('changes.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('changes.backToList') }}
                </Button>
            </CardContent>
        </Card>

        <template v-else>
            <!-- 对象头（共享组件，票 #105；计划页 #108 / 受管范围页 #110 复用） -->
            <WorkspaceObjectHead
                v-if="wsRow"
                :project="wsRow.relation.project.display_name"
                :runtime="wsRow.relation.runtime.display_name"
                :health-badge="healthBadge"
                :diff-badge="diffBadge"
                :adapters="adaptersText"
                :last-scan="lastScanText"
                :primary-action="primaryAction"
                :menu-items="menuItems"
                :tabs="tabs"
                active-tab="changes"
                @primary="onPrimary"
                @menu="onMenu"
            />
            <h1 v-else class="page-title">{{ t('changes.title') }}</h1>

            <!-- 状态横幅：同屏最多一条（§7.3 优先级，四色 tint） -->
            <div
                v-if="banner"
                class="flex items-center gap-2.5 rounded-lg border px-3.5 py-2.5 text-[12.5px]"
                :class="bannerClass[banner.tone]"
            >
                <component :is="banner.icon" class="size-4 flex-none" :class="bannerIconClass[banner.tone]" />
                <div class="min-w-0 flex-1">
                    <span class="font-bold">{{ banner.title }}</span>
                    <template v-if="banner.text">
                        <span> · {{ banner.text }}</span>
                    </template>
                </div>
                <Button
                    v-if="banner.action"
                    size="sm"
                    :variant="banner.action.variant"
                    :disabled="banner.action.variant === 'destructive' ? recovering : false"
                    @click="banner.action.run"
                >
                    {{ banner.action.label }}
                </Button>
            </div>

            <!-- C-01 未扫描空态（原型 C-01：对象头 + 空态面板，无横幅/表格） -->
            <Card v-if="wsRow && wsRow.state.scan_state === 'never_scanned'">
                <CardContent class="flex flex-col items-center gap-3 py-14">
                    <Scan class="text-faint size-10" aria-hidden="true" />
                    <h3 class="text-[15px] font-semibold">{{ t('changes.emptyTitle') }}</h3>
                    <p class="text-muted-foreground max-w-md text-center text-xs">{{ t('changes.emptyDesc') }}</p>
                    <Button :disabled="scanning" @click="startScan">
                        <Play class="size-3.5" aria-hidden="true" />
                        {{ t('changes.startScan') }}
                    </Button>
                </CardContent>
            </Card>

            <!-- C-02 扫描中：保留对象头/横幅/指标位，表格给同构骨架（§9 状态矩阵） -->
            <Card v-else-if="wsRow && scanRunning">
                <CardContent class="py-2">
                    <Table>
                        <TableHeader>
                            <TableRow>
                                <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                            </TableRow>
                        </TableHeader>
                        <TableBody>
                            <TableRow v-for="i in 6" :key="i">
                                <TableCell v-for="c in cols" :key="c">
                                    <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                </TableCell>
                            </TableRow>
                        </TableBody>
                    </Table>
                </CardContent>
            </Card>

            <template v-else-if="wsRow">
                <!-- 指标条（ready/failed 展示，扫描中/未扫描不展示；§9 状态矩阵） -->
                <div
                    v-if="showMetrics"
                    class="flex flex-wrap items-center gap-x-5 gap-y-1.5 rounded-lg border border-border bg-card px-3 py-2 text-xs text-muted-foreground"
                >
                    <span>
                        {{ t('changes.metrics.projectScan') }}
                        <b class="text-foreground font-semibold">{{ projectScanTime }}</b>
                    </span>
                    <span>
                        {{ t('changes.metrics.runtimeScan') }}
                        <b class="text-foreground font-semibold">{{ runtimeScanTime }}</b>
                    </span>
                    <span class="flex items-center gap-1.5">
                        {{ t('workspaces.colBaseline') }}
                        <Badge v-if="baselineBadge" :variant="baselineBadge.tone.variant" :class="baselineBadge.tone.class">
                            {{ baselineBadge.label }}
                        </Badge>
                    </span>
                    <span>
                        {{ t('changes.metrics.resources') }}
                        <b class="text-foreground font-semibold">{{ resourceCount }}</b>
                    </span>
                </div>

                <!-- 查询失败且无旧快照：错误态可重试（仅 scan ready 会有查询） -->
                <Card v-if="tableState === 'error'">
                    <CardContent class="flex items-center justify-between gap-3 py-4">
                        <span class="text-destructive text-sm">{{ t('changes.errorTitle') }}：{{ errorMsg }}</span>
                        <Button variant="outline" size="sm" :disabled="inflight" @click="reload">
                            {{ t('changes.retry') }}
                        </Button>
                    </CardContent>
                </Card>

                <!-- 扫描失败且无 ready 数据时只给横幅 + 指标条，不出筛选/表格（§9） -->
                <template v-else-if="scanReady">
                    <!-- 工具栏：筛选胶囊 + 类型下拉（Select）+ 实时搜索 -->
                    <div class="flex flex-wrap items-center gap-2">
                        <div class="flex flex-wrap gap-1.5">
                            <button
                                v-for="c in CHIPS"
                                :key="c.id"
                                type="button"
                                :class="[chipBaseClass, chip === c.id ? chipOnClass : chipOffClass]"
                                @click="chip = c.id"
                            >
                                {{ t(c.key) }}
                            </button>
                        </div>
                        <div class="min-w-2 flex-1"></div>
                        <div class="relative">
                            <Search
                                class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2"
                                aria-hidden="true"
                            />
                            <Input v-model="search" class="h-8 w-52 pl-8" :placeholder="t('changes.searchPlaceholder')" />
                        </div>
                        <Select v-model="kind">
                            <SelectTrigger size="sm" class="w-44">
                                <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                                <SelectItem value="all">{{ t('changes.filter.kindAll') }}</SelectItem>
                                <SelectItem v-for="k in kindOptions" :key="k" :value="k">
                                    {{ t('changes.kind.' + k) }}
                                </SelectItem>
                            </SelectContent>
                        </Select>
                    </div>

                    <!-- 固定提示行：筛选只影响表格展示 -->
                    <p class="text-faint text-[11.5px]">{{ t('changes.filterNote') }}</p>

                    <Card>
                        <CardContent class="py-2">
                            <!-- 首查 loading：骨架行（保留表头同构） -->
                            <template v-if="tableState === 'loading'">
                                <Table>
                                    <TableHeader>
                                        <TableRow>
                                            <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                                        </TableRow>
                                    </TableHeader>
                                    <TableBody>
                                        <TableRow v-for="i in 5" :key="i">
                                            <TableCell v-for="c in cols" :key="c">
                                                <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                            </TableCell>
                                        </TableRow>
                                    </TableBody>
                                </Table>
                            </template>

                            <!-- 空态（无筛选）/ 筛选空态（原型：无匹配资源 + 清除筛选） -->
                            <div
                                v-else-if="tableState === 'empty' || tableState === 'filteredEmpty'"
                                class="flex flex-col items-center gap-3 py-10"
                            >
                                <template v-if="tableState === 'filteredEmpty'">
                                    <Search class="text-faint size-8" aria-hidden="true" />
                                    <h3 class="text-[15px] font-semibold">{{ t('changes.filteredEmptyTitle') }}</h3>
                                    <p class="text-muted-foreground text-xs">{{ t('changes.filteredEmpty') }}</p>
                                    <Button variant="secondary" size="sm" @click="clearFilters">
                                        {{ t('changes.resetFilters') }}
                                    </Button>
                                </template>
                                <p v-else class="text-muted-foreground text-sm">{{ t('changes.empty') }}</p>
                            </div>

                            <template v-else>
                                <!-- 三方资源表 6 列（判断列纯文字着色，票 #102 verdictClass） -->
                                <Table>
                                    <TableHeader>
                                        <TableRow>
                                            <TableHead v-for="c in cols" :key="c" :class="c === 'changes.colEnd' ? 'text-right' : ''">
                                                {{ t(c) }}
                                            </TableHead>
                                        </TableRow>
                                    </TableHeader>
                                    <TableBody>
                                        <TableRow v-for="row in displayRows" :key="row.resource_id">
                                            <TableCell>
                                                <div
                                                    class="max-w-[320px] truncate font-mono text-xs font-semibold"
                                                    :title="row.relative_path || row.resource_id"
                                                >
                                                    {{ row.relative_path || row.resource_id }}
                                                </div>
                                                <div class="text-faint mt-0.5 text-[11.5px]">
                                                    {{ t('changes.kind.' + row.resource_kind) }}
                                                </div>
                                            </TableCell>
                                            <TableCell>
                                                <template v-if="row.project">
                                                    <div>{{ row.project.format }}</div>
                                                    <div class="text-muted-foreground text-xs">
                                                        {{ humanSize(row.project.content?.size) }}
                                                    </div>
                                                </template>
                                                <span v-else class="text-muted-foreground">—</span>
                                            </TableCell>
                                            <TableCell>
                                                <template v-if="row.base">
                                                    <div>{{ row.base.format }}</div>
                                                    <div class="text-muted-foreground text-xs">
                                                        {{ humanSize(row.base.content?.size) }}
                                                    </div>
                                                </template>
                                                <span v-else class="text-muted-foreground">{{ t('changes.baselineMissing') }}</span>
                                            </TableCell>
                                            <TableCell>
                                                <template v-if="row.runtime">
                                                    <div>{{ row.runtime.format }}</div>
                                                    <div class="text-muted-foreground text-xs">
                                                        {{ humanSize(row.runtime.content?.size) }}
                                                    </div>
                                                </template>
                                                <span v-else class="text-muted-foreground">—</span>
                                            </TableCell>
                                            <TableCell>
                                                <span
                                                    class="text-xs font-semibold"
                                                    :class="verdictClass(verdictKeyOf(row.classification))"
                                                >
                                                    {{ t('changes.class.' + row.classification) }}
                                                </span>
                                            </TableCell>
                                            <!-- 行末：诊断类资源标「诊断」（证据入口不画，拍板 Q9-b） -->
                                            <TableCell class="text-right whitespace-nowrap">
                                                <Badge
                                                    v-if="row.diagnostics?.length"
                                                    variant="st-mut"
                                                    plain
                                                    :title="diagTitle(row)"
                                                >
                                                    {{ t('changes.diagBadge') }}
                                                </Badge>
                                            </TableCell>
                                        </TableRow>
                                    </TableBody>
                                </Table>

                                <!-- 表下计数行：随筛选联动 + 加载更多 -->
                                <div class="flex items-center justify-between gap-2 py-2">
                                    <span class="text-muted-foreground flex items-center gap-2 text-xs">
                                        {{ t('changes.matchOf', [displayRows.length, summary?.total ?? 0]) }}
                                        <Badge v-if="inflight" variant="st-run" plain>{{ t('changes.refreshing') }}</Badge>
                                    </span>
                                    <Button v-if="nextCursor" variant="outline" size="sm" :disabled="inflight" @click="loadMore">
                                        {{ t('changes.loadMore') }}
                                    </Button>
                                </div>
                            </template>
                        </CardContent>
                    </Card>
                </template>
            </template>
        </template>
    </div>
</template>
