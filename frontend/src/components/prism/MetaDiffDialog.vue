<script setup lang="ts">
// meta 差异对话框：三区展示（实例独有/项目独有/版本差异），全部可操作闭环。
// 单 mod 拉取保留确认；操作后经任务中心，拉取后自动 refresh + 全端缓存失效。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PackwizService, PrismService } from '../../api'
import type { MetaDiff } from '../../../bindings/packgradle/internal/prism'
import { bumpProjectsVersion, invalidateProjects } from '../../stores/projects'
import { loadOverview } from '../../stores/instances'
import { runTask } from '../../stores/taskCenter'
import { showSnackbar } from '../../stores/ui'
import { displayText, errText } from '../../utils/errors'
import ConfirmDialog from '../common/ConfirmDialog.vue'

const { t } = useI18n()

const props = defineProps<{
    modelValue: boolean
    project: string
}>()

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
}>()

const diff = ref<MetaDiff | null>(null)
const loading = ref(false)
const diffBusy = ref('')
const pullOneDialog = ref(false)
const pullOneTarget = ref('')
const pullOneError = ref('')

watch(
    () => props.modelValue,
    open => {
        if (open) void refreshDiff()
    },
)

async function refreshDiff(propagateError = false) {
    loading.value = true
    try {
        diff.value = await PrismService.MetaDiff(props.project)
    } catch (e) {
        if (propagateError) throw e
        showSnackbar(errText(e))
    } finally {
        loading.value = false
    }
}

function askPullOne(id: string) {
    pullOneTarget.value = id
    pullOneError.value = ''
    pullOneDialog.value = true
}

async function confirmPullOne() {
    const id = pullOneTarget.value
    if (!id || diffBusy.value) return
    diffBusy.value = id
    pullOneError.value = ''
    let refreshFailed = false
    try {
        const result = await runTask({
            title: t('tasks.metaPullOne', [id]),
            kind: 'meta',
            run: async () => {
                await PrismService.PullMeta(props.project, id)
                try {
                    const refreshed = await PackwizService.RefreshProject(props.project)
                    if (!refreshed.ok) throw new Error(displayText(refreshed.output))
                    await loadOverview(true)
                    await refreshDiff(true)
                } catch (e) {
                    refreshFailed = true
                    showSnackbar(errText(e), 'warning')
                }
                bumpProjectsVersion()
                invalidateProjects()
                return t('prism.metaOneDone', [t('prism.metaPullOne'), id])
            },
            warn: () => refreshFailed,
            onError: message => (pullOneError.value = message),
        })
        if (result !== null) {
            pullOneDialog.value = false
            pullOneTarget.value = ''
        }
    } finally {
        diffBusy.value = ''
    }
}

async function pushOne(id: string) {
    diffBusy.value = id
    let refreshFailed = false
    try {
        await runTask({
            title: t('tasks.metaPushOne', [id]),
            kind: 'meta',
            run: async () => {
                await PrismService.PushMeta(props.project, id)
                try {
                    await refreshDiff(true)
                } catch (e) {
                    refreshFailed = true
                    showSnackbar(errText(e), 'warning')
                }
                return t('prism.metaOneDone', [t('prism.metaPushOne'), id])
            },
            warn: () => refreshFailed,
        })
    } finally {
        diffBusy.value = ''
    }
}

function diffFetchedText(): string {
    const ts = diff.value?.fetched_at
    if (!ts) return ''
    return t('prism.metaFetchedAt', [ts])
}

const diffInstanceOnly = computed(() => diff.value?.instance_only ?? [])
const diffProjectOnly = computed(() => diff.value?.project_only ?? [])
const diffVersionDiff = computed(() => diff.value?.version_diff ?? [])
const hasDiff = computed(
    () => diffInstanceOnly.value.length > 0 || diffProjectOnly.value.length > 0 || diffVersionDiff.value.length > 0,
)
</script>

