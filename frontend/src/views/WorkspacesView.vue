<script setup lang="ts">
// /workspaces：工作区列表（契约 04 缓存骨架 + UX 原型 §7.1，票 #104 六列骨架）。
// 数据全部来自 stores/syncCache（查询 API 的投影）：bootstrap 首次填充前渲骨架，
// 事件/对账触发的受控重查原地更新，页面不做第二处数据获取、不订阅事件。
// 列结构对照原型 W-01..W-04：工作区、关系健康、变化状态、当前任务、最近活动、
// 行操作；行末唯一主操作按原型 §7.1 优先级链给出（处理恢复 > 重新绑定 >
// 开始扫描 > 重试扫描 > 查看任务 > 生成同步分析 > 查看变化）；扫描/重绑入口
// 沿用 features/availability 唯一门控（契约 03 §2.1）：不可用灰置保留位置并显
// 后端原因码；快速更新不在行末（拍板移入对象头「更多」菜单，#105 落地）。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ArrowLeftRight } from '@lucide/vue'
import { SyncService } from '../api'
import type { TaskDTO, WorkspaceDTO } from '../api'
import { bootstrapped, bootstrapError, retryBootstrap, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar, taskDrawerOpen } from '../stores/ui'
import { errText } from '../utils/errors'
import { availabilityReasonText, canPrepareSync, canRebind, prepareSync } from '../utils/plans'
import { DIFF_TONES, HEALTH_TONES, toneOf, type BadgeTone } from '../utils/pageState'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const router = useRouter()

// 正交状态徽标（UX 原型 §7.1 状态画板）：色调映射收敛于 utils/pageState（票 #102）

// —— 行视图模型：缓存每次提交后统一派生，模板不重复取值 ——
// 行末唯一主操作（UX 原型 §7.1 优先级链，票 #104）：primary 对应原型
// btn-primary / btn-tonal 两档强调；disabled+title 承接 availability 灰置语义。
type RowActionKind = 'recover' | 'rebind' | 'scan' | 'rescan' | 'viewTask' | 'prepare' | 'viewChanges'

interface RowAction {
    kind: RowActionKind
    label: string
    primary: boolean
    disabled?: boolean
    title?: string
}

interface WorkspaceRow {
    workspace: WorkspaceDTO
    task: TaskDTO | null
    pendingPlanID: string
    recoveryRequired: boolean
    healthTone: BadgeTone
    diffTone: BadgeTone
    activity: string
    action: RowAction
}

// rowActionFor 按原型 §7.1 优先级链派生行末唯一主操作（七态互斥，文案逐字对照
// 原型）：恢复门最先；rebind_required 出「重新绑定」（路径迁移是合法主动操作，
// 恢复门已在上一分支拦下）；scan never/failed 出扫描入口（availability 灰置时
// 保留位置显原因码）；其余活跃任务（扫描中/排队/应用/回滚）出「查看任务」；
// prepare_sync 可用出「生成同步分析」（T11 计划页承接）；兜底「查看变化」。
function rowActionFor(w: WorkspaceDTO, task: TaskDTO | null): RowAction {
    if (w.relation.health === 'recovery_required') {
        return { kind: 'recover', label: t('workspaces.recoverAction'), primary: true }
    }
    if (w.relation.health === 'rebind_required') {
        const reason = availabilityReasonText(w, 'rebind')
        return {
            kind: 'rebind',
            label: t('workspaces.rebindAction'),
            primary: true,
            disabled: !canRebind(w),
            title: reason || undefined,
        }
    }
    const canScan = w.features.scan && w.availability?.some(a => a.action === 'scan' && a.available) === true
    const scanReason = availabilityReasonText(w, 'scan')
    if (w.state.scan_state === 'never_scanned') {
        return { kind: 'scan', label: t('workspaces.scanAction'), primary: true, disabled: !canScan, title: scanReason || undefined }
    }
    if (w.state.scan_state === 'failed') {
        return { kind: 'rescan', label: t('workspaces.scanRetryAction'), primary: true, disabled: !canScan, title: scanReason || undefined }
    }
    if (task || w.state.scan_state === 'scanning' || w.state.scan_state === 'queued') {
        return { kind: 'viewTask', label: t('workspaces.viewTaskAction'), primary: false }
    }
    if (canPrepareSync(w)) {
        return { kind: 'prepare', label: t('workspaces.planAction'), primary: true }
    }
    return { kind: 'viewChanges', label: t('workspaces.changesAction'), primary: false }
}

