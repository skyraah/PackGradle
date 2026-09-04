<script setup lang="ts">
// /workspaces/:id/plans/restore/:plan_id：回滚计划页（契约 06 §9 结构 B 定稿，票 #61；
// UX 原型 H-04）。与 P2 plans 页同构：计划数据为本页查询快照（GetRestorePlan 读投影），
// 工作区上下文读 stores/syncCache 投影，页面不做第二处取数。
// 信息结构＝单表全列（资源/判定/CF 可用性/处理说明四列 + 顶部计数条）；
// 流转＝draft 只读预览 → 决策（exact/allow_partial + skip）→ resolved 确认。
// 判定面不复制后端决策：exact 解锁读 exact_feasible 投影、四标记/marker_reason/
// staged/skipped/双警示全读 DTO 行投影，前端只做渲染与流转（可用性同样只用
// prepare_restore availability，前端不得自行推断）。
// 补全（StageUserObject）：行内「提供文件」对话框三态 busy/ready/miss；draft/resolved
// 均可补全，字节绑计划暂存不进 CAS（ADR-0005 §7），staged 即入 exact 就绪面。
// 确认框四要素（删除损失面 / CF 重取失败=整场退出 / 永远人工确认 / 新记录不改写历史）
// 逐条可见；确认建 kind=restore 任务移交任务中心（可离开页面，committed 后历史
// 新增 kind=restore 记录，历史详情页 P2 投影复用）。
// stale/expired 不白屏：内容继续可读，主操作收敛为「重新准备回滚计划」（对同一
// 目标提交重新 PrepareRestore，router.replace 到新计划）。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { RestorePlanDTO, RestorePlanItemDTO } from '../api'
import { bootstrapped, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { pickFile } from '../utils/dialogs'
import { errorCode, errText } from '../utils/errors'
import { availabilityReasonText, canPrepareRestore } from '../utils/plans'
import {
    BAD,
    INFO,
    NEUTRAL,
    OK,
    PLAN_TONES,
    WARN,
    formatTime,
    toneOf,
    type BadgeTone,
} from '../utils/pageState'
import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))
const planID = computed(() => String(route.params.plan_id ?? ''))

// —— 计划查询快照 ——
const plan = ref<RestorePlanDTO | null>(null)
const phase = ref<'loading' | 'error' | 'ready'>('loading')
const inflight = ref(false)
const errorMsg = ref('')
let querySeq = 0

async function loadPlan(): Promise<void> {
    const seq = ++querySeq
    inflight.value = true
    try {
        const p = await SyncService.GetRestorePlan(planID.value)
        if (seq !== querySeq) return
        plan.value = p
        phase.value = 'ready'
        errorMsg.value = ''
    } catch (e) {
        if (seq !== querySeq) return
        plan.value = null
        phase.value = 'error'
        errorMsg.value = errText(e)
    } finally {
        if (seq === querySeq) inflight.value = false
    }
}

watch([relationID, planID], () => void loadPlan(), { immediate: true })

// —— 工作区上下文（syncCache 投影）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && phase.value === 'ready' && plan.value !== null && !wsRow.value)
const activeTask = computed(() => {
    const id = wsRow.value?.state.active_task_id
    return id ? (tasks.value.get(id) ?? null) : null
})

// —— 状态投影（status 沿 sync_plans CHECK 读取时投影）——
const isStale = computed(() => plan.value?.status === 'stale')
const isExpired = computed(() => plan.value?.status === 'expired')
const isApplied = computed(() => plan.value?.status === 'applied')
const isConfirmed = computed(() => plan.value?.status === 'confirmed')
// 停用态：内容继续可读，决策/补全/确认控件全部收敛为「重新准备」引导（不白屏）
const frozen = computed(() => isStale.value || isExpired.value)
const canDecide = computed(() => plan.value?.status === 'draft' && !frozen.value)
const canConfirm = computed(() => plan.value?.status === 'resolved' && !frozen.value)
// 补全在 draft/resolved 均可进行（契约 06 §3.5），停用/已确认/已完成收敛
const canProvide = computed(() => !frozen.value && !isApplied.value && !isConfirmed.value)
// 确认建任务即回滚执行面：restore_apply feature 门控（契约 06 §1 能力位）
const confirmFeatureOn = computed(() => wsRow.value?.features.restore_apply === true)

const items = computed(() => plan.value?.items ?? [])

