<script setup lang="ts">
// 更新检查对话框：页签化结果（可更新 / 失败跳过）+ 应用全部（确认 → 任务中心）+ 单 mod 更新。
// 打开即检查；应用（全部或单个）后自动重查刷新列表。CLI 输出收进折叠区，失败自动展开。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PackwizService } from '../../api'
import type { PackProject, UpdateCheckResult, ModUpdateInfo } from '../../../bindings/packgradle/internal/packwiz'
import { runTask } from '../../stores/taskCenter'
import { showSnackbar } from '../../stores/ui'
import { errText, displayText } from '../../utils/errors'
import ConfirmDialog from '../common/ConfirmDialog.vue'

const { t } = useI18n()

const props = defineProps<{
    modelValue: boolean
    project: PackProject | null
}>()

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
    /** 应用了更新（全部或单个），父级应刷新项目数据 */
    (e: 'changed'): void
}>()

const result = ref<UpdateCheckResult | null>(null)
const checking = ref(false)
const applyingAll = ref(false)
const updating = ref('') // 正在单更的 mod id
const applyAllConfirm = ref(false)
const applyAllError = ref('')
const outputOpen = ref(false)
let output = ''
const tab = ref<'updates' | 'errors'>('updates')
const mutating = computed(() => applyingAll.value || updating.value !== '')
const busy = computed(() => checking.value || mutating.value)
let checkGeneration = 0
let checkingGeneration = -1

watch(
    () => [props.modelValue, props.project?.name] as const,
    ([open]) => {
        checkGeneration++
        if (!open) {
            result.value = null
            output = ''
            outputOpen.value = false
            applyAllError.value = ''
            return
        }
        result.value = null
        output = ''
        outputOpen.value = false
        tab.value = 'updates'
        void runCheck()
    },
)

async function runCheck(propagateError = false) {
    const projectName = props.project?.name
    if (!projectName) return
    const generation = checkGeneration
    if (checking.value && checkingGeneration === generation) return
    checkingGeneration = generation
    checking.value = true
    try {
        const next = await PackwizService.CheckUpdates(projectName)
        if (generation !== checkGeneration || !props.modelValue || props.project?.name !== projectName) return
        result.value = next
        const upd = next?.updates?.length ?? 0
        const err = next?.errors?.length ?? 0
        if (upd === 0 && err > 0) tab.value = 'errors'
        showSnackbar(
            err > 0 ? t('projects.checkDoneWithErrors', [upd, err]) : t('projects.checkDone', [upd]),
            err > 0 ? 'warning' : 'success',
        )
    } catch (e) {
        if (propagateError) throw e
        if (generation === checkGeneration) showSnackbar(errText(e), 'error')
    } finally {
        if (checkingGeneration === generation) checking.value = false
    }
}

// update 输出中的 name 是显示名；单 mod 更新要求 pw.toml 文件名（mod id）
function modIDForUpdate(displayName: string): string {
    const mods = props.project?.mods ?? []
    const byName = mods.find(m => m.name === displayName)
    if (byName) return byName.id
    const byID = mods.find(m => m.id === displayName)
    return byID?.id ?? ''
}

function askApplyAll() {
    applyAllError.value = ''
    applyAllConfirm.value = true
}

async function confirmApplyAll() {
    if (!props.project || busy.value) return
    applyingAll.value = true
    applyAllError.value = ''
    let refreshFailed = false
    try {
        const result = await runTask({
            title: t('tasks.updateAll', [props.project.name]),
            kind: 'update',
            run: async () => {
                const r = await PackwizService.UpdateMods(props.project!.name, '')
                output = r.output
                if (!r.ok) throw new Error(displayText(r.output || t('projects.outputFailed')))
                emit('changed')
                try {
                    await runCheck(true)
                } catch (e) {
                    refreshFailed = true
                    showSnackbar(errText(e), 'warning')
                }
                return t('projects.outputSuccess', ['packwiz update'])
            },
            output: () => output,
            warn: () => refreshFailed,
            onError: message => (applyAllError.value = message),
        })
        if (result !== null) applyAllConfirm.value = false
    } finally {
        applyingAll.value = false
    }
}

async function updateOne(u: ModUpdateInfo) {
    if (!props.project || busy.value) return
    const modID = modIDForUpdate(u.name)
    if (!modID) {
        showSnackbar(t('projects.updateOneNotFound', [u.name]), 'warning')
        return
    }
    updating.value = modID
    let refreshFailed = false
    try {
        await runTask({
            title: t('tasks.updateOne', [u.name]),
            kind: 'update',
            run: async () => {
                const r = await PackwizService.UpdateMods(props.project!.name, modID)
                output = r.output
                if (!r.ok) throw new Error(displayText(r.output || t('projects.outputFailed')))
                emit('changed')
                try {
                    await runCheck(true)
                } catch (e) {
                    refreshFailed = true
                    showSnackbar(errText(e), 'warning')
                }
                return t('projects.updateOneDone', [u.name])
            },
            output: () => output,
            warn: () => refreshFailed,
        })
    } finally {
        updating.value = ''
    }
}

