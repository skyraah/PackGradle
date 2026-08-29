<script setup lang="ts">
// 任务中心抽屉（shadcn-vue Sheet；UX 原型 §5.3）：后端任务投影。
// 数据全部来自 stores/syncCache（查询 API 的投影）：ListTasks(active) 缓存 +
// task_updated 事件经 GetTask 重读；这里不做第二处数据获取、不订阅事件。
// 顶部徽标 = 活跃任务数；条目内联取消与「查看工作区」上下文动作。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { TaskDTO, WorkspaceDTO } from '../../../bindings/packgradle/internal/transport/models'
import { tasks, workspaces, triggerRequery } from '../../stores/syncCache'
import { SyncService } from '../../api'
import { showSnackbar } from '../../stores/ui'
import { errText } from '../../utils/errors'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
    Sheet,
    SheetContent,
    SheetDescription,
    SheetHeader,
    SheetTitle,
} from '@/components/ui/sheet'

const { t } = useI18n()
const router = useRouter()

const open = defineModel<boolean>({ default: false })

// 按创建时间倒序（最新在前）
const list = computed(() => [...tasks.value.values()].sort((a, b) => b.created_at.localeCompare(a.created_at)))

const runningCount = computed(() => list.value.filter(t => t.status === 'running' || t.status === 'queued').length)

function workspaceOf(task: TaskDTO): WorkspaceDTO | null {
    if (!task.relation_id) return null
    return workspaces.value.find(w => w.relation.relation_id === task.relation_id) ?? null
}

function workspaceLabel(task: TaskDTO): string {
    const w = workspaceOf(task)
    if (!w) return ''
    return w.relation.project.display_name + ' ↔ ' + w.relation.runtime.display_name
}

function kindLabel(task: TaskDTO): string {
    return t('workspaces.taskKind.' + task.kind)
}

function statusLabel(status: string): string {
    return t('tasks.status.' + status)
}

// 状态徽标：ok=绿 / 进行中=secondary / 需注意=琥珀 / 异常=destructive（与工作区列表同画板）
const statusTones: Record<string, { variant: 'default' | 'secondary' | 'destructive' | 'outline'; class?: string }> = {
    queued: { variant: 'secondary' },
    running: { variant: 'outline', class: 'text-primary' },
    succeeded: { variant: 'outline', class: 'text-emerald-600 dark:text-emerald-400' },
    failed: { variant: 'destructive' },
    cancelled: { variant: 'outline' },
    recovery_required: { variant: 'outline', class: 'text-amber-600 dark:text-amber-400' },
}

function statusTone(status: string) {
    return statusTones[status] ?? { variant: 'outline' }
}

function progress(task: TaskDTO): number | null {
    if (task.status !== 'running' || task.total <= 0) return null
    return Math.min(100, Math.round((task.completed / task.total) * 100))
}

function timeText(iso: string): string {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return ''
    return d.toLocaleTimeString()
}

const cancelling = ref(new Set<string>())

async function cancelTask(task: TaskDTO): Promise<void> {
    if (cancelling.value.has(task.task_id)) return
    const next = new Set(cancelling.value)
    next.add(task.task_id)
    cancelling.value = next
    try {
        await SyncService.CancelTask(task.task_id)
        triggerRequery()
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        const rest = new Set(cancelling.value)
        rest.delete(task.task_id)
        cancelling.value = rest
    }
}
</script>

<template>
    <Sheet v-model:open="open">
        <SheetContent side="right" class="flex w-[400px] flex-col gap-0 sm:max-w-[calc(100vw-68px)]">
            <SheetHeader class="flex-row items-center gap-2 border-b pb-3">
                <SheetTitle class="flex items-center gap-2">
                    {{ t('tasks.title') }}
                    <Badge v-if="runningCount > 0" variant="secondary">{{ t('tasks.running', [runningCount]) }}</Badge>
                </SheetTitle>
                <SheetDescription class="sr-only">{{ t('tasks.title') }}</SheetDescription>
            </SheetHeader>

            <div class="min-h-0 flex-1 overflow-y-auto p-3">
                <div v-if="list.length === 0" class="text-muted-foreground flex flex-col items-center gap-2 py-16 text-sm">
                    <p>{{ t('tasks.empty') }}</p>
                </div>

                <div
                    v-for="task in list"
                    :key="task.task_id"
                    class="bg-card mb-2.5 rounded-[10px] border p-3"
                    :class="{
                        'border-l-primary border-l-2': task.status === 'running' || task.status === 'queued',
                        'border-l-destructive border-l-2': task.status === 'failed',
                        'border-l-2 border-l-amber-500': task.status === 'recovery_required',
                    }"
                >
                    <div class="flex items-start gap-2.5">
                        <div class="min-w-0 flex-1">
                            <div class="truncate text-sm font-medium">{{ kindLabel(task) }}</div>
                            <div v-if="workspaceLabel(task)" class="text-muted-foreground mt-0.5 truncate text-xs">
                                {{ workspaceLabel(task) }}
                            </div>
                            <div class="text-muted-foreground mt-0.5 text-xs">{{ timeText(task.created_at) }}</div>
                        </div>
                        <Badge :variant="statusTone(task.status).variant" :class="statusTone(task.status).class">
                            {{ statusLabel(task.status) }}
                        </Badge>
                    </div>

                    <!-- 执行中：确定进度条或不确定动画；阶段文本随 message_key 展示 -->
                    <template v-if="task.status === 'running' || task.status === 'queued'">
                        <div v-if="progress(task) !== null" class="bg-primary/20 mt-2 h-1.5 w-full overflow-hidden rounded-full">
                            <div class="bg-primary h-full rounded-full transition-all" :style="{ width: progress(task) + '%' }" />
                        </div>
                        <div v-else class="bg-primary/20 mt-2 h-1.5 w-full overflow-hidden rounded-full">
                            <div class="bg-primary h-full w-1/3 animate-pulse rounded-full" />
                        </div>
                        <div v-if="task.message_key" class="text-muted-foreground mt-1.5 text-xs">
                            {{ t(task.message_key, task.message_args ?? []) }}
                        </div>
                        <div class="mt-2 flex justify-end gap-2">
                            <Button
                                v-if="task.can_cancel"
                                size="xs"
                                variant="ghost"
                                :disabled="cancelling.has(task.task_id)"
                                @click="cancelTask(task)"
                            >
                                {{ t('workspaces.cancelTask') }}
                            </Button>
                        </div>
                    </template>

                    <!-- 终态：结果消息 + 上下文动作 -->
                    <template v-else>
                        <div
                            v-if="task.message_key"
                            class="mt-1.5 text-xs"
                            :class="task.status === 'failed' ? 'text-destructive' : 'text-muted-foreground'"
                        >
                            {{ t(task.message_key, task.message_args ?? []) }}
                        </div>
                        <div v-if="task.relation_id" class="mt-2 flex justify-end gap-2">
                            <Button size="xs" variant="outline" @click="router.push('/workspaces/' + task.relation_id + '/changes')">
                                {{ t('tasks.viewWorkspace') }}
                            </Button>
                        </div>
                    </template>
                </div>
            </div>
        </SheetContent>
    </Sheet>
</template>
