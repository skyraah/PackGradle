<script setup lang="ts">
// /workspaces/:id/recoveries/:run_id：恢复详情（契约 05 §3.2/§3.3/§3.4、§5 D2；
// UX 原型 §7.12）。run_id 即 task_id（apply_runs 主键）。
// 数据源：GetApplyRun（运行头投影，六阶段 state）+ ListApplyOperations（逐操作
// 清单分页，白名单投影——普通视图无临时路径/无 ownership proof，硬约束 4）。
// 动作：「确认人工处理」AcknowledgeRecovery（内联确认条沿 mappings 页先例，仅
// state=recovery_required 且未 acknowledged 时可用）；收口后重扫引导（StartScan
// 沿既有入口先例，availability 唯一门控；relation_invalidated 由后端在收口后
// 发布，经既有受控重查管线自然刷新工作区投影，本页事件管线零改动）。
// 工作区上下文读 stores/syncCache；任务推进经 task_updated → syncCache 投影变化
// 触发本页重查，页面不自建轮询。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { ApplyOperationDTO, ApplyRunDTO } from '../api'
import { bootstrapped, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errorCode, errText } from '../utils/errors'
import {
    BAD,
    RUN,
    formatTime,
    NEUTRAL,
    OK,
    PAGE_LIMIT,
    toneOf,
    WARN,
    type BadgeTone,
    type QueryPhase,
} from '../utils/pageState'
import { canRescan } from '../utils/plans'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))
const runID = computed(() => String(route.params.run_id ?? ''))

// —— 运行头查询快照 ——
const run = ref<ApplyRunDTO | null>(null)
const phase = ref<QueryPhase>('loading')
const inflight = ref(false)
const errorMsg = ref('')
// GetApplyRun 按关系返回最近一次运行：路由指向的 run_id 与其不符时为陈旧深链
// （如收口后又发起新 Apply），按错误态处理
const runMismatch = ref(false)
let runSeq = 0

async function loadRun(): Promise<void> {
    const seq = ++runSeq
    inflight.value = true
    try {
        const r = await SyncService.GetApplyRun(relationID.value)
        if (seq !== runSeq) return
        runMismatch.value = r.task_id !== runID.value
        run.value = r
        phase.value = 'ready'
        errorMsg.value = ''
    } catch (e) {
        if (seq !== runSeq) return
        run.value = null
        runMismatch.value = false
        phase.value = 'error'
        errorMsg.value = errText(e)
    } finally {
        if (seq === runSeq) inflight.value = false
    }
}

// —— 操作清单查询快照（独立于 run 头：失败不打断摘要区）——
const ops = ref<ApplyOperationDTO[]>([])
const opsNextCursor = ref('')
const opsPhase = ref<QueryPhase>('loading')
const opsInflight = ref(false)
const opsErrorMsg = ref('')
let opsSeq = 0

async function queryOps(cursor: string): Promise<void> {
    const seq = ++opsSeq
    opsInflight.value = true
    try {
        const page = await SyncService.ListApplyOperations(relationID.value, runID.value, cursor, PAGE_LIMIT)
        if (seq !== opsSeq) return
        ops.value = cursor ? [...ops.value, ...(page.items ?? [])] : (page.items ?? [])
        opsNextCursor.value = page.next_cursor ?? ''
        opsPhase.value = 'ready'
        opsErrorMsg.value = ''
    } catch (e) {
        if (seq !== opsSeq) return
        if (opsPhase.value === 'ready') {
            opsErrorMsg.value = errText(e)
        } else {
            opsPhase.value = 'error'
            opsErrorMsg.value = errText(e)
        }
    } finally {
        if (seq === opsSeq) opsInflight.value = false
    }
}

const reloadOps = () => void queryOps('')
const loadMoreOps = () => void queryOps(opsNextCursor.value)

function reloadAll(): void {
    void loadRun()
    reloadOps()
}

// 路由切换 → 全量重查
watch([relationID, runID], reloadAll, { immediate: true })

// —— 工作区上下文（读 syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)

