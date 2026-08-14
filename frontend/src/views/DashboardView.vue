<script setup lang="ts">
// 工作台：开发环境健康 + 快速开始清单 + 项目/联动概览。
// 数据全部来自共享缓存，与设置页/项目页/联动页一致；
// 快速开始清单按「配置工具 → 填 Key → 导入项目 → 关联实例」的工作流引导新用户。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Dialogs } from '@wailsio/runtime'
import { PackwizService } from '../../bindings/packgradle/internal/service'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'
import { loadProjects, projects } from '../stores/projects'
import { useEnv } from '../stores/env'
import { useInstances } from '../stores/instances'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import EmptyState from '../components/common/EmptyState.vue'

const { t } = useI18n()
const router = useRouter()

const { tools, apiKey, loadTools, loadApiKey } = useEnv()
const { overview, loadOverview } = useInstances()
const loading = ref(true)
const importing = ref(false)

async function load() {
    loading.value = true
    try {
        // 四个独立查询并发执行，个别失败不影响其余展示
        await Promise.allSettled([loadTools(), loadApiKey(), loadProjects(), loadOverview()])
    } finally {
        loading.value = false
    }
}

onMounted(load)

// —— 快速开始清单 ——
interface ChecklistItem {
    key: string
    label: string
    done: boolean
    to: string
}

const checklist = computed<ChecklistItem[]>(() => {
    const packwiz = tools.value.find(t => t.name === 'packwiz')?.found ?? false
    const prism = tools.value.find(t => t.name === 'prism-launcher')?.found ?? false
    return [
        { key: 'packwiz', label: t('dashboard.qs.packwiz'), done: packwiz, to: '/settings' },
        { key: 'prism', label: t('dashboard.qs.prism'), done: prism, to: '/settings' },
        { key: 'apiKey', label: t('dashboard.qs.apiKey'), done: !!apiKey.value, to: '/settings' },
        { key: 'project', label: t('dashboard.qs.project'), done: projects.value.length > 0, to: '/projects' },
        { key: 'link', label: t('dashboard.qs.link'), done: (overview.value?.links?.length ?? 0) > 0, to: '/instances' },
    ]
})

const doneCount = computed(() => checklist.value.filter(c => c.done).length)
const checklistProgress = computed(() =>
    checklist.value.length === 0 ? 0 : Math.round((doneCount.value / checklist.value.length) * 100),
)

// —— 环境健康卡 ——
interface EnvCard {
    key: string
    label: string
    icon: string
    ok: boolean
    text: string
    to: string
}

function toolText(tool: ToolInfo | undefined): string {
    if (!tool || !tool.found) return t('tool.not_found')
    return t('tool.source.' + tool.source, [tool.path])
}

const envCards = computed<EnvCard[]>(() => {
    const packwiz = tools.value.find(t => t.name === 'packwiz')
    const prism = tools.value.find(t => t.name === 'prism-launcher')
    const ov = overview.value
    const locateOk = !!ov && !ov.locate_error
    return [
        { key: 'packwiz', label: t('dashboard.env.packwiz'), icon: 'mdi-package-variant-closed', ok: !!packwiz?.found, text: toolText(packwiz), to: '/settings' },
        { key: 'prism', label: t('dashboard.env.prism'), icon: 'mdi-launch', ok: !!prism?.found, text: toolText(prism), to: '/settings' },
        { key: 'apiKey', label: t('dashboard.env.apiKey'), icon: 'mdi-key-outline', ok: !!apiKey.value, text: apiKey.value ? t('env.apiKeyConfigured') : t('env.apiKeyNotConfigured'), to: '/settings' },
        { key: 'instances', label: t('dashboard.env.instances'), icon: 'mdi-prism', ok: locateOk, text: locateOk ? t('prism.instanceCount', [ov?.instances?.length ?? 0]) : t('tool.not_found'), to: '/instances' },
    ]
})

// —— 项目 / 关联概览 ——
const links = computed(() => overview.value?.links ?? [])

function openProject(name: string) {
    router.push({ name: 'project-detail', params: { name } })
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
        await loadProjects(true)
        openProject(proj.name)
    } catch (e) {
        showSnackbar(t('projects.importFailed', [errText(e)]))
    } finally {
        importing.value = false
    }
}
</script>

