<script setup lang="ts">
// meta 差异对话框：每次打开重新计算并刷新缓存，三区展示（实例独有/项目独有/版本差异），
// 支持逐 mod 拉取/推送；拉取成功后自动 packwiz refresh + 全端缓存失效。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PrismService, PackwizService } from '../../../bindings/packgradle/internal/service'
import type { MetaDiff } from '../../../bindings/packgradle/internal/prism'
import { bumpProjectsVersion, invalidateProjects } from '../../stores/projects'
import { loadOverview } from '../../stores/instances'
import { showSnackbar } from '../../stores/ui'
import { errText } from '../../utils/errors'
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
const diffBusy = ref('') // 单操作中的 mod id
const pullOneDialog = ref(false)
const pullOneTarget = ref('')

watch(
    () => props.modelValue,
    open => {
        if (open) void refreshDiff()
    },
)

async function refreshDiff() {
    loading.value = true
    try {
        diff.value = await PrismService.MetaDiff(props.project)
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        loading.value = false
    }
}

function askPullOne(id: string) {
    pullOneTarget.value = id
    pullOneDialog.value = true
}

async function confirmPullOne() {
    const id = pullOneTarget.value
    if (!id) return
    pullOneDialog.value = false
    pullOneTarget.value = ''
    diffBusy.value = id
    try {
        await PrismService.PullMeta(props.project, id)
        showSnackbar(t('prism.metaOneDone', [t('prism.metaPullOne'), id]))
        await refreshProjectIndex()
        await loadOverview(true) // 刷新实例/关联列表
        bumpProjectsVersion() // 拉取改变了项目 mods，通知项目页刷新
        invalidateProjects() // 共享项目缓存失效
        await refreshDiff()
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        diffBusy.value = ''
    }
}

async function pushOne(id: string) {
    diffBusy.value = id
    try {
        await PrismService.PushMeta(props.project, id)
        showSnackbar(t('prism.metaOneDone', [t('prism.metaPushOne'), id]))
        await refreshDiff()
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        diffBusy.value = ''
    }
}

// refreshProjectIndex 执行 packwiz refresh 收录新拉取的 pw.toml（差异以 index.toml 为权威）。
// 失败时提示，不阻断主流程。
async function refreshProjectIndex() {
    try {
        const result = await PackwizService.RefreshProject(props.project)
        if (result && !result.ok) {
            showSnackbar(t('prism.metaRefreshFailed'))
        }
    } catch (e) {
        showSnackbar(t('prism.metaRefreshFailed') + ': ' + errText(e))
    }
}

function diffFetchedText(): string {
    const ts = diff.value?.fetched_at
    if (!ts) return ''
    return t('prism.metaFetchedAt', [ts])
}

// 差异三区（模板安全访问：null 时为空数组）
const diffInstanceOnly = computed(() => diff.value?.instance_only ?? [])
const diffProjectOnly = computed(() => diff.value?.project_only ?? [])
const diffVersionDiff = computed(() => diff.value?.version_diff ?? [])
const hasDiff = computed(
    () => diffInstanceOnly.value.length > 0 || diffProjectOnly.value.length > 0 || diffVersionDiff.value.length > 0,
)
</script>

<template>
    <v-dialog :model-value="modelValue" max-width="680" @update:model-value="emit('update:modelValue', $event)">
        <v-card elevation="8">
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

                        <!-- 版本差异 -->
                        <v-list-subheader v-if="diffVersionDiff.length > 0" class="text-caption text-warning">
                            {{ t('prism.metaDiffVersionDiff') }}（{{ diffVersionDiff.length }}）
                        </v-list-subheader>
                        <v-list-item
                            v-for="v in diffVersionDiff"
                            :key="'v' + v.id"
                            density="compact"
                            :title="v.id"
                            :subtitle="'项目 ' + v.project_version + ' → 实例 ' + v.instance_version"
                        />
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
    </v-dialog>

    <!-- 单 mod 拉取确认 -->
    <ConfirmDialog
        v-model="pullOneDialog"
        :title="t('prism.metaPullOneConfirmTitle')"
        :text="t('prism.metaPullOneConfirmText', [pullOneTarget])"
        :confirm-text="t('prism.metaPullOne')"
        icon="mdi-alert-outline"
        @confirm="confirmPullOne"
    />
</template>