const rows = computed<WorkspaceRow[]>(() =>
    workspaces.value.map(w => {
        const task = (w.state.active_task_id ? tasks.value.get(w.state.active_task_id) : null) ?? null
        return {
            workspace: w,
            task,
            // 待确认角标数据源（契约 07 §3.2/§6，票 #86）：后端投影的最新待人工
            // 计划，relation_invalidated 到达即经受控重查刷新（§4 新发射点收口时序）
            pendingPlanID: w.state.pending_plan_id ?? '',
            recoveryRequired: w.relation.health === 'recovery_required',
            // 正交状态徽标（票 #102 共享映射；列结构对照原型 W-03 只保留健康/变化两组，
            // 扫描与基线信息由行末主操作与 #105 变化页指标条承接）
            healthTone: toneOf(HEALTH_TONES, w.relation.health),
            diffTone: toneOf(DIFF_TONES, w.state.diff_state),
            activity: lastActivity(w),
            action: rowActionFor(w, task),
        }
    }),
)

// watch 状态横幅（契约 07 §6，票 #92）：watch_status ∈ {paused, unavailable}
// 渲染横幅——paused=自动物化连败暂停（手动快速更新成功即后端复位，横幅随
// 受控重查消失）/ unavailable=监听死亡降级回手动；active 与未挂载不渲染。
// 数据源=WorkspaceStateDTO.watch_status（会话内存态，事件到达经受控重查刷新）。
const watchBanners = computed(() =>
    workspaces.value
        .filter(w => w.state.watch_status === 'paused' || w.state.watch_status === 'unavailable')
        .map(w => ({
            relationID: w.relation.relation_id,
            name: w.relation.project.display_name,
            paused: w.state.watch_status === 'paused',
        })),
)

// 任务列（原型 .task-cell 形态，票 #104）：类型徽章（st-run 呼吸动画由 Badge
// pulse 承接）+ 90px 细进度条 + 百分比；total<=0 无法推导百分比时只显零位进度条
function taskPercent(task: TaskDTO): number | null {
    if (task.total <= 0) return null
    return Math.min(100, Math.round((task.completed / task.total) * 100))
}

function taskKindLabel(task: TaskDTO): string {
    return t('workspaces.taskKind.' + task.kind)
}

function lastActivity(w: WorkspaceDTO): string {
    const stamps = [w.latest_project_snapshot?.captured_at, w.latest_runtime_snapshot?.captured_at]
        .filter((s): s is string => !!s)
        .map(s => Date.parse(s))
        .filter(v => !Number.isNaN(v))
    if (!stamps.length) return '—'
    return new Date(Math.max(...stamps)).toLocaleString()
}

// —— 行操作（动作成功后立即触发一轮受控重查；后续事件继续经管线刷新）——
// withPending 统一防重入：同 id 动作进行中禁点，结束后释放（票 #104 收敛后
// 行内只剩扫描/生成同步分析/处理恢复三类写动作）
const pending = ref({ scanning: new Set<string>(), preparing: new Set<string>(), recovering: new Set<string>() })

function isPending(kind: 'scanning' | 'preparing' | 'recovering', id: string): boolean {
    return pending.value[kind].has(id)
}

async function withPending(kind: 'scanning' | 'preparing' | 'recovering', id: string, action: () => Promise<void>): Promise<void> {
    if (isPending(kind, id)) return
    const next = new Set(pending.value[kind])
    next.add(id)
    pending.value = { ...pending.value, [kind]: next }
    try {
        await action()
    } finally {
        const rest = new Set(pending.value[kind])
        rest.delete(id)
        pending.value = { ...pending.value, [kind]: rest }
    }
}

