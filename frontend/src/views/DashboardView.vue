<script setup lang="ts">
// 工作台：欢迎横幅 + 环境健康卡 + 项目/关联概览。
// 快速开始清单已移至 OnboardingDialog（仅首次未检测到 config.toml 时弹出）。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { PackwizService } from '../api'
import { pickPackToml } from '../utils/dialogs'
import { loadProjects, projects } from '../stores/projects'
import { useEnv } from '../stores/env'
import { useInstances } from '../stores/instances'
import { runTask } from '../stores/taskCenter'
import { errText } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import EmptyState from '../components/common/EmptyState.vue'

const { t } = useI18n()
const router = useRouter()

const { tools, apiKey, loadTools, loadApiKey } = useEnv()
const { overview, loadOverview } = useInstances()
const loading = ref(true)
const ready = ref(false)
const loadError = ref('')

async function load() {
    loading.value = true
    loadError.value = ''
    try {
        const results = await Promise.allSettled([loadTools(), loadApiKey(), loadProjects(), loadOverview()])
        const failed = results.find(result => result.status === 'rejected')
        if (failed?.status === 'rejected') loadError.value = errText(failed.reason)
        else ready.value = true
    } finally {
        loading.value = false
    }
}

onMounted(load)

// —— 环境健康卡 ——
interface EnvCard {
    key: string
    label: string
    icon: string
    ok: boolean
    text: string
    to: string
}

function toolText(tool: { found: boolean; source: string; path: string } | undefined): string {
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

const links = computed(() => overview.value?.links ?? [])

function openProject(name: string) {
    router.push({ name: 'project-detail', params: { name } })
}

// 导入项目：系统对话框选择 pack.toml → PackwizService.ImportProject；取消选择则静默返回
async function importProject() {
    const picked = await pickPackToml()
    if (!picked) return
    await runTask({
        title: t('tasks.importProject'),
        kind: 'import',
        run: async () => {
            const proj = await PackwizService.ImportProject(picked)
            await loadProjects(true)
            openProject(proj.name)
            return t('projects.imported', [proj.name, (proj.mods ?? []).length])
        },
    })
}
</script>

<template>
    <v-container fluid class="pa-6">
        <v-progress-linear v-if="loading" indeterminate class="mb-4" />

        <v-alert v-if="loadError" type="error" variant="tonal" class="mb-5">
            <div class="d-flex align-center ga-3">
                <span class="flex-grow-1">{{ loadError }}</span>
                <v-btn size="small" variant="tonal" @click="load">{{ t('common.refresh') }}</v-btn>
            </div>
        </v-alert>

        <!-- 欢迎横幅 + 主操作 -->
        <v-card class="hero-card mb-6">
            <v-card-text class="d-flex align-center flex-wrap ga-4 pa-6">
                <div class="flex-grow-1" style="min-width: 240px">
                    <h1 class="text-h5 font-weight-bold mb-1">{{ t('dashboard.greeting') }}</h1>
                    <div class="text-body-2 text-medium-emphasis">{{ t('dashboard.subtitle') }}</div>
                </div>
                <div class="d-flex ga-2 flex-wrap">
                    <v-btn color="primary" size="large" class="primary-action" prepend-icon="mdi-folder-open" @click="importProject">
                        {{ t('dashboard.importBtn') }}
                    </v-btn>
                    <v-btn variant="tonal" size="large" prepend-icon="mdi-link-variant" @click="router.push('/instances')">
                        {{ t('dashboard.linkBtn') }}
                    </v-btn>
                </div>
            </v-card-text>
        </v-card>

        <!-- 环境健康卡 -->
        <v-row v-if="ready" class="mb-2">
            <v-col v-for="card in envCards" :key="card.key" cols="12" sm="6" lg="3">
                <div
                    class="surface-tile hover-card env-card"
                    :class="{ 'card-error': !card.ok }"
                    role="button"
                    tabindex="0"
                    @click="router.push(card.to)"
                    @keyup.enter="router.push(card.to)"
                >
                    <div class="d-flex align-center mb-2">
                        <v-avatar rounded="lg" size="44" :color="card.ok ? 'success' : 'error'" variant="tonal" class="mr-3">
                            <v-icon :icon="card.icon" size="24" />
                        </v-avatar>
                        <div class="flex-grow-1" style="min-width: 0">
                            <div class="text-caption text-medium-emphasis">{{ card.label }}</div>
                            <div class="text-subtitle-2" :class="card.ok ? '' : 'text-error'">
                                {{ card.ok ? t('dashboard.envOk') : t('dashboard.envBad') }}
                            </div>
                        </div>
                    </div>
                    <div class="text-caption text-medium-emphasis env-text">{{ card.text }}</div>
                </div>
            </v-col>
        </v-row>

        <v-row v-if="ready" class="dashboard-overview">
            <!-- 我的项目 -->
            <v-col cols="12" lg="6">
                <v-card class="h-100">
                    <v-card-title class="d-flex align-center pt-5">
                        <v-icon icon="mdi-package-variant-closed" color="primary" class="mr-2" />
                        {{ t('dashboard.projectsTitle') }}
                        <v-spacer />
                        <v-btn size="small" variant="text" append-icon="mdi-chevron-right" @click="router.push('/projects')">
                            {{ t('common.viewAll') }}
                        </v-btn>
                    </v-card-title>
                    <v-card-text v-if="projects.length > 0">
                        <v-list density="compact">
                            <v-list-item v-for="proj in projects.slice(0, 5)" :key="proj.name" @click="openProject(proj.name)">
                                <template #prepend>
                                    <v-avatar rounded="lg" size="36" :color="proj.error ? 'error' : loaderChips[proj.modloader]?.color ?? 'primary'" variant="tonal">
                                        <v-icon :icon="proj.error ? 'mdi-alert-outline' : 'mdi-package-variant-closed'" size="20" />
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
                    <EmptyState v-else icon="mdi-package-variant-closed" :title="t('dashboard.projectsEmpty')" :text="t('dashboard.projectsEmptyHint')">
                        <template #actions>
                            <v-btn color="primary" prepend-icon="mdi-folder-open" @click="importProject">
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
                        <v-btn size="small" variant="text" append-icon="mdi-chevron-right" @click="router.push('/instances')">
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
                                    <v-chip size="x-small" :color="link.instance_valid ? 'success' : 'error'" variant="tonal">
                                        {{ link.instance_name || link.instance_id }}
                                    </v-chip>
                                    <v-chip v-if="!link.instance_valid" size="x-small" color="error" variant="outlined" class="ml-2">
                                        {{ t('dashboard.linkStatus.invalid') }}
                                    </v-chip>
                                </template>
                            </v-list-item>
                        </v-list>
                    </v-card-text>
                    <EmptyState v-else icon="mdi-link-variant-off" :title="t('dashboard.linksEmpty')" :text="t('dashboard.linksEmptyHint')">
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
.hero-card {
    background: linear-gradient(135deg, rgba(76, 194, 255, 0.1), transparent 55%) !important;
}
.env-card {
    min-height: 96px;
}
.env-text {
    display: block;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
.dashboard-overview {
    margin-top: 20px;
}
</style>
