<script setup lang="ts">
// Prism 联动页：定位状态横幅 + 关联工作台（卡片式，操作视觉分级）+ 实例折叠面板。
// 定位失败时关联区置灰禁用；meta 推送/拉取经任务中心，拉取保留确认（后果四要素）。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { PackwizService, PrismService } from '../api'
import { pickDirectory } from '../utils/dialogs'
import type { LinkView } from '../../bindings/packgradle/internal/prism'
import { useInstances } from '../stores/instances'
import { bumpProjectsVersion, invalidateProjects } from '../stores/projects'
import { runTask } from '../stores/taskCenter'
import { showSnackbar } from '../stores/ui'
import { errText, displayText, parseAppErr } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import PageHeader from '../components/common/PageHeader.vue'
import EmptyState from '../components/common/EmptyState.vue'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'
import LinkDialog from '../components/prism/LinkDialog.vue'
import MetaDiffDialog from '../components/prism/MetaDiffDialog.vue'

const { t } = useI18n()
const router = useRouter()

const loading = ref(false)
const loadError = ref('')
const { overview, loadOverview } = useInstances()

// —— 实例目录手动指定 ——
const manualPath = ref('')
const pathEditing = ref(false)
const pathSaving = ref(false)

// —— 关联 / meta ——
const linkDialog = ref(false)
const pullTarget = ref<LinkView | null>(null)
const pullOpen = ref(false)
const pullBusy = ref(false)
const pullError = ref('')
const unlinkTarget = ref<LinkView | null>(null)
const unlinkOpen = ref(false)
const unlinkBusy = ref(false)
const unlinkError = ref('')

// —— 目录同步 / 差异对话框 ——
const diffProject = ref('')
const diffOpen = ref(false)

const instances = computed(() => overview.value?.instances ?? [])
const instancesDir = computed(() => overview.value?.instances_dir ?? '')
const links = computed(() => overview.value?.links ?? [])

const locateFailed = computed(() => !!overview.value?.locate_error)
const locateReady = computed(() => !!overview.value && !overview.value.locate_error)
const failedCode = computed(() => parseAppErr(overview.value?.locate_error)?.code ?? '')
const failedError = computed(() => displayText(overview.value?.locate_error ?? ''))

function loaderInfo(inst: { modloader: string }): { label: string; color?: string } {
    if (!inst.modloader) return { label: t('prism.loaderVanilla') }
    return loaderChips[inst.modloader] ?? { label: inst.modloader }
}

async function load(force = false) {
    loading.value = true
    try {
        await loadOverview(force)
    } finally {
        loading.value = false
    }
}

async function reload(force = false) {
    loadError.value = ''
    try {
        await load(force)
    } catch (e) {
        loadError.value = errText(e)
    }
}

function togglePathEditor() {
    if (pathEditing.value) {
        pathEditing.value = false
        return
    }
    manualPath.value = instancesDir.value
    pathEditing.value = true
}

async function saveManualPath() {
    if (pathSaving.value) return
    const path = manualPath.value.trim()
    let refreshFailed = false
    pathSaving.value = true
    try {
        const result = await runTask({
            title: t('tasks.setInstancesPath'),
            kind: 'config',
            run: async () => {
                await PrismService.SetInstancesPath(path)
                try {
                    await load(true)
                } catch (e) {
                    refreshFailed = true
                    showSnackbar(errText(e), 'warning')
                }
                return path ? t('prism.manualPathSaved') : t('prism.manualPathCleared')
            },
            warn: () => refreshFailed,
        })
        if (result !== null) pathEditing.value = false
    } finally {
        pathSaving.value = false
    }
}

// 浏览选择实例根目录；取消则不动
async function browseInstancesPath() {
    const picked = await pickDirectory(t('prism.manualPathLabel'))
    if (!picked) return
    manualPath.value = picked
    pathEditing.value = true
}

// —— 关联 ——
function askUnlink(link: LinkView) {
    unlinkTarget.value = link
    unlinkError.value = ''
    unlinkOpen.value = true
}

async function doUnlink() {
    const link = unlinkTarget.value
    if (!link || unlinkBusy.value) return
    unlinkBusy.value = true
    unlinkError.value = ''
    let refreshFailed = false
    try {
        const result = await runTask({
            title: t('tasks.unlink', [link.project]),
            kind: 'link',
            run: async () => {
                await PrismService.UnlinkProject(link.project)
                try {
                    await load(true)
                } catch (e) {
                    refreshFailed = true
                    showSnackbar(errText(e), 'warning')
                }
                return t('prism.linkRemoved', [link.project])
            },
            warn: () => refreshFailed,
            onError: message => (unlinkError.value = message),
        })
        if (result !== null) {
            unlinkOpen.value = false
            unlinkTarget.value = null
        }
    } finally {
        unlinkBusy.value = false
    }
}

