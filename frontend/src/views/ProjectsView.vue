<script setup lang="ts">
// 项目列表：卡片 + 搜索，行内操作收敛为溢出菜单；点击卡片进入详情页。
// 列表由 keep-alive 保持存活（返回时保留搜索/滚动状态），数据经共享缓存与详情页联动。
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Dialogs } from '@wailsio/runtime'
import { PackwizService } from '../../bindings/packgradle/internal/service'
import type { PackProject } from '../../bindings/packgradle/internal/packwiz'
import { loadProjects, setProjects, projectsVersion, projects, loaded } from '../stores/projects'
import { showSnackbar } from '../stores/ui'
import { handleApiKeyError } from '../stores/apiKeyGuide'
import { errText, displayText } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import PageHeader from '../components/common/PageHeader.vue'
import EmptyState from '../components/common/EmptyState.vue'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'
import OutputDialog from '../components/common/OutputDialog.vue'
import CheckUpdatesDialog from '../components/projects/CheckUpdatesDialog.vue'

// keep-alive 依赖组件名匹配
defineOptions({ name: 'ProjectsView' })

const { t } = useI18n()
const router = useRouter()

const loading = ref(false)
const importing = ref(false)
const search = ref('')

// 行级操作状态
const refreshing = ref<string | null>(null)
const fetchingAll = ref<string | null>(null)

// 对话框
const removeDialog = ref(false)
const removing = ref<PackProject | null>(null)
const outputOpen = ref(false)
const outputTitle = ref('')
const output = ref('')
const checkTarget = ref<PackProject | null>(null)
const checkOpen = ref(false)

const filtered = computed(() => {
    const q = search.value.trim().toLowerCase()
    if (!q) return projects.value
    return projects.value.filter(p => p.name.toLowerCase().includes(q))
})

function loaderChip(loader: string) {
    return loaderChips[loader] ?? { label: loader, color: 'grey' }
}

// 项目解析失败原因（错误码 JSON 文本）→ 用户可读文本
function projectError(proj: PackProject): string {
    return displayText(proj.error)
}

async function load(force = false) {
    loading.value = true
    try {
        await loadProjects(force)
    } finally {
        loading.value = false
    }
}

function openDetail(proj: PackProject) {
    router.push({ name: 'project-detail', params: { name: proj.name } })
}

async function importProject() {
    let picked: string | string[]
    try {
        picked = await Dialogs.OpenFile({
            Title: t('projects.pickPackToml'),
            CanChooseFiles: true,
            Filters: [{ DisplayName: 'pack.toml', Pattern: 'pack.toml' }],
        })
    } catch {
        return // 用户取消选择，静默忽略
    }
    if (!picked) return
    importing.value = true
    try {
        const proj = await PackwizService.ImportProject(String(picked))
        showSnackbar(t('projects.imported', [proj.name, (proj.mods ?? []).length]))
        await load(true)
        openDetail(proj)
    } catch (e) {
        showSnackbar(t('projects.importFailed', [errText(e)]))
    } finally {
        importing.value = false
    }
}

function askRemove(proj: PackProject) {
    removing.value = proj
    removeDialog.value = true
}

async function confirmRemove() {
    const proj = removing.value
    if (!proj) return
    removeDialog.value = false
    removing.value = null
    try {
        setProjects(await PackwizService.RemoveProject(proj.name))
        showSnackbar(t('projects.removed', [proj.name]))
        await load(true)
    } catch (e) {
        showSnackbar(t('projects.removeFailed', [errText(e)]))
    }
}

async function refreshProject(proj: PackProject) {
    refreshing.value = proj.name
    try {
        const result = await PackwizService.RefreshProject(proj.name)
        outputTitle.value = t('projects.refreshOutputTitle')
        output.value = displayText(
            result.output || (result.ok ? t('projects.outputSuccess', ['packwiz refresh']) : t('projects.outputFailed')),
        )
        outputOpen.value = true
        await load(true)
    } finally {
        refreshing.value = null
    }
}

async function fetchAllVersions(proj: PackProject) {
    fetchingAll.value = proj.name
    try {
        const results = (await PackwizService.FetchAllModVersions(proj.name)) ?? []
        const ok = results.filter(r => r.ok).length
        showSnackbar(t('projects.versionsFetched', [ok, results.length]))
        await load(true)
    } catch (e) {
        handleApiKeyError(e)
    } finally {
        fetchingAll.value = null
    }
}

function openCheck(proj: PackProject) {
    checkTarget.value = proj
    checkOpen.value = true
}

onMounted(async () => {
    // 共享缓存已就绪时直接展示；未就绪才拉取
    if (!loaded.value) await load()
})

// 跨视图数据变更（如 Prism 联动页拉取 meta 改变项目 mods）后自动刷新列表
watch(projectsVersion, () => {
    void load(true)
})
</script>

