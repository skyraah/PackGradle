<script setup lang="ts">
// 项目详情：项目信息 + mod 管理（搜索/side 过滤/版本获取）+ packwiz 刷新与更新检查。
// 路由参数为项目名，数据来自共享项目缓存；跨视图变更（meta 拉取）自动刷新。
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { PackwizService } from '../../bindings/packgradle/internal/service'
import type { ModInfo } from '../../bindings/packgradle/internal/packwiz'
import { loadProjects, setProjects, findProject, projectsVersion } from '../stores/projects'
import { showSnackbar } from '../stores/ui'
import { handleApiKeyError } from '../stores/apiKeyGuide'
import { errText, displayText } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'
import OutputDialog from '../components/common/OutputDialog.vue'
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

// 行级操作状态
const refreshing = ref(false)
const fetching = ref<string | null>(null)
const fetchingAll = ref(false)

// 对话框
const removeDialog = ref(false)
const removing = ref(false)
const outputOpen = ref(false)
const outputTitle = ref('')
const output = ref('')
const checkOpen = ref(false)

function loaderChip(loader: string) {
    return loaderChips[loader] ?? { label: loader, color: 'grey' }
}

// 确保项目缓存就绪且包含当前项目
async function ensureLoaded() {
    await loadProjects()
    if (!findProject(name.value)) await loadProjects(true)
}

onMounted(async () => {
    await ensureLoaded()
    notFound.value = !findProject(name.value)
})

// 路由参数变化（页面内跳转到另一项目）
watch(
    () => route.params.name,
    async () => {
        notFound.value = false
        await ensureLoaded()
        notFound.value = !findProject(name.value)
    },
)

// 跨视图数据变更（如 Prism 联动页拉取 meta 改变项目 mods）后自动刷新
watch(projectsVersion, () => {
    void loadProjects(true)
})

async function refreshProject() {
    if (!project.value) return
    refreshing.value = true
    try {
        const result = await PackwizService.RefreshProject(project.value.name)
        outputTitle.value = t('projects.refreshOutputTitle')
        output.value = displayText(
            result.output || (result.ok ? t('projects.outputSuccess', ['packwiz refresh']) : t('projects.outputFailed')),
        )
        outputOpen.value = true
        await loadProjects(true)
    } finally {
        refreshing.value = false
    }
}

async function fetchModVersion(mod: ModInfo) {
    if (!project.value) return
    fetching.value = mod.id
    try {
        const updated = await PackwizService.FetchModVersion(project.value.name, mod.id)
        const target = project.value.mods?.find(m => m.id === mod.id)
        if (target && updated) Object.assign(target, updated)
        showSnackbar(t('projects.versionFetched', [updated?.name ?? mod.name]))
    } catch (e) {
        handleApiKeyError(e)
    } finally {
        fetching.value = null
    }
}

async function fetchAllVersions() {
    if (!project.value) return
    fetchingAll.value = true
    try {
        const results = (await PackwizService.FetchAllModVersions(project.value.name)) ?? []
        const ok = results.filter(r => r.ok).length
        showSnackbar(t('projects.versionsFetched', [ok, results.length]))
        await loadProjects(true)
    } catch (e) {
        handleApiKeyError(e)
    } finally {
        fetchingAll.value = false
    }
}

async function confirmRemove() {
    if (!project.value) return
    removeDialog.value = false
    removing.value = true
    try {
        setProjects(await PackwizService.RemoveProject(project.value.name))
        showSnackbar(t('projects.removed', [project.value.name]))
        router.push('/projects')
    } catch (e) {
        showSnackbar(t('projects.removeFailed', [errText(e)]))
    } finally {
        removing.value = false
    }
}

async function copyPath() {
    if (!project.value) return
    try {
        await navigator.clipboard.writeText(project.value.path)
        showSnackbar(t('common.copied'))
    } catch {
        // 剪贴板不可用时静默忽略（WebView 权限差异）
    }
}
</script>

<template>
    <v-container fluid class="pa-6">
        <v-btn variant="text" prepend-icon="mdi-arrow-left" class="mb-3" @click="router.push('/projects')">
            {{ t('projects.detailBack') }}
        </v-btn>

        <v-progress-linear v-if="loading" indeterminate class="mb-4" />

        <!-- 项目不存在 -->
        <v-card v-if="notFound" class="py-4">
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
            <v-card class="mb-5">
                <v-card-text class="d-flex align-center flex-wrap ga-4">
                    <v-avatar rounded="xl" size="56" :color="project.error ? 'error' : 'primary'" variant="tonal">
                        <v-icon :icon="project.error ? 'mdi-alert-outline' : 'mdi-package-variant-closed'" size="30" />
                    </v-avatar>
                    <div class="flex-grow-1" style="min-width: 200px">
                        <div class="d-flex align-center flex-wrap ga-2">
                            <span class="text-h6 font-weight-bold">{{ project.name }}</span>
                            <v-chip v-if="project.error" size="x-small" color="error" variant="tonal">
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
                            <v-chip v-if="project.modloader" size="x-small" :color="loaderChip(project.modloader).color" variant="tonal">
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
                    <div v-if="!project.error" class="d-flex flex-wrap ga-2">
                        <v-btn variant="tonal" prepend-icon="mdi-refresh" :loading="refreshing" @click="refreshProject">
                            {{ t('projects.tooltipRefresh') }}
                        </v-btn>
                        <v-btn variant="tonal" prepend-icon="mdi-cloud-download" :loading="fetchingAll" @click="fetchAllVersions">
                            {{ t('projects.tooltipFetchAll') }}
                        </v-btn>
                        <v-btn variant="tonal" prepend-icon="mdi-update" @click="checkOpen = true">
                            {{ t('projects.tooltipCheckUpdates') }}
                        </v-btn>
                    </div>
                    <div v-else class="text-body-2 text-error">{{ displayText(project.error) }}</div>
                    <v-btn
                        icon="mdi-delete-outline"
                        variant="text"
                        color="error"
                        :title="t('projects.tooltipRemove')"
                        @click="removeDialog = true"
                    />
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
                        @fetch="fetchModVersion"
                    />
                </v-card-text>
            </v-card>
        </template>

        <!-- 移除确认 -->
        <ConfirmDialog
            v-model="removeDialog"
            :title="t('projects.removeTitle')"
            :text="t('projects.removeMessage', [project?.name ?? name])"
            :confirm-text="t('projects.removeBtn')"
            confirm-color="error"
            icon="mdi-alert-outline"
            :loading="removing"
            @confirm="confirmRemove"
        />

        <!-- 命令输出 -->
        <OutputDialog v-model="outputOpen" :title="outputTitle" :output="output" />

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
