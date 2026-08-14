<script setup lang="ts">
// Prism 联动页：实例目录定位 + 项目 ↔ 实例关联（meta 推送/拉取/差异）+ 目录同步入口。
// 页面数据来自共享 Overview 缓存（实例目录 + 实例 + 关联视图一次返回）；
// 关联/目录同步/差异的交互逻辑分别落在子组件中。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Dialogs } from '@wailsio/runtime'
import { PrismService, PackwizService } from '../../bindings/packgradle/internal/service'
import type { LinkView } from '../../bindings/packgradle/internal/prism'
import { useInstances } from '../stores/instances'
import { bumpProjectsVersion, invalidateProjects } from '../stores/projects'
import { showSnackbar } from '../stores/ui'
import { errText, displayText, parseAppErr } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import PageHeader from '../components/common/PageHeader.vue'
import EmptyState from '../components/common/EmptyState.vue'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'
import LinkDialog from '../components/prism/LinkDialog.vue'
import DirLinksDialog from '../components/prism/DirLinksDialog.vue'
import MetaDiffDialog from '../components/prism/MetaDiffDialog.vue'

const { t } = useI18n()

const loading = ref(false)
const { overview, loadOverview } = useInstances()

// —— 实例目录手动指定 ——
const saving = ref(false)
const manualPath = ref('')

// —— 关联 / meta ——
const linkDialog = ref(false)
const metaBusy = ref('')
const pullTarget = ref<LinkView | null>(null)
const pullOpen = ref(false)
const unlinkTarget = ref<LinkView | null>(null)
const unlinkOpen = ref(false)
const unlinkBusy = ref(false)

// —— 目录同步 / 差异对话框 ——
const dirLinkProject = ref('')
const dirLinksOpen = ref(false)
const diffProject = ref('')
const diffOpen = ref(false)

const instances = computed(() => overview.value?.instances ?? [])
const instancesDir = computed(() => overview.value?.instances_dir ?? '')
const links = computed(() => overview.value?.links ?? [])

const locateFailed = computed(() => !!overview.value?.locate_error)
const failedCode = computed(() => parseAppErr(overview.value?.locate_error)?.code ?? '')
const failedError = computed(() => displayText(overview.value?.locate_error ?? ''))

// 加载器 chip：已识别 → 颜色标签；空 → 原版；其余 → 原样文本
function loaderInfo(inst: { modloader: string }): { label: string; color?: string } {
    if (!inst.modloader) return { label: t('prism.loaderVanilla') }
    return loaderChips[inst.modloader] ?? { label: inst.modloader }
}

async function load(force = false) {
    loading.value = true
    try {
        await loadOverview(force)
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        loading.value = false
    }
}

// 浏览选择实例目录
async function browse() {
    try {
        const picked = await Dialogs.OpenFile({
            Title: t('prism.manualPathLabel'),
            CanChooseFiles: false,
            CanChooseDirectories: true,
        })
        if (picked) manualPath.value = String(picked)
    } catch {
        // 用户取消选择时 Wails 会以错误形式返回，静默忽略即可
    }
}

// 保存手动路径并重试定位
async function saveManualPath() {
    saving.value = true
    try {
        await PrismService.SetInstancesPath(manualPath.value)
        showSnackbar(manualPath.value.trim() ? t('prism.manualPathSaved') : t('prism.manualPathCleared'))
        await load(true)
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        saving.value = false
    }
}

async function clearManualPath() {
    manualPath.value = ''
    await saveManualPath()
}

// —— 关联 ——
async function doUnlink() {
    const link = unlinkTarget.value
    if (!link) return
    unlinkOpen.value = false
    unlinkTarget.value = null
    unlinkBusy.value = true
    try {
        await PrismService.UnlinkProject(link.project)
        showSnackbar(t('prism.linkRemoved', [link.project]))
        await load(true)
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        unlinkBusy.value = false
    }
}

// meta 推送：项目 mod 元数据 → 实例 mods/.index
async function pushMeta(link: LinkView) {
    metaBusy.value = link.project
    try {
        const count = await PrismService.PushMeta(link.project, '')
        showSnackbar(t('prism.metaPushed', [count ?? 0]))
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        metaBusy.value = ''
    }
}

// meta 拉取：确认后从实例 .index 拉回（自动 refresh + 全端缓存失效）
function askPullMeta(link: LinkView) {
    pullTarget.value = link
    pullOpen.value = true
}

async function confirmPullMeta() {
    const link = pullTarget.value
    if (!link) return
    pullOpen.value = false
    pullTarget.value = null
    metaBusy.value = link.project
    try {
        const count = await PrismService.PullMeta(link.project, '')
        showSnackbar(t('prism.metaPulled', [count ?? 0]))
        await refreshProjectIndex(link.project)
        await load(true)
        bumpProjectsVersion()
        invalidateProjects()
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        metaBusy.value = ''
    }
}

