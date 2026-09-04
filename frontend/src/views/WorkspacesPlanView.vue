<script setup lang="ts">
// /workspaces/:id/plans/:plan_id：只读计划、冲突解决与应用同步（契约 03 §2.6、
// 契约 05 §1/§5；UX 原型 §7.5）。
// 计划数据为本页查询快照（GetPlan 读投影），工作区上下文（关系名/availability）
// 读 stores/syncCache 投影，页面不做第二处取数。
// 硬约束（验收规格 UX §15.1）：计划内容不可编辑——冲突选择只用于 ResolvePlan 产生
// 全新不可变计划，旧计划保持只读；History/Restore 入口不在本页（Phase 2/3 各归其页）。
// 应用链路（T11 票 #43）：resolved 计划主操作「应用同步」由 apply_sync availability
// 唯一门控（契约 05 §1，不可用显后端原因码）；ConfirmPlan 成功即长任务移交任务中心
// （UX §7.9 可离开页面）；committed 后 GetPlan 投影 status=applied，主操作区收敛为重扫引导。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { ConflictDTO, SyncPlanDTO } from '../api'
import MergePreviewDrawer from '../components/common/MergePreviewDrawer.vue'
import { bootstrapped, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

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

// —— 摘要（UX 原型 §7.5：双向计划分别计数，不用单一箭头；不可恢复数量必显）——
const writeRuntimeCount = computed(
    () => (plan.value?.operations ?? []).filter(op => op.kind === 'write_runtime').length,
)
const writeProjectCount = computed(
    () => (plan.value?.operations ?? []).filter(op => op.kind === 'write_project').length,
)
const unrecoverableCount = computed(
    () =>
        (plan.value?.confirmation_requirements ?? []).find(r => r.code === 'unrecoverable')
            ?.resource_count ?? 0,
)
const summaryChips = computed(() => {
    const s = plan.value?.summary
    if (!s) return []
    return [
        { key: 'plans.summary.resourceTotal', count: s.resource_total },
        { key: 'plans.summary.adoptEqual', count: s.adopt_equal_count },
        { key: 'plans.summary.create', count: s.create_count },
        { key: 'plans.summary.modify', count: s.modify_count },
        { key: 'plans.summary.delete', count: s.delete_count },
        { key: 'plans.summary.writeRuntime', count: writeRuntimeCount.value },
        { key: 'plans.summary.writeProject', count: writeProjectCount.value },
        { key: 'plans.summary.mergedClean', count: s.merged_clean_count },
        { key: 'plans.summary.conflict', count: s.conflict_count },
        { key: 'plans.summary.unrecoverable', count: unrecoverableCount.value },
    ]
})
const bidirectional = computed(() => writeRuntimeCount.value > 0 && writeProjectCount.value > 0)

const statusTones: Record<string, { variant: 'default' | 'secondary' | 'destructive' | 'outline'; class?: string }> = {
    draft: { variant: 'secondary' },
    resolved: { variant: 'outline', class: 'text-emerald-600 dark:text-emerald-400' },
    applied: { variant: 'outline', class: 'text-emerald-600 dark:text-emerald-400' },
    stale: { variant: 'outline', class: 'text-amber-600 dark:text-amber-400' },
    expired: { variant: 'outline', class: 'text-amber-600 dark:text-amber-400' },
}

// —— 页签（shadcn 注册表当前不可达，Tabs 待 add tabs 后替换为注册表组件；
// 页面内状态切换沿 T09/T10 原生控件先例）——
const tab = ref<'operations' | 'conflicts' | 'risks'>('operations')

// —— 冲突决议草稿（按 plan_id 保存于本页内存；导航离开即弃，不迁移旧草稿）——
const choices = ref<Record<string, string>>({})

watch(planID, () => {
    choices.value = {}
    tab.value = 'operations'
})

// 四选（票 #100，ADR-0013 §0 Q1-c）：三类冲突统一「择侧 ×2 + 忽略 + 手动处理」；
// 忽略按资源身份隐藏——mod 资源不提供忽略（编译器禁文件规则入 mods/ 前缀，
// 无法合成规则；resource_id 的 mod: 前缀即 mod 身份）。mod 冲突仍有择侧 +
// 手动处理。本判断无前端单测设施：验收 6 由项目口径（vue-tsc + 构建 + L1
// 走查）承接——走查项 = mod 冲突卡不出现「忽略此文件」选项。
function choiceOptions(c: ConflictDTO): { value: string; labelKey: string; disabled?: boolean }[] {
    const ignore = c.resource_id.startsWith('mod:')
        ? []
        : [{ value: 'skip', labelKey: 'plans.choice.ignore' }]
    const manual = [{ value: 'manual', labelKey: 'plans.choice.manual' }]
    if (c.kind === 'initialize_choice') {
        return [
            { value: 'initialize_from_project', labelKey: 'plans.choice.initializeFromProject', disabled: !c.project },
            { value: 'initialize_from_runtime', labelKey: 'plans.choice.initializeFromRuntime', disabled: !c.runtime },
            ...ignore,
            ...manual,
        ]
    }
    return [
        { value: 'take_project', labelKey: 'plans.choice.takeProject' },
        { value: 'take_runtime', labelKey: 'plans.choice.takeRuntime' },
        ...ignore,
        ...manual,
    ]
}

// 合并行呈现（契约 07 §3.3/§6，票 #93）：write_merged 是后端固化的默认推荐
//（非冲突操作，随授权模式免确认），本页只读呈现「将自动合并」，无决议入口。
const isMergedOp = (kind: string): boolean => kind === 'write_merged'

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
// stale 主操作「重新扫描并生成新计划」（UX §7.5）：发起扫描后回列表页，扫描完成
// 由列表行入口继续 PrepareSync；「用当前快照重新生成」适用策略修改后快照仍新鲜的场景。
const regenerating = ref(false)
const rescanning = ref(false)

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
        <!-- 头部：计划标题 + 状态 + 有效期 + 来源 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="flex items-center gap-2 text-xl font-semibold">
                    {{ plan ? t('plans.title.' + plan.kind) : t('plans.title') }}
                    <Badge v-if="plan" :variant="statusTones[plan.status]?.variant ?? 'outline'" :class="statusTones[plan.status]?.class">
                        {{ t('plans.status.' + plan.status) }}
                    </Badge>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }} ·
                    </template>
                    <template v-if="plan">
                        {{ t('plans.expiresAt') }} {{ new Date(plan.expires_at).toLocaleString() }}
                        <template v-if="plan.resolved_from_plan_id">
                            · {{ t('plans.resolvedFrom') }}
                        </template>
                        <template v-if="activeTask"> · {{ t(activeTask.message_key, activeTask.message_args ?? []) }}</template>
                    </template>
                </p>
            </div>
            <div class="flex shrink-0 gap-2">
                <Button variant="ghost" size="sm" @click="router.push('/workspaces/' + relationID + '/changes')">
                    {{ t('plans.backToChanges') }}
                </Button>
                <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                    {{ t('plans.backToList') }}
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
                <!-- 停用/已应用：内容继续可读，顶部说明具体原因（UX 原型 §7.5）。
                     已应用主操作区收敛为重扫引导，沿停用态的既有次级操作 -->
                <Card v-if="retired">
                    <CardContent class="flex flex-wrap items-center justify-between gap-3 py-4">
                        <span class="text-sm">
                            <span :class="isApplied ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'">
                                {{ t(isApplied ? 'plans.appliedTitle' : 'plans.frozen.' + plan.status) }}
                            </span>
                            <span class="text-muted-foreground"> — {{ t(isApplied ? 'plans.appliedHint' : isStale ? 'plans.frozen.staleHint' : 'plans.frozen.expiredHint') }}</span>
                        </span>
                        <div class="flex flex-wrap gap-2">
                            <!-- 重扫引导：重新扫描并生成新计划（发起扫描后回列表继续） -->
                            <Button v-if="canRescan(wsRow)" size="sm" :disabled="rescanning" @click="rescan">
                                {{ t('plans.rescan') }}
                            </Button>
                            <Button v-if="canPrepareSync(wsRow)" variant="outline" size="sm" :disabled="regenerating" @click="regenerate">
                                {{ t('plans.regenerate') }}
                            </Button>
                            <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                                {{ t('plans.backToList') }}
                            </Button>
                        </div>
                    </CardContent>
                </Card>

                <!-- 摘要条 -->
                <div class="flex flex-wrap items-center gap-2">
                    <Badge v-for="chip in summaryChips" :key="chip.key" variant="outline" class="text-muted-foreground">
                        {{ t(chip.key) }} {{ chip.count }}
                    </Badge>
                    <Badge v-if="bidirectional" variant="outline">{{ t('plans.bidirectional') }}</Badge>
                </div>

                <!-- 页签切换 -->
                <div class="flex items-center gap-1">
                    <Button v-for="key in ['operations', 'conflicts', 'risks']" :key="key" size="sm" :variant="tab === key ? 'default' : 'ghost'" @click="tab = key as typeof tab">
                        {{ t('plans.tab.' + key) }}
                        <Badge v-if="key === 'conflicts' && (plan.conflicts?.length ?? 0) > 0" variant="destructive" class="ml-1">
                            {{ plan.conflicts!.length }}
                        </Badge>
                    </Button>
                </div>

                <!-- 操作页签：只读操作表（计划内容不可编辑） -->
                <Card v-if="tab === 'operations'">
                    <CardContent class="py-2">
                        <div v-if="!plan.operations?.length" class="text-muted-foreground py-8 text-center text-sm">
                            {{ t('plans.operationsEmpty') }}
                        </div>
                        <Table v-else>
                            <TableHeader>
                                <TableRow>
                                    <TableHead>{{ t('plans.colResource') }}</TableHead>
                                    <TableHead>{{ t('plans.colOperation') }}</TableHead>
                                    <TableHead>{{ t('plans.colReversible') }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="op in plan.operations" :key="op.id">
                                    <TableCell class="max-w-96 truncate font-medium" :title="op.resource_id">
                                        {{ op.resource_id }}
                                    </TableCell>
                                    <TableCell>
                                        <div class="flex flex-wrap items-center gap-1">
                                            <Badge variant="outline">{{ t('plans.op.' + op.kind) }}</Badge>
                                            <Badge v-if="isMergedOp(op.kind)" variant="secondary" class="text-emerald-600 dark:text-emerald-400">
                                                {{ t('plans.mergeBadge') }}
                                            </Badge>
                                            <!-- merged_clean 行「查看合并结果」（契约 07 §6，票 #94）：
                                                 预览抽屉 = 全文 + 行级绿红黄标注 + 语法高亮 -->
                                            <Button
                                                v-if="op.kind === 'write_merged'"
                                                variant="ghost"
                                                size="xs"
                                                @click="openMergePreview(op.resource_id)"
                                            >
                                                {{ t('plans.mergePreview.open') }}
                                            </Button>
                                        </div>
                                        <div v-if="isMergedOp(op.kind)" class="text-muted-foreground mt-1 text-xs">
                                            {{ t('plans.mergeHint') }}
                                        </div>
                                    </TableCell>
                                    <TableCell>
                                        <span :class="op.reversible ? 'text-muted-foreground' : 'text-amber-600 dark:text-amber-400'">
                                            {{ t(op.reversible ? 'plans.reversible.yes' : 'plans.reversible.no') }}
                                        </span>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>
                    </CardContent>
                </Card>

                <!-- 冲突页签：P1 choose_side 决议草稿（draft）/ 只读证据（其余） -->
                <template v-else-if="tab === 'conflicts'">
                    <Card v-if="!plan.conflicts?.length">
                        <CardContent class="text-muted-foreground py-8 text-center text-sm">
                            {{ t('plans.conflictsEmpty') }}
                        </CardContent>
                    </Card>

                    <template v-else>
                        <p v-if="canResolve" class="text-muted-foreground text-sm">{{ t('plans.resolveHint') }}</p>
                        <Card v-for="c in plan.conflicts" :key="c.resource_id">
                            <CardContent class="flex flex-col gap-3 py-4">
                                <div class="flex flex-wrap items-center justify-between gap-2">
                                    <div class="max-w-md truncate font-medium" :title="c.resource_id">{{ c.resource_id }}</div>
                                    <Badge variant="destructive">{{ t('changes.conflict.' + c.kind) }}</Badge>
                                </div>
                                <div class="grid gap-2 text-xs sm:grid-cols-2">
                                    <div class="rounded-md border p-2">
                                        <div class="text-muted-foreground mb-1">{{ t('plans.sideProject') }}</div>
                                        <template v-if="c.project">{{ c.project.format }}<div class="text-muted-foreground">{{ c.project.content?.digest ?? '' }}</div></template>
                                        <span v-else class="text-muted-foreground">{{ t('plans.sideMissing') }}</span>
                                    </div>
                                    <div class="rounded-md border p-2">
                                        <div class="text-muted-foreground mb-1">{{ t('plans.sideRuntime') }}</div>
                                        <template v-if="c.runtime">{{ c.runtime.format }}<div class="text-muted-foreground">{{ c.runtime.content?.digest ?? '' }}</div></template>
                                        <span v-else class="text-muted-foreground">{{ t('plans.sideMissing') }}</span>
                                    </div>
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
                                <!-- 决议控件：仅 draft 且可推进时；计划内容本身不可编辑 -->
                                <div v-if="canResolve" class="flex flex-col gap-1">
                                    <div class="flex flex-wrap items-center gap-2">
                                        <label v-for="opt in choiceOptions(c)" :key="opt.value" class="flex items-center gap-1 text-sm" :class="opt.disabled ? 'text-muted-foreground cursor-not-allowed' : 'cursor-pointer'">
                                            <input v-model="choices[c.resource_id]" type="radio" :name="'choice-' + c.resource_id" :value="opt.value" :disabled="opt.disabled" class="accent-current" />
                                            {{ t(opt.labelKey) }}
                                        </label>
                                    </div>
                                    <!-- 选中忽略/手动处理时的后果提示（票 #100，ADR-0013 §1）：
                                         忽略=随提交持久移出受管范围；手动处理=本次吸收进基线 -->
                                    <div
                                        v-if="choices[c.resource_id] === 'skip' || choices[c.resource_id] === 'manual'"
                                        class="text-muted-foreground text-xs"
                                    >
                                        {{ t('plans.choiceHint.' + choices[c.resource_id]) }}
                                    </div>
                                </div>
                                <div v-else-if="plan.status === 'resolved'" class="text-muted-foreground text-xs">
                                    {{ t('plans.choiceRecorded') }}
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
                </template>

                <!-- 风险与前置条件页签 -->
                <template v-else>
                    <Card>
                        <CardContent class="flex flex-col gap-3 py-4">
                            <div class="font-medium">{{ t('plans.confirmationTitle') }}</div>
                            <div v-if="!plan.confirmation_requirements?.length" class="text-muted-foreground text-sm">
                                {{ t('plans.confirmationEmpty') }}
                            </div>
                            <div v-else class="flex flex-col gap-2">
                                <div v-for="req in plan.confirmation_requirements" :key="req.code" class="flex items-center justify-between gap-2 rounded-md border p-2">
                                    <span class="text-sm">{{ t('plans.confirm.' + req.code) }}</span>
                                    <div class="flex items-center gap-2">
                                        <Badge v-if="req.severity === 'blocking'" variant="destructive">{{ t('plans.severity.blocking') }}</Badge>
                                        <Badge variant="outline" class="text-muted-foreground">{{ t('plans.resourceCount', [req.resource_count]) }}</Badge>
                                    </div>
                                </div>
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
                </template>

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
