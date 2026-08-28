<script setup lang="ts">
// 项目列表：加载器着色卡片 + 搜索 + 加载器过滤 + 行内操作。
// 写操作全部经任务中心（进度可见、结果可追溯）；列表 keep-alive 保持搜索/滚动状态。
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { PackwizService } from '../api'
import { pickPackToml } from '../utils/dialogs'
import type { PackProject } from '../../bindings/packgradle/internal/packwiz'
import { loadProjects, setProjects, projectsVersion, projects, loaded } from '../stores/projects'
import { runTask } from '../stores/taskCenter'
import { showSnackbar } from '../stores/ui'
import { displayText, errText } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import PageHeader from '../components/common/PageHeader.vue'
import EmptyState from '../components/common/EmptyState.vue'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'
import CheckUpdatesDialog from '../components/projects/CheckUpdatesDialog.vue'

// keep-alive 依赖组件名匹配
defineOptions({ name: 'ProjectsView' })

const { t } = useI18n()
const router = useRouter()

const loading = ref(false)
const loadError = ref('')
const search = ref('')
const loaderFilter = ref<string[]>([])

// 行级操作状态
const refreshing = ref<string | null>(null)
const fetchingAll = ref<string | null>(null)

// 对话框
const removeDialog = ref(false)
const removing = ref<PackProject | null>(null)
const removeBusy = ref(false)
const removeError = ref('')
const checkTarget = ref<PackProject | null>(null)
const checkOpen = ref(false)

const loaderOptions = computed(() => {
    const set = new Set<string>()
    for (const p of projects.value) if (p.modloader) set.add(p.modloader)
    return [...set]
})

const filtered = computed(() => {
    const q = search.value.trim().toLowerCase()
    return projects.value.filter(p => {
        if (loaderFilter.value.length > 0 && !loaderFilter.value.includes(p.modloader)) return false
        if (!q) return true
        return p.name.toLowerCase().includes(q)
    })
})

function loaderChip(loader: string) {
    return loaderChips[loader] ?? { label: loader, color: 'grey' }
}

function projectError(proj: PackProject): string {
    return displayText(proj.error)
}

async function load(force = false): Promise<boolean> {
    loading.value = true
    loadError.value = ''
    try {
        await loadProjects(force)
        return true
    } catch (e) {
        loadError.value = errText(e)
        return false
    } finally {
        loading.value = false
    }
}

function openDetail(proj: PackProject) {
    router.push({ name: 'project-detail', params: { name: proj.name } })
}

// 导入项目：系统对话框选择 pack.toml → PackwizService.ImportProject；取消选择则静默返回
async function importProject() {
    const picked = await pickPackToml()
    if (!picked) return
    let refreshFailed = false
    await runTask({
        title: t('tasks.importProject'),
        kind: 'import',
        run: async () => {
            const proj = await PackwizService.ImportProject(picked)
            refreshFailed = !(await load(true))
            openDetail(proj)
            return t('projects.imported', [proj.name, (proj.mods ?? []).length])
        },
        warn: () => refreshFailed,
    })
}

function askRemove(proj: PackProject) {
    removing.value = proj
    removeError.value = ''
    removeDialog.value = true
}

async function confirmRemove() {
    const proj = removing.value
    if (!proj || removeBusy.value) return
    removeBusy.value = true
    removeError.value = ''
    try {
        const result = await runTask({
            title: t('tasks.removeProject', [proj.name]),
            kind: 'remove',
            run: async () => {
                setProjects(await PackwizService.RemoveProject(proj.name))
                return t('projects.removed', [proj.name])
            },
            onError: message => (removeError.value = message),
        })
        if (result !== null) {
            removeDialog.value = false
            removing.value = null
        }
    } finally {
        removeBusy.value = false
    }
}

async function refreshProject(proj: PackProject) {
    refreshing.value = proj.name
    let output = ''
    let refreshFailed = false
    try {
        await runTask({
            title: t('tasks.refreshProject', [proj.name]),
            kind: 'refresh',
            run: async () => {
                const result = await PackwizService.RefreshProject(proj.name)
                output = result.output
                if (!result.ok) throw new Error(displayText(result.output || t('projects.outputFailed')))
                refreshFailed = !(await load(true))
                return t('projects.outputSuccess', ['packwiz refresh'])
            },
            output: () => output,
            warn: () => refreshFailed,
        })
    } finally {
        refreshing.value = null
    }
}

