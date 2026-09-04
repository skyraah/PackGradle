<script setup lang="ts">
// /workspaces/:id/history：同步历史（契约 05 §3.5 ListCommits；UX 原型 P3 H-01，
// 票 #110）。头部走共享工作区对象头 WorkspaceObjectHead，页签扩为
// 「变化 | 受管范围 | 历史」三常驻页签（拍板 Q8-a），本页历史恒活动。
// 表为 5 列（记录、类型、完整性、时间、操作，H-01 骨架）：head 行（列表首条，
// created_at DESC）操作列显禁选徽章「当前状态」，其余行「查看详情」；partial 行
// 剩余差异并入完整性徽章。墓碑行点名保留策略（契约 06 §3.8）。
// 历史数据为本页查询快照（游标分页），工作区上下文（关系名/features.history_view
// 门控）读 stores/syncCache 投影，页面不做第二处取数。
// 状态机互斥：loading / error / gate / empty / ready / refreshing（沿 changes 页先例）。
// 失败执行不进入历史（进任务/恢复），故空态即「尚无已提交的同步」。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Clock } from '@lucide/vue'
import { Browser, Clipboard } from '@wailsio/runtime'
import { SyncService } from '../api'
import type { CommitSummaryDTO, DiagnosticDTO, WorkspaceDTO } from '../api'
import { bootstrapped, bootstrapError, retryBootstrap, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar, taskDrawerOpen } from '../stores/ui'
import { errText } from '../utils/errors'
import {
    DIFF_TONES,
    HEALTH_TONES,
    NEUTRAL,
    PAGE_LIMIT,
    completenessTone,
    formatTime,
    latestScanText,
    resolvePageState,
    toneOf,
    type BadgeTone,
    type QueryPhase,
} from '../utils/pageState'
import { availabilityReasonText, canPrepareSync, canQuickUpdate, prepareSync } from '../utils/plans'
import { useWorkspaceHeadTabs } from '../composables/useWorkspaceHeadTabs'
import WorkspaceObjectHead from '../components/common/WorkspaceObjectHead.vue'
import type { HeadBadge, HeadMenuItem } from '../components/common/WorkspaceObjectHead.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))
const rebindRoute = computed(() => '/workspaces/' + relationID.value + '/rebind')

// —— 查询快照（旧数据在刷新失败时保留）——
const items = ref<CommitSummaryDTO[]>([])
const nextCursor = ref('')
// 墓碑计数（契约 06 §3.8）：按保留策略已清理的更早提交数；N=0 不渲染墓碑行。
const prunedBeforeCount = ref(0)
// 查询生命周期：phase 是互斥主状态；inflight 只在已有快照时投影为 refreshing
const phase = ref<QueryPhase>('loading')
const inflight = ref(false)
const errorMsg = ref('')

// —— 工作区上下文（读 syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)

// feature 门控（契约 03 §2.1：feature=false 前端不渲染）：history_view 未点亮时
// 页面不渲染内容（入口不渲染由列表行承接）
const gated = computed(() => wsRow.value !== undefined && wsRow.value?.features.history_view !== true)

const pageState = computed(() => resolvePageState(phase.value, gated.value, items.value.length > 0))
// 重查失败保留旧快照时的提示（成功后清空；契约 04 受控重查的失败语义）
const refreshing = computed(() => inflight.value && items.value.length > 0)
const refreshFailed = computed(() => phase.value === 'ready' && errorMsg.value !== '')

let querySeq = 0

