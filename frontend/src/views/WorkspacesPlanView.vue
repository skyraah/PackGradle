<script setup lang="ts">
// /workspaces/:id/plans/:plan_id：只读计划、冲突解决与应用同步（契约 03 §2.6、
// 契约 05 §1/§5；UX 原型 §7.5 画板 P-01/P-02/P-05，票 #108）。
// 计划数据为本页查询快照（GetPlan 读投影），工作区上下文（关系名/availability）
// 读 stores/syncCache 投影，页面不做第二处取数。
// 画板对齐（票 #108）：自有页头——标题 + 状态徽章（draft · 只读 灰 / 已过期 琥珀）
// + 类型 chip（initialize/sync）+ 副行有效期 + 右侧「返回变化页」，停用态追加
// 「重新扫描并生成新计划」主操作；汇总计数条 6 枚（冲突>0 警告色、不可恢复>0
// 错误色）；三页签换真 Tabs（主色文字 + 底部 2px 下划线，与对象头页签同语言）；
// 操作表 6 列、待决议行整行警告着色底；冲突表 5 列（证据列只占位，Q9-b 后置）、
// 冲突决议控件换 RadioGroup；风险与前置条件为预检行面板（对勾/警告图标着色）。
// 硬约束（验收规格 UX §15.1）：计划内容不可编辑——冲突选择只用于 ResolvePlan 产生
// 全新不可变计划，旧计划保持只读；History/Restore 入口不在本页（Phase 2/3 各归其页）。
// 应用链路（T11 票 #43）：resolved 计划主操作「应用同步」由 apply_sync availability
// 唯一门控（契约 05 §1，不可用显后端原因码）；ConfirmPlan 成功即长任务移交任务中心
// （UX §7.9 可离开页面）；committed 后 GetPlan 投影 status=applied，主操作区收敛。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { CircleAlert, CircleCheck, Clock, ShieldCheck, TriangleAlert } from '@lucide/vue'
import { SyncService } from '../api'
import type { ConflictDTO, SyncPlanDTO } from '../api'
// OperationDTO 走 bindings 类型直读（门面未再出口；同 components/common/MergePreviewDrawer 先例）
import type { OperationDTO } from '../../bindings/packgradle/internal/transport/models'
import MergePreviewDrawer from '../components/common/MergePreviewDrawer.vue'
import { bootstrapped, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { PLAN_TONES, formatTime, toneOf } from '../utils/pageState'
import type { BadgeTone } from '../utils/pageState'
import {
    availabilityReasonText,
    canApplySync,
    canPrepareSync,
    canRescan,
    prepareSync,
} from '../utils/plans'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))
const planID = computed(() => String(route.params.plan_id ?? ''))

// —— 计划查询快照 ——
const plan = ref<SyncPlanDTO | null>(null)
const phase = ref<'loading' | 'error' | 'ready'>('loading')
const inflight = ref(false)
const errorMsg = ref('')
let querySeq = 0