// meta 推送：项目 mod 元数据 → 实例 mods/.index
async function pushMeta(link: LinkView) {
    await runTask({
        title: t('tasks.metaPush', [link.project]),
        kind: 'meta',
        run: async () => {
            const count = await PrismService.PushMeta(link.project, '')
            return t('prism.metaPushed', [count ?? 0])
        },
    })
}

function askPullMeta(link: LinkView) {
    pullTarget.value = link
    pullError.value = ''
    pullOpen.value = true
}

async function confirmPullMeta() {
    const link = pullTarget.value
    if (!link || pullBusy.value) return
    pullBusy.value = true
    pullError.value = ''
    let refreshFailed = false
    try {
        const result = await runTask({
            title: t('tasks.metaPull', [link.project]),
            kind: 'meta',
            run: async () => {
                const count = await PrismService.PullMeta(link.project, '')
                try {
                    const refreshed = await PackwizService.RefreshProject(link.project)
                    if (!refreshed.ok) throw new Error(displayText(refreshed.output))
                    await load(true)
                } catch (e) {
                    refreshFailed = true
                    showSnackbar(errText(e), 'warning')
                }
                bumpProjectsVersion()
                invalidateProjects()
                return t('prism.metaPulled', [count ?? 0])
            },
            warn: () => refreshFailed,
            onError: message => (pullError.value = message),
        })
        if (result !== null) {
            pullOpen.value = false
            pullTarget.value = null
        }
    } finally {
        pullBusy.value = false
    }
}

function openDirLinks(link: LinkView) {
    // 目录同步已迁移到「开发版本」页（focus model），此处跳转而不再弹对话框
    router.push({ path: '/dev', query: { project: link.project } })
}

function openMetaDiff(link: LinkView) {
    diffProject.value = link.project
    diffOpen.value = true
}

onMounted(reload)
</script>