// 生成同步计划（T11 点亮的 prepare_sync 入口，availability 驱动）：
// 用列表缓存的当前修订与最新双端快照直接发起，成功后进入计划页；
// PrepareSync 不发事件，立即补一轮受控重查让计划页拿到新鲜 availability（apply_sync 门控）。
function prepareSyncPlan(row: WorkspaceRow): void {
    const ws = row.workspace
    void withPending('preparing', ws.relation.relation_id, async () => {
        try {
            const plan = await prepareSync(ws)
            triggerRequery()
            await router.push('/workspaces/' + ws.relation.relation_id + '/plans/' + plan.plan_id)
        } catch (e) {
            showSnackbar(errText(e), 'error')
        }
    })
}

// 待确认角标（契约 07 §6，票 #86）：点击直达计划页。pending_plan_id 投影不区分
// 计划类别，restore 计划的承接页在 restore 路由段（router/index.ts），先 GetPlan
// 读 kind 分派；读取失败退同步计划路由（GetPlan 为 kind 无关读投影）。
async function openPendingPlan(row: WorkspaceRow): Promise<void> {
    const relID = row.workspace.relation.relation_id
    const planID = row.pendingPlanID
    try {
        const plan = await SyncService.GetPlan(planID)
        if (plan.kind === 'restore') {
            await router.push('/workspaces/' + relID + '/plans/restore/' + planID)
        } else {
            await router.push('/workspaces/' + relID + '/plans/' + planID)
        }
    } catch {
        await router.push('/workspaces/' + relID + '/plans/' + planID)
    }
}

// 处理恢复（契约 05 §5 列表行入口，与任务中心同款双入口）：导航恢复详情页
// /workspaces/:id/recoveries/:run_id，run_id=task_id。任务中心缓存持有恢复任务
// 时直接取其 task_id，否则 GetApplyRun 取最近运行（恢复门期间即恢复中的运行）。
function openRecovery(row: WorkspaceRow): void {
    const relID = row.workspace.relation.relation_id
    void withPending('recovering', relID, async () => {
        try {
            const cached = [...tasks.value.values()].find(
                k => k.relation_id === relID && k.status === 'recovery_required',
            )
            const runID = cached?.task_id ?? (await SyncService.GetApplyRun(relID)).task_id
            await router.push('/workspaces/' + relID + '/recoveries/' + runID)
        } catch (e) {
            showSnackbar(errText(e), 'error')
        }
    })
}

function startScan(w: WorkspaceRow): void {
    void withPending('scanning', w.workspace.relation.relation_id, async () => {
        try {
            await SyncService.StartScan(w.workspace.relation.relation_id)
            triggerRequery()
        } catch (e) {
            showSnackbar(errText(e), 'error')
        }
    })
}

// 行点击进变化页（原型 rowlink → ws-open）；recovery_required 行不设行点击
// （票 #104）：恢复语义由行末「处理恢复」唯一承接，避免误入恢复中的工作区
function openWorkspace(row: WorkspaceRow): void {
    if (row.recoveryRequired) return
    void router.push('/workspaces/' + row.workspace.relation.relation_id + '/changes')
}

// 行末主操作分发（原型 §7.1 act 映射）：viewTask 打开任务中心抽屉（壳层共用
// stores/ui 的 taskDrawerOpen），其余为导航或写动作
function runRowAction(row: WorkspaceRow): void {
    const relID = row.workspace.relation.relation_id
    switch (row.action.kind) {
        case 'recover':
            openRecovery(row)
            break
        case 'rebind':
            void router.push('/workspaces/' + relID + '/rebind')
            break
        case 'scan':
        case 'rescan':
            startScan(row)
            break
        case 'viewTask':
            taskDrawerOpen.value = true
            break
        case 'prepare':
            prepareSyncPlan(row)
            break
        case 'viewChanges':
            void router.push('/workspaces/' + relID + '/changes')
            break
    }
}