// 任务投影变化（task_updated 事件经 syncCache 受控重查落到 tasks Map）→ 本页重查；
// 任务终态离开活跃列表时 updated_at 投影为空串，同样触发一次收敛重查（seq 守卫去抖）
const runTaskUpdated = computed(() => tasks.value.get(runID.value)?.updated_at ?? '')
watch(runTaskUpdated, (now, prev) => {
    if (now !== prev && phase.value === 'ready') reloadAll()
})
// 活跃任务收敛（恢复探测自动收口 committed / 任务终态）→ 重查
watch(
    () => wsRow.value?.state.active_task_id ?? '',
    (now, prev) => {
        if (prev && !now && phase.value === 'ready') reloadAll()
    },
)

// —— 状态投影 ——
const runReady = computed(() => phase.value === 'ready' && run.value !== null && !runMismatch.value)
// 确认动作可用面（契约 05 §3.4）：仅 recovery_required 且未 acknowledged
const canAcknowledge = computed(() => runReady.value && run.value!.state === 'recovery_required' && !run.value!.acknowledged_at)
// 收口后重扫引导：已确认（恢复路径不推进基线，引导重扫）
const acknowledged = computed(() => runReady.value && !!run.value!.acknowledged_at)

const confirming = ref(false) // 确认条（沿 mappings 页内联确认先例）
const acknowledging = ref(false)

async function acknowledge(): Promise<void> {
    if (!run.value || acknowledging.value) return
    acknowledging.value = true
    try {
        await SyncService.AcknowledgeRecovery(run.value.task_id)
        confirming.value = false
        showSnackbar(t('recovery.ackSuccess'), 'success')
        // 后端发布 relation_invalidated 会走受控重查；这里立即补一轮并刷新本页快照
        triggerRequery()
        reloadAll()
    } catch (e) {
        // 状态已变（如 not_required）：提示并重查拿最新投影
        if (errorCode(e) === 'err.recovery.not_required') confirming.value = false
        showSnackbar(errText(e), 'error')
        void loadRun()
    } finally {
        acknowledging.value = false
    }
}

// —— 重扫引导（availability 唯一门控，沿 utils/plans 先例；发起后回列表追踪）——
const rescanning = ref(false)

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

// —— 展示辅助（色调/时间收敛于 utils/pageState）——
const stateTones: Record<string, BadgeTone> = {
    prepared: RUN,
    staged: RUN,
    applying: RUN,
    verifying: RUN,
    committed: OK,
    recovery_required: BAD,
}

const statusTones: Record<string, BadgeTone> = {
    pending: NEUTRAL,
    running: RUN,
    applied: OK,
    verified: OK,
    failed: BAD,
    compensated: WARN,
}

const runFacts = computed(() => {
    const r = run.value
    if (!r) return []
    return [
        { label: 'recovery.createdAt', value: formatTime(r.created_at) },
        { label: 'recovery.updatedAt', value: formatTime(r.updated_at) },
        { label: 'recovery.operationCount', value: String(r.operation_count) },
        { label: 'recovery.staging', value: t(r.staging_cleared ? 'recovery.stagingCleared' : 'recovery.stagingKept') },
    ]
})

