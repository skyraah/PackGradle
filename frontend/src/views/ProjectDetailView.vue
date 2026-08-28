<script setup lang="ts">
// 项目详情：项目信息 + mod 管理（v-data-table：排序/过滤/版本获取）+ 刷新与更新检查。
// 写操作经任务中心；返回按钮来源感知（历史栈内 back，深链 fallback 列表）。
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { PackwizService } from '../api'
import { handleApiKeyError } from '../stores/apiKeyGuide'
import type { ModInfo } from '../../bindings/packgradle/internal/packwiz'
import { loadProjects, setProjects, findProject, projectsVersion } from '../stores/projects'
import { runTask } from '../stores/taskCenter'
import { showSnackbar } from '../stores/ui'
import { displayText, errText } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'
import EmptyState from '../components/common/EmptyState.vue'
import ModsTable from '../components/projects/ModsTable.vue'
import CheckUpdatesDialog from '../components/projects/CheckUpdatesDialog.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const name = computed(() => String(route.params.name ?? ''))
const project = computed(() => findProject(name.value))
const notFound = ref(false)
const loading = ref(false)
const loadError = ref('')
let loadGeneration = 0

const fetching = ref<string | null>(null)
const fetchingAll = ref(false)
const flashed = ref<string | null>(null)
let flashTimer: ReturnType<typeof setTimeout> | undefined

const removeDialog = ref(false)
const removing = ref(false)
const removeError = ref('')
const checkOpen = ref(false)

function loaderChip(loader: string) {
    return loaderChips[loader] ?? { label: loader, color: 'grey' }
}

async function ensureLoaded(force = false) {
    const requestedName = name.value
    const generation = ++loadGeneration
    loading.value = true
    loadError.value = ''
    notFound.value = false
    try {
        await loadProjects(force)
        if (!findProject(requestedName)) await loadProjects(true)
        if (generation !== loadGeneration || name.value !== requestedName) return
        notFound.value = !findProject(requestedName)
    } catch (e) {
        if (generation !== loadGeneration || name.value !== requestedName) return
        loadError.value = errText(e)
    } finally {
        if (generation === loadGeneration) loading.value = false
    }
}

onMounted(() => void ensureLoaded())

watch(
    () => route.params.name,
    async () => {
        await ensureLoaded()
    },
)

watch(projectsVersion, () => {
    void ensureLoaded(true)
})

// 返回：历史栈内 back，深链 fallback 项目列表
function goBack() {
    if (window.history.length > 1) router.back()
    else router.push('/projects')
}

function askRemove() {
    removeError.value = ''
    removeDialog.value = true
}

async function refreshProject() {
    if (!project.value) return
    const projectName = project.value.name
    let refreshFailed = false
    await runTask({
        title: t('tasks.refreshProject', [projectName]),
        kind: 'refresh',
        run: async () => {
            const result = await PackwizService.RefreshProject(projectName)
            if (!result.ok) throw new Error(displayText(result.output || t('projects.outputFailed')))
            try {
                await ensureLoaded(true)
            } catch {
                refreshFailed = true
            }
            refreshFailed ||= !!loadError.value
            return t('projects.outputSuccess', ['packwiz refresh'])
        },
        warn: () => refreshFailed,
    })
}

async function fetchModVersion(mod: ModInfo) {
    if (!project.value) return
    const projectName = project.value.name
    const modID = mod.id
    fetching.value = modID
    try {
        const updated = await PackwizService.FetchModVersion(projectName, modID)
        const target = findProject(projectName)?.mods?.find(m => m.id === modID)
        if (target && updated) Object.assign(target, updated)
        if (flashTimer) clearTimeout(flashTimer)
        flashed.value = name.value === projectName ? modID : null
        flashTimer = setTimeout(() => (flashed.value = null), 2000)
        showSnackbar(t('projects.versionFetched', [updated?.name ?? mod.name]), 'success')
    } catch (e) {
        // CurseForge 请求失败：Key 缺失/无效弹全局引导，其余 snackbar
        handleApiKeyError(e)
    } finally {
        if (fetching.value === modID) fetching.value = null
    }
}

async function fetchAllVersions() {
    if (!project.value) return
    const projectName = project.value.name
    fetchingAll.value = true
    let hasFailures = false
    try {
        await runTask({
            title: t('tasks.fetchAllVersions', [projectName]),
            kind: 'fetch',
            run: async () => {
                const results = (await PackwizService.FetchAllModVersions(projectName)) ?? []
                const ok = results.filter(r => r.ok).length
                hasFailures = ok !== results.length
                await ensureLoaded(true)
                hasFailures ||= !!loadError.value
                return t('projects.versionsFetched', [ok, results.length])
            },
            warn: () => hasFailures,
        })
    } finally {
        fetchingAll.value = false
    }
}

async function confirmRemove() {
    if (!project.value || removing.value) return
    removing.value = true
    removeError.value = ''
    try {
        const projName = project.value.name
        const result = await runTask({
            title: t('tasks.removeProject', [projName]),
            kind: 'remove',
            run: async () => {
                setProjects(await PackwizService.RemoveProject(projName))
                return t('projects.removed', [projName])
            },
            onError: message => (removeError.value = message),
        })
        if (result !== null) {
            removeDialog.value = false
            await router.push('/projects')
        }
    } finally {
        removing.value = false
    }
}

async function copyPath() {
    if (!project.value) return
    try {
        await navigator.clipboard.writeText(project.value.path)
        showSnackbar(t('common.copied'), 'success')
    } catch {
        // 剪贴板不可用时静默忽略（WebView 权限差异）
    }
}
</script>

