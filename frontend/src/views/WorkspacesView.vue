<script setup lang="ts">
// /workspaces：工作区列表（契约 04 缓存骨架 + UX 原型 §7.1）。
// 数据全部来自 stores/syncCache（查询 API 的投影）：bootstrap 首次填充前渲骨架，
// 事件/对账触发的受控重查原地更新，页面不做第二处数据获取、不订阅事件。
// 行操作由 features/availability 驱动渲染（契约 03 §2.1）：未注册的动作不出现入口。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { TaskDTO, WorkspaceDTO } from '../api'
import { bootstrapped, bootstrapError, retryBootstrap, tasks, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const router = useRouter()

// 正交状态徽标：ok=绿 / 进行中=secondary / 需注意=琥珀 / 异常=destructive（稳定状态色，
// 不使用整行鲜红背景——UX 原型 §7.1 状态画板）
interface BadgeTone {
    variant: 'default' | 'secondary' | 'destructive' | 'outline'
    class?: string
}
const OK: BadgeTone = { variant: 'outline', class: 'text-emerald-600 dark:text-emerald-400' }
const WARN: BadgeTone = { variant: 'outline', class: 'text-amber-600 dark:text-amber-400' }
const NEUTRAL: BadgeTone = { variant: 'outline' }
const BUSY: BadgeTone = { variant: 'secondary' }
const BAD: BadgeTone = { variant: 'destructive' }

const scanTones: Record<string, BadgeTone> = {
    ready: OK,
    scanning: BUSY,
    queued: BUSY,
    never_scanned: NEUTRAL,
    failed: BAD,
}
const baselineTones: Record<string, BadgeTone> = { ready: OK, stale: WARN, none: NEUTRAL }
const diffTones: Record<string, BadgeTone> = {
    clean: OK,
    dirty: BUSY,
    conflicted: BAD,
    initialization_required: WARN,
    unknown: NEUTRAL,
}
const healthTones: Record<string, BadgeTone> = {
    healthy: OK,
    endpoint_missing: WARN,
    rebind_required: WARN,
    recovery_required: BAD,
}

function toneOf(map: Record<string, BadgeTone>, value: string): BadgeTone {
    return map[value] ?? NEUTRAL
}

// —— 行视图模型：缓存每次提交后统一派生，模板不重复取值 ——
interface WorkspaceRow {
    workspace: WorkspaceDTO
    task: TaskDTO | null
    canScan: boolean
    scanLabel: string
    healthTone: BadgeTone
    scanTone: BadgeTone
    baselineTone: BadgeTone
    diffTone: BadgeTone
    activity: string
}

const rows = computed<WorkspaceRow[]>(() =>
    workspaces.value.map(w => {
        const task = (w.state.active_task_id ? tasks.value.get(w.state.active_task_id) : null) ?? null
        return {
            workspace: w,
            task,
            // 能力驱动入口（契约 03 §2.1）：features.scan 且 availability 可用才渲染扫描入口；
            // prepare_sync/rebind 的承接页随 T09–T12 落地后再由同一 availability 机制点亮
            canScan: w.features.scan && w.availability?.some(a => a.action === 'scan' && a.available) === true,
            scanLabel: w.state.scan_state === 'failed' ? t('workspaces.scanRetryAction') : t('workspaces.scanAction'),
            healthTone: toneOf(healthTones, w.relation.health),
            scanTone: toneOf(scanTones, w.state.scan_state),
            baselineTone: toneOf(baselineTones, w.state.baseline_state),
            diffTone: toneOf(diffTones, w.state.diff_state),
            activity: lastActivity(w),
        }
    }),
)

function taskProgress(task: TaskDTO): string {
    if (task.total <= 0) return ''
    return Math.min(100, Math.round((task.completed / task.total) * 100)) + '%'
}

function taskLabel(task: TaskDTO): string {
    return task.message_key ? t(task.message_key, task.message_args ?? []) : t('workspaces.taskKind.' + task.kind)
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
// withPending 统一防重入：同 id 动作进行中禁点，结束后释放
const pending = ref({ scanning: new Set<string>(), cancelling: new Set<string>() })

function isPending(kind: 'scanning' | 'cancelling', id: string): boolean {
    return pending.value[kind].has(id)
}

async function withPending(kind: 'scanning' | 'cancelling', id: string, action: () => Promise<void>): Promise<void> {
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

function cancelTask(task: TaskDTO): void {
    void withPending('cancelling', task.task_id, async () => {
        try {
            await SyncService.CancelTask(task.task_id)
            triggerRequery()
        } catch (e) {
            showSnackbar(errText(e), 'error')
        }
    })
}

// 列表头（骨架与数据表共用同一组表头）
const cols: { key: string; alignRight?: boolean }[] = [
    { key: 'workspaces.colWorkspace' },
    { key: 'workspaces.colHealth' },
    { key: 'workspaces.colScan' },
    { key: 'workspaces.colBaseline' },
    { key: 'workspaces.colDiff' },
    { key: 'workspaces.colTask' },
    { key: 'workspaces.colActivity' },
    { key: 'workspaces.colActions', alignRight: true },
]
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <div class="flex items-center justify-between gap-4">
            <div>
                <h1 class="text-xl font-semibold">{{ t('workspaces.title') }}</h1>
                <p class="text-muted-foreground mt-1 text-sm">{{ t('workspaces.subtitle') }}</p>
            </div>
            <Button @click="router.push('/workspaces/new')">{{ t('workspaces.new') }}</Button>
        </div>

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
                                {{ t(c.key) }}
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

                        <!-- 列表：正交状态 + 能力驱动行操作（UX 原型 W-03） -->
                        <template v-else>
                            <TableRow v-for="row in rows" :key="row.workspace.relation.relation_id">
                                <TableCell>
                                    <div class="font-medium">
                                        {{ row.workspace.relation.project.display_name }}
                                        <span class="text-muted-foreground">↔</span>
                                        {{ row.workspace.relation.runtime.display_name }}
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
                                    <Badge :variant="row.scanTone.variant" :class="row.scanTone.class">
                                        {{ t('workspaces.scanState.' + row.workspace.state.scan_state) }}
                                    </Badge>
                                </TableCell>
                                <TableCell>
                                    <Badge :variant="row.baselineTone.variant" :class="row.baselineTone.class">
                                        {{ t('workspaces.baselineState.' + row.workspace.state.baseline_state) }}
                                    </Badge>
                                </TableCell>
                                <TableCell>
                                    <Badge :variant="row.diffTone.variant" :class="row.diffTone.class">
                                        {{ t('workspaces.diffState.' + row.workspace.state.diff_state) }}
                                    </Badge>
                                </TableCell>
                                <TableCell>
                                    <div v-if="row.task" class="flex flex-col">
                                        <span class="text-sm">{{ taskLabel(row.task) }}</span>
                                        <span v-if="taskProgress(row.task)" class="text-muted-foreground text-xs">
                                            {{ taskProgress(row.task) }}
                                        </span>
                                    </div>
                                    <span v-else class="text-muted-foreground">—</span>
                                </TableCell>
                                <TableCell class="text-muted-foreground text-xs">{{ row.activity }}</TableCell>
                                <TableCell>
                                    <div class="flex justify-end gap-2">
                                        <!-- 变更浏览入口：两侧快照齐备（scan ready）才可读时计算 diff -->
                                        <Button
                                            v-if="row.workspace.state.scan_state === 'ready'"
                                            size="xs"
                                            variant="outline"
                                            @click="router.push('/workspaces/' + row.workspace.relation.relation_id + '/changes')"
                                        >
                                            {{ t('workspaces.changesAction') }}
                                        </Button>
                                        <Button
                                            v-if="row.canScan"
                                            size="xs"
                                            variant="outline"
                                            :disabled="isPending('scanning', row.workspace.relation.relation_id)"
                                            @click="startScan(row)"
                                        >
                                            {{ row.scanLabel }}
                                        </Button>
                                        <Button
                                            v-if="row.task?.can_cancel"
                                            size="xs"
                                            variant="ghost"
                                            :disabled="isPending('cancelling', row.task.task_id)"
                                            @click="cancelTask(row.task)"
                                        >
                                            {{ t('workspaces.cancelTask') }}
                                        </Button>
                                    </div>
                                </TableCell>
                            </TableRow>
                        </template>
                    </TableBody>
                </Table>
            </CardContent>
        </Card>
    </div>
</template>
