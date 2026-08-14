<script setup lang="ts">
// 更新检查对话框：packwiz update 检查结果展示 + 应用全部 / 单 mod 更新。
// 打开即检查；应用（全部或单个）后自动重查以刷新列表。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PackwizService } from '../../../bindings/packgradle/internal/service'
import type { PackProject, UpdateCheckResult, ModUpdateInfo } from '../../../bindings/packgradle/internal/packwiz'
import { showSnackbar } from '../../stores/ui'
import { errText, displayText } from '../../utils/errors'

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
const updating = ref('') // 正在单更的 mod 名
const output = ref('')
const outputTitle = ref('')
const mutating = computed(() => applyingAll.value || updating.value !== '')
const busy = computed(() => checking.value || mutating.value)
let checkGeneration = 0
let checkPending = false

watch(
    () => [props.modelValue, props.project?.name] as const,
    ([open]) => {
        checkGeneration++
        if (!open) {
            checkPending = false
            result.value = null
            output.value = ''
            outputTitle.value = ''
            return
        }

        result.value = null
        output.value = ''
        outputTitle.value = ''
        void runCheck()
    },
)

async function runCheck() {
    const projectName = props.project?.name
    if (!projectName) return
    if (checking.value) {
        checkPending = true
        return
    }

    const generation = checkGeneration
    checking.value = true
    try {
        const next = await PackwizService.CheckUpdates(projectName)
        if (generation !== checkGeneration || !props.modelValue || props.project?.name !== projectName) return

        result.value = next
        const upd = next?.updates?.length ?? 0
        const err = next?.errors?.length ?? 0
        showSnackbar(
            err > 0 ? t('projects.checkDoneWithErrors', [upd, err]) : t('projects.checkDone', [upd]),
            err > 0 ? 'warning' : 'success',
        )
    } catch (e) {
        if (generation === checkGeneration) showSnackbar(errText(e), 'error')
    } finally {
        checking.value = false
        if (checkPending && props.modelValue) {
            checkPending = false
            void runCheck()
        }
    }
}

// update 输出中的 name 是显示名；packwiz 单 mod 更新要求 .pw.toml 文件名（mod id/slug）。
// 用父级项目数据把显示名反查为 mod id，显示名与 id 不一致时也能正确更新。
function modIDForUpdate(displayName: string): string {
    const mods = props.project?.mods ?? []
    const byName = mods.find(m => m.name === displayName)
    if (byName) return byName.id
    const byID = mods.find(m => m.id === displayName)
    return byID?.id ?? ''
}

async function applyAll() {
    if (!props.project || busy.value) return
    applyingAll.value = true
    try {
        const r = await PackwizService.UpdateMods(props.project.name, '')
        outputTitle.value = t('projects.updateOutputTitle')
        output.value = displayText(r.output || (r.ok ? t('projects.outputSuccess', ['packwiz update']) : t('projects.outputFailed')))
        emit('changed')
        await runCheck()
    } catch (e) {
        showSnackbar(errText(e), 'error')
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
    try {
        const r = await PackwizService.UpdateMods(props.project.name, modID)
        if (r && !r.ok) {
            showSnackbar(displayText(r.output || t('projects.outputFailed')), 'error')
        } else {
            showSnackbar(t('projects.updateOneDone', [u.name]), 'success')
        }
        emit('changed')
        await runCheck()
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        updating.value = ''
    }
}
</script>

<template>
    <v-dialog
        :model-value="modelValue"
        :persistent="mutating"
        max-width="680"
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
                    v-else-if="!checking && (result?.updates?.length ?? 0) === 0 && (result?.errors?.length ?? 0) === 0"
                    type="success"
                    variant="tonal"
                    density="compact"
                    class="mb-3"
                >
                    {{ t('projects.allUpToDate') }}
                </v-alert>

                <!-- 可更新列表：每项支持单 mod 更新 -->
                <v-list v-if="(result?.updates?.length ?? 0) > 0" density="compact" class="mb-3">
                    <v-list-subheader class="text-caption text-primary">
                        {{ t('projects.hasUpdates', [result?.updates?.length]) }}
                    </v-list-subheader>
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

                <!-- 失败 / 跳过列表 -->
                <v-list v-if="(result?.errors?.length ?? 0) > 0" density="compact" class="mb-3">
                    <v-list-subheader class="text-caption text-warning">
                        {{ t('projects.failedSkipped', [result?.errors?.length]) }}
                    </v-list-subheader>
                    <v-list-item v-for="e in result?.errors ?? []" :key="e.name + e.error">
                        <v-list-item-title class="text-caption">
                            {{ e.name }}：<span class="text-error">{{ displayText(e.error) }}</span>
                        </v-list-item-title>
                    </v-list-item>
                </v-list>

                <!-- 应用更新后的 CLI 输出 -->
                <template v-if="output">
                    <div class="text-caption text-medium-emphasis mb-1">{{ outputTitle }}</div>
                    <pre class="output-pre text-body-2">{{ output }}</pre>
                </template>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-spacer />
                <v-btn variant="text" :disabled="mutating" @click="emit('update:modelValue', false)">
                    {{ t('projects.close') }}
                </v-btn>
                <v-btn
                    v-if="(result?.updates?.length ?? 0) > 0"
                    color="primary"
                    variant="flat"
                    :loading="applyingAll"
                    :disabled="checking || updating !== ''"
                    @click="applyAll"
                >
                    {{ t('projects.applyAll') }}
                </v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>

<style scoped>
.output-pre {
    max-height: 200px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
    background: rgb(var(--v-theme-background));
    border: 1px solid rgba(255, 255, 255, 0.08);
    border-radius: 10px;
    padding: 12px;
}
</style>