<template>
    <v-container fluid class="pa-6">
        <v-btn variant="text" prepend-icon="mdi-arrow-left" class="mb-3" @click="goBack">
            {{ t('projects.detailBack') }}
        </v-btn>

        <v-progress-linear v-if="loading" indeterminate class="mb-4" />

        <v-alert v-if="loadError" type="error" variant="tonal" class="mb-5">
            <div class="d-flex align-center ga-3">
                <span class="flex-grow-1">{{ loadError }}</span>
                <v-btn size="small" variant="tonal" @click="ensureLoaded(true)">{{ t('common.refresh') }}</v-btn>
            </div>
        </v-alert>

        <!-- 项目不存在 -->
        <v-card v-if="notFound && !loadError" class="py-4">
            <EmptyState icon="mdi-alert-circle-outline" :title="t('projects.projectNotFound', [name])" :text="t('projects.projectNotFoundHint', [name])">
                <template #actions>
                    <v-btn color="primary" variant="tonal" @click="router.push('/projects')">
                        {{ t('projects.detailBack') }}
                    </v-btn>
                </template>
            </EmptyState>
        </v-card>

        <template v-else-if="project">
            <!-- 项目信息卡 -->
            <v-card class="mb-5" :class="{ 'card-error': project.error }">
                <v-card-text class="d-flex align-center flex-wrap ga-4">
                    <v-avatar rounded="xl" size="56" :color="project.error ? 'error' : loaderChip(project.modloader).color" variant="tonal">
                        <span class="text-h6 font-weight-bold">{{ project.name.slice(0, 1).toUpperCase() }}</span>
                    </v-avatar>
                    <div class="flex-grow-1" style="min-width: 200px">
                        <div class="d-flex align-center flex-wrap ga-2">
                            <span class="text-h6 font-weight-bold">{{ project.name }}</span>
                            <v-chip v-if="project.error" size="x-small" color="error" variant="flat">
                                {{ t('projects.parseFailed') }}
                            </v-chip>
                        </div>
                        <div class="d-flex align-center text-caption text-medium-emphasis mt-1">
                            <span class="path-text">{{ project.path }}</span>
                            <v-btn
                                icon="mdi-content-copy"
                                size="x-small"
                                variant="text"
                                :title="t('common.copyPath')"
                                class="ml-2"
                                @click="copyPath"
                            />
                        </div>
                        <div v-if="!project.error" class="d-flex flex-wrap ga-1 mt-2">
                            <v-chip v-if="project.modloader" size="x-small" :color="loaderChip(project.modloader).color" variant="flat">
                                {{ loaderChip(project.modloader).label }} {{ project.modloader_version }}
                            </v-chip>
                            <v-chip v-if="project.minecraft" size="x-small" variant="tonal">
                                {{ t('projects.minecraft', [project.minecraft]) }}
                            </v-chip>
                            <v-chip v-if="project.version" size="x-small" variant="tonal">v{{ project.version }}</v-chip>
                            <v-chip v-if="project.author" size="x-small" variant="tonal">
                                {{ t('projects.author', [project.author]) }}
                            </v-chip>
                            <v-chip size="x-small" variant="tonal">
                                {{ t('projects.modCount', [(project.mods ?? []).length]) }}
                            </v-chip>
                        </div>
                    </div>
                    <div v-if="!project.error" class="d-flex flex-wrap ga-2 align-center">
                        <v-btn color="primary" class="primary-action" prepend-icon="mdi-update" @click="checkOpen = true">
                            {{ t('projects.tooltipCheckUpdates') }}
                        </v-btn>
                        <v-btn variant="tonal" prepend-icon="mdi-refresh" @click="refreshProject">
                            {{ t('projects.tooltipRefresh') }}
                        </v-btn>
                        <v-btn
                            variant="tonal"
                            prepend-icon="mdi-rocket-launch-outline"
                            @click="router.push({ path: '/dev', query: { project: project.name } })"
                        >
                            {{ t('dev.goDev') }}
                        </v-btn>
                        <v-btn
                            icon="mdi-delete-outline"
                            variant="text"
                            color="error"
                            :title="t('projects.tooltipRemove')"
                            @click="askRemove"
                        />
                    </div>
                    <div v-else class="text-body-2 text-error">{{ displayText(project.error) }}</div>
                </v-card-text>
            </v-card>

            <!-- mod 管理 -->
            <v-card>
                <v-card-title class="d-flex align-center pt-5">
                    <v-icon icon="mdi-puzzle-outline" color="primary" class="mr-2" />
                    {{ t('projects.modsTitle') }}
                </v-card-title>
                <v-card-text class="text-body-2 text-medium-emphasis pb-0">{{ t('projects.modsHint') }}</v-card-text>
                <v-card-text>
                    <ModsTable
                        :mods="project.mods ?? []"
                        :fetching="fetching"
                        :fetch-disabled="fetchingAll"
                        :flashed="flashed"
                        @fetch="fetchModVersion"
                        @fetch-all="fetchAllVersions"
                    />
                </v-card-text>
            </v-card>
        </template>

        <!-- 移除确认 -->
        <ConfirmDialog
            v-model="removeDialog"
            :title="t('projects.removeTitle')"
            :text="t('projects.removeMessage', [project?.name ?? name])"
            :consequences="[t('projects.removeC1'), t('projects.removeC2')]"
            :confirm-text="t('projects.removeBtn')"
            icon="mdi-delete-alert-outline"
            danger
            :loading="removing"
            :error="removeError"
            @confirm="confirmRemove"
        />

        <!-- 更新检查 -->
        <CheckUpdatesDialog v-model="checkOpen" :project="project ?? null" @changed="loadProjects(true)" />
    </v-container>
</template>

<style scoped>
.path-text {
    max-width: 420px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
</style>