// 行末主操作禁用态：availability 灰置（disabled+title 已派生）叠加防重入 pending
function actionDisabled(row: WorkspaceRow): boolean {
    const relID = row.workspace.relation.relation_id
    if (row.action.kind === 'prepare') return isPending('preparing', relID)
    if (row.action.kind === 'recover') return isPending('recovering', relID)
    if (row.action.kind === 'scan' || row.action.kind === 'rescan') {
        return row.action.disabled === true || isPending('scanning', relID)
    }
    return row.action.disabled === true
}

// 列表头（骨架与数据表共用同一组表头；六列对照原型 W-01/W-03，行操作列
// 表头留空，基线/扫描状态列按票 #104 取消——基线归 #105 变化页指标条）
const cols: { key: string; alignRight?: boolean }[] = [
    { key: 'workspaces.colWorkspace' },
    { key: 'workspaces.colHealth' },
    { key: 'workspaces.colDiff' },
    { key: 'workspaces.colTask' },
    { key: 'workspaces.colActivity' },
    { key: '', alignRight: true },
]
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <div class="flex items-center justify-between gap-4">
            <div>
                <h1 class="page-title">{{ t('workspaces.title') }}</h1>
                <p class="text-muted-foreground mt-1 text-sm">{{ t('workspaces.subtitle') }}</p>
            </div>
            <Button @click="router.push('/workspaces/new')">{{ t('workspaces.new') }}</Button>
        </div>

        <!-- watch 状态横幅（契约 07 §6，票 #92）：paused/unavailable 才渲染，
             active 不渲染；横幅在 bootstrap 错误卡与工作区表之间常驻展示 -->
        <Card v-for="b in watchBanners" :key="b.relationID">
            <CardContent class="flex items-center gap-3 py-3">
                <span
                    :class="b.paused ? 'text-amber-600 dark:text-amber-400' : 'text-destructive'"
                    class="text-sm"
                >
                    {{ b.name }}：{{ t(b.paused ? 'workspaces.watchPausedBanner' : 'workspaces.watchUnavailableBanner') }}
                </span>
            </CardContent>
        </Card>

        <!-- bootstrap 失败：统一错误态 + 重试（走同一管线） -->
        <Card v-if="bootstrapError">
            <CardContent class="flex items-center justify-between gap-3 py-4">
                <span class="text-destructive text-sm">{{ t('workspaces.errorTitle') }}：{{ bootstrapError }}</span>
                <Button variant="outline" size="sm" @click="retryBootstrap">{{ t('workspaces.retry') }}</Button>
            </CardContent>
        </Card>

        <Card v-else>
            <CardContent class="py-2">
                <Table>
                    <TableHeader>
                        <TableRow>
                            <TableHead v-for="c in cols" :key="c.key" :class="c.alignRight ? 'text-right' : ''">
                                {{ c.key ? t(c.key) : '' }}
                            </TableHead>
                        </TableRow>
                    </TableHeader>
                    <TableBody>
                        <!-- bootstrap 骨架：保留标题/表头，5 行同构骨架（UX 原型 W-01） -->
                        <template v-if="!bootstrapped">
                            <TableRow v-for="i in 5" :key="i">
                                <TableCell v-for="c in cols" :key="c.key">
                                    <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                </TableCell>
                            </TableRow>
                        </template>

                        <!-- 空态：唯一动作新建工作区（UX 原型 W-02） -->
                        <TableRow v-else-if="!rows.length">
                            <TableCell :colspan="cols.length">
                                <div class="flex flex-col items-center gap-3 py-10">
                                    <p class="text-muted-foreground text-sm">{{ t('workspaces.emptyTitle') }}</p>
                                    <p class="text-muted-foreground text-xs">{{ t('workspaces.emptyHint') }}</p>
                                    <Button @click="router.push('/workspaces/new')">{{ t('workspaces.new') }}</Button>
                                </div>
                            </TableCell>
                        </TableRow>

                        <!-- 列表：六列骨架 + 行末唯一主操作（UX 原型 W-03，票 #104）；
                             recovery_required 行不设行点击，其余行点击进变化页 -->
                        <template v-else>
                            <TableRow
                                v-for="row in rows"
                                :key="row.workspace.relation.relation_id"
                                :class="row.recoveryRequired ? '' : 'cursor-pointer'"
                                @click="openWorkspace(row)"
                            >
                                <TableCell>
                                    <div class="flex items-center gap-2">
                                        <div class="flex items-center gap-1.5 font-medium">
                                            {{ row.workspace.relation.project.display_name }}
                                            <ArrowLeftRight class="text-muted-foreground size-3.5" />
                                            {{ row.workspace.relation.runtime.display_name }}
                                        </div>
                                        <!-- 待确认角标（契约 07 §6，票 #86）：pending_plan_id
                                             数据源，「有待确认计划」直达计划页（阻断行点击冒泡） -->
                                        <button
                                            v-if="row.pendingPlanID"
                                            type="button"
                                            class="cursor-pointer"
                                            :title="t('workspaces.pendingPlanBadge')"
                                            @click.stop="openPendingPlan(row)"
                                        >
                                            <Badge variant="st-warn">
                                                {{ t('workspaces.pendingPlanBadge') }}
                                            </Badge>
                                        </button>
                                    </div>
                                    <div
                                        class="text-muted-foreground max-w-96 truncate text-xs"
                                        :title="row.workspace.relation.project.root_path + ' ↔ ' + row.workspace.relation.runtime.root_path"
                                    >
                                        {{ row.workspace.relation.project.root_path }} ↔
                                        {{ row.workspace.relation.runtime.root_path }}
                                    </div>
                                </TableCell>
                                <TableCell>
                                    <Badge :variant="row.healthTone.variant" :class="row.healthTone.class">
                                        {{ t('workspaces.health.' + row.workspace.relation.health) }}
                                    </Badge>
                                </TableCell>
                                <TableCell>
                                    <Badge :variant="row.diffTone.variant" :class="row.diffTone.class">
                                        {{ t('workspaces.diffState.' + row.workspace.state.diff_state) }}
                                    </Badge>
                                </TableCell>
                                <TableCell>
                                    <!-- 任务列（原型 .task-cell）：类型徽章（呼吸动画）+
                                         90px 细进度条 + 百分比；无任务显「—」 -->
                                    <div v-if="row.task" class="flex items-center gap-2">
                                        <Badge variant="st-run" pulse>{{ taskKindLabel(row.task) }}</Badge>
                                        <Progress
                                            :model-value="taskPercent(row.task) ?? 0"
                                            class="h-[5px] w-[90px] rounded-[3px] bg-surface-3"
                                        />
                                        <span
                                            v-if="taskPercent(row.task) !== null"
                                            class="text-muted-foreground text-xs tabular-nums"
                                        >
                                            {{ taskPercent(row.task) }}%
                                        </span>
                                    </div>
                                    <span v-else class="text-muted-foreground">—</span>
                                </TableCell>
                                <TableCell class="text-muted-foreground text-xs">{{ row.activity }}</TableCell>
                                <TableCell class="text-right" @click.stop>
                                    <!-- 行末唯一主操作（UX 原型 §7.1 优先级链，票 #104）：
                                         七态各出唯一按钮，btn-primary/btn-tonal 两档强调；
                                         快速更新不占行末（#105 移入对象头「更多」菜单） -->
                                    <Button
                                        size="sm"
                                        class="h-7 px-2.5 text-xs"
                                        :variant="row.action.primary ? 'default' : 'secondary'"
                                        :class="row.action.primary ? '' : 'bg-tint-primary text-primary hover:brightness-105'"
                                        :disabled="actionDisabled(row)"
                                        :title="row.action.title"
                                        @click="runRowAction(row)"
                                    >
                                        {{ row.action.label }}
                                    </Button>
                                </TableCell>
                            </TableRow>
                        </template>
                    </TableBody>
                </Table>
            </CardContent>
        </Card>
    </div>
</template>