async function queryPage(cursor: string): Promise<void> {
    const seq = ++querySeq
    inflight.value = true
    try {
        const page = await SyncService.ListCommits(relationID.value, cursor, PAGE_LIMIT)
        if (seq !== querySeq) return
        items.value = cursor ? [...items.value, ...(page.items ?? [])] : (page.items ?? [])
        nextCursor.value = page.next_cursor ?? ''
        prunedBeforeCount.value = page.pruned_before_count ?? 0
        phase.value = 'ready'
        errorMsg.value = ''
    } catch (e) {
        if (seq !== querySeq) return
        if (phase.value === 'ready') {
            // 已有查询快照：保留旧数据，仅提示（契约 04 受控重查的失败语义）
            errorMsg.value = errText(e)
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

// 路由切换工作区 → 全量重查
watch(relationID, reload, { immediate: true })

// —— head 判定（H-01 首行禁选）：列表 created_at DESC，首条即 head（提交不可变，
// head 恒在保留窗口内）；head 禁选＝UI 防误触，后端无特判路径 ——
const headCommitID = computed(() => (pageState.value === 'ready' ? (items.value[0]?.commit_id ?? '') : ''))

// —— 展示辅助（色调/时间/相位状态机收敛于 utils/pageState）——
const kindTones: Record<string, BadgeTone> = {
    initialize: NEUTRAL,
    sync: NEUTRAL,
    restore: NEUTRAL,
}

// 完整性徽章文案：partial 带剩余数（H-01：partial · 剩余 N）
function completenessLabel(row: CommitSummaryDTO): string {
    return row.completeness === 'partial'
        ? t('history.completenessRemaining', [row.remaining_change_count])
        : t('history.completeness.' + row.completeness)
}

const cols = ['history.colCommit', 'history.colKind', 'history.colCompleteness', 'history.colTime']

// —— 对象头视图模型（副行徽章 / 适配器 / 最近扫描，与变化页同口径）——
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

// 页签「变化 | 受管范围 | 历史」三常驻（拍板 Q8-a）：跨页导航，活动态各页自持（本页历史恒活动）
const tabs = useWorkspaceHeadTabs(() => relationID.value, 'history')

// —— 主操作（§7.1 行操作优先级，与变化页同一唯一主操作链）——
const preparing = ref(false)
const canPrepareSyncNow = computed(() => canPrepareSync(wsRow.value))
const canQuickUpdateNow = computed(() => canQuickUpdate(wsRow.value))
const quickUpdateReason = computed(() => availabilityReasonText(wsRow.value, 'quick_update'))

const scanRunning = computed(
    () => wsRow.value?.state.scan_state === 'scanning' || wsRow.value?.state.scan_state === 'queued',
)

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

// —— 「更多」菜单（规范五项，票 #105 拍板；行为与变化页一致）——
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

async function openEndpoint(): Promise<void> {
    const path = wsRow.value?.relation.project.root_path
    if (!path) return
    try {
        await Browser.OpenURL(path)
    } catch (e) {
        showSnackbar(t('changes.openEndpoint.failed') + '：' + errText(e), 'error')
    }
}

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

        <!-- 工作区不存在：syncCache 引导完成后仍找不到该关系 -->
        <Card v-else-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('history.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('history.backToList') }}
                </Button>
            </CardContent>
        </Card>

        <template v-else>
            <!-- 对象头（共享组件，三常驻页签；票 #110 Q8-a） -->
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
                active-tab="history"
                @primary="onPrimary"
                @menu="onMenu"
            />
            <h1 v-else class="page-title">{{ t('history.title') }}</h1>

            <!-- 重查中 / 重查失败（保留旧快照）提示 -->
            <div v-if="refreshing || refreshFailed" class="flex flex-wrap items-center gap-2">
                <Badge v-if="refreshing" variant="st-run" plain>{{ t('history.refreshing') }}</Badge>
                <span v-if="refreshFailed" class="text-destructive text-xs">{{ t('history.refreshFailed') }}：{{ errorMsg }}</span>
                <Button v-if="refreshFailed" variant="outline" size="xs" :disabled="inflight" @click="reload">
                    {{ t('history.retry') }}
                </Button>
            </div>

            <!-- 首查 loading：骨架行（保留表头同构） -->
            <Card v-if="pageState === 'loading'">
                <CardContent class="py-2">
                    <Table>
                        <TableHeader>
                            <TableRow>
                                <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                                <TableHead class="text-right">{{ t('history.colActions') }}</TableHead>
                            </TableRow>
                        </TableHeader>
                        <TableBody>
                            <TableRow v-for="i in 3" :key="i">
                                <TableCell v-for="c in cols" :key="c">
                                    <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                </TableCell>
                                <TableCell>
                                    <div class="ml-auto h-4 w-16 animate-pulse rounded bg-muted"></div>
                                </TableCell>
                            </TableRow>
                        </TableBody>
                    </Table>
                </CardContent>
            </Card>

            <!-- 首查/重查失败且无旧快照：错误态可重试 -->
            <Card v-else-if="pageState === 'error'">
                <CardContent class="flex items-center justify-between gap-3 py-4">
                    <span class="text-destructive text-sm">{{ t('history.errorTitle') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="reload">{{ t('history.retry') }}</Button>
                </CardContent>
            </Card>

            <!-- feature 门控：history_view 未点亮（契约 03 §2.1） -->
            <Card v-else-if="pageState === 'gate'">
                <CardContent class="text-muted-foreground py-8 text-center text-sm">
                    {{ t('history.featureOff') }}
                </CardContent>
            </Card>

            <Card v-else>
                <CardContent class="py-2">
                    <!-- 空态：尚无已提交的同步（失败执行不进入历史，进任务/恢复） -->
                    <div v-if="pageState === 'empty'" class="text-muted-foreground py-10 text-center text-sm">
                        {{ t('history.empty') }}
                    </div>

                    <template v-else>
                        <!-- 5 列表（H-01）：记录、类型、完整性、时间、操作 -->
                        <Table>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                                    <TableHead class="text-right">{{ t('history.colActions') }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="row in items" :key="row.commit_id">
                                    <TableCell class="max-w-56 truncate font-mono text-xs font-semibold" :title="row.commit_id">
                                        {{ row.commit_id }}
                                    </TableCell>
                                    <TableCell>
                                        <Badge :variant="(kindTones[row.kind] ?? NEUTRAL).variant" plain>
                                            {{ t('history.kind.' + row.kind) }}
                                        </Badge>
                                    </TableCell>
                                    <TableCell>
                                        <Badge
                                            :variant="completenessTone(row.completeness).variant"
                                            :title="row.completeness === 'partial' ? t('history.remainingCount', [row.remaining_change_count]) : undefined"
                                        >
                                            {{ completenessLabel(row) }}
                                        </Badge>
                                    </TableCell>
                                    <TableCell class="text-muted-foreground text-sm">{{ formatTime(row.created_at) }}</TableCell>
                                    <TableCell class="text-right whitespace-nowrap">
                                        <!-- head 行禁选（H-01：当前状态即为该提交的结果，回滚到当前＝空操作） -->
                                        <Badge v-if="row.commit_id === headCommitID" variant="st-mut" :title="t('restore.headBannerTitle') + ' · ' + t('restore.headBannerHint')">
                                            {{ t('history.headBadge') }}
                                        </Badge>
                                        <Button
                                            v-else
                                            variant="ghost"
                                            size="xs"
                                            @click="router.push('/workspaces/' + relationID + '/history/' + row.commit_id)"
                                        >
                                            {{ t('history.openDetail') }}
                                        </Button>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>

                        <!-- 墓碑行（H-01，契约 06 §3.8）：被裁提交行自然消失，列表尾点名
                             保留策略可调；N=0 不渲染 -->
                        <div
                            v-if="prunedBeforeCount > 0"
                            class="text-muted-foreground mt-1 flex flex-wrap items-center gap-1.5 border-t px-2 py-2.5 text-xs"
                        >
                            <Clock class="text-faint size-3.5 flex-none" aria-hidden="true" />
                            <span>{{ t('history.prunedTombstone', [prunedBeforeCount]) }}</span>
                            <button class="text-primary hover:underline" @click="router.push('/settings')">
                                {{ t('history.prunedTombstoneLink') }}
                            </button>
                            <span>{{ t('history.prunedTombstoneTail') }}</span>
                        </div>
                    </template>

                    <!-- 页脚：已展示计数 + 加载更多 -->
                    <div v-if="pageState === 'ready'" class="flex items-center justify-between gap-2 py-2">
                        <span class="text-muted-foreground text-xs">{{ t('history.shownOf', [items.length]) }}</span>
                        <Button v-if="nextCursor" variant="outline" size="sm" :disabled="inflight" @click="loadMore">
                            {{ t('history.loadMore') }}
                        </Button>
                    </div>
                </CardContent>
            </Card>
        </template>
    </div>
</template>