// —— 顶部计数条（结构 B；RestorePlanDTO 无 summary，行投影聚合属纯渲染）——
const counts = computed(() => {
    const rows = items.value
    return {
        total: rows.length,
        cas: rows.filter(r => r.marker === 'restorable_from_cas').length,
        dl: rows.filter(r => r.marker === 'redownload_required').length,
        user: rows.filter(r => r.marker === 'user_object_required').length,
        userReady: rows.filter(r => r.marker === 'user_object_required' && r.staged).length,
        unrec: rows.filter(r => r.marker === 'unrecoverable').length,
        del: rows.filter(r => r.change_kind === 'delete').length,
        warn: rows.filter(r => r.deletion_warn === true || r.preserve_skip === true).length,
    }
})

// —— 判定徽标色调（四标记 + delete 行；色调常量与计划状态映射收敛于
// utils/pageState，票 #102）——
function markerTone(row: RestorePlanItemDTO): BadgeTone {
    switch (row.marker) {
        case 'restorable_from_cas':
            return OK
        case 'redownload_required':
            return INFO
        case 'user_object_required':
            return WARN
        case 'unrecoverable':
            return BAD
        default:
            return NEUTRAL // delete 行 marker 为空串（契约 06 §3.2）
    }
}
function markerLabel(row: RestorePlanItemDTO): string {
    return row.marker ? t('restore.marker.' + row.marker) : t('restore.marker.delete')
}
function changeLabel(kind: string): string {
    return t('restore.change.' + kind)
}
// marker_reason 文案（仅 user_object_required 行；缺键渲染键名本身便于发现遗漏）
function reasonText(row: RestorePlanItemDTO): string {
    return row.marker_reason ? t('restore.markerReason.' + row.marker_reason) : ''
}

// —— 决策草稿（draft；导航离开即弃，不迁移旧草稿）——
const exactness = ref<'exact' | 'allow_partial'>('exact')
// checkbox v-model 用数组承载（Vue 3 checkbox 群组绑定不支持 Set）
const skips = ref<string[]>([])

watch(planID, () => {
    exactness.value = 'exact'
    skips.value = []
    provideTarget.value = null
    confirmOpen.value = false
    ack.value = false
})

// 计划就绪时初始化确切度缺省：exact_feasible 为 DTO 投影（实时就绪面），不自行判定
watch(plan, p => {
    if (p?.status === 'draft' && p.exact_feasible === false) exactness.value = 'allow_partial'
})

// skip 仅对未补全的 user_object_required 与 unrecoverable 行合法（契约 06 §3.3）；
// 可选清单是行投影的渲染，合法性仍由后端裁决（err.restore.skip_invalid 落 snackbar）
function skippable(row: RestorePlanItemDTO): boolean {
    return (
        !row.skipped &&
        !frozen.value &&
        (row.marker === 'unrecoverable' || (row.marker === 'user_object_required' && !row.staged))
    )
}

const resolving = ref(false)
async function submitResolve(): Promise<void> {
    if (!plan.value || resolving.value) return
    resolving.value = true
    try {
        const next = await SyncService.ResolveRestorePlan({
            plan_id: plan.value.plan_id,
            requested_exactness: exactness.value,
            skip_resource_ids: skips.value,
        })
        showSnackbar(t('restore.resolveSuccess'), 'success')
        // 固化于同一计划（status→resolved），原地换新不导航；补一轮受控重查对齐投影
        plan.value = next
        triggerRequery()
    } catch (e) {
        // err.restore.exact_infeasible / err.restore.skip_invalid 均为后端引导文案
        showSnackbar(errText(e), 'error')
    } finally {
        resolving.value = false
    }
}

// —— exact 解锁横幅（读 DTO 投影）：exact_feasible=true ⇒ 绿；否则列阻塞行 ——
const blockerRows = computed(() =>
    items.value.filter(
        r => r.marker === 'unrecoverable' || (r.marker === 'user_object_required' && !r.staged),
    ),
)
const exactUnlocked = computed(() => plan.value?.exact_feasible === true)

