<script setup lang="ts">
// /workspaces/:id/plans/restore/:plan_id：回滚计划页（契约 06 §9 结构 B 定稿，票 #61；
// UX 原型 P3 H-04，票 #110）。信息结构＝单表全列（资源/判定/CF 可用性/处理说明
// 四列 + 顶部 6 枚计数条）；页头＝「回滚计划」+ draft·只读徽章 + restore chip +「放弃」。
// 横幅状态机（draft 决策相位）：有阻塞项 → 警告横幅（N 项阻塞 + 行内 mono 文件名，
// 补文件或选 partial）；全部就绪 → 成功横幅。判定面不复制后端决策：exact 解锁读
// exact_feasible 投影、四标记/marker_reason/staged/skipped/双警示全读 DTO 行投影。
// 决策条＝exact/partial 两张单选卡（有阻塞项时 exact 置灰说明）+ 右侧「确认并回滚」；
// 确认链 = ResolveRestorePlan（draft 时，带逐资源 skip）→ ConfirmRestorePlan，成功建
// kind=restore 任务并跳任务中心追踪（可离开页面）。
// 补全（StageUserObject，ADR-0005 §7）：行内三态 busy（旋转）/ ready（绿 toast 带
// 就绪计数）/ miss（红 toast 带错误码 + 行内「重选文件」重试）；字节绑计划暂存不进
// CAS。CF 可用性「依据」弹窗接真数据：PackwizService.CheckUpdates / FetchModVersion
// （packwiz 更新清单按下载文件名匹配 → CF 最新版本信息），失败降级只显计划内探测。
// stale/expired 不白屏：内容继续可读，主操作收敛为「重新准备回滚计划」。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
    CircleAlert,
    CircleCheck,
    Database,
    Download,
    ShieldCheck,
    Trash2,
    TriangleAlert,
    Upload,
} from '@lucide/vue'
import type { Component } from 'vue'
import { PackwizService, SyncService } from '../api'
import type { ModInfo, ModUpdateInfo } from '../../bindings/packgradle/internal/packwiz/models'
import type { RestorePlanDTO, RestorePlanItemDTO } from '../api'
import { bootstrapped, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar, taskDrawerOpen } from '../stores/ui'
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
import RestoreConfirmDialog from '../components/common/RestoreConfirmDialog.vue'
import type { ConfirmRequirementVM } from '../components/common/DangerConfirmDialog.vue'
import {
    AlertDialog,
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
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog'
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

// —— 顶部计数条（结构 B 6 枚；RestorePlanDTO 无 summary，行投影聚合属纯渲染）——
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

// —— 判定五标记（图标 + 配色语义，H-04）：cas 绿/db、dl 蓝/下载、user 琥珀/提供、
// unrec 红/警示、del 灰/删除；色调常量收敛于 utils/pageState（票 #102）——
const MARKER_ICONS: Record<string, Component> = {
    restorable_from_cas: Database,
    redownload_required: Download,
    user_object_required: Upload,
    unrecoverable: CircleAlert,
    delete: Trash2,
}

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
function markerIcon(row: RestorePlanItemDTO): Component {
    return MARKER_ICONS[row.marker] ?? Trash2
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
    probeDone.value = false
    newerVersions.value = {}
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

// —— exact 阻塞清单（读 DTO 投影）：未补全 user 行 + 全部不可恢复行 ——
const blockerRows = computed(() =>
    items.value.filter(
        r => r.marker === 'unrecoverable' || (r.marker === 'user_object_required' && !r.staged),
    ),
)
const exactUnlocked = computed(() => plan.value?.exact_feasible === true)

// 横幅阻塞文件名（H-04：行内 mono 文件名，多取 3 个 + 「等 N 项」）
function baseName(p: string): string {
    return p.split(/[\\/]/).pop() ?? p
}
const blockerNames = computed(() => {
    const rows = blockerRows.value
    const heads = rows.slice(0, 3).map(r => baseName(r.relative_path))
    if (rows.length > 3) heads.push(t('restore.bannerMore', [rows.length - 3]))
    return heads.join('、')
})

// —— 用户对象补全（行内三态 busy/ready/miss，契约 06 §3.5）：对话框只承担选文件
// 与 miss 错误显示；提交即关对话框，行内转 busy 旋转，结果以 toast 收口 ——
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
const providePath = ref('')

function openProvide(row: RestorePlanItemDTO): void {
    provideTarget.value = row
    providePath.value = ''
}

async function browseProvide(): Promise<void> {
    const picked = await pickFile(t('restore.provide.dialogTitle'))
    if (picked) providePath.value = picked
}

async function submitProvide(): Promise<void> {
    if (!plan.value || !provideTarget.value || !providePath.value) return
    const resourceID = provideTarget.value.resource_id
    const expect = provideTarget.value.expected_digest ?? ''
    provideTarget.value = null // 关对话框；行内转 busy 旋转（原型 provCell busy）
    rowStates.value = { ...rowStates.value, [resourceID]: 'busy' }
    try {
        const next = await SyncService.StageUserObject({
            plan_id: plan.value.plan_id,
            resource_id: resourceID,
            source_path: providePath.value,
        })
        // 成功：重载投影该行 staged=true，exact 就绪面随 DTO 刷新（横幅转绿）；
        // 绿 toast 带就绪计数，全部就绪时再补一条解锁提示
        plan.value = next
        const rest = { ...rowStates.value }
        delete rest[resourceID]
        rowStates.value = rest
        triggerRequery()
        showSnackbar(t('restore.provide.readyToast', [counts.value.userReady, counts.value.user]), 'success')
        if (next.exact_feasible) showSnackbar(t('restore.provide.allReadyToast'), 'success')
    } catch (e) {
        if (errorCode(e) === 'err.userobject.hash_mismatch') {
            // miss：内容与目标摘要不符——红 toast 带错误码，行内留存「重选文件」重试
            //（绝不出错字节入库）
            rowStates.value = { ...rowStates.value, [resourceID]: 'miss' }
            showSnackbar('err.userobject.hash_mismatch：' + t('err.userobject.hash_mismatch', [expect]), 'error')
        } else {
            const rest = { ...rowStates.value }
            delete rest[resourceID]
            rowStates.value = rest
            showSnackbar(errText(e), 'error')
        }
    }
}

// —— CF 可用性实时核对（「有更新 x.y.z」chip 与「依据」弹窗共用，真数据：
// PackwizService.CheckUpdates 更新清单按下载文件名匹配 + FetchModVersion 取 CF
// 最新版本信息）。尽力探测、静默失败：核对不到时 chip 退化为「有更新」、弹窗
// 只显计划内探测，不阻塞决策面 ——
const newerVersions = ref<Record<string, string>>({})
const probeDone = ref(false)

function matchEntry(base: string, entries: ModUpdateInfo[]): ModUpdateInfo | undefined {
    return entries.find(u => baseName(u.current_file).toLowerCase() === base || baseName(u.latest_file).toLowerCase() === base)
}

async function probeNewer(): Promise<void> {
    const p = plan.value
    const projectName = wsRow.value?.relation.project.display_name
    if (!p || !projectName || probeDone.value) return
    probeDone.value = true
    const targets = (p.items ?? []).filter(
        r => r.marker === 'redownload_required' && r.availability === 'ok' && r.newer_available === true,
    )
    if (!targets.length) return
    try {
        const res = await PackwizService.CheckUpdates(projectName)
        const entries = [...(res.updates ?? []), ...(res.errors ?? [])]
        for (const row of targets) {
            const cand = matchEntry(baseName(row.relative_path).toLowerCase(), entries)
            if (!cand) continue
            try {
                const mod = await PackwizService.FetchModVersion(projectName, cand.name)
                const v = mod.cf_version || mod.version || baseName(cand.latest_file)
                if (v) newerVersions.value = { ...newerVersions.value, [row.resource_id]: v }
            } catch {
                // 单个 mod 版本获取失败不影响其余行
            }
        }
    } catch {
        // packwiz 清单不可用（非托管/项目名不匹配）：chip 保持「有更新」无版本
    }
}

watch(plan, p => {
    if (p) void probeNewer()
})

function newerVersion(row: RestorePlanItemDTO): string {
    return newerVersions.value[row.resource_id] ?? ''
}

// —— 「依据」探测依据弹窗（H-04 availCell）：计划内探测事实 + 实时更新核对 ——
const evidenceTarget = ref<RestorePlanItemDTO | null>(null)
const evidenceOpen = computed({
    get: () => evidenceTarget.value !== null,
    set: (v: boolean) => {
        if (!v) evidenceTarget.value = null
    },
})
const evidenceLoading = ref(false)
interface EvidenceResult {
    update?: ModUpdateInfo
    latestVersion?: string
    fileDate?: string
    error?: string
}
const evidenceResult = ref<EvidenceResult | null>(null)

async function openEvidence(row: RestorePlanItemDTO): Promise<void> {
    evidenceTarget.value = row
    evidenceResult.value = null
    const projectName = wsRow.value?.relation.project.display_name
    if (!projectName) {
        evidenceResult.value = {}
        return
    }
    evidenceLoading.value = true
    try {
        const res = await PackwizService.CheckUpdates(projectName)
        const cand = matchEntry(baseName(row.relative_path).toLowerCase(), [
            ...(res.updates ?? []),
            ...(res.errors ?? []),
        ])
        if (!cand) {
            evidenceResult.value = {}
            return
        }
        const result: EvidenceResult = { update: cand }
        try {
            const mod: ModInfo = await PackwizService.FetchModVersion(projectName, cand.name)
            result.latestVersion = mod.cf_version || mod.version
            result.fileDate = mod.cf_file_date
        } catch (e) {
            result.error = errText(e)
        }
        evidenceResult.value = result
    } catch (e) {
        evidenceResult.value = { error: errText(e) }
    } finally {
        evidenceLoading.value = false
    }
}

// —— 确认（四要素知情确认 → Resolve+Confirm 链 → kind=restore 任务，契约 06 §9）——
const confirmOpen = ref(false)
const confirming = ref(false)

// 确认框确切度：draft 跟随决策草稿；resolved 读固化决议
const confirmPartial = computed(() => {
    const p = plan.value
    if (!p) return true
    if (p.status === 'draft') return exactness.value === 'allow_partial'
    return (p.requested_exactness ?? 'allow_partial') === 'allow_partial'
})
const confirmTargetLine = computed(() => t('restore.confirmTargetLine', [plan.value?.target_commit_id ?? '']))
const requirementVMs = computed<ConfirmRequirementVM[]>(() =>
    (plan.value?.confirmation_requirements ?? []).map(r => ({
        label: r.code === 'restore_acknowledge' ? t('req.restore_acknowledge') : t('plans.confirm.' + r.code),
        count: r.resource_count,
    })),
)

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
    if (!plan.value || confirming.value) return
    confirming.value = true
    try {
        // 决策条单击直达确认：draft 先固化决议（exactness + 逐资源 skip；后端裁决
        // err.restore.exact_infeasible / skip_invalid），再确认建 kind=restore 任务
        if (plan.value.status === 'draft') {
            const resolved = await SyncService.ResolveRestorePlan({
                plan_id: plan.value.plan_id,
                requested_exactness: exactness.value,
                skip_resource_ids: skips.value,
            })
            plan.value = resolved
        }
        await SyncService.ConfirmRestorePlan({ plan_id: plan.value.plan_id })
        showSnackbar(t('restore.confirmSuccess'), 'success')
        triggerRequery()
        void loadPlan() // 反映 confirmed 投影
        // 任务移交任务中心（可离开页面，跳任务中心追踪）：committed 后历史新增
        // kind=restore 记录，历史不改写
        taskDrawerOpen.value = true
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        confirming.value = false
    }
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

// 单选卡形态（H-04 .exopt：radio 点 + 标签；选中主色边 + tint 底 + 内嵌环）
function decideCardClass(selected: boolean, disabled = false): string {
    if (disabled) return 'border-border text-muted-foreground opacity-50'
    return selected
        ? 'border-primary bg-tint-success text-foreground shadow-[inset_0_0_0_1px_var(--primary)]'
        : 'border-border text-muted-foreground hover:text-foreground'
}
function decideDotClass(selected: boolean): string {
    return selected
        ? 'border-primary bg-primary shadow-[inset_0_0_0_3px_var(--surface)]'
        : 'border-muted-foreground/60 bg-transparent'
}

function pickExactness(v: 'exact' | 'allow_partial'): void {
    if (v === 'exact' && !exactUnlocked.value) {
        showSnackbar(t('restore.exactBlockedTip'), 'warning')
        return
    }
    exactness.value = v
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部（H-04）：回滚计划 + draft·只读 + restore chip；右侧「放弃」 -->
        <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
                <h1 class="page-title flex flex-wrap items-center gap-2">
                    {{ t('restore.title') }}
                    <Badge v-if="plan" :variant="toneOf(PLAN_TONES, plan.status).variant">
                        {{ t('restore.status.' + plan.status) }}
                    </Badge>
                    <Badge variant="secondary" class="font-mono" plain>{{ t('restore.kindChip') }}</Badge>
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
                <Button variant="outline" size="sm" @click="router.push('/workspaces/' + relationID + '/history')">
                    {{ t('restore.abandon') }}
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

                <template v-else>
                    <!-- 顶部计数条（结构 B 6 枚） -->
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

                    <!-- 横幅状态机（draft 决策相位）：阻塞 → 警告（N 项 + 文件名）；
                         全部就绪 → 成功。resolved 后决议已固化，横幅不再暗示可改选 -->
                    <div
                        v-if="canDecide"
                        class="flex flex-wrap items-center gap-2.5 rounded-lg border px-3.5 py-2.5 text-[12.5px]"
                        :class="exactUnlocked ? 'border-tint-success bg-tint-success' : 'border-tint-warning bg-tint-warning'"
                    >
                        <CircleCheck v-if="exactUnlocked" class="text-success size-4 flex-none" aria-hidden="true" />
                        <TriangleAlert v-else class="text-warning size-4 flex-none" aria-hidden="true" />
                        <div class="min-w-0 flex-1">
                            <span class="font-bold">
                                {{ exactUnlocked ? t('restore.bannerExactReady') : t('restore.bannerBlocked', [blockerRows.length]) }}
                            </span>
                            <template v-if="!exactUnlocked && blockerNames">
                                <span> · <span class="font-mono">{{ blockerNames }}</span></span>
                            </template>
                            <span> {{ exactUnlocked ? t('restore.bannerExactReadyHint') : t('restore.bannerBlockedHint') }}</span>
                        </div>
                    </div>

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
                                        <!-- 判定：五标记（图标 + 配色语义）+ 降级/无重取原因 -->
                                        <TableCell>
                                            <Badge :variant="markerTone(row).variant">
                                                <component :is="markerIcon(row)" aria-hidden="true" />
                                                {{ markerLabel(row) }}
                                            </Badge>
                                            <div v-if="reasonText(row)" class="text-muted-foreground mt-1 max-w-56 text-xs">{{ reasonText(row) }}</div>
                                        </TableCell>
                                        <!-- CF 可用性：仅 redownload 行（ok|unknown；unavailable 是降标非行内态）；
                                             「依据」开探测依据弹窗（真数据核对） -->
                                        <TableCell>
                                            <template v-if="row.marker === 'redownload_required'">
                                                <div v-if="row.availability === 'ok'" class="flex flex-wrap items-center gap-1.5 text-sm">
                                                    <span class="h-2 w-2 flex-none rounded-full bg-emerald-500"></span>
                                                    {{ t('restore.availOk') }}
                                                    <Badge v-if="row.newer_available" variant="st-warn" plain :title="t('restore.availNewerTip')">
                                                        {{ newerVersion(row) ? t('restore.availNewer', [newerVersion(row)]) : t('restore.availNewerPlain') }}
                                                    </Badge>
                                                    <Button variant="ghost" size="xs" class="text-muted-foreground" @click="openEvidence(row)">
                                                        {{ t('restore.evidence.action') }}
                                                    </Button>
                                                </div>
                                                <div v-else class="text-muted-foreground flex flex-wrap items-center gap-1.5 text-sm">
                                                    <span class="h-2 w-2 flex-none rounded-full bg-muted-foreground/40"></span>
                                                    {{ t('restore.availUnknown') }}
                                                    <Button variant="ghost" size="xs" @click="openEvidence(row)">
                                                        {{ t('restore.evidence.action') }}
                                                    </Button>
                                                </div>
                                            </template>
                                            <span v-else class="text-muted-foreground">—</span>
                                        </TableCell>
                                        <!-- 处理 / 说明：按行投影渲染（含损失面双警示与补全三态） -->
                                        <TableCell>
                                            <div class="flex max-w-96 flex-col gap-1 text-sm">
                                                <template v-if="row.deletion_warn">
                                                    <span class="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400">
                                                        <TriangleAlert class="size-3.5 flex-none" aria-hidden="true" />
                                                        {{ t('restore.warnDeletion') }}
                                                    </span>
                                                </template>
                                                <template v-if="row.preserve_skip">
                                                    <span class="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400">
                                                        <TriangleAlert class="size-3.5 flex-none" aria-hidden="true" />
                                                        {{ t('restore.warnPreserve') }}
                                                    </span>
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
                                                    <!-- 补全三态：ready（staged 投影）/ busy（旋转）/ miss（重选重试） -->
                                                    <span v-if="row.staged" class="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                                                        <CircleCheck class="size-3.5 flex-none" aria-hidden="true" />
                                                        {{ t('restore.provide.ready') }}
                                                    </span>
                                                    <span v-else-if="stageState(row.resource_id) === 'busy'" class="text-muted-foreground inline-flex items-center gap-1.5">
                                                        <span class="border-primary border-t-primary h-3.5 w-3.5 animate-spin rounded-full border-2"></span>
                                                        {{ t('restore.provide.busy') }}
                                                    </span>
                                                    <div v-else class="flex flex-wrap items-center gap-2">
                                                        <Button
                                                            v-if="canProvide"
                                                            variant="outline"
                                                            size="xs"
                                                            @click="openProvide(row)"
                                                        >
                                                            <Upload class="size-3" aria-hidden="true" />
                                                            {{ t('restore.provide.action') }}
                                                        </Button>
                                                        <span v-if="stageState(row.resource_id) === 'miss'" class="text-destructive inline-flex items-center gap-2 text-xs">
                                                            {{ t('restore.provide.mismatchShort', [row.expected_digest ?? '']) }}
                                                            <Button variant="outline" size="xs" @click="openProvide(row)">
                                                                {{ t('restore.provide.retry') }}
                                                            </Button>
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

                    <!-- 决策条（仅 draft）：exact/partial 两张单选卡 + 右侧「确认并回滚」；
                         有阻塞项时 exact 置灰并说明 -->
                    <Card v-if="canDecide && items.length > 0">
                        <CardContent class="flex flex-col gap-3 py-4">
                            <div class="flex flex-wrap items-center gap-3">
                                <button
                                    type="button"
                                    class="flex cursor-pointer items-center gap-2.5 rounded-lg border px-3.5 py-2.5 text-left transition-colors"
                                    :class="decideCardClass(exactness === 'exact', !exactUnlocked)"
                                    :title="exactUnlocked ? undefined : t('restore.exactBlockedTip')"
                                    @click="pickExactness('exact')"
                                >
                                    <span class="size-4 flex-none rounded-full border-2" :class="decideDotClass(exactness === 'exact')"></span>
                                    <span>
                                        <span class="block text-[12.5px] font-semibold">{{ t('restore.exactness.exact') }}</span>
                                        <span class="text-muted-foreground block text-xs font-normal">{{ t('restore.decide.exactDesc') }}</span>
                                    </span>
                                </button>
                                <button
                                    type="button"
                                    class="flex cursor-pointer items-center gap-2.5 rounded-lg border px-3.5 py-2.5 text-left transition-colors"
                                    :class="decideCardClass(exactness === 'allow_partial')"
                                    @click="pickExactness('allow_partial')"
                                >
                                    <span class="size-4 flex-none rounded-full border-2" :class="decideDotClass(exactness === 'allow_partial')"></span>
                                    <span>
                                        <span class="block text-[12.5px] font-semibold">{{ t('restore.exactness.allow_partial') }}</span>
                                        <span class="text-muted-foreground block text-xs font-normal">{{ t('restore.decide.partialDesc') }}</span>
                                    </span>
                                </button>
                                <div class="min-w-4 grow"></div>
                                <Button v-if="confirmFeatureOn" size="sm" :disabled="confirming" @click="confirmOpen = true">
                                    <ShieldCheck class="size-3.5" aria-hidden="true" />
                                    {{ t('restore.confirmAction') }}
                                </Button>
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
                                        <Badge :variant="markerTone(row).variant">{{ markerLabel(row) }}</Badge>
                                    </label>
                                </div>
                            </template>
                        </CardContent>
                    </Card>

                    <!-- resolved：决议摘要（既成事实只读）+ 确认主操作 -->
                    <Card v-if="canConfirm && items.length > 0">
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
                                <ShieldCheck class="size-3.5" aria-hidden="true" />
                                {{ t('restore.confirmAction') }}
                            </Button>
                        </CardContent>
                    </Card>
                </template>
            </template>
        </template>

        <!-- 提供文件对话框（idle 选文件 / miss 显错误重选；busy 与结果以行内三态 +
             toast 收口，契约 06 §3.5） -->
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
                    <!-- miss：hash 不符错误先行（重选后重试） -->
                    <div v-if="provideTarget && stageState(provideTarget.resource_id) === 'miss'" class="flex flex-col gap-1">
                        <span class="text-destructive text-sm">
                            {{ t('err.userobject.hash_mismatch', [provideTarget.expected_digest ?? '']) }}
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
                </div>

                <AlertDialogFooter>
                    <AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel>
                    <Button size="sm" :disabled="!providePath" @click="submitProvide">
                        {{ t('restore.provide.submit') }}
                    </Button>
                </AlertDialogFooter>
            </AlertDialogContent>
        </AlertDialog>

        <!-- 探测依据弹窗（「依据」；真数据：packwiz 更新清单 + CF 版本信息） -->
        <Dialog v-model:open="evidenceOpen">
            <DialogContent class="sm:max-w-lg">
                <DialogHeader>
                    <DialogTitle>
                        {{ t('restore.evidence.title') }}
                        <span v-if="evidenceTarget" class="text-muted-foreground font-mono text-sm">
                            {{ evidenceTarget.relative_path.split('/').pop() }}
                        </span>
                    </DialogTitle>
                    <DialogDescription v-if="evidenceTarget" class="font-mono text-xs break-all">
                        {{ evidenceTarget.relative_path }}
                    </DialogDescription>
                </DialogHeader>

                <div class="grid grid-cols-[120px_1fr] gap-x-3 gap-y-2 text-[12.5px]">
                    <span class="text-muted-foreground">{{ t('restore.evidence.resource') }}</span>
                    <span class="truncate" :title="evidenceTarget?.relative_path">{{ baseName(evidenceTarget?.relative_path ?? '') }}</span>

                    <span class="text-muted-foreground">{{ t('restore.evidence.planProbe') }}</span>
                    <span>
                        <template v-if="evidenceTarget?.availability === 'ok'">{{ t('restore.evidence.probeOk') }}</template>
                        <template v-else>{{ t('restore.evidence.probeUnknown') }}</template>
                    </span>

                    <span class="text-muted-foreground">{{ t('restore.evidence.liveCheck') }}</span>
                    <span>
                        <template v-if="evidenceLoading">
                            <span class="text-muted-foreground inline-flex items-center gap-1.5">
                                <span class="border-primary border-t-primary h-3 w-3 animate-spin rounded-full border-2"></span>
                                {{ t('restore.evidence.liveChecking') }}
                            </span>
                        </template>
                        <template v-else-if="evidenceResult?.update">
                            <span class="text-amber-600 dark:text-amber-400">
                                {{ t('restore.evidence.liveUpdate', [evidenceResult.update.current_file, evidenceResult.update.latest_file]) }}
                            </span>
                            <span v-if="evidenceResult.latestVersion" class="text-muted-foreground block">
                                {{ t('restore.evidence.latestVersion') }}：{{ evidenceResult.latestVersion }}
                                <template v-if="evidenceResult.fileDate"> · {{ t('restore.evidence.fileDate') }} {{ evidenceResult.fileDate }}</template>
                            </span>
                            <span v-else-if="evidenceResult.error" class="text-destructive block text-xs">{{ evidenceResult.error }}</span>
                        </template>
                        <template v-else>
                            <span class="text-muted-foreground">{{ t('restore.evidence.liveCurrent') }}</span>
                            <span v-if="evidenceResult?.error" class="text-destructive block text-xs">{{ evidenceResult.error }}</span>
                        </template>
                    </span>

                    <span class="text-muted-foreground">{{ t('restore.evidence.failureSemantics') }}</span>
                    <span class="text-muted-foreground">{{ t('restore.evidence.failureText') }}</span>
                </div>

                <DialogFooter>
                    <Button variant="outline" size="sm" @click="evidenceOpen = false">{{ t('common.cancel') }}</Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>

        <!-- 确认回滚对话框（四要素逐条可见 + 知情勾选门；标题按确切度区分） -->
        <RestoreConfirmDialog
            v-model:open="confirmOpen"
            :partial="confirmPartial"
            :target-line="confirmTargetLine"
            :delete-count="deleteCount"
            :loss-warn-count="lossWarnCount"
            :partial-remain="partialRemain"
            :requirements="requirementVMs"
            :busy="confirming"
            @confirm="confirmRestore"
        />
    </div>
</template>