async function refreshProjectIndex(project: string) {
    try {
        const result = await PackwizService.RefreshProject(project)
        if (result && !result.ok) {
            showSnackbar(t('prism.metaRefreshFailed'))
        }
    } catch (e) {
        showSnackbar(t('prism.metaRefreshFailed') + ': ' + errText(e))
    }
}

function openDirLinks(link: LinkView) {
    dirLinkProject.value = link.project
    dirLinksOpen.value = true
}

function openMetaDiff(link: LinkView) {
    diffProject.value = link.project
    diffOpen.value = true
}

onMounted(async () => {
    // 手动路径与页面总览并发加载
    const [path] = await Promise.all([PrismService.GetInstancesPath(), load()])
    manualPath.value = path ?? ''
})
</script>

<template>
    <v-container fluid class="pa-6">
        <PageHeader :title="t('prism.title')" :subtitle="t('prism.subtitle')">
            <template #actions>
                <v-btn
                    v-if="!locateFailed"
                    color="primary"
                    prepend-icon="mdi-link-variant"
                    @click="linkDialog = true"
                >
                    {{ t('prism.linkBtn') }}
                </v-btn>
                <v-btn variant="text" icon="mdi-refresh" :loading="loading" @click="load(true)" />
            </template>
        </PageHeader>

        <!-- 实例目录设置 -->
        <v-card class="mb-5">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-folder-cog-outline" class="mr-2" color="primary" />
                {{ t('prism.instancesDirTitle') }}
                <v-chip size="x-small" variant="tonal" class="ml-3">
                    {{ t('prism.instancesDirCurrent', [instancesDir || t('prism.autoDetect')]) }}
                </v-chip>
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-3">{{ t('prism.instancesDirHint') }}</div>
                <v-alert
                    v-if="locateFailed"
                    :type="failedCode === 'err.prism.not_found' ? 'warning' : 'error'"
                    variant="tonal"
                    density="compact"
                    class="mb-3"
                >
                    <div class="d-flex align-center justify-space-between">
                        <span>{{ failedError }}</span>
                        <v-btn
                            v-if="failedCode === 'err.prism.not_found'"
                            color="primary"
                            size="small"
                            variant="tonal"
                            @click="$router.push('/settings')"
                        >
                            {{ t('prism.goConfigure') }}
                        </v-btn>
                    </div>
                </v-alert>
                <v-text-field
                    v-model="manualPath"
                    :label="t('prism.manualPathLabel')"
                    density="comfortable"
                    hide-details="auto"
                    clearable
                    style="max-width: 640px"
                    @keyup.enter="saveManualPath"
                >
                    <template #append>
                        <v-btn size="small" variant="text" icon="mdi-folder-search" :title="t('prism.browse')" @click="browse" />
                        <v-btn size="small" variant="tonal" :loading="saving" @click="saveManualPath">{{ t('prism.save') }}</v-btn>
                        <v-btn v-if="manualPath" size="small" variant="text" @click="clearManualPath">
                            {{ t('prism.clearManual') }}
                        </v-btn>
                    </template>
                </v-text-field>
            </v-card-text>
        </v-card>

        <!-- 关联列表（核心工作区） -->
        <v-card class="mb-5">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-link-variant" class="mr-2" color="primary" />
                {{ t('prism.linksTitle') }}
                <v-spacer />
                <span class="text-caption text-medium-emphasis">{{ t('prism.linksSubtitle') }}</span>
            </v-card-title>
            <v-card-text>
                <v-progress-linear v-if="loading" indeterminate class="mb-3" />
                <template v-if="links.length > 0">
                    <div v-for="link in links" :key="link.project" class="link-row mb-3">
                        <div class="d-flex align-center flex-wrap ga-2">
                            <v-avatar rounded="md" size="32" color="primary" variant="tonal">
                                <v-icon icon="mdi-package-variant-closed" size="18" />
                            </v-avatar>
                            <span class="text-body-2 font-weight-medium">{{ link.project }}</span>
                            <span class="text-caption text-medium-emphasis link-path">{{ link.project_path }}</span>
                            <v-icon icon="mdi-arrow-right" size="small" class="text-medium-emphasis" />
                            <v-chip
                                size="small"
                                :color="link.instance_valid ? 'success' : 'error'"
                                variant="tonal"
                            >
                                {{ link.instance_name || link.instance_id }}
                            </v-chip>
                            <v-chip v-if="!link.instance_valid" size="x-small" color="error" variant="outlined">
                                {{ t('prism.instanceInvalidChip') }}
                            </v-chip>
                        </div>
                        <div class="d-flex flex-wrap ga-1 mt-2 pl-10">
                            <v-btn
                                size="small"
                                variant="tonal"
                                :loading="metaBusy === link.project"
                                :disabled="metaBusy !== ''"
                                @click="pushMeta(link)"
                            >
                                {{ t('prism.metaPushBtn') }}
                            </v-btn>
                            <v-btn
                                size="small"
                                variant="tonal"
                                :loading="metaBusy === link.project"
                                :disabled="metaBusy !== ''"
                                @click="askPullMeta(link)"
                            >
                                {{ t('prism.metaPullBtn') }}
                            </v-btn>
                            <v-btn size="small" variant="tonal" @click="openMetaDiff(link)">
                                {{ t('prism.metaDiffBtn') }}
                            </v-btn>
                            <v-btn size="small" variant="tonal" @click="openDirLinks(link)">
                                {{ t('prism.dirLinkBtn') }}
                            </v-btn>
                            <v-btn size="small" variant="text" color="error" @click="unlinkTarget = link; unlinkOpen = true">
                                {{ t('prism.unlinkBtn') }}
                            </v-btn>
                        </div>
                    </div>
                </template>
                <EmptyState
                    v-else-if="!loading"
                    icon="mdi-link-variant-off"
                    :title="t('prism.linksEmpty')"
                    :text="t('prism.linksEmptyHint')"
                >
                    <template #actions>
                        <v-btn
                            v-if="!locateFailed"
                            color="primary"
                            prepend-icon="mdi-link-variant"
                            @click="linkDialog = true"
                        >
                            {{ t('prism.linkBtn') }}
                        </v-btn>
                    </template>
                </EmptyState>
            </v-card-text>
        </v-card>

        <!-- 实例列表 -->
        <v-card>
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-prism" class="mr-2" color="primary" />
                {{ t('prism.instancesTitle') }}
                <v-spacer />
                <span class="text-caption text-medium-emphasis">{{ t('prism.instanceCount', [instances.length]) }}</span>
            </v-card-title>
            <v-card-text class="text-body-2 text-medium-emphasis pb-0">{{ t('prism.instancesHint') }}</v-card-text>
            <v-card-text>
                <v-row v-if="instances.length > 0">
                    <v-col v-for="inst in instances" :key="inst.id" cols="12" md="6" xl="4">
                        <v-card variant="flat" color="surface-bright" class="inst-card">
                            <v-card-text>
                                <div class="d-flex align-center mb-2">
                                    <v-icon icon="mdi-folder-outline" color="primary" class="mr-2" />
                                    <span class="text-subtitle-2 font-weight-medium flex-grow-1 inst-name">{{ inst.name }}</span>
                                    <v-chip v-if="inst.error" size="x-small" color="error" variant="tonal">
                                        {{ t('prism.parseFailed') }}
                                    </v-chip>
                                </div>
                                <div class="d-flex flex-wrap ga-1 mb-2">
                                    <v-chip v-if="inst.group" size="x-small" variant="outlined">{{ inst.group }}</v-chip>
                                    <v-chip size="x-small" :color="loaderInfo(inst).color ?? undefined" variant="tonal">
                                        {{ loaderInfo(inst).label }}
                                    </v-chip>
                                    <v-chip v-if="inst.minecraft" size="x-small" variant="tonal">{{ inst.minecraft }}</v-chip>
                                </div>
                                <div class="text-caption text-medium-emphasis inst-path">{{ inst.path }}</div>
                                <div v-if="inst.error" class="text-caption text-error mt-1">{{ displayText(inst.error) }}</div>
                            </v-card-text>
                        </v-card>
                    </v-col>
                </v-row>
                <div v-else-if="!locateFailed" class="text-body-2 text-medium-emphasis">
                    {{ t('prism.empty') }}
                </div>
            </v-card-text>
        </v-card>

        <!-- 关联对话框 -->
        <LinkDialog v-model="linkDialog" :instances="instances" @changed="load(true)" />

        <!-- 目录同步管理 -->
        <DirLinksDialog v-model="dirLinksOpen" :project="dirLinkProject" />

        <!-- meta 差异 -->
        <MetaDiffDialog v-model="diffOpen" :project="diffProject" />

        <!-- 拉取 meta 确认 -->
        <ConfirmDialog
            v-model="pullOpen"
            :title="t('prism.metaPullConfirmTitle')"
            :text="t('prism.metaPullConfirmText')"
            :confirm-text="t('prism.metaPullBtn')"
            icon="mdi-alert-outline"
            @confirm="confirmPullMeta"
        />

        <!-- 解除关联确认 -->
        <ConfirmDialog
            v-model="unlinkOpen"
            :title="t('prism.unlinkConfirmTitle')"
            :text="t('prism.unlinkConfirmText', [unlinkTarget?.project ?? '', unlinkTarget?.instance_name || unlinkTarget?.instance_id || ''])"
            :confirm-text="t('prism.unlinkBtn')"
            confirm-color="error"
            icon="mdi-alert-outline"
            :loading="unlinkBusy"
            @confirm="doUnlink"
        />
    </v-container>
</template>

<style scoped>
.link-row {
    border: 1px solid rgba(255, 255, 255, 0.07);
    border-radius: 12px;
    padding: 12px 14px;
}
.link-path {
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.inst-card {
    border: 1px solid rgba(255, 255, 255, 0.06);
    border-radius: 14px;
}
.inst-name,
.inst-path {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
</style>