// —— 用户对象补全（行内「提供文件」对话框三态 busy/ready/miss，契约 06 §3.5）——
// ready 的持久事实源是重载计划行的 staged 投影；busy/miss 是对话框本地相位，
// 关闭对话框后 miss 留存行内提示，重开即可重选重试
const rowStates = ref<Record<string, 'busy' | 'miss'>>({})
function stageState(resourceID: string): 'busy' | 'miss' | undefined {
    return rowStates.value[resourceID]
}
const provideTarget = ref<RestorePlanItemDTO | null>(null)
const provideOpen = computed({
    get: () => provideTarget.value !== null,
    set: (v: boolean) => {
        if (!v) provideTarget.value = null
    },
})
const providePhase = ref<'idle' | 'busy' | 'ready' | 'miss'>('idle')
const providePath = ref('')

function openProvide(row: RestorePlanItemDTO): void {
    provideTarget.value = row
    providePath.value = ''
    providePhase.value = 'idle'
}

async function browseProvide(): Promise<void> {
    const picked = await pickFile(t('restore.provide.dialogTitle'))
    if (picked) providePath.value = picked
}

async function submitProvide(): Promise<void> {
    if (!plan.value || !provideTarget.value || !providePath.value || providePhase.value === 'busy')
        return
    const resourceID = provideTarget.value.resource_id
    rowStates.value = { ...rowStates.value, [resourceID]: 'busy' }
    providePhase.value = 'busy'
    try {
        const next = await SyncService.StageUserObject({
            plan_id: plan.value.plan_id,
            resource_id: resourceID,
            source_path: providePath.value,
        })
        // 成功：重载投影该行 staged=true，exact 就绪面随 DTO 刷新（横幅转绿）
        plan.value = next
        providePhase.value = 'ready'
        const rest = { ...rowStates.value }
        delete rest[resourceID]
        rowStates.value = rest
        triggerRequery()
    } catch (e) {
        if (errorCode(e) === 'err.userobject.hash_mismatch') {
            // miss：内容与目标摘要不符，留在对话框内重选重试（绝不出错字节入库）
            rowStates.value = { ...rowStates.value, [resourceID]: 'miss' }
            providePhase.value = 'miss'
        } else {
            showSnackbar(errText(e), 'error')
            providePhase.value = 'idle'
        }
    }
}

// —— 确认（resolved → kind=restore 任务；四要素知情确认，契约 06 §9）——
const confirmOpen = ref(false)
const ack = ref(false)
const confirming = ref(false)

// 四要素①删除损失面：N 项将删除 + 不可找回/不留存警示行计数
const deleteCount = computed(() => counts.value.del)
const lossWarnCount = computed(() => counts.value.warn)
const skippedCount = computed(() => items.value.filter(r => r.skipped).length)
// partial 提示的未恢复项 = 跳过 + 未补全 user 行 + 不可恢复（ADR-0006 §9 剩余口径的行投影）
const partialRemain = computed(() => {
    const rows = items.value
    return rows.filter(
        r =>
            r.skipped ||
            r.marker === 'unrecoverable' ||
            (r.marker === 'user_object_required' && !r.staged),
    ).length
})

async function confirmRestore(): Promise<void> {
    if (!plan.value || confirming.value || !ack.value) return
    confirming.value = true
    try {
        await SyncService.ConfirmRestorePlan({ plan_id: plan.value.plan_id })
        showSnackbar(t('restore.confirmSuccess'), 'success')
        triggerRequery()
        // 任务移交任务中心（可离开页面）：committed 后历史新增 kind=restore 记录
        await router.push('/workspaces/' + relationID.value + '/history')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        confirming.value = false
    }
}

// 确认要求渲染：restore_acknowledge 走 req.* 键，其余沿 P2 plans.confirm.* 键
function requirementLabel(code: string): string {
    return code === 'restore_acknowledge' ? t('req.restore_acknowledge') : t('plans.confirm.' + code)
}

// —— stale/expired 重新准备（重 prepare 引导，不白屏）：对同一目标提交重新
// PrepareRestore，成功 router.replace 到新计划；availability 唯一门控 ——
const reprepareReady = computed(() => canPrepareRestore(wsRow.value))
const reprepareReason = computed(() => availabilityReasonText(wsRow.value, 'prepare_restore'))
const repreparing = ref(false)
async function reprepare(): Promise<void> {
    if (!plan.value || repreparing.value || !reprepareReady.value) return
    repreparing.value = true
    try {
        const next = await SyncService.PrepareRestore({
            relation_id: relationID.value,
            commit_id: plan.value.target_commit_id,
        })
        showSnackbar(t('restore.prepared'), 'success')
        await router.replace('/workspaces/' + relationID.value + '/plans/restore/' + next.plan_id)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        repreparing.value = false
    }
}