<template>
    <v-container fluid class="pa-6">
        <PageHeader :title="t('prism.title')" :subtitle="t('prism.subtitle')">
            <template #actions>
                <v-btn variant="text" icon="mdi-refresh" :loading="loading" :title="t('common.refresh')" @click="reload(true)" />
                <v-btn
                    v-if="locateReady"
                    color="primary"
                    class="primary-action"
                    prepend-icon="mdi-link-variant"
                    @click="linkDialog = true"
                >
                    {{ t('prism.linkBtn') }}
                </v-btn>
            </template>
        </PageHeader>

        <v-alert v-if="loadError" type="error" variant="tonal" class="mb-5">
            <div class="d-flex align-center ga-3">
                <span class="flex-grow-1">{{ loadError }}</span>
                <v-btn size="small" variant="tonal" @click="reload(true)">{{ t('common.refresh') }}</v-btn>
            </div>
        </v-alert>

        <!-- 定位状态横幅 -->
        <v-card v-if="locateReady" class="mb-5 locate-ok">
            <v-card-text class="d-flex align-center flex-wrap ga-3 py-3">
                <v-icon icon="mdi-folder-check-outline" color="success" />
                <div class="flex-grow-1" style="min-width: 200px">
                    <span class="text-body-2 font-weight-medium">{{ t('prism.instancesDirTitle') }}</span>
                    <span class="text-body-2 text-medium-emphasis ml-2 locate-path">{{ instancesDir || t('prism.autoDetect') }}</span>
                </div>
                <v-btn size="small" variant="tonal" :disabled="pathSaving" @click="togglePathEditor">
                    {{ pathEditing ? t('common.cancel') : t('prism.changeDir') }}
                </v-btn>
            </v-card-text>
            <v-card-text v-if="pathEditing" class="pt-0">
                <div class="d-flex align-center ga-2">
                    <v-text-field
                        v-model="manualPath"
                        :label="t('prism.manualPathLabel')"
                        density="comfortable"
                        hide-details="auto"
                        clearable
                        style="max-width: 560px"
                        @keyup.enter="saveManualPath"
                    >
                        <template #append>
                            <v-btn
                                size="small"
                                variant="text"
                                icon="mdi-folder-search"
                                :title="t('prism.browse')"
                                :disabled="pathSaving"
                                @click="browseInstancesPath"
                            />
                        </template>
                    </v-text-field>
                    <v-btn color="primary" variant="tonal" :loading="pathSaving" @click="saveManualPath">{{ t('prism.save') }}</v-btn>
                </div>
            </v-card-text>
        </v-card>

        <!-- 定位失败：大 alert 卡，内嵌修复 -->
        <v-card v-else-if="locateFailed" class="mb-5 card-error">
            <v-card-text>
                <div class="d-flex align-center mb-3">
                    <v-icon icon="mdi-folder-alert-outline" color="error" class="mr-2" />
                    <span class="text-subtitle-2 font-weight-bold text-error">{{ t('prism.locateFailedTitle') }}</span>
                    <v-spacer />
                    <v-btn
                        v-if="failedCode === 'err.prism.not_found'"
                        color="primary"
                        size="small"
                        variant="flat"
                        @click="$router.push('/settings')"
                    >
                        {{ t('prism.goConfigure') }}
                    </v-btn>
                </div>
                <div class="text-body-2 text-medium-emphasis mb-3">{{ failedError }}</div>
                <div class="d-flex align-center ga-2">
                    <v-text-field
                        v-model="manualPath"
                        :label="t('prism.manualPathLabel')"
                        density="comfortable"
                        hide-details="auto"
                        clearable
                        style="max-width: 560px"
                        @keyup.enter="saveManualPath"
                    >
                        <template #append>
                            <v-btn
                                size="small"
                                variant="text"
                                icon="mdi-folder-search"
                                :title="t('prism.browse')"
                                :disabled="pathSaving"
                                @click="browseInstancesPath"
                            />
                        </template>
                    </v-text-field>
                    <v-btn color="primary" variant="tonal" :loading="pathSaving" @click="saveManualPath">{{ t('prism.save') }}</v-btn>
                </div>
            </v-card-text>
        </v-card>

        <!-- 关联工作台 -->
        <v-card class="mb-5" :class="{ 'links-disabled': !locateReady }">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-link-variant" class="mr-2" color="primary" />
                {{ t('prism.linksTitle') }}
                <v-spacer />
                <span class="text-caption text-medium-emphasis">{{ t('prism.linksSubtitle') }}</span>
            </v-card-title>
            <v-card-text>
                <v-progress-linear v-if="loading" indeterminate class="mb-3" />
                <template v-if="links.length > 0">
                    <div v-for="link in links" :key="link.project" class="link-row surface-tile mb-3">
                        <div class="d-flex align-center flex-wrap ga-2">
                            <v-avatar rounded="lg" size="36" color="primary" variant="tonal">
                                <span class="text-body-2 font-weight-bold">{{ link.project.slice(0, 1).toUpperCase() }}</span>
                            </v-avatar>
                            <span class="text-body-2 font-weight-bold">{{ link.project }}</span>
                            <v-icon icon="mdi-arrow-right" size="small" class="text-medium-emphasis" />
                            <v-chip size="small" :color="link.instance_valid ? 'success' : 'error'" variant="flat">
                                {{ link.instance_name || link.instance_id }}
                            </v-chip>
                            <v-chip v-if="!link.instance_valid" size="x-small" color="error" variant="outlined">
                                {{ t('prism.instanceInvalidChip') }}
                            </v-chip>
                        </div>
                        <div class="d-flex align-center flex-wrap ga-2 mt-3 link-actions">
                            <v-btn size="small" color="primary" variant="tonal" prepend-icon="mdi-compare-horizontal" @click="openMetaDiff(link)">
                                {{ t('prism.metaDiffBtn') }}
                            </v-btn>
                            <v-tooltip :text="t('prism.metaPushTip')" location="top">
                                <template #activator="{ props }">
                                    <v-btn v-bind="props" size="small" variant="text" prepend-icon="mdi-arrow-up-bold-outline" @click="pushMeta(link)">
                                        {{ t('prism.metaPushBtn') }}
                                    </v-btn>
                                </template>
                            </v-tooltip>
                            <v-tooltip :text="t('prism.metaPullTip')" location="top">
                                <template #activator="{ props }">
                                    <v-btn v-bind="props" size="small" variant="text" prepend-icon="mdi-arrow-down-bold-outline" @click="askPullMeta(link)">
                                        {{ t('prism.metaPullBtn') }}
                                    </v-btn>
                                </template>
                            </v-tooltip>
                            <v-spacer />
                            <v-btn
                                size="small"
                                icon="mdi-folder-sync-outline"
                                variant="text"
                                :title="t('prism.dirLinkBtn')"
                                @click="openDirLinks(link)"
                            />
                            <v-btn
                                size="small"
                                icon="mdi-link-variant-off"
                                variant="text"
                                color="error"
                                :title="t('prism.unlinkBtn')"
                                @click="askUnlink(link)"
                            />
                        </div>
                    </div>
                </template>
                <EmptyState v-else-if="!loading && locateReady" icon="mdi-link-variant-off" :title="t('prism.linksEmpty')" :text="t('prism.linksEmptyHint')">
                    <template #actions>
                        <v-btn v-if="locateReady" color="primary" prepend-icon="mdi-link-variant" @click="linkDialog = true">
                            {{ t('prism.linkBtn') }}
                        </v-btn>
                    </template>
                </EmptyState>
            </v-card-text>
        </v-card>

        <!-- 实例列表：折叠面板 -->
        <v-expansion-panels class="mb-5">
            <v-expansion-panel>
                <v-expansion-panel-title>
                    <div class="d-flex align-center">
                        <v-icon icon="mdi-prism" class="mr-2" color="primary" />
                        {{ t('prism.instancesTitle') }}
                        <v-chip size="x-small" variant="tonal" class="ml-2">{{ instances.length }}</v-chip>
                    </div>
                </v-expansion-panel-title>
                <v-expansion-panel-text>
                    <v-row v-if="instances.length > 0">
                        <v-col v-for="inst in instances" :key="inst.id" cols="12" md="6" xl="4">
                            <article class="inst-card surface-tile" :class="{ 'card-error': inst.error }">
                                <div>
                                    <div class="d-flex align-center mb-2">
                                        <v-icon icon="mdi-folder-outline" color="primary" class="mr-2" />
                                        <span class="text-subtitle-2 font-weight-medium flex-grow-1 inst-name">{{ inst.name }}</span>
                                        <v-chip v-if="inst.error" size="x-small" color="error" variant="flat">
                                            {{ t('prism.parseFailed') }}
                                        </v-chip>
                                    </div>
                                    <div class="d-flex flex-wrap ga-1 mb-2">
                                        <v-chip v-if="inst.group" size="x-small" variant="outlined">{{ inst.group }}</v-chip>
                                        <v-chip size="x-small" :color="loaderInfo(inst).color ?? undefined" variant="flat">
                                            {{ loaderInfo(inst).label }}
                                        </v-chip>
                                        <v-chip v-if="inst.minecraft" size="x-small" variant="tonal">{{ inst.minecraft }}</v-chip>
                                    </div>
                                    <div class="text-caption text-medium-emphasis inst-path">{{ inst.path }}</div>
                                    <div v-if="inst.error" class="text-caption text-error mt-1">{{ displayText(inst.error) }}</div>
                                </div>
                            </article>
                        </v-col>
                    </v-row>
                    <div v-else-if="locateReady" class="text-body-2 text-medium-emphasis">
                        {{ t('prism.empty') }}
                    </div>
                </v-expansion-panel-text>
            </v-expansion-panel>
        </v-expansion-panels>

        <!-- 关联对话框 -->
        <LinkDialog v-model="linkDialog" :instances="instances" @changed="reload(true)" />

        <!-- meta 差异 -->
        <MetaDiffDialog v-model="diffOpen" :project="diffProject" />

        <!-- 拉取 meta 确认（后果四要素） -->
        <ConfirmDialog
            v-model="pullOpen"
            :title="t('prism.metaPullConfirmTitle')"
            :text="t('prism.metaPullConfirmText', [pullTarget?.project ?? ''])"
            :consequences="[t('prism.metaPullC1'), t('prism.metaPullC2'), t('prism.metaPullC3')]"
            :confirm-text="t('prism.metaPullBtn')"
            icon="mdi-arrow-down-bold-outline"
            icon-color="primary"
            :loading="pullBusy"
            :error="pullError"
            @confirm="confirmPullMeta"
        />

        <!-- 解除关联确认 -->
        <ConfirmDialog
            v-model="unlinkOpen"
            :title="t('prism.unlinkConfirmTitle')"
            :text="t('prism.unlinkConfirmText', [unlinkTarget?.project ?? '', unlinkTarget?.instance_name || unlinkTarget?.instance_id || ''])"
            :consequences="[t('prism.unlinkC1'), t('prism.unlinkC2')]"
            :confirm-text="t('prism.unlinkBtn')"
            icon="mdi-link-variant-off"
            danger
            :loading="unlinkBusy"
            :error="unlinkError"
            @confirm="doUnlink"
        />
    </v-container>
</template>

<style scoped>
.locate-ok {
    border-left: 3px solid rgb(var(--v-theme-success)) !important;
}
.locate-path {
    word-break: break-all;
}
.link-row {
    padding: 14px 16px;
}
.link-actions {
    padding-left: 48px;
}
.links-disabled {
    opacity: 0.5;
    pointer-events: none;
}
.inst-card {
    height: 100%;
}
.inst-name,
.inst-path {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
</style>