<template>
    <v-dialog :model-value="modelValue" max-width="680" @update:model-value="emit('update:modelValue', $event)">
        <v-card class="dialog-card" elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-compare-horizontal" color="primary" class="mr-2" />
                {{ t('prism.metaDiffTitle') }}
                <v-chip size="x-small" variant="tonal" class="ml-2">{{ project }}</v-chip>
                <v-spacer />
                <v-chip v-if="diffFetchedText()" size="x-small" variant="tonal">{{ diffFetchedText() }}</v-chip>
                <v-btn icon="mdi-refresh" size="small" variant="text" :loading="loading" :title="t('prism.diffRefresh')" @click="refreshDiff" />
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-2">{{ t('prism.metaDiffHint') }}</div>
                <v-progress-linear v-if="loading" indeterminate class="mb-3" />
                <template v-else>
                    <div v-if="hasDiff">
                        <!-- 实例独有：可拉取 -->
                        <v-list-subheader v-if="diffInstanceOnly.length > 0" class="text-caption text-primary">
                            {{ t('prism.metaDiffInstanceOnly') }}（{{ diffInstanceOnly.length }}）
                        </v-list-subheader>
                        <v-list-item v-for="id in diffInstanceOnly" :key="'i' + id" density="compact" :title="id">
                            <template #append>
                                <v-btn
                                    size="small"
                                    variant="tonal"
                                    :loading="diffBusy === id"
                                    :disabled="diffBusy !== ''"
                                    @click="askPullOne(id)"
                                >
                                    {{ t('prism.metaPullOne') }}
                                </v-btn>
                            </template>
                        </v-list-item>

                        <!-- 项目独有：可推送 -->
                        <v-list-subheader v-if="diffProjectOnly.length > 0" class="text-caption text-success">
                            {{ t('prism.metaDiffProjectOnly') }}（{{ diffProjectOnly.length }}）
                        </v-list-subheader>
                        <v-list-item v-for="id in diffProjectOnly" :key="'p' + id" density="compact" :title="id">
                            <template #append>
                                <v-btn
                                    size="small"
                                    variant="tonal"
                                    :loading="diffBusy === id"
                                    :disabled="diffBusy !== ''"
                                    @click="pushOne(id)"
                                >
                                    {{ t('prism.metaPushOne') }}
                                </v-btn>
                            </template>
                        </v-list-item>

                        <!-- 版本差异：可操作（拉取以实例为准 / 推送以项目为准） -->
                        <v-list-subheader v-if="diffVersionDiff.length > 0" class="text-caption text-warning">
                            {{ t('prism.metaDiffVersionDiff') }}（{{ diffVersionDiff.length }}）
                        </v-list-subheader>
                        <v-list-item
                            v-for="v in diffVersionDiff"
                            :key="'v' + v.id"
                            density="compact"
                            :title="v.id"
                            :subtitle="t('prism.metaVersionDiffText', [v.project_version, v.instance_version])"
                        >
                            <template #append>
                                <v-btn
                                    size="small"
                                    variant="text"
                                    class="mr-1"
                                    prepend-icon="mdi-arrow-down-bold-outline"
                                    :loading="diffBusy === v.id"
                                    :disabled="diffBusy !== ''"
                                    :title="t('prism.metaPullTip')"
                                    @click="askPullOne(v.id)"
                                >
                                    {{ t('prism.metaPullOne') }}
                                </v-btn>
                                <v-btn
                                    size="small"
                                    variant="text"
                                    prepend-icon="mdi-arrow-up-bold-outline"
                                    :loading="diffBusy === v.id"
                                    :disabled="diffBusy !== ''"
                                    :title="t('prism.metaPushTip')"
                                    @click="pushOne(v.id)"
                                >
                                    {{ t('prism.metaPushOne') }}
                                </v-btn>
                            </template>
                        </v-list-item>
                    </div>
                    <div v-else class="text-body-2 text-medium-emphasis py-6 text-center">
                        <v-icon icon="mdi-check-circle-outline" color="success" size="32" class="mb-2" />
                        <div>{{ t('prism.metaDiffEmpty') }}</div>
                    </div>
                </template>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-spacer />
                <v-btn variant="tonal" @click="emit('update:modelValue', false)">{{ t('common.close') }}</v-btn>
            </v-card-actions>
        </v-card>

        <!-- 单 mod 拉取确认（后果说明） -->
        <ConfirmDialog
            v-model="pullOneDialog"
            :title="t('prism.metaPullOneConfirmTitle')"
            :text="t('prism.metaPullOneConfirmText', [pullOneTarget])"
            :consequences="[t('prism.metaPullC1'), t('prism.metaPullC3')]"
            :confirm-text="t('prism.metaPullOne')"
            icon="mdi-arrow-down-bold-outline"
            icon-color="primary"
            :loading="diffBusy === pullOneTarget && pullOneTarget !== ''"
            :error="pullOneError"
            @confirm="confirmPullOne"
        />
    </v-dialog>
</template>