async function fetchAllVersions(proj: PackProject) {
    fetchingAll.value = proj.name
    let hasFailures = false
    try {
        await runTask({
            title: t('tasks.fetchAllVersions', [proj.name]),
            kind: 'fetch',
            run: async () => {
                const results = (await PackwizService.FetchAllModVersions(proj.name)) ?? []
                const ok = results.filter(r => r.ok).length
                hasFailures = ok !== results.length
                const refreshFailed = !(await load(true))
                hasFailures ||= refreshFailed
                return t('projects.versionsFetched', [ok, results.length])
            },
            warn: () => hasFailures,
        })
    } finally {
        fetchingAll.value = null
    }
}

function openCheck(proj: PackProject) {
    checkTarget.value = proj
    checkOpen.value = true
}

onMounted(async () => {
    if (!loaded.value) await load()
})

watch(projectsVersion, () => {
    void load(true)
})
</script>

<template>
    <v-container fluid class="pa-6">
        <PageHeader :title="t('projects.title')" :subtitle="t('projects.subtitle')">
            <template #actions>
                <v-btn variant="text" icon="mdi-refresh" :loading="loading" :title="t('common.refresh')" @click="load(true)" />
                <v-btn color="primary" class="primary-action" prepend-icon="mdi-folder-open" @click="importProject">
                    {{ t('projects.importBtn') }}
                </v-btn>
            </template>
        </PageHeader>

        <v-progress-linear v-if="loading" indeterminate class="mb-4" />

        <v-alert v-if="loadError" type="error" variant="tonal" class="mb-5">
            <div class="d-flex align-center ga-3">
                <span class="flex-grow-1">{{ loadError }}</span>
                <v-btn size="small" variant="tonal" @click="load(true)">{{ t('common.refresh') }}</v-btn>
            </div>
        </v-alert>

        <!-- 工具行：搜索 + 加载器过滤 -->
        <div v-if="projects.length > 0" class="d-flex align-center flex-wrap ga-3 mb-4">
            <v-text-field
                v-model="search"
                :placeholder="t('projects.search')"
                prepend-inner-icon="mdi-magnify"
                density="comfortable"
                hide-details
                clearable
                class="search-field"
            />
            <v-chip-group v-model="loaderFilter" multiple selected-class="chip-on">
                <v-chip v-for="l in loaderOptions" :key="l" :value="l" variant="tonal" filter>
                    {{ loaderChip(l).label }}
                </v-chip>
            </v-chip-group>
        </div>

        <!-- 空状态 -->
        <v-card v-if="projects.length === 0 && !loading && !loadError" class="py-4">
            <EmptyState icon="mdi-package-variant-closed" :title="t('dashboard.projectsEmpty')" :text="t('dashboard.projectsEmptyHint')">
                <template #actions>
                    <v-btn color="primary" prepend-icon="mdi-folder-open" @click="importProject">
                        {{ t('projects.importBtn') }}
                    </v-btn>
                </template>
            </EmptyState>
        </v-card>

        <!-- 无匹配 -->
        <div v-else-if="filtered.length === 0" class="text-body-2 text-medium-emphasis text-center py-8">
            {{ t('projects.noMatch', [search]) }}
        </div>

        <!-- 项目卡片 -->
        <v-row v-else>
            <v-col v-for="proj in filtered" :key="proj.name" cols="12" md="6" xl="4">
                <v-card class="hover-card project-card" :class="{ 'card-error': proj.error }" @click="openDetail(proj)">
                    <v-card-text>
                        <div class="d-flex align-center mb-3">
                            <v-avatar
                                rounded="lg"
                                size="48"
                                :color="proj.error ? 'error' : loaderChip(proj.modloader).color"
                                variant="tonal"
                                class="mr-3"
                            >
                                <span class="text-subtitle-1 font-weight-bold">{{ proj.name.slice(0, 1).toUpperCase() }}</span>
                            </v-avatar>
                            <div class="flex-grow-1" style="min-width: 0">
                                <div class="d-flex align-center">
                                    <span class="text-subtitle-2 font-weight-bold project-name">{{ proj.name }}</span>
                                    <v-chip v-if="proj.error" size="x-small" color="error" variant="flat" class="ml-2">
                                        {{ t('projects.parseFailed') }}
                                    </v-chip>
                                </div>
                                <div class="text-caption text-medium-emphasis project-path">{{ proj.path }}</div>
                            </div>
                        </div>

                        <template v-if="!proj.error">
                            <div class="d-flex flex-wrap ga-1 mb-2">
                                <v-chip v-if="proj.modloader" size="x-small" :color="loaderChip(proj.modloader).color" variant="flat">
                                    {{ loaderChip(proj.modloader).label }} {{ proj.modloader_version }}
                                </v-chip>
                                <v-chip v-if="proj.minecraft" size="x-small" variant="tonal">
                                    {{ t('projects.minecraft', [proj.minecraft]) }}
                                </v-chip>
                                <v-chip v-if="proj.version" size="x-small" variant="tonal">v{{ proj.version }}</v-chip>
                            </div>
                            <div class="text-caption text-medium-emphasis">
                                {{ t('projects.modCount', [(proj.mods ?? []).length]) }}
                            </div>
                        </template>
                        <div v-else class="text-caption text-error">{{ projectError(proj) }}</div>
                    </v-card-text>

                    <v-divider />

                    <v-card-actions class="px-3 py-1">
                        <v-btn size="small" variant="text" color="primary" append-icon="mdi-arrow-top-right" @click.stop="openDetail(proj)">
                            {{ t('projects.openDetail') }}
                        </v-btn>
                        <v-spacer />
                        <v-btn
                            v-if="!proj.error"
                            icon="mdi-refresh"
                            size="small"
                            variant="text"
                            :title="t('projects.tooltipRefresh')"
                            :loading="refreshing === proj.name"
                            @click.stop="refreshProject(proj)"
                        />
                        <v-btn
                            v-if="!proj.error"
                            icon="mdi-update"
                            size="small"
                            variant="text"
                            :title="t('projects.tooltipCheckUpdates')"
                            @click.stop="openCheck(proj)"
                        />
                        <v-menu>
                            <template v-slot:activator="{ props }">
                                <v-btn v-bind="props" icon="mdi-dots-vertical" size="small" variant="text" :title="t('projects.menu')" @click.stop />
                            </template>
                            <v-list density="compact" min-width="220">
                                <v-list-item
                                    v-if="!proj.error"
                                    prepend-icon="mdi-cloud-download"
                                    :title="t('projects.tooltipFetchAll')"
                                    :disabled="fetchingAll === proj.name"
                                    @click="fetchAllVersions(proj)"
                                />
                                <v-list-item
                                    prepend-icon="mdi-rocket-launch-outline"
                                    :title="t('dev.goDev')"
                                    @click="router.push({ path: '/dev', query: { project: proj.name } })"
                                />
                                <v-divider />
                                <v-list-item
                                    prepend-icon="mdi-delete-outline"
                                    class="menu-danger"
                                    :title="t('projects.tooltipRemove')"
                                    @click="askRemove(proj)"
                                />
                            </v-list>
                        </v-menu>
                    </v-card-actions>
                </v-card>
            </v-col>
        </v-row>

        <!-- 移除确认（danger 变体 + 后果列表） -->
        <ConfirmDialog
            v-model="removeDialog"
            :title="t('projects.removeTitle')"
            :text="t('projects.removeMessage', [removing?.name ?? ''])"
            :consequences="[t('projects.removeC1'), t('projects.removeC2')]"
            :confirm-text="t('projects.removeBtn')"
            icon="mdi-delete-alert-outline"
            danger
            :loading="removeBusy"
            :error="removeError"
            @confirm="confirmRemove"
        />

        <!-- 更新检查 -->
        <CheckUpdatesDialog v-model="checkOpen" :project="checkTarget" @changed="load(true)" />
    </v-container>
</template>

<style scoped>
.search-field {
    max-width: 320px;
}
.project-card {
    border-radius: 14px;
}
.project-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.project-path {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.menu-danger {
    color: rgb(var(--v-theme-error));
}
.chip-on {
    background: rgb(var(--v-theme-primary)) !important;
    color: rgb(var(--v-theme-on-primary)) !important;
}
</style>