// 活跃任务收敛（restore committed / 收口）→ 重查一次计划快照：confirmed 后读取
// 投影 status=applied，主操作区随之收敛；relation_invalidated（restore committed
// 新发射点，契约 06 §7）经既有受控重查管线刷新工作区投影，零新管线。
watch(
    () => wsRow.value?.state.active_task_id ?? '',
    (now, prev) => {
        if (prev && !now) void loadPlan()
    },
)

const cols = ['restore.colResource', 'restore.colMarker', 'restore.colAvailability', 'restore.colAction']
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部：标题 + 状态 + 回滚目标 + 有效期 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="flex items-center gap-2 text-xl font-semibold">
                    {{ t('restore.title') }}
                    <Badge v-if="plan" :variant="toneOf(PLAN_TONES, plan.status).variant">
                        {{ t('restore.status.' + plan.status) }}
                    </Badge>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }} ·
                    </template>
                    <template v-if="plan">
                        {{ t('restore.target') }}
                        <button
                            class="text-primary font-mono text-xs hover:underline"
                            @click="router.push('/workspaces/' + relationID + '/history/' + plan.target_commit_id)"
                        >
                            {{ plan.target_commit_id }}
                        </button>
                        · {{ t('restore.expiresAt') }} {{ formatTime(plan.expires_at) }}
                        <template v-if="activeTask"> · {{ t(activeTask.message_key, activeTask.message_args ?? []) }}</template>
                    </template>
                </p>
            </div>
            <div class="flex shrink-0 gap-2">
                <Button variant="ghost" size="sm" @click="router.push('/workspaces/' + relationID + '/history')">
                    {{ t('restore.backToHistory') }}
                </Button>
                <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                    {{ t('restore.backToList') }}
                </Button>
            </div>
        </div>

        <!-- 工作区不存在 -->
        <Card v-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('restore.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">{{ t('restore.backToList') }}</Button>
            </CardContent>
        </Card>

        <template v-else>
            <!-- 首查 loading -->
            <Card v-if="phase === 'loading'">
                <CardContent class="py-2">
                    <div v-for="i in 5" :key="i" class="mb-2 h-4 w-full animate-pulse rounded bg-muted"></div>
                </CardContent>
            </Card>

            <!-- 查询失败：错误态可重试（不白屏） -->
            <Card v-else-if="phase === 'error'">
                <CardContent class="flex items-center justify-between gap-3 py-4">
                    <span class="text-destructive text-sm">{{ t('restore.errorTitle') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="loadPlan">{{ t('restore.retry') }}</Button>
                </CardContent>
            </Card>

            <template v-else-if="plan">
                <!-- stale/expired：内容继续可读，主操作收敛为重新准备引导（不白屏） -->
                <Card v-if="frozen">
                    <CardContent class="flex flex-wrap items-center justify-between gap-3 py-4">
                        <span class="text-sm">
                            <span class="text-amber-600 dark:text-amber-400">{{ t('restore.frozen.' + plan.status) }}</span>
                            <span class="text-muted-foreground"> — {{ t('restore.frozen.' + plan.status + 'Hint') }}</span>
                        </span>
                        <div class="flex flex-wrap gap-2">
                            <Button v-if="reprepareReady" size="sm" :disabled="repreparing" @click="reprepare">
                                {{ t('restore.reprepare') }}
                            </Button>
                            <span v-else class="self-center text-sm text-amber-600 dark:text-amber-400">
                                {{ t('restore.reprepareUnavailable') }}<template v-if="reprepareReason">：{{ reprepareReason }}</template>
                            </span>
                            <Button variant="outline" size="sm" @click="router.push('/workspaces/' + relationID + '/history')">
                                {{ t('restore.backToHistory') }}
                            </Button>
                        </div>
                    </CardContent>
                </Card>

                <!-- applied：回滚已完成（committed 后读取投影），历史新增记录不改写 -->
                <Card v-else-if="isApplied">
                    <CardContent class="flex flex-wrap items-center justify-between gap-3 py-4">
                        <span class="text-sm">
                            <span class="text-emerald-600 dark:text-emerald-400">{{ t('restore.appliedTitle') }}</span>
                            <span class="text-muted-foreground"> — {{ t('restore.appliedHint') }}</span>
                        </span>
                        <Button variant="outline" size="sm" @click="router.push('/workspaces/' + relationID + '/history')">
                            {{ t('restore.appliedToHistory') }}
                        </Button>
                    </CardContent>
                </Card>

                <!-- confirmed：任务执行中（可离开页面，任务中心追踪；完成后经重查收敛为已完成） -->
                <Card v-else-if="isConfirmed">
                    <CardContent class="flex flex-wrap items-center justify-between gap-3 py-4">
                        <span class="text-sm">
                            <span class="text-primary font-medium">{{ t('restore.confirmedTitle') }}</span>
                            <span class="text-muted-foreground"> — {{ t('restore.confirmedHint') }}</span>
                            <template v-if="activeTask"> · {{ t(activeTask.message_key, activeTask.message_args ?? []) }}</template>
                        </span>
                        <Button variant="outline" size="sm" @click="router.push('/workspaces/' + relationID + '/history')">
                            {{ t('restore.backToHistory') }}
                        </Button>
                    </CardContent>
                </Card>

                <!-- 顶部计数条（结构 B） -->
                <div class="flex flex-wrap items-center gap-2">
                    <Badge variant="outline" class="text-muted-foreground">{{ t('restore.count.total') }} {{ counts.total }}</Badge>
                    <Badge variant="outline" class="text-emerald-600 dark:text-emerald-400">{{ t('restore.count.cas') }} {{ counts.cas }}</Badge>
                    <Badge variant="outline" class="text-blue-600 dark:text-blue-400">{{ t('restore.count.dl') }} {{ counts.dl }}</Badge>
                    <Badge variant="outline" class="text-amber-600 dark:text-amber-400">
                        {{ t('restore.count.user') }} {{ counts.user }}（{{ t('restore.count.userReady') }} {{ counts.userReady }}）
                    </Badge>
                    <Badge v-if="counts.unrec > 0" variant="destructive">{{ t('restore.count.unrec') }} {{ counts.unrec }}</Badge>
                    <Badge variant="outline" class="text-muted-foreground">
                        {{ t('restore.count.del') }} {{ counts.del }}<template v-if="counts.warn > 0">（{{ t('restore.count.warn') }} {{ counts.warn }}）</template>
                    </Badge>
                </div>

                <!-- exact 解锁横幅（读 exact_feasible 投影）：全部就绪且无阻塞 ⇒ 绿。
                     仅 draft 决策相位渲染——resolved 后决议已固化，由决议摘要卡承接，
                     横幅不再暗示可改选 exact -->
                <Card v-if="canDecide">
                    <CardContent
                        class="flex flex-wrap items-center gap-2 py-3 text-sm"
                        :class="exactUnlocked ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'"
                    >
                        <span class="font-medium">
                            {{ exactUnlocked ? t('restore.bannerExactReady') : t('restore.bannerBlocked', [blockerRows.length]) }}
                        </span>
                        <span class="text-muted-foreground">
                            {{ exactUnlocked ? t('restore.bannerExactReadyHint') : t('restore.bannerBlockedHint') }}
                        </span>
                    </CardContent>
                </Card>

                <!-- 结构 B 单表全列：资源/判定/CF 可用性/处理说明 -->
                <Card>
                    <CardContent class="py-2">
                        <div v-if="items.length === 0" class="text-muted-foreground py-10 text-center text-sm">
                            {{ t('restore.itemsEmpty') }}
                        </div>
                        <Table v-else>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="row in items" :key="row.resource_id">
                                    <!-- 资源：路径 + 变更类别 -->
                                    <TableCell>
                                        <div class="max-w-80 truncate font-mono text-sm font-medium" :title="row.relative_path">
                                            {{ row.relative_path }}
                                        </div>
                                        <div class="text-muted-foreground text-xs">
                                            {{ changeLabel(row.change_kind) }}
                                            <span v-if="row.skipped" class="text-amber-600 dark:text-amber-400"> · {{ t('restore.skippedLabel') }}</span>
                                        </div>
                                    </TableCell>
                                    <!-- 判定：四标记徽标 + 降级/无重取原因 -->
                                    <TableCell>
                                        <Badge :variant="markerTone(row).variant" :class="markerTone(row).class">{{ markerLabel(row) }}</Badge>
                                        <div v-if="reasonText(row)" class="text-muted-foreground mt-1 max-w-56 text-xs">{{ reasonText(row) }}</div>
                                    </TableCell>
                                    <!-- CF 可用性：仅 redownload 行（ok|unknown；unavailable 是降标非行内态） -->
                                    <TableCell>
                                        <template v-if="row.marker === 'redownload_required'">
                                            <div v-if="row.availability === 'ok'" class="flex items-center gap-1.5 text-sm">
                                                <span class="h-2 w-2 rounded-full bg-emerald-500"></span>
                                                {{ t('restore.availOk') }}
                                                <Badge v-if="row.newer_available" variant="st-warn" plain :title="t('restore.availNewerTip')">
                                                    {{ t('restore.availNewer') }}
                                                </Badge>
                                            </div>
                                            <div v-else class="text-muted-foreground flex items-center gap-1.5 text-sm">
                                                <span class="bg-muted-foreground/40 h-2 w-2 rounded-full"></span>
                                                {{ t('restore.availUnknown') }}
                                            </div>
                                        </template>
                                        <span v-else class="text-muted-foreground">—</span>
                                    </TableCell>
                                    <!-- 处理说明：按行投影渲染（含双警示与补全三态） -->
                                    <TableCell>
                                        <div class="flex max-w-96 flex-col gap-1 text-sm">
                                            <template v-if="row.deletion_warn">
                                                <span class="text-amber-600 dark:text-amber-400">⚠ {{ t('restore.warnDeletion') }}</span>
                                            </template>
                                            <template v-if="row.preserve_skip">
                                                <span class="text-amber-600 dark:text-amber-400">⚠ {{ t('restore.warnPreserve') }}</span>
                                            </template>
                                            <template v-if="row.marker === 'restorable_from_cas'">
                                                <span class="text-muted-foreground">{{ t('restore.noteCas') }}</span>
                                            </template>
                                            <template v-else-if="row.marker === 'redownload_required'">
                                                <span class="text-muted-foreground">{{ t('restore.noteDl') }}</span>
                                            </template>
                                            <template v-else-if="row.marker === 'unrecoverable'">
                                                <span class="text-muted-foreground">{{ t('restore.noteUnrec') }}</span>
                                            </template>
                                            <template v-else-if="row.marker === 'user_object_required'">
                                                <!-- 补全三态：ready（staged 投影）/ busy / miss（重选重试） -->
                                                <span v-if="row.staged" class="text-emerald-600 dark:text-emerald-400">✓ {{ t('restore.provide.ready') }}</span>
                                                <span v-else-if="stageState(row.resource_id) === 'busy'" class="text-muted-foreground">{{ t('restore.provide.busy') }}</span>
                                                <div v-else class="flex flex-wrap items-center gap-2">
                                                    <Button
                                                        v-if="canProvide"
                                                        variant="outline"
                                                        size="xs"
                                                        @click="openProvide(row)"
                                                    >
                                                        {{ t('restore.provide.action') }}
                                                    </Button>
                                                    <span v-if="stageState(row.resource_id) === 'miss'" class="text-destructive text-xs">
                                                        {{ t('restore.provide.mismatchShort') }}
                                                    </span>
                                                </div>
                                            </template>
                                            <template v-else-if="row.change_kind === 'delete'">
                                                <span class="text-muted-foreground">{{ t('restore.noteDelete') }}</span>
                                            </template>
                                            <template v-if="row.expected_digest">
                                                <span class="text-muted-foreground truncate font-mono text-xs" :title="row.expected_digest">
                                                    {{ t('restore.expectedDigest') }} {{ row.expected_digest }}
                                                </span>
                                            </template>
                                        </div>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>
                    </CardContent>
                </Card>

                <!-- 决策区（仅 draft；计划内容本身只读预览） -->
                <Card v-if="canDecide && items.length > 0">
                    <CardContent class="flex flex-col gap-3 py-4">
                        <div class="font-medium">{{ t('restore.decideTitle') }}</div>
                        <div class="flex flex-wrap items-center gap-4">
                            <label
                                class="flex cursor-pointer items-center gap-1.5 text-sm"
                                :class="exactUnlocked ? '' : 'text-muted-foreground cursor-not-allowed'"
                                :title="exactUnlocked ? undefined : t('restore.exactBlockedTip')"
                            >
                                <input
                                    v-model="exactness"
                                    type="radio"
                                    name="restore-exactness"
                                    value="exact"
                                    :disabled="!exactUnlocked"
                                    class="accent-current"
                                />
                                {{ t('restore.exactness.exact') }}
                            </label>
                            <label class="flex cursor-pointer items-center gap-1.5 text-sm">
                                <input v-model="exactness" type="radio" name="restore-exactness" value="allow_partial" class="accent-current" />
                                {{ t('restore.exactness.allow_partial') }}
                            </label>
                        </div>
                        <p class="text-muted-foreground text-xs">
                            {{ exactUnlocked ? t('restore.decideHintExactOk') : t('restore.decideHintBlocked') }}
                        </p>
                        <!-- skip 决议清单（仅可跳过行；合法性由后端裁决） -->
                        <template v-if="items.some(r => skippable(r))">
                            <div class="text-muted-foreground text-xs">{{ t('restore.skipTitle') }}</div>
                            <div class="flex flex-col gap-1.5">
                                <label
                                    v-for="row in items.filter(r => skippable(r))"
                                    :key="row.resource_id"
                                    class="flex cursor-pointer items-center gap-2 text-sm"
                                >
                                    <input v-model="skips" type="checkbox" :value="row.resource_id" class="accent-current" />
                                    <span class="max-w-80 truncate font-mono text-xs" :title="row.relative_path">{{ row.relative_path }}</span>
                                    <Badge :variant="markerTone(row).variant" :class="markerTone(row).class">{{ markerLabel(row) }}</Badge>
                                </label>
                            </div>
                        </template>
                        <div class="flex items-center justify-end gap-2">
                            <Button size="sm" :disabled="resolving" @click="submitResolve">{{ t('restore.resolveAction') }}</Button>
                        </div>
                    </CardContent>
                </Card>

                <!-- resolved：决议摘要（既成事实只读）+ 确认主操作 -->
                <template v-if="canConfirm && items.length > 0">
                    <Card>
                        <CardContent class="flex flex-wrap items-center justify-between gap-3 py-4">
                            <span class="text-sm">
                                <span class="font-medium">{{ t('restore.resolvedTitle') }}</span>
                                <span class="text-muted-foreground">
                                    — {{ t('restore.resolvedExactness') }}
                                    {{ t('restore.exactness.' + (plan.requested_exactness ?? 'allow_partial')) }}
                                    <template v-if="skippedCount > 0"> · {{ t('restore.resolvedSkipped', [skippedCount]) }}</template>
                                </span>
                            </span>
                            <Button v-if="confirmFeatureOn" size="sm" @click="confirmOpen = true">
                                {{ t('restore.confirmAction') }}
                            </Button>
                        </CardContent>
                    </Card>
                </template>
            </template>
        </template>

        <!-- 提供文件对话框（三态：idle 输入 / busy 校验中 / ready 已校验 / miss 重选重试） -->
        <AlertDialog v-model:open="provideOpen">
            <AlertDialogContent>
                <AlertDialogHeader>
                    <AlertDialogTitle>
                        {{ t('restore.provide.dialogTitle') }}
                        <span v-if="provideTarget" class="text-muted-foreground font-mono text-sm">
                            {{ provideTarget.relative_path.split('/').pop() }}
                        </span>
                    </AlertDialogTitle>
                    <AlertDialogDescription v-if="provideTarget">
                        {{ t('restore.provide.dialogHint', [provideTarget.expected_digest ?? '']) }}
                    </AlertDialogDescription>
                </AlertDialogHeader>

                <div class="flex flex-col gap-3">
                    <!-- busy：校验中 -->
                    <template v-if="providePhase === 'busy'">
                        <div class="text-muted-foreground flex items-center gap-2 text-sm">
                            <span class="border-primary border-t-primary h-4 w-4 animate-spin rounded-full border-2"></span>
                            {{ t('restore.provide.busy') }}
                        </div>
                    </template>
                    <!-- ready：已校验 · 字节就绪（计划行 staged 投影已翻转） -->
                    <template v-else-if="providePhase === 'ready'">
                        <div class="flex flex-col gap-1">
                            <span class="text-emerald-600 text-sm dark:text-emerald-400">✓ {{ t('restore.provide.ready') }}</span>
                            <span class="text-muted-foreground text-xs">{{ t('restore.provide.readyHint') }}</span>
                        </div>
                    </template>
                    <!-- idle / miss：本地路径 + 浏览（miss 先显错误，重选后重试） -->
                    <template v-else>
                        <div v-if="providePhase === 'miss'" class="flex flex-col gap-1">
                            <span class="text-destructive text-sm">
                                {{ t('err.userobject.hash_mismatch', [provideTarget?.expected_digest ?? '']) }}
                            </span>
                            <span class="text-muted-foreground text-xs">{{ t('restore.provide.missHint') }}</span>
                        </div>
                        <div class="flex items-center gap-2">
                            <input
                                v-model="providePath"
                                class="border-input bg-background h-9 w-full rounded-md border px-3 font-mono text-xs"
                                :placeholder="t('restore.provide.pathPlaceholder')"
                            />
                            <Button variant="outline" size="sm" class="shrink-0" @click="browseProvide">
                                {{ t('restore.provide.browse') }}
                            </Button>
                        </div>
                    </template>
                </div>

                <AlertDialogFooter>
                    <AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel>
                    <!-- 关闭（ready 态收尾）与提交按钮：不用 AlertDialogAction，避免自动关闭打断三态 -->
                    <Button v-if="providePhase === 'ready'" size="sm" @click="provideOpen = false">
                        {{ t('restore.provide.done') }}
                    </Button>
                    <Button v-else size="sm" :disabled="!providePath" @click="submitProvide">
                        {{ providePhase === 'miss' ? t('restore.provide.retry') : t('restore.provide.submit') }}
                    </Button>
                </AlertDialogFooter>
            </AlertDialogContent>
        </AlertDialog>

        <!-- 确认回滚对话框（四要素逐条可见 + 知情勾选，契约 06 §9） -->
        <AlertDialog v-model:open="confirmOpen">
            <AlertDialogContent class="sm:max-w-xl">
                <AlertDialogHeader>
                    <AlertDialogTitle>{{ t('restore.confirmTitle') }}</AlertDialogTitle>
                    <AlertDialogDescription>
                        {{ t('restore.confirmTargetLine', [plan?.target_commit_id ?? '']) }}
                    </AlertDialogDescription>
                </AlertDialogHeader>

                <div class="flex flex-col gap-2 text-sm">
                    <!-- ① 删除损失面 -->
                    <div class="rounded-md border p-2">
                        <div class="text-amber-600 dark:text-amber-400">
                            ① {{ deleteCount > 0 ? t('restore.confirm.deleteLoss', [deleteCount, lossWarnCount]) : t('restore.confirm.noDelete') }}
                        </div>
                    </div>
                    <!-- ② CF 重取失败语义 -->
                    <div class="rounded-md border p-2">
                        <div>② {{ t('restore.confirm.cfFailure') }}</div>
                    </div>
                    <!-- ③ 永远人工确认 -->
                    <div class="rounded-md border p-2">
                        <div>③ {{ t('restore.confirm.manualOnly') }}</div>
                    </div>
                    <!-- ④ 新记录不改写历史 -->
                    <div class="rounded-md border p-2">
                        <div>④ {{ t('restore.confirm.newRecord') }}</div>
                    </div>
                    <!-- partial 后果提示（allow_partial 决议时） -->
                    <div v-if="(plan?.requested_exactness ?? '') === 'allow_partial'" class="text-muted-foreground text-xs">
                        {{ t('restore.confirm.partialNote', [partialRemain]) }}
                    </div>
                    <!-- 后端确认要求投影（恒非空，restore_acknowledge 恒在） -->
                    <div class="text-muted-foreground text-xs">{{ t('restore.confirm.reqTitle') }}</div>
                    <div class="flex flex-col gap-1">
                        <div
                            v-for="req in plan?.confirmation_requirements ?? []"
                            :key="req.code"
                            class="flex items-center justify-between gap-2"
                        >
                            <span class="text-xs">{{ requirementLabel(req.code) }}</span>
                            <Badge variant="outline" class="text-muted-foreground">{{ t('plans.resourceCount', [req.resource_count]) }}</Badge>
                        </div>
                    </div>
                    <!-- 知情勾选：确认按钮门 -->
                    <label class="mt-1 flex cursor-pointer items-start gap-2 text-sm">
                        <input v-model="ack" type="checkbox" class="accent-current" />
                        <span>{{ t('req.restore_acknowledge') }}</span>
                    </label>
                </div>

                <AlertDialogFooter>
                    <AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel>
                    <AlertDialogAction
                        :disabled="!ack || confirming"
                        :class="confirming ? 'pointer-events-none opacity-50' : ''"
                        @click="confirmRestore"
                    >
                        {{ t('restore.confirmSubmit') }}
                    </AlertDialogAction>
                </AlertDialogFooter>
            </AlertDialogContent>
        </AlertDialog>
    </div>
</template>
