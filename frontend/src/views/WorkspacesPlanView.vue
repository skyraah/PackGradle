<script setup lang="ts">
// /workspaces/:id/plans/:plan_id：只读计划与冲突解决（契约 03 §2.6；UX 原型 §7.5）。
// 计划数据为本页查询快照（GetPlan 读投影），工作区上下文（关系名/availability）
// 读 stores/syncCache 投影，页面不做第二处取数。
// 硬约束（验收规格 UX §15.1）：页面无 Apply/History/Restore 入口；计划内容不可编辑——
// 冲突选择只用于 ResolvePlan 产生全新不可变计划，旧计划保持只读。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { ConflictDTO, SyncPlanDTO } from '../api'
import { bootstrapped, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { canPrepareSync, canRescan, prepareSync } from '../utils/plans'
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
        { key: 'plans.summary.conflict', count: s.conflict_count },
        { key: 'plans.summary.unrecoverable', count: unrecoverableCount.value },
    ]
})
const bidirectional = computed(() => writeRuntimeCount.value > 0 && writeProjectCount.value > 0)

const statusTones: Record<string, { variant: 'default' | 'secondary' | 'destructive' | 'outline'; class?: string }> = {
    draft: { variant: 'secondary' },
    resolved: { variant: 'outline', class: 'text-emerald-600 dark:text-emerald-400' },
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
        // 全新不可变计划：导航到新 plan_id（路由参数变化触发整页重查，草稿不迁移）
        await router.replace('/workspaces/' + relationID.value + '/plans/' + next.plan_id)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        resolving.value = false
    }
}

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
                <!-- 停用态：内容继续可读，顶部说明具体原因，推进控件隐藏（UX 原型 §7.5） -->
                <Card v-if="frozen">
                    <CardContent class="flex flex-wrap items-center justify-between gap-3 py-4">
                        <span class="text-sm">
                            <span class="text-amber-600 dark:text-amber-400">{{ t('plans.frozen.' + plan.status) }}</span>
                            <span class="text-muted-foreground"> — {{ t(isStale ? 'plans.frozen.staleHint' : 'plans.frozen.expiredHint') }}</span>
                        </span>
                        <div class="flex flex-wrap gap-2">
                            <!-- stale 主操作：重新扫描并生成新计划（发起扫描后回列表继续） -->
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
                                        <Badge variant="outline">{{ t('plans.op.' + op.kind) }}</Badge>
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
                                <!-- 决议控件：仅 draft 且可推进时；计划内容本身不可编辑 -->
                                <div v-if="canResolve" class="flex flex-wrap items-center gap-2">
                                    <label v-for="opt in choiceOptions(c)" :key="opt.value" class="flex items-center gap-1 text-sm" :class="opt.disabled ? 'text-muted-foreground cursor-not-allowed' : 'cursor-pointer'">
                                        <input v-model="choices[c.resource_id]" type="radio" :name="'choice-' + c.resource_id" :value="opt.value" :disabled="opt.disabled" class="accent-current" />
                                        {{ t(opt.labelKey) }}
                                    </label>
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
            </template>
        </template>
    </div>
</template>