<template>
    <v-container fluid class="pa-6">
        <PageHeader :title="t('projects.title')" :subtitle="t('projects.subtitle')">
            <template #actions>
                <v-btn variant="text" icon="mdi-refresh" :loading="loading" @click="load(true)" />
                <v-btn color="primary" prepend-icon="mdi-folder-open" :loading="importing" @click="importProject">
                    {{ t('projects.importBtn') }}
                </v-btn>
            </template>
        </PageHeader>

        <v-progress-linear v-if="loading" indeterminate class="mb-4" />

        <!-- 搜索 -->
        <v-text-field
            v-if="projects.length > 0"
            v-model="search"
            :placeholder="t('projects.search')"
            prepend-inner-icon="mdi-magnify"
            density="comfortable"
            hide-details
            clearable
            class="mb-4 search-field"
        />

        <!-- 空状态 -->
        <v-card v-if="projects.length === 0 && !loading" class="py-4">
            <EmptyState
                icon="mdi-package-variant-closed"
                :title="t('dashboard.projectsEmpty')"
                :text="t('dashboard.projectsEmptyHint')"
            >
                <template #actions>
                    <v-btn color="primary" prepend-icon="mdi-folder-open" :loading="importing" @click="importProject">
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
                <v-card class="hover-card project-card" @click="openDetail(proj)">
                    <v-card-item>
                        <template #prepend>
                            <v-avatar rounded="lg" size="44" :color="proj.error ? 'error' : 'primary'" variant="tonal">
                                <v-icon :icon="proj.error ? 'mdi-alert-outline' : 'mdi-package-variant-closed'" size="24" />
                            </v-avatar>
                        </template>
                        <v-card-title class="py-0">
                            {{ proj.name }}
                            <v-chip v-if="proj.error" size="x-small" color="error" variant="tonal" class="ml-2">
                                {{ t('projects.parseFailed') }}
                            </v-chip>
                        </v-card-title>
                        <v-card-subtitle class="text-caption project-path">
                            {{ proj.path }}
                        </v-card-subtitle>
                    </v-card-item>

                    <v-card-text>
                        <template v-if="!proj.error">
                            <div class="d-flex flex-wrap ga-1 mb-2">
                                <v-chip v-if="proj.modloader" size="x-small" :color="loaderChip(proj.modloader).color" variant="tonal">
                                    {{ loaderChip(proj.modloader).label }} {{ proj.modloader_version }}
                                </v-chip>
                                <v-chip v-if="proj.minecraft" size="x-small" variant="tonal">
                                    {{ t('projects.minecraft', [proj.minecraft]) }}
                                </v-chip>
                                <v-chip v-if="proj.version" size="x-small" variant="tonal">v{{ proj.version }}</v-chip>
                                <v-chip v-if="proj.author" size="x-small" variant="tonal">
                                    {{ t('projects.author', [proj.author]) }}
                                </v-chip>
                            </div>
                            <div class="text-caption text-medium-emphasis">
                                {{ t('projects.modCount', [(proj.mods ?? []).length]) }}
                            </div>
                        </template>
                        <div v-else class="text-caption text-error">{{ projectError(proj) }}</div>
                    </v-card-text>

                    <v-card-actions class="px-4 pb-3">
                        <v-btn
                            size="small"
                            variant="text"
                            color="primary"
                            append-icon="mdi-arrow-top-right"
                            @click.stop="openDetail(proj)"
                        >
                            {{ t('projects.openDetail') }}
                        </v-btn>
                        <v-spacer />
                        <v-menu>
                            <template v-slot:activator="{ props }">
                                <v-btn v-bind="props" icon="mdi-dots-vertical" size="small" variant="text" :title="t('projects.menu')" @click.stop />
                            </template>
                            <v-list density="compact" min-width="220">
                                <v-list-item
                                    v-if="!proj.error"
                                    prepend-icon="mdi-refresh"
                                    :title="t('projects.tooltipRefresh')"
                                    :disabled="refreshing === proj.name"
                                    @click="refreshProject(proj)"
                                />
                                <v-list-item
                                    v-if="!proj.error"
                                    prepend-icon="mdi-cloud-download"
                                    :title="t('projects.tooltipFetchAll')"
                                    :disabled="fetchingAll === proj.name"
                                    @click="fetchAllVersions(proj)"
                                />
                                <v-list-item
                                    v-if="!proj.error"
                                    prepend-icon="mdi-update"
                                    :title="t('projects.tooltipCheckUpdates')"
                                    @click="openCheck(proj)"
                                />
                                <v-divider />
                                <v-list-item
                                    prepend-icon="mdi-delete-outline"
                                    color="error"
                                    :title="t('projects.tooltipRemove')"
                                    @click="askRemove(proj)"
                                />
                            </v-list>
                        </v-menu>
                    </v-card-actions>
                </v-card>
            </v-col>
        </v-row>

        <!-- 移除确认 -->
        <ConfirmDialog
            v-model="removeDialog"
            :title="t('projects.removeTitle')"
            :text="t('projects.removeMessage', [removing?.name ?? ''])"
            :confirm-text="t('projects.removeBtn')"
            confirm-color="error"
            icon="mdi-alert-outline"
            @confirm="confirmRemove"
        />

        <!-- 命令输出 -->
        <OutputDialog v-model="outputOpen" :title="outputTitle" :output="output" />

        <!-- 更新检查 -->
        <CheckUpdatesDialog v-model="checkOpen" :project="checkTarget" @changed="load(true)" />
    </v-container>
</template>

<style scoped>
.search-field {
    max-width: 360px;
}
.project-card {
    border-radius: 14px;
}
.project-path {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
</style>
