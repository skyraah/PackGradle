<script setup lang="ts">
// 任务中心抽屉：操作历史（进度/结果/输出）统一追溯入口。
// 顶栏 bell 触发；执行中任务实时显示进度条，完成后驻留可展开输出。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { taskList, runningCount, unseenCount, markAllSeen, clearFinished } from '../../stores/taskCenter'
import type { TaskItem, TaskKind, TaskStatus } from '../../stores/taskCenter'
import { showSnackbar } from '../../stores/ui'

const { t } = useI18n()

const open = defineModel<boolean>({ default: false })

const onlyFailed = ref(false)

const shown = computed(() => (onlyFailed.value ? taskList.value.filter(t => t.status === 'error') : taskList.value))

const kindIcons: Record<TaskKind, string> = {
    refresh: 'mdi-refresh',
    fetch: 'mdi-cloud-download-outline',
    update: 'mdi-update',
    meta: 'mdi-swap-horizontal',
    link: 'mdi-link-variant',
    import: 'mdi-folder-open',
    remove: 'mdi-delete-outline',
    config: 'mdi-cog-outline',
    other: 'mdi-play-circle-outline',
}

function statusChip(s: TaskStatus): { color: string; label: string } {
    switch (s) {
        case 'running':
            return { color: 'info', label: t('tasks.statusRunning') }
        case 'success':
            return { color: 'success', label: t('tasks.statusSuccess') }
        case 'warning':
            return { color: 'warning', label: t('tasks.statusWarning') }
        default:
            return { color: 'error', label: t('tasks.statusError') }
    }
}

function timeText(d: Date): string {
    return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
}

const expanded = ref<Set<number>>(new Set())

function toggleExpand(task: TaskItem) {
    const next = new Set(expanded.value)
    if (next.has(task.id)) next.delete(task.id)
    else next.add(task.id)
    expanded.value = next
}

async function copyOutput(task: TaskItem) {
    try {
        await navigator.clipboard.writeText(task.output || task.resultText)
        showSnackbar(t('common.copied'), 'success')
    } catch {
        showSnackbar(t('common.copyFailed'), 'warning')
    }
}

function onOpen(v: boolean) {
    open.value = v
    if (v) markAllSeen()
}
</script>

<template>
    <v-navigation-drawer
        :model-value="open"
        location="right"
        width="380"
        temporary
        class="task-drawer"
        style="z-index: 2400"
        @update:model-value="onOpen"
    >
        <div class="d-flex align-center px-4 pt-4 pb-2">
            <v-icon icon="mdi-bell-outline" color="primary" class="mr-2" />
            <span class="text-subtitle-1 font-weight-bold">{{ t('tasks.title') }}</span>
            <v-chip v-if="runningCount > 0" size="x-small" color="info" variant="flat" class="ml-2">
                {{ t('tasks.running', [runningCount]) }}
            </v-chip>
            <v-spacer />
            <v-btn icon="mdi-close" size="small" variant="text" @click="open = false" />
        </div>

        <div class="d-flex align-center px-4 pb-2 ga-2">
            <v-btn
                size="small"
                :variant="onlyFailed ? 'flat' : 'tonal'"
                :color="onlyFailed ? 'error' : undefined"
                @click="onlyFailed = !onlyFailed"
            >
                {{ t('tasks.onlyFailed') }}
            </v-btn>
            <v-spacer />
            <v-btn size="small" variant="text" :disabled="taskList.length === 0" @click="clearFinished">
                {{ t('tasks.clearDone') }}
            </v-btn>
        </div>

        <v-divider />

        <div class="task-list px-3 py-3">
            <div v-if="shown.length === 0" class="text-body-2 text-medium-emphasis text-center py-10">
                <v-icon icon="mdi-bell-sleep-outline" size="44" class="mb-2 d-block mx-auto" />
                {{ onlyFailed ? t('tasks.noFailed') : t('tasks.empty') }}
            </div>

            <div v-for="task in shown" :key="task.id" class="task-card" :class="'task-' + task.status">
                <div class="d-flex align-center">
                    <v-avatar size="32" rounded="md" :color="statusChip(task.status).color" variant="tonal" class="mr-3">
                        <v-icon :icon="kindIcons[task.kind]" size="18" />
                    </v-avatar>
                    <div class="flex-grow-1" style="min-width: 0">
                        <div class="text-body-2 font-weight-medium task-title">{{ task.title }}</div>
                        <div class="text-caption text-medium-emphasis">
                            {{ timeText(task.startedAt) }}
                            <template v-if="task.status === 'running' && task.stepText"> · {{ task.stepText }}</template>
                        </div>
                    </div>
                    <v-chip size="x-small" :color="statusChip(task.status).color" variant="tonal">
                        {{ statusChip(task.status).label }}
                    </v-chip>
                </div>

                <v-progress-linear
                    v-if="task.status === 'running'"
                    :model-value="task.progress * 100"
                    :indeterminate="task.progress <= 0"
                    color="primary"
                    height="4"
                    rounded
                    class="mt-2"
                />

                <template v-else>
                    <div class="text-caption mt-2 task-result" :class="task.status === 'error' ? 'text-error' : ''">
                        {{ task.resultText }}
                    </div>
                    <div v-if="task.output" class="d-flex align-center mt-1">
                        <v-btn size="x-small" variant="text" @click="toggleExpand(task)">
                            {{ expanded.has(task.id) ? t('tasks.hideOutput') : t('tasks.showOutput') }}
                        </v-btn>
                        <v-btn size="x-small" variant="text" icon="mdi-content-copy" :title="t('common.copyOutput')" @click="copyOutput(task)" />
                    </div>
                    <pre v-if="task.output && expanded.has(task.id)" class="output-pre text-caption mt-1">{{ task.output }}</pre>
                </template>
            </div>
        </div>
    </v-navigation-drawer>
</template>

<style scoped>
.task-drawer {
    border-left: 1px solid var(--pg-border) !important;
}
.task-list {
    height: calc(100% - 108px);
    overflow-y: auto;
}
.task-card {
    padding: 12px;
    border: 1px solid var(--pg-border);
    border-radius: 10px;
    margin-bottom: 10px;
    background: var(--pg-layer);
}
.task-card.task-error {
    border-left: 3px solid rgb(var(--v-theme-error));
}
.task-card.task-warning {
    border-left: 3px solid rgb(var(--v-theme-warning));
}
.task-card.task-success {
    border-left: 3px solid rgb(var(--v-theme-success));
}
.task-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.task-result {
    word-break: break-all;
}
</style>