const updateCount = computed(() => result.value?.updates?.length ?? 0)
const errorCount = computed(() => result.value?.errors?.length ?? 0)
</script>

<template>
    <v-dialog
        :model-value="modelValue"
        :persistent="mutating"
        max-width="720"
        @update:model-value="emit('update:modelValue', $event)"
    >
        <v-card class="dialog-card" elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-update" color="primary" class="mr-2" />
                {{ t('projects.checkDialogTitle') }}
                <v-chip v-if="project" size="x-small" variant="tonal" class="ml-2">{{ project.name }}</v-chip>
            </v-card-title>
            <v-card-text>
                <v-progress-linear v-if="checking" indeterminate class="mb-3" />

                <v-alert v-if="!checking && !result?.ok" type="error" variant="tonal" density="compact" class="mb-3">
                    {{ t('projects.checkFailed') }}
                </v-alert>
                <v-alert
                    v-else-if="!checking && updateCount === 0 && errorCount === 0"
                    type="success"
                    variant="tonal"
                    density="compact"
                    class="mb-3"
                >
                    {{ t('projects.allUpToDate') }}
                </v-alert>

                <template v-if="!checking && result?.ok && (updateCount > 0 || errorCount > 0)">
                    <v-tabs v-model="tab" density="compact" color="primary" class="mb-3">
                        <v-tab value="updates">
                            {{ t('projects.tabUpdates') }}
                            <v-chip size="x-small" variant="tonal" class="ml-2">{{ updateCount }}</v-chip>
                        </v-tab>
                        <v-tab value="errors" :disabled="errorCount === 0">
                            {{ t('projects.tabErrors') }}
                            <v-chip size="x-small" variant="tonal" class="ml-2">{{ errorCount }}</v-chip>
                        </v-tab>
                    </v-tabs>

                    <v-window v-model="tab">
                        <v-window-item value="updates">
                            <v-list v-if="updateCount > 0" density="compact" class="mb-3">
                                <v-list-item v-for="u in result?.updates ?? []" :key="u.name">
                                    <v-list-item-title class="text-body-2">{{ u.name }}</v-list-item-title>
                                    <v-list-item-subtitle class="text-caption">
                                        {{ u.current_file }}
                                        <v-icon icon="mdi-arrow-right" size="x-small" />
                                        <span class="text-primary">{{ u.latest_file }}</span>
                                    </v-list-item-subtitle>
                                    <template #append>
                                        <v-btn
                                            size="small"
                                            variant="tonal"
                                            color="primary"
                                            :loading="updating === modIDForUpdate(u.name)"
                                            :disabled="checking || applyingAll || (updating !== '' && updating !== modIDForUpdate(u.name))"
                                            @click="updateOne(u)"
                                        >
                                            {{ t('projects.updateOne') }}
                                        </v-btn>
                                    </template>
                                </v-list-item>
                            </v-list>
                            <div v-else class="text-body-2 text-medium-emphasis py-4 text-center">
                                {{ t('projects.allUpToDate') }}
                            </div>
                        </v-window-item>

                        <v-window-item value="errors">
                            <v-list density="compact" class="mb-3">
                                <v-list-item v-for="e in result?.errors ?? []" :key="e.name + e.error">
                                    <v-list-item-title class="text-caption">
                                        {{ e.name }}：<span class="text-error">{{ displayText(e.error) }}</span>
                                    </v-list-item-title>
                                </v-list-item>
                            </v-list>
                        </v-window-item>
                    </v-window>
                </template>

                <!-- CLI 输出折叠区（失败时自动展开） -->
                <div v-if="output" class="mt-2">
                    <v-btn size="small" variant="text" @click="outputOpen = !outputOpen">
                        {{ outputOpen ? t('tasks.hideOutput') : t('tasks.showOutput') }}
                    </v-btn>
                    <pre v-if="outputOpen" class="output-pre text-body-2 mt-1">{{ output }}</pre>
                </div>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-spacer />
                <v-btn variant="text" :disabled="mutating" @click="emit('update:modelValue', false)">
                    {{ t('projects.close') }}
                </v-btn>
                <v-btn
                    v-if="updateCount > 0"
                    color="primary"
                    variant="flat"
                    :loading="applyingAll"
                    :disabled="checking || updating !== ''"
                    @click="askApplyAll"
                >
                    {{ t('projects.applyAll') }}（{{ updateCount }}）
                </v-btn>
            </v-card-actions>
        </v-card>

        <!-- 应用全部确认：后果列表四要素 -->
        <ConfirmDialog
            v-model="applyAllConfirm"
            :title="t('projects.applyAllConfirmTitle')"
            :text="t('projects.applyAllConfirmText', [project?.name ?? '', updateCount])"
            :consequences="[t('projects.applyAllC1'), t('projects.applyAllC2'), t('projects.applyAllC3')]"
            :confirm-text="t('projects.applyAll')"
            icon="mdi-update"
            icon-color="primary"
            :loading="applyingAll"
            :error="applyAllError"
            @confirm="confirmApplyAll"
        />
    </v-dialog>
</template>

<style scoped>
.output-pre {
    max-height: 200px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
    padding: 12px;
}
</style>