async function loadPlan(): Promise<void> {
    const seq = ++querySeq
    inflight.value = true
    try {
        const p = await SyncService.GetPlan(planID.value)
        if (seq !== querySeq) return
        plan.value = p
        phase.value = 'ready'
        errorMsg.value = ''
    } catch (e) {
        if (seq !== querySeq) return
        phase.value = 'error'
        errorMsg.value = errText(e)
        plan.value = null
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

// —— 状态投影 ——
const isStale = computed(() => plan.value?.status === 'stale')
const isExpired = computed(() => plan.value?.status === 'expired')
// 停用态：内容继续可读，但冲突决议与重新生成等推进控件全部隐藏（UX 原型 §7.5 Stale/expired）
const frozen = computed(() => isStale.value || isExpired.value)
// 已应用（committed 后 GetPlan 读取时投影，契约 05 §5）：不可重入（ConfirmPlan 返回
// err.plan.apply_not_reentrant），主操作区收敛为重扫引导
const isApplied = computed(() => plan.value?.status === 'applied')
const retired = computed(() => frozen.value || isApplied.value)
// 冲突决议只在 draft 且有冲突时开放；resolved 计划的选择为既成事实只读展示
const canResolve = computed(
    () => plan.value?.status === 'draft' && (plan.value.conflicts?.length ?? 0) > 0,
)

// —— 页头视图模型（画板 P-01/P-05：标题 + 状态徽章 + 类型 chip + 副行）——
// draft 徽章文案按画板为「draft · 只读」（灰）；stale/expired 琥珀；其余沿 plans.status.*
const statusBadge = computed<{ label: string; tone: BadgeTone } | null>(() => {
    const p = plan.value
    if (!p) return null
    const label = p.status === 'draft' ? t('plans.statusDraftReadOnly') : t('plans.status.' + p.status)
    return { label, tone: toneOf(PLAN_TONES, p.status) }
})
const kindChip = computed(() => {
    const k = plan.value?.kind ?? ''
    return k === 'initialize' || k === 'sync' ? t('plans.kind.' + k) : k
})

// —— 摘要计数条（画板 P-02：6 枚；冲突>0 警告色、不可恢复>0 错误色）——
const unrecoverableCount = computed(
    () =>
        (plan.value?.confirmation_requirements ?? []).find(r => r.code === 'unrecoverable')
            ?.resource_count ?? 0,
)
interface SummaryChip {
    key: string
    count: number
    tone: 'mut' | 'warn' | 'err'
}
const summaryChips = computed<SummaryChip[]>(() => {
    const s = plan.value?.summary
    if (!s) return []
    return [
        { key: 'plans.summary.resourceTotal', count: s.resource_total, tone: 'mut' },
        { key: 'plans.summary.create', count: s.create_count, tone: 'mut' },
        { key: 'plans.summary.modify', count: s.modify_count, tone: 'mut' },
        { key: 'plans.summary.delete', count: s.delete_count, tone: 'mut' },
        { key: 'plans.summary.conflict', count: s.conflict_count, tone: s.conflict_count > 0 ? 'warn' : 'mut' },
        { key: 'plans.summary.unrecoverable', count: unrecoverableCount.value, tone: unrecoverableCount.value > 0 ? 'err' : 'mut' },
    ]
})
const summaryChipClass: Record<SummaryChip['tone'], string> = {
    mut: 'text-muted-foreground',
    warn: 'border-transparent bg-tint-warning text-warning',
    err: 'border-transparent bg-tint-error text-error',
}

// 双向计划分别计数（画板 P-02 note 行：写入 Runtime N 项 · 写入 Project N 项）
const writeRuntimeCount = computed(
    () => (plan.value?.operations ?? []).filter(op => op.kind === 'write_runtime').length,
)
const writeProjectCount = computed(
    () => (plan.value?.operations ?? []).filter(op => op.kind === 'write_project').length,
)
const bidirectional = computed(() => writeRuntimeCount.value > 0 && writeProjectCount.value > 0)

// —— 页签（真 Tabs，票 #108；下划线语言与共享对象头 WorkspaceObjectHead 一致）——
const tab = ref<'operations' | 'conflicts' | 'risks'>('operations')
const TAB_KEYS = ['operations', 'conflicts', 'risks'] as const
const planTabTriggerClass =
    'h-auto flex-none rounded-[6px_6px_0_0] border-none bg-transparent px-3.5 py-2 text-[12.5px] font-semibold text-muted-foreground shadow-none data-[state=active]:bg-transparent data-[state=active]:text-primary data-[state=active]:shadow-none dark:data-[state=active]:bg-transparent dark:data-[state=active]:border-transparent dark:data-[state=active]:text-primary relative after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full after:bg-primary data-[state=active]:after:content-[""]'

// —— 冲突决议草稿（按 plan_id 保存于本页内存；导航离开即弃，不迁移旧草稿）——
const choices = ref<Record<string, string>>({})

watch(planID, () => {
    choices.value = {}
    tab.value = 'operations'
})

// P1 choose_side：modify_modify/delete_modify 二选一，initialize_choice 从存在侧初始化
function choiceOptions(c: ConflictDTO): { value: string; labelKey: string; disabled?: boolean }[] {
    if (c.kind === 'initialize_choice') {
        return [
            { value: 'initialize_from_project', labelKey: 'plans.choice.initializeFromProject', disabled: !c.project },
            { value: 'initialize_from_runtime', labelKey: 'plans.choice.initializeFromRuntime', disabled: !c.runtime },
        ]
    }
    return [
        { value: 'take_project', labelKey: 'plans.choice.takeProject' },
        { value: 'take_runtime', labelKey: 'plans.choice.takeRuntime' },
    ]
}

// —— 操作表 6 列投影（画板 P-02：资源、方向、操作、目标表示、可恢复性、风险）——

// 方向：按操作 kind 推导同步方向（remove 行沿用写入方向——被删内容来自对侧）
const OP_DIRECTIONS: Record<string, string> = {
    write_runtime: 'plans.dir.p2r',
    materialize: 'plans.dir.p2r',
    remove_runtime: 'plans.dir.p2r',
    write_project: 'plans.dir.r2p',
    remove_project: 'plans.dir.r2p',
    write_merged: 'plans.dir.both',
}
function directionKey(kind: string): string {
    return OP_DIRECTIONS[kind] ?? ''
}

// 目标表示：后端操作投影不携带表示格式，取物化模式（copy|download，契约 06 §3.7）
function targetKey(op: OperationDTO): string {
    if (op.materialization === 'download') return 'plans.target.download'
    if (op.materialization === 'copy') return 'plans.target.copy'
    return ''
}

// 风险列：逐行可推导的风险标记（保 Judgement：不可恢复归「可恢复性」列）
function riskKey(op: OperationDTO): string {
    if (op.preserve_skip) return 'plans.risk.preserveSkip'
    if (op.kind === 'write_project') return 'plans.risk.gitTree'
    if (op.kind === 'remove_runtime' || op.kind === 'remove_project') return 'plans.risk.delete'
    return ''
}

// 合并行呈现（契约 07 §3.3/§6，票 #93）：write_merged 是后端固化的默认推荐
//（非冲突操作，随授权模式免确认），本页只读呈现「将自动合并」，无决议入口。
const isMergedOp = (kind: string): boolean => kind === 'write_merged'

// 待决议行（画板 P-02 op-conflict：整行警告着色底）：draft 且该资源冲突未选择
const conflictIDs = computed(() => new Set((plan.value?.conflicts ?? []).map(c => c.resource_id)))
function pendingDecision(resourceId: string): boolean {
    return canResolve.value && conflictIDs.value.has(resourceId) && !choices.value[resourceId]
}

// 冲突块明细（ADR-0009 §3；detail 为 hunk JSON 时点开块列表：三侧行片段 + 起始行号）。
interface HunkSide {
    start: number
    lines: string[]
}
interface Hunk {
    project: HunkSide
    base: HunkSide
    runtime: HunkSide
}
const hunkSides: { key: keyof Hunk; labelKey: string }[] = [
    { key: 'project', labelKey: 'plans.hunk.project' },
    { key: 'base', labelKey: 'plans.hunk.base' },
    { key: 'runtime', labelKey: 'plans.hunk.runtime' },
]
function parseHunks(detail: string | undefined): Hunk[] {
    if (!detail) return []
    try {
        const obj = JSON.parse(detail) as { hunks?: Hunk[] }
        return Array.isArray(obj.hunks) ? obj.hunks : []
    } catch {
        return []
    }
}

// —— 冲突表 5 列投影（画板 P-02：冲突资源、Project、Runtime、类型、证据）——
// 两侧状态词：delete_modify 缺失侧 = 已删除；initialize_choice 存在侧 = 待纳入。
// 证据列只占位不设入口（拍板 Q9-b：资源级三栏内容 API 未实现，与 #105 同口径后置）。
function sideLabel(present: boolean, kind: string): string {
    if (!present) return t('plans.sideDeleted')
    return kind === 'initialize_choice' ? t('plans.sidePending') : t('plans.sideModified')
}
function sideTitle(side: ConflictDTO['project']): string {
    return side?.content?.digest ?? ''
}

// resolved 计划的既定选择（只读回显）
const recordedChoices = computed(() => {
    const m = new Map<string, string>()
    for (const r of plan.value?.resolutions ?? []) m.set(r.resource_id, r.choice)
    return m
})

const unresolvedCount = computed(() => {
    const cs = plan.value?.conflicts ?? []
    return cs.filter(c => !choices.value[c.resource_id]).length
})

const resolving = ref(false)

async function submitResolutions(): Promise<void> {
    if (!plan.value || resolving.value) return
    resolving.value = true
    try {
        const resolutions = Object.entries(choices.value).map(([resource_id, choice]) => ({
            resource_id,
            choice,
        }))
        const next = await SyncService.ResolvePlan({ plan_id: plan.value.plan_id, resolutions })
        showSnackbar(t('plans.resolveSuccess'), 'success')
        // PrepareSync/ResolvePlan 不发事件：补一轮受控重查，新计划页才能拿到新鲜的
        // apply_sync availability（否则停留在 none_ready 直到下次事件/对账）
        triggerRequery()
        // 全新不可变计划：导航到新 plan_id（路由参数变化触发整页重查，草稿不迁移）
        await router.replace('/workspaces/' + relationID.value + '/plans/' + next.plan_id)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        resolving.value = false
    }
}

// 无冲突草稿自动推进（T13 B 口径走查发现修，票 #45）：纯 UI 流程中 0 冲突草稿
// 没有决议入口（冲突决议控件仅 draft 且有冲突时开放），「应用同步」永远不可达。
// 无冲突即无决议需要——自动提交空决议走既有 ResolvePlan 产生全新 resolved 计划
// （router.replace 导航复用）；用户仍需显式点「应用同步」，计划不可编辑语义不变。
watch(plan, p => {
    if (
        p?.status === 'draft' &&
        (p.conflicts?.length ?? 0) === 0 &&
        !frozen.value &&
        !resolving.value
    ) {
        void submitResolutions()
    }
})

// —— 停用态的推进动作（availability 唯一门控，逻辑收敛于 utils/plans）——
// stale/expired 主操作「重新扫描并生成新计划」（UX §7.5，票 #108 提升进页头）：
// 发起扫描后回列表页，扫描完成由列表行入口继续 PrepareSync；
// 「用当前快照重新生成」适用策略修改后快照仍新鲜的场景，保留在停用横幅内。
const regenerating = ref(false)
const rescanning = ref(false)
const showRescanPrimary = computed(() => frozen.value && !!wsRow.value && canRescan(wsRow.value))

async function regenerate(): Promise<void> {
    const ws = wsRow.value
    if (!ws || regenerating.value) return
    regenerating.value = true
    try {
        const next = await prepareSync(ws)
        triggerRequery()
        await router.replace('/workspaces/' + relationID.value + '/plans/' + next.plan_id)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        regenerating.value = false
    }
}

async function rescan(): Promise<void> {
    const ws = wsRow.value
    if (!ws || rescanning.value) return
    rescanning.value = true
    try {
        await SyncService.StartScan(relationID.value)
        triggerRequery()
        await router.push('/workspaces')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        rescanning.value = false
    }
}

// —— 应用同步（契约 05 §1/§5；UX 原型 §7.5 resolved plan 风险确认与 Apply）——
// 主操作由 apply_sync availability 唯一门控；不可用时主操作区显后端原因码文案
// （already_running/in_progress/incomplete/stale/expired/none_ready 各态均为后端推导）。
// ConfirmPlan 直接创建 apply 任务（契约 05 §3.1，token 幂等重入在后端）：成功即长任务
// 移交任务中心（UX §7.9 可离开页面），跳回工作区变化页继续追踪。
const canApply = computed(() => canApplySync(wsRow.value))
const applyReason = computed(() => availabilityReasonText(wsRow.value, 'apply_sync'))
const applying = ref(false)

async function applyPlan(): Promise<void> {
    if (!plan.value || applying.value) return
    applying.value = true
    try {
        await SyncService.ConfirmPlan({ plan_id: plan.value.plan_id })
        showSnackbar(t('plans.applySuccess'), 'success')
        triggerRequery()
        await router.push('/workspaces/' + relationID.value + '/changes')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        applying.value = false
    }
}

// 活跃任务收敛（apply committed / 恢复收口）→ 重查一次计划快照：committed 后
// GetPlan 投影 status=applied（契约 05 §5），主操作区随之收敛；relation_invalidated
// （committed/恢复收口发射点，契约 05 §4）经既有受控重查管线刷新工作区投影，零新管线。
watch(
    () => wsRow.value?.state.active_task_id ?? '',
    (now, prev) => {
        if (prev && !now) void loadPlan()
    },
)

// —— 合并预览抽屉（契约 07 §3.4/§6，票 #94）——
// merged_clean 行 = write_merged 操作（一资源一操作，契约 07 §3.3）；操作表行内
// 「查看合并结果」入口打开抽屉，实时预览不落库，停用/过期计划同样可看（只读）。
const mergePreviewOpen = ref(false)
const mergePreviewResourceId = ref('')

function openMergePreview(resourceId: string): void {
    mergePreviewResourceId.value = resourceId
    mergePreviewOpen.value = true
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 自有页头（画板 P-01/P-05）：标题 + 状态徽章 + 类型 chip + 副行；右侧
             返回变化页 + 停用态「重新扫描并生成新计划」主操作 -->
        <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
                <h1 class="page-title flex flex-wrap items-center gap-2">
                    {{ plan ? t('plans.title.' + plan.kind) : t('plans.title') }}
                    <Badge v-if="statusBadge" :variant="statusBadge.tone.variant" :class="statusBadge.tone.class">
                        {{ statusBadge.label }}
                    </Badge>
                    <Badge v-if="plan" variant="outline" class="text-muted-foreground">{{ kindChip }}</Badge>
                </h1>
                <p class="mt-1.5 flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
                    <template v-if="wsRow">
                        <span>
                            {{ wsRow.relation.project.display_name }}
                            <span class="mx-0.5">→</span>
                            {{ wsRow.relation.runtime.display_name }}
                        </span>
                        <span>·</span>
                    </template>
                    <template v-if="plan">
                        <span>{{ t('plans.expiresAt') }} {{ formatTime(plan.expires_at) }}</span>
                        <template v-if="plan.resolved_from_plan_id">
                            <span>·</span>
                            <span>{{ t('plans.resolvedFrom') }}</span>
                        </template>
                        <template v-if="activeTask">
                            <span>·</span>
                            <span>{{ t(activeTask.message_key, activeTask.message_args ?? []) }}</span>
                        </template>
                    </template>
                </p>
            </div>
            <div class="flex shrink-0 gap-2">
                <Button variant="outline" size="sm" @click="router.push('/workspaces/' + relationID + '/changes')">
                    {{ t('plans.backToChanges') }}
                </Button>
                <!-- 停用态主操作（画板 P-05）：重新扫描并生成新计划（availability 门控） -->
                <Button v-if="showRescanPrimary" size="sm" :disabled="rescanning" @click="rescan">
                    {{ t('plans.rescan') }}
                </Button>
            </div>
        </div>

        <!-- 工作区不存在 -->
        <Card v-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('plans.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">{{ t('plans.backToList') }}</Button>
            </CardContent>
        </Card>

        <template v-else>
            <!-- 首查 loading -->
            <Card v-if="phase === 'loading'">
                <CardContent class="py-2">
                    <div v-for="i in 5" :key="i" class="mb-2 h-4 w-full animate-pulse rounded bg-muted"></div>
                </CardContent>
            </Card>

            <!-- 查询失败：错误态可重试 -->
            <Card v-else-if="phase === 'error'">
                <CardContent class="flex items-center justify-between gap-3 py-4">
                    <span class="text-destructive text-sm">{{ t('plans.errorTitle') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="loadPlan">{{ t('plans.retry') }}</Button>
                </CardContent>
            </Card>

            <template v-else-if="plan">
                <!-- 停用/已应用横幅（画板 P-05）：内容继续可读，顶部说明具体原因。
                     已应用绿色 ok 横幅；停用琥珀 warn 横幅（重扫主操作已提升进页头）。
                     「用当前快照重新生成」与回列表保留在横幅内 -->
                <div
                    v-if="retired"
                    class="flex flex-wrap items-center gap-2.5 rounded-lg border px-3.5 py-2.5 text-[12.5px]"
                    :class="isApplied ? 'border-tint-success bg-tint-success' : 'border-tint-warning bg-tint-warning'"
                >
                    <component
                        :is="isApplied ? CircleCheck : Clock"
                        class="size-4 flex-none"
                        :class="isApplied ? 'text-success' : 'text-warning'"
                        aria-hidden="true"
                    />
                    <div class="min-w-0 flex-1">
                        <span class="font-bold" :class="isApplied ? 'text-success' : 'text-warning'">
                            {{ t(isApplied ? 'plans.appliedTitle' : 'plans.frozen.' + plan.status) }}
                        </span>
                        <span class="text-muted-foreground">
                            — {{ t(isApplied ? 'plans.appliedHint' : isStale ? 'plans.frozen.staleHint' : 'plans.frozen.expiredHint') }}
                        </span>
                    </div>
                    <div class="flex flex-wrap gap-2">
                        <Button v-if="canPrepareSync(wsRow)" variant="outline" size="sm" :disabled="regenerating" @click="regenerate">
                            {{ t('plans.regenerate') }}
                        </Button>
                        <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                            {{ t('plans.backToList') }}
                        </Button>
                    </div>
                </div>

                <!-- 汇总计数条（画板 P-02：6 枚；冲突>0 警告色、不可恢复>0 错误色） -->
                <div class="flex flex-wrap items-center gap-2">
                    <Badge v-for="chip in summaryChips" :key="chip.key" variant="outline" :class="summaryChipClass[chip.tone]">
                        {{ t(chip.key) }}
                        <b class="ml-1 font-semibold">{{ chip.count }}</b>
                    </Badge>
                </div>
                <p v-if="bidirectional" class="text-faint -mt-2 text-[11.5px]">
                    {{ t('plans.bidirectionalNote', [writeRuntimeCount, writeProjectCount]) }}
                </p>

                <!-- 三页签（真 Tabs，主色文字 + 底部 2px 下划线；冲突页签带计数） -->
                <Tabs v-model="tab" class="gap-2">
                    <TabsList class="h-auto w-full justify-start gap-0.5 rounded-none border-b bg-transparent p-0">
                        <TabsTrigger v-for="key in TAB_KEYS" :key="key" :value="key" :class="planTabTriggerClass">
                            {{ t('plans.tab.' + key) }}
                            <template v-if="key === 'conflicts' && (plan.conflicts?.length ?? 0) > 0">
                                · {{ plan.conflicts!.length }}
                            </template>
                        </TabsTrigger>
                    </TabsList>

                    <!-- 操作页签：只读操作表 6 列（计划内容不可编辑；待决议行整行警告着色底） -->
                    <TabsContent value="operations">
                        <Card>
                            <CardContent class="py-2">
                                <div v-if="!plan.operations?.length" class="text-muted-foreground py-8 text-center text-sm">
                                    {{ t('plans.operationsEmpty') }}
                                </div>
                                <Table v-else>
                                    <TableHeader>
                                        <TableRow>
                                            <TableHead>{{ t('plans.colResource') }}</TableHead>
                                            <TableHead>{{ t('plans.colDirection') }}</TableHead>
                                            <TableHead>{{ t('plans.colOperation') }}</TableHead>
                                            <TableHead>{{ t('plans.colTarget') }}</TableHead>
                                            <TableHead>{{ t('plans.colReversible') }}</TableHead>
                                            <TableHead>{{ t('plans.colRisk') }}</TableHead>
                                        </TableRow>
                                    </TableHeader>
                                    <TableBody>
                                        <TableRow
                                            v-for="op in plan.operations"
                                            :key="op.id"
                                            :class="pendingDecision(op.resource_id) ? 'bg-tint-warning hover:bg-tint-warning' : ''"
                                        >
                                            <TableCell class="max-w-72 truncate font-mono text-xs font-semibold" :title="op.resource_id">
                                                {{ op.resource_id }}
                                            </TableCell>
                                            <TableCell class="text-xs whitespace-nowrap">
                                                <template v-if="directionKey(op.kind)">{{ t(directionKey(op.kind)) }}</template>
                                                <span v-else class="text-muted-foreground">—</span>
                                            </TableCell>
                                            <TableCell>
                                                <div class="flex flex-wrap items-center gap-1">
                                                    <span>{{ t('plans.op.' + op.kind) }}</span>
                                                    <Badge v-if="isMergedOp(op.kind)" variant="st-ok" plain>
                                                        {{ t('plans.mergeBadge') }}
                                                    </Badge>
                                                </div>
                                                <!-- merged_clean 行「查看合并结果」（契约 07 §6，票 #94）：
                                                     预览抽屉 = 全文 + 行级绿红黄标注 + 语法高亮 -->
                                                <Button
                                                    v-if="op.kind === 'write_merged'"
                                                    variant="ghost"
                                                    size="xs"
                                                    class="mt-0.5 -ml-2"
                                                    @click="openMergePreview(op.resource_id)"
                                                >
                                                    {{ t('plans.mergePreview.open') }}
                                                </Button>
                                                <div v-if="isMergedOp(op.kind)" class="text-muted-foreground mt-0.5 text-xs">
                                                    {{ t('plans.mergeHint') }}
                                                </div>
                                            </TableCell>
                                            <TableCell class="text-xs whitespace-nowrap">
                                                <template v-if="targetKey(op)">{{ t(targetKey(op)) }}</template>
                                                <span v-else class="text-muted-foreground">—</span>
                                            </TableCell>
                                            <TableCell>
                                                <span :class="op.reversible ? 'text-muted-foreground' : 'text-amber-600 dark:text-amber-400'" class="text-xs">
                                                    {{ t(op.reversible ? 'plans.reversible.yes' : 'plans.reversible.no') }}
                                                </span>
                                            </TableCell>
                                            <TableCell class="text-xs whitespace-nowrap">
                                                <template v-if="riskKey(op)">{{ t(riskKey(op)) }}</template>
                                                <span v-else class="text-muted-foreground">—</span>
                                            </TableCell>
                                        </TableRow>
                                    </TableBody>
                                </Table>
                            </CardContent>
                        </Card>
                    </TabsContent>

                    <!-- 冲突页签：5 列冲突表（证据列占位，Q9-b 后置）+ RadioGroup 决议 -->
                    <TabsContent value="conflicts">
                        <Card v-if="!plan.conflicts?.length">
                            <CardContent class="text-muted-foreground py-8 text-center text-sm">
                                {{ t('plans.conflictsEmpty') }}
                            </CardContent>
                        </Card>

                        <template v-else>
                            <p v-if="canResolve" class="text-muted-foreground text-sm">{{ t('plans.resolveHint') }}</p>

                            <Card>
                                <CardContent class="py-2">
                                    <Table>
                                        <TableHeader>
                                            <TableRow>
                                                <TableHead>{{ t('plans.colConflictResource') }}</TableHead>
                                                <TableHead>{{ t('plans.colProjectSide') }}</TableHead>
                                                <TableHead>{{ t('plans.colRuntimeSide') }}</TableHead>
                                                <TableHead>{{ t('plans.colType') }}</TableHead>
                                                <TableHead class="text-right">{{ t('plans.colEvidence') }}</TableHead>
                                            </TableRow>
                                        </TableHeader>
                                        <TableBody>
                                            <TableRow v-for="c in plan.conflicts" :key="c.resource_id">
                                                <TableCell class="max-w-72 truncate font-mono text-xs font-semibold" :title="c.resource_id">
                                                    {{ c.resource_id }}
                                                </TableCell>
                                                <TableCell class="text-xs whitespace-nowrap" :title="sideTitle(c.project)">
                                                    <template v-if="c.project">{{ sideLabel(true, c.kind) }}</template>
                                                    <span v-else class="text-amber-600 dark:text-amber-400">{{ sideLabel(false, c.kind) }}</span>
                                                </TableCell>
                                                <TableCell class="text-xs whitespace-nowrap" :title="sideTitle(c.runtime)">
                                                    <template v-if="c.runtime">{{ sideLabel(true, c.kind) }}</template>
                                                    <span v-else class="text-amber-600 dark:text-amber-400">{{ sideLabel(false, c.kind) }}</span>
                                                </TableCell>
                                                <TableCell class="text-xs whitespace-nowrap">{{ t('changes.conflict.' + c.kind) }}</TableCell>
                                                <!-- 证据入口不画（Q9-b 后置，与 #105 同口径）：列占位 -->
                                                <TableCell class="text-right text-muted-foreground text-xs">—</TableCell>
                                            </TableRow>
                                        </TableBody>
                                    </Table>
                                </CardContent>
                            </Card>

                            <!-- 决议区：draft 用 RadioGroup 择侧（含块明细可点开）；
                                 resolved 只读回显既定选择 -->
                            <Card v-if="canResolve || plan.status === 'resolved'">
                                <CardContent class="flex flex-col divide-y py-1">
                                    <div v-for="c in plan.conflicts" :key="'d-' + c.resource_id" class="flex flex-col gap-2.5 py-3">
                                        <div class="flex flex-wrap items-center justify-between gap-2">
                                            <span class="max-w-md truncate font-mono text-xs font-semibold" :title="c.resource_id">
                                                {{ c.resource_id }}
                                            </span>
                                            <Badge variant="outline" class="text-muted-foreground">{{ t('changes.conflict.' + c.kind) }}</Badge>
                                        </div>

                                        <!-- 冲突块明细（ADR-0009 §3；点开块列表：project/base/runtime 行片段 + 起始行号） -->
                                        <details v-if="parseHunks(c.detail).length > 0" class="rounded-md border p-2">
                                            <summary class="cursor-pointer text-xs font-medium">{{ t('plans.hunkTitle') }}（{{ parseHunks(c.detail).length }}）</summary>
                                            <div v-for="(h, hi) in parseHunks(c.detail)" :key="hi" class="mt-2 grid gap-2 sm:grid-cols-3">
                                                <div v-for="side in hunkSides" :key="side.key" class="rounded-md border bg-muted/40 p-2">
                                                    <div class="text-muted-foreground mb-1">
                                                        {{ t(side.labelKey) }} · {{ t('plans.hunk.startLine', [h[side.key]?.start ?? 0]) }}
                                                    </div>
                                                    <pre class="overflow-x-auto font-mono text-[11px] leading-5">{{ (h[side.key]?.lines ?? []).join('\n') }}</pre>
                                                </div>
                                            </div>
                                        </details>

                                        <!-- 决议控件（真 RadioGroup）：仅 draft 且可推进时；计划内容本身不可编辑 -->
                                        <RadioGroup
                                            v-if="canResolve"
                                            v-model="choices[c.resource_id]"
                                            class="flex flex-wrap gap-x-5 gap-y-2"
                                        >
                                            <label
                                                v-for="opt in choiceOptions(c)"
                                                :key="opt.value"
                                                class="flex cursor-pointer items-center gap-2 text-sm"
                                                :class="opt.disabled ? 'cursor-not-allowed text-muted-foreground' : ''"
                                            >
                                                <RadioGroupItem :value="opt.value" :disabled="opt.disabled" />
                                                {{ t(opt.labelKey) }}
                                            </label>
                                        </RadioGroup>
                                        <div v-else class="text-muted-foreground text-xs">
                                            <template v-if="recordedChoices.has(c.resource_id)">
                                                {{ t('plans.choiceApplied', [t('plans.choice.' + recordedChoices.get(c.resource_id))]) }}
                                                · {{ t('plans.choiceRecorded') }}
                                            </template>
                                            <template v-else>{{ t('plans.choiceRecorded') }}</template>
                                        </div>
                                    </div>
                                </CardContent>
                            </Card>

                            <!-- 固定决议提交区：恰好覆盖全部冲突后才可提交（后端校验对称） -->
                            <Card v-if="canResolve">
                                <CardContent class="flex items-center justify-between gap-3 py-4">
                                    <span class="text-muted-foreground text-sm">
                                        {{ unresolvedCount > 0 ? t('plans.resolveRemaining', [unresolvedCount]) : t('plans.resolveReady') }}
                                    </span>
                                    <Button size="sm" :disabled="unresolvedCount > 0 || resolving" @click="submitResolutions">
                                        {{ t('plans.resolveAction') }}
                                    </Button>
                                </CardContent>
                            </Card>
                        </template>
                    </TabsContent>

                    <!-- 风险与前置条件页签：预检行面板（阻断/提示图标着色）+ 输入证据 -->
                    <TabsContent value="risks">
                        <Card>
                            <CardContent class="flex flex-col gap-1 py-4">
                                <div class="mb-1 flex items-center gap-2 text-sm font-medium">
                                    <ShieldCheck class="text-muted-foreground size-4" aria-hidden="true" />
                                    {{ t('plans.confirmationTitle') }}
                                </div>
                                <div v-if="!plan.confirmation_requirements?.length" class="text-muted-foreground py-2 text-sm">
                                    {{ t('plans.confirmationEmpty') }}
                                </div>
                                <div
                                    v-for="req in plan.confirmation_requirements"
                                    :key="req.code"
                                    class="flex items-center justify-between gap-3 border-b py-2 text-[12.5px] last:border-b-0"
                                >
                                    <span class="flex min-w-0 items-center gap-2">
                                        <component
                                            :is="req.severity === 'blocking' ? CircleAlert : TriangleAlert"
                                            class="size-4 flex-none"
                                            :class="req.severity === 'blocking' ? 'text-error' : 'text-warning'"
                                            aria-hidden="true"
                                        />
                                        {{ t('plans.confirm.' + req.code) }}
                                    </span>
                                    <span class="flex flex-none items-center gap-2">
                                        <Badge v-if="req.severity === 'blocking'" variant="st-err">{{ t('plans.severity.blocking') }}</Badge>
                                        <Badge variant="outline" class="text-muted-foreground">{{ t('plans.resourceCount', [req.resource_count]) }}</Badge>
                                    </span>
                                </div>
                            </CardContent>
                        </Card>

                        <Card>
                            <CardContent class="flex flex-col gap-2 py-4 text-xs">
                                <div class="mb-1 font-medium text-sm">{{ t('plans.evidenceTitle') }}</div>
                                <div class="flex justify-between gap-4">
                                    <span class="text-muted-foreground">{{ t('plans.exactness') }}</span>
                                    <span>{{ t('plans.exactnessValue.' + plan.requested_exactness) }}</span>
                                </div>
                                <div class="flex justify-between gap-4">
                                    <span class="text-muted-foreground">{{ t('plans.inputProjectSnapshot') }}</span>
                                    <span class="max-w-96 truncate font-mono" :title="plan.input_project_snapshot_digest">{{ plan.input_project_snapshot_id }}</span>
                                </div>
                                <div class="flex justify-between gap-4">
                                    <span class="text-muted-foreground">{{ t('plans.inputRuntimeSnapshot') }}</span>
                                    <span class="max-w-96 truncate font-mono" :title="plan.input_runtime_snapshot_digest">{{ plan.input_runtime_snapshot_id }}</span>
                                </div>
                                <div class="flex justify-between gap-4">
                                    <span class="text-muted-foreground">{{ t('plans.planDigest') }}</span>
                                    <span class="max-w-96 truncate font-mono" :title="plan.plan_digest">{{ plan.plan_digest }}</span>
                                </div>
                            </CardContent>
                        </Card>
                    </TabsContent>
                </Tabs>

                <!-- 应用同步主操作区（UX §7.5 固定确认区；风险与前置条件见对应页签）。
                     apply_sync availability 唯一门控：可用显主按钮，不可用显后端原因码
                     （err.recovery.in_progress / err.scan.* / err.plan.* 各态）；未注册不渲染。
                     等工作区投影到位再渲染，避免引导完成前误显「不可用」 -->
                <Card v-if="plan.status === 'resolved' && wsRow">
                    <CardContent class="flex flex-wrap items-center justify-between gap-3 py-4">
                        <span v-if="canApply" class="text-muted-foreground text-sm">{{ t('plans.applyHint') }}</span>
                        <span v-else class="text-sm text-amber-600 dark:text-amber-400">
                            {{ t('plans.applyUnavailable') }}<template v-if="applyReason">：{{ applyReason }}</template>
                        </span>
                        <Button v-if="canApply" size="sm" :disabled="applying" @click="applyPlan">
                            {{ t('plans.applyAction') }}
                        </Button>
                    </CardContent>
                </Card>

                <!-- 合并预览抽屉（票 #94）：merged_clean 行入口，实时计算不落库；
                     停用/过期计划仍可预览（只读横幅） -->
                <MergePreviewDrawer
                    v-model:open="mergePreviewOpen"
                    :plan-id="plan.plan_id"
                    :resource-id="mergePreviewResourceId"
                    :readonly-hint="retired"
                />
            </template>
        </template>
    </div>
</template>