const opsCols = ['recovery.colOrdinal', 'recovery.colStatus', 'recovery.colResource', 'recovery.colOperation', 'recovery.colResult']
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部：工作区上下文 + 返回 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="flex items-center gap-2 page-title">
                    {{ t('recovery.title') }}
                    <Badge v-if="runReady" :variant="toneOf(stateTones, run!.state).variant" :class="toneOf(stateTones, run!.state).class">
                        {{ t('recovery.state.' + run!.state) }}
                    </Badge>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }} ·
                    </template>
                    <span class="font-mono text-xs" :title="runID">{{ runID }}</span>
                </p>
            </div>
            <div class="flex shrink-0 gap-2">
                <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                    {{ t('recovery.backToList') }}
                </Button>
            </div>
        </div>

        <!-- 工作区不存在：syncCache 引导完成后仍找不到该关系 -->
        <Card v-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('recovery.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('recovery.backToList') }}
                </Button>
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
                    <span class="text-destructive text-sm">{{ t('recovery.errorTitle') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="loadRun">{{ t('recovery.retry') }}</Button>
                </CardContent>
            </Card>

            <!-- 陈旧深链：该关系最近一次运行已不是路由指向的 run -->
            <Card v-else-if="runMismatch">
                <CardContent class="flex flex-col items-start gap-3 py-6">
                    <span class="text-destructive text-sm">{{ t('recovery.runMismatch') }}</span>
                    <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                        {{ t('recovery.backToList') }}
                    </Button>
                </CardContent>
            </Card>

            <template v-else-if="run">
                <!-- 运行摘要卡（六阶段 state / acknowledged / commit_id 等，GetApplyRun 投影） -->
                <Card>
                    <CardContent class="flex flex-col gap-3 py-4">
                        <div class="flex flex-wrap items-center gap-2">
                            <span class="font-medium">{{ t('recovery.summaryTitle') }}</span>
                            <Badge :variant="toneOf(stateTones, run.state).variant" :class="toneOf(stateTones, run.state).class">
                                {{ t('recovery.state.' + run.state) }}
                            </Badge>
                            <Badge v-if="run.acknowledged_at" variant="st-ok" plain>
                                {{ t('recovery.acknowledgedAt') }} {{ formatTime(run.acknowledged_at) }}
                            </Badge>
                            <span v-else class="text-muted-foreground text-xs">{{ t('recovery.notAcknowledged') }}</span>
                        </div>

                        <div class="grid gap-x-8 gap-y-1 text-xs sm:grid-cols-2">
                            <div class="flex items-center justify-between gap-4">
                                <span class="text-muted-foreground">{{ t('recovery.commit') }}</span>
                                <template v-if="run.commit_id">
                                    <router-link
                                        :to="'/workspaces/' + relationID + '/history/' + run.commit_id"
                                        class="max-w-72 truncate font-mono hover:underline"
                                        :title="run.commit_id"
                                    >{{ run.commit_id }}</router-link>
                                </template>
                                <span v-else class="text-muted-foreground">—</span>
                            </div>
                            <div class="flex items-center justify-between gap-4">
                                <span class="text-muted-foreground">{{ t('recovery.plan') }}</span>
                                <template v-if="run.plan_id">
                                    <router-link
                                        :to="'/workspaces/' + relationID + '/plans/' + run.plan_id"
                                        class="max-w-72 truncate font-mono hover:underline"
                                        :title="run.plan_id"
                                    >{{ run.plan_id }}</router-link>
                                </template>
                                <span v-else class="text-muted-foreground">—</span>
                            </div>
                            <div class="flex items-center justify-between gap-4">
                                <span class="text-muted-foreground">{{ t('recovery.planDigest') }}</span>
                                <span class="max-w-72 truncate font-mono" :title="run.plan_digest">{{ run.plan_digest }}</span>
                            </div>
                            <div v-for="f in runFacts" :key="f.label" class="flex items-center justify-between gap-4">
                                <span class="text-muted-foreground">{{ t(f.label) }}</span>
                                <span class="max-w-72 truncate" :title="f.value">{{ f.value }}</span>
                            </div>
                        </div>
                    </CardContent>
                </Card>

                <!-- 「确认人工处理」内联确认条（mappings 页先例） -->
                <div v-if="canAcknowledge && confirming" class="flex flex-wrap items-center justify-between gap-3 rounded-md border px-3 py-2">
                    <span class="text-sm">{{ t('recovery.ackHint') }}</span>
                    <div class="flex gap-2">
                        <Button size="sm" :disabled="acknowledging" @click="acknowledge">{{ t('recovery.ackConfirm') }}</Button>
                        <Button variant="ghost" size="sm" :disabled="acknowledging" @click="confirming = false">
                            {{ t('recovery.ackCancel') }}
                        </Button>
                    </div>
                </div>
                <div v-else-if="canAcknowledge" class="flex justify-end">
                    <Button size="sm" @click="confirming = true">{{ t('recovery.ackAction') }}</Button>
                </div>

                <!-- 收口后重扫引导（恢复路径不推进基线，acknowledge 后引导重扫） -->
                <Card v-if="acknowledged">
                    <CardContent class="flex flex-wrap items-center justify-between gap-3 py-4">
                        <div class="flex flex-col gap-1">
                            <span class="text-sm font-medium">{{ t('recovery.rescanTitle') }}</span>
                            <span class="text-muted-foreground text-xs">{{ t('recovery.rescanHint') }}</span>
                        </div>
                        <!-- availability 唯一门控（契约 03 §2.1）：scan 不可用时不渲染按钮 -->
                        <Button v-if="canRescan(wsRow)" size="sm" :disabled="rescanning" @click="rescan">
                            {{ t('recovery.rescanAction') }}
                        </Button>
                    </CardContent>
                </Card>

                <!-- 操作清单（逐资源证据，ordinal 升序分页） -->
                <Card>
                    <CardContent class="py-2">
                        <div class="text-muted-foreground flex items-center justify-between gap-2 px-2 py-2 text-xs">
                            <span class="font-medium text-sm text-foreground">{{ t('recovery.operationsTitle') }}</span>
                            <span>{{ t('history.shownOf', [ops.length]) }}</span>
                        </div>

                        <!-- 操作清单首查失败：区内错误态可重试 -->
                        <div v-if="opsPhase === 'error'" class="flex items-center justify-between gap-3 py-4">
                            <span class="text-destructive text-sm">{{ t('recovery.operationsError') }}：{{ opsErrorMsg }}</span>
                            <Button variant="outline" size="sm" :disabled="opsInflight" @click="reloadOps">{{ t('recovery.retry') }}</Button>
                        </div>

                        <template v-else>
                            <!-- 首查 loading：骨架行（保留表头同构） -->
                            <Table v-if="opsPhase === 'loading'">
                                <TableHeader>
                                    <TableRow>
                                        <TableHead v-for="c in opsCols" :key="c">{{ t(c) }}</TableHead>
                                    </TableRow>
                                </TableHeader>
                                <TableBody>
                                    <TableRow v-for="i in 3" :key="i">
                                        <TableCell v-for="c in opsCols" :key="c">
                                            <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                        </TableCell>
                                    </TableRow>
                                </TableBody>
                            </Table>

                            <template v-else>
                                <!-- 空态 -->
                                <div v-if="!ops.length" class="text-muted-foreground py-8 text-center text-sm">
                                    {{ t('recovery.operationsEmpty') }}
                                </div>

                                <Table v-else>
                                    <TableHeader>
                                        <TableRow>
                                            <TableHead v-for="c in opsCols" :key="c">{{ t(c) }}</TableHead>
                                        </TableRow>
                                    </TableHeader>
                                    <TableBody>
                                        <TableRow v-for="op in ops" :key="op.operation_id">
                                            <TableCell class="text-muted-foreground text-xs">{{ op.ordinal }}</TableCell>
                                            <TableCell>
                                                <Badge :variant="toneOf(statusTones, op.status).variant" :class="toneOf(statusTones, op.status).class">
                                                    {{ t('recovery.status.' + op.status) }}
                                                </Badge>
                                            </TableCell>
                                            <TableCell class="max-w-80 truncate font-medium" :title="op.relative_path || op.resource_id">
                                                {{ op.relative_path || op.resource_id || '—' }}
                                            </TableCell>
                                            <TableCell>
                                                <Badge v-if="op.change_kind" variant="outline">{{ t('history.change.' + op.change_kind) }}</Badge>
                                                <span v-else class="text-muted-foreground">—</span>
                                            </TableCell>
                                            <!-- 结果码为引擎定义的技术摘要码（非 err.* 码，不经 locale）；
                                                 成功为空显「—」 -->
                                            <TableCell class="max-w-56 truncate font-mono text-xs" :title="op.result_code">
                                                {{ op.result_code || '—' }}
                                            </TableCell>
                                        </TableRow>
                                    </TableBody>
                                </Table>

                                <!-- 页脚：已展示计数 + 加载更多 -->
                                <div v-if="ops.length" class="flex items-center justify-between gap-2 py-2">
                                    <span class="text-muted-foreground text-xs">{{ t('history.shownOf', [ops.length]) }}</span>
                                    <Button v-if="opsNextCursor" variant="outline" size="sm" :disabled="opsInflight" @click="loadMoreOps">
                                        {{ t('recovery.loadMore') }}
                                    </Button>
                                </div>
                                <p v-if="opsErrorMsg" class="text-destructive px-2 pb-2 text-xs">{{ t('recovery.operationsError') }}：{{ opsErrorMsg }}</p>
                            </template>
                        </template>
                    </CardContent>
                </Card>
            </template>
        </template>
    </div>
</template>