<template>
    <v-container fluid class="pa-6">
        <v-progress-linear v-if="loading" indeterminate class="mb-4" />

        <!-- 顶部问候 + 快捷操作 -->
        <div class="d-flex align-center flex-wrap ga-3 mb-5">
            <div class="flex-grow-1" style="min-width: 240px">
                <h1 class="text-h5 font-weight-bold mb-1">{{ t('dashboard.greeting') }}</h1>
                <div class="text-body-2 text-medium-emphasis">{{ t('dashboard.subtitle') }}</div>
            </div>
            <div class="d-flex ga-2 flex-wrap">
                <v-btn color="primary" prepend-icon="mdi-folder-open" :loading="importing" @click="importProject">
                    {{ t('dashboard.importBtn') }}
                </v-btn>
                <v-btn variant="tonal" prepend-icon="mdi-link-variant" @click="router.push('/instances')">
                    {{ t('dashboard.linkBtn') }}
                </v-btn>
                <v-btn variant="text" prepend-icon="mdi-cog-outline" @click="router.push('/settings')">
                    {{ t('dashboard.goSettings') }}
                </v-btn>
            </div>
        </div>

        <v-row>
            <!-- 快速开始 -->
            <v-col cols="12" lg="4">
                <v-card class="h-100">
                    <v-card-title class="d-flex align-center pt-5">
                        <v-icon icon="mdi-map-marker-path" color="primary" class="mr-2" />
                        {{ t('dashboard.quickStartTitle') }}
                        <v-spacer />
                        <span class="text-caption text-medium-emphasis">
                            {{ t('dashboard.quickStartProgress', [doneCount, checklist.length]) }}
                        </span>
                    </v-card-title>
                    <v-progress-linear :model-value="checklistProgress" color="primary" height="4" />
                    <v-card-text>
                        <div class="text-body-2 text-medium-emphasis mb-2">{{ t('dashboard.quickStartHint') }}</div>
                        <v-list density="compact">
                            <v-list-item
                                v-for="item in checklist"
                                :key="item.key"
                                :prepend-icon="item.done ? 'mdi-check-circle' : 'mdi-circle-outline'"
                                :title="item.label"
                                :color="item.done ? 'success' : undefined"
                                class="checklist-item"
                                @click="router.push(item.to)"
                            >
                                <template #append>
                                    <v-icon :icon="item.done ? 'mdi-check-circle' : 'mdi-chevron-right'" :color="item.done ? 'success' : undefined" size="small" />
                                </template>
                            </v-list-item>
                        </v-list>
                    </v-card-text>
                </v-card>
            </v-col>

            <!-- 开发环境 -->
            <v-col cols="12" lg="8">
                <v-card class="h-100">
                    <v-card-title class="d-flex align-center pt-5">
                        <v-icon icon="mdi-pulse" color="primary" class="mr-2" />
                        {{ t('dashboard.envTitle') }}
                    </v-card-title>
                    <v-card-text class="text-body-2 text-medium-emphasis pb-0">{{ t('dashboard.envHint') }}</v-card-text>
                    <v-card-text>
                        <v-row>
                            <v-col v-for="card in envCards" :key="card.key" cols="12" sm="6">
                                <v-card variant="flat" color="surface-bright" class="hover-card env-card" @click="router.push(card.to)">
                                    <v-card-text class="d-flex align-center">
                                        <v-avatar
                                            rounded="lg"
                                            size="40"
                                            :color="card.ok ? 'success' : 'warning'"
                                            variant="tonal"
                                            class="mr-3"
                                        >
                                            <v-icon :icon="card.icon" size="22" />
                                        </v-avatar>
                                        <div class="flex-grow-1" style="min-width: 0">
                                            <div class="text-caption text-medium-emphasis">{{ card.label }}</div>
                                            <div class="text-subtitle-2 env-text">{{ card.text }}</div>
                                        </div>
                                        <v-icon icon="mdi-chevron-right" size="small" class="text-medium-emphasis" />
                                    </v-card-text>
                                </v-card>
                            </v-col>
                        </v-row>
                    </v-card-text>
                </v-card>
            </v-col>
        </v-row>

        <v-row class="mt-1">
            <!-- 我的项目 -->
            <v-col cols="12" lg="6">
                <v-card class="h-100">
                    <v-card-title class="d-flex align-center pt-5">
                        <v-icon icon="mdi-package-variant-closed" color="primary" class="mr-2" />
                        {{ t('dashboard.projectsTitle') }}
                        <v-spacer />
                        <v-btn
                            size="small"
                            variant="text"
                            append-icon="mdi-chevron-right"
                            @click="router.push('/projects')"
                        >
                            {{ t('common.viewAll') }}
                        </v-btn>
                    </v-card-title>
                    <v-card-text v-if="projects.length > 0">
                        <v-list density="compact">
                            <v-list-item
                                v-for="proj in projects.slice(0, 5)"
                                :key="proj.name"
                                @click="openProject(proj.name)"
                            >
                                <template #prepend>
                                    <v-avatar rounded="md" size="36" color="primary" variant="tonal">
                                        <v-icon icon="mdi-package-variant-closed" size="20" />
                                    </v-avatar>
                                </template>
                                <v-list-item-title>{{ proj.name }}</v-list-item-title>
                                <v-list-item-subtitle class="text-caption">
                                    <template v-if="proj.modloader">
                                        {{ loaderChips[proj.modloader]?.label ?? proj.modloader }}
                                        <span class="text-medium-emphasis"> · </span>
                                    </template>
                                    <span class="text-medium-emphasis">{{ t('projects.modCount', [(proj.mods ?? []).length]) }}</span>
                                </v-list-item-subtitle>
                                <template #append>
                                    <v-icon icon="mdi-chevron-right" size="small" class="text-medium-emphasis" />
                                </template>
                            </v-list-item>
                        </v-list>
                    </v-card-text>
                    <EmptyState
                        v-else
                        icon="mdi-package-variant-closed"
                        :title="t('dashboard.projectsEmpty')"
                        :text="t('dashboard.projectsEmptyHint')"
                    >
                        <template #actions>
                            <v-btn color="primary" prepend-icon="mdi-folder-open" :loading="importing" @click="importProject">
                                {{ t('dashboard.importBtn') }}
                            </v-btn>
                        </template>
                    </EmptyState>
                </v-card>
            </v-col>

            <!-- 关联概览 -->
            <v-col cols="12" lg="6">
                <v-card class="h-100">
                    <v-card-title class="d-flex align-center pt-5">
                        <v-icon icon="mdi-link-variant" color="primary" class="mr-2" />
                        {{ t('dashboard.linksTitle') }}
                        <v-spacer />
                        <v-btn
                            size="small"
                            variant="text"
                            append-icon="mdi-chevron-right"
                            @click="router.push('/instances')"
                        >
                            {{ t('common.viewAll') }}
                        </v-btn>
                    </v-card-title>
                    <v-card-text v-if="links.length > 0">
                        <v-list density="compact">
                            <v-list-item v-for="link in links.slice(0, 5)" :key="link.project" @click="router.push('/instances')">
                                <template #prepend>
                                    <v-icon icon="mdi-package-variant-closed" class="mr-2" />
                                </template>
                                <v-list-item-title>{{ link.project }}</v-list-item-title>
                                <template #append>
                                    <v-icon icon="mdi-arrow-right" size="small" class="text-medium-emphasis mr-2" />
                                    <v-chip
                                        size="x-small"
                                        :color="link.instance_valid ? 'success' : 'error'"
                                        variant="tonal"
                                    >
                                        {{ link.instance_name || link.instance_id }}
                                    </v-chip>
                                    <v-chip v-if="!link.instance_valid" size="x-small" color="error" variant="outlined" class="ml-2">
                                        {{ t('dashboard.linkStatus.invalid') }}
                                    </v-chip>
                                </template>
                            </v-list-item>
                        </v-list>
                    </v-card-text>
                    <EmptyState
                        v-else
                        icon="mdi-link-variant-off"
                        :title="t('dashboard.linksEmpty')"
                        :text="t('dashboard.linksEmptyHint')"
                    >
                        <template #actions>
                            <v-btn color="primary" prepend-icon="mdi-link-variant" @click="router.push('/instances')">
                                {{ t('dashboard.linkBtn') }}
                            </v-btn>
                        </template>
                    </EmptyState>
                </v-card>
            </v-col>
        </v-row>
    </v-container>
</template>

<style scoped>
.env-card {
    border: 1px solid rgba(255, 255, 255, 0.06);
    border-radius: 14px;
}
.env-text {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
.checklist-item {
    border-radius: 10px;
    cursor: pointer;
}
</style>
