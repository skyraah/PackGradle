<script setup lang="ts">
// 开发版本页（focus model）：聚焦单个「项目 ↔ 实例」开发环境。
// 同步状态横幅 + 三个互斥区块（页签）：同步目录/文件、mod 差异（双边不一致时展示）。
// 目录管理逻辑复用 composables/useDirLinks；mod 差异来自后端 mods 目录监听
// （packgradle:mods-diff 事件推送）+ 主动 MetaDiff，写操作经任务中心。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Events } from '@wailsio/runtime'
import { PackwizService, PrismService } from '../api'
import type { MetaDiff, DirLinkView, ModsWatchEvent } from '../../bindings/packgradle/internal/prism'
import type { ModInfo } from '../../bindings/packgradle/internal/packwiz'
import { loadProjects, findProject, bumpProjectsVersion, invalidateProjects, projects as projectsCache } from '../stores/projects'
import { loadOverview, useInstances } from '../stores/instances'
import { useDirLinks } from '../composables/useDirLinks'
import { runTask } from '../stores/taskCenter'
import { showSnackbar } from '../stores/ui'
import { errText, displayText } from '../utils/errors'
import { loaderChips, sideColors } from '../utils/cf'
import PageHeader from '../components/common/PageHeader.vue'
import EmptyState from '../components/common/EmptyState.vue'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'
import FileSelectDialog from '../components/prism/FileSelectDialog.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

// —— focus 项目选择 ——
const { overview } = useInstances()
const focus = ref('')
const loading = ref(true)
const ready = ref(false)
const loadError = ref('')

const links = computed(() => overview.value?.links ?? [])
const instances = computed(() => overview.value?.instances ?? [])

const currentLink = computed(() => links.value.find(l => l.project === focus.value) ?? null)
const currentProject = computed(() => (focus.value ? findProject(focus.value) : undefined))
const currentInstance = computed(() => instances.value.find(i => i.id === currentLink.value?.instance_id) ?? null)

// focus 候选：已关联的项目优先，其次未关联项目（选择后引导去关联）
interface FocusItem {
    value: string
    title: string
    raw: {
        subtitle: string
        linked: boolean
    }
}
const focusItems = computed<FocusItem[]>(() => {
    const linked = links.value.map(l => ({
        value: l.project,
        title: l.project,
        raw: {
            subtitle: t('dev.focusLinkedTo', [l.instance_name || l.instance_id]),
            linked: true,
        },
    }))
    const linkedNames = new Set(links.value.map(l => l.project))
    const unlinked = loadableProjects.value
        .filter(p => !linkedNames.has(p.name))
        .map(p => ({ value: p.name, title: p.name, raw: { subtitle: t('dev.focusNotLinked'), linked: false } }))
    return [...linked, ...unlinked]
})

const loadableProjects = computed(() => projectsCache.value)

// —— 区块切换（同一时间只展示一个列表） ——
type DevTab = 'dirs' | 'mods'
const tab = ref<DevTab>('dirs')

// —— 目录同步（复用 composable） ——
const {
    dirLinks,
    candidates,
    selDir,
    adding,
    linkAllResults,
    loading: dlLoading,
    linkAllDialog,
    linkAllProject,
    pgignoreExists,
    linkingAll,
    linkAllError,
    manualLinkDialog,
    manualLinkTarget,
    manualLinkBusy,
    manualLinkError,
    removeDirDialog,
    removeDirTarget,
    removeDirBusy,
    removeDirError,
    fileSelectOpen,
    fileSelectProject,
    fileSelectDir,
    fileSelectFiles,
    setProject: setDlProject,
    refreshDirLinks,
    addDirLink,
    askRemoveDirLink,
    confirmRemoveDirLink,
    doLinkAll,
    confirmLinkAll,
    askManualLink,
    confirmManualLink,
    openFileSelect,
    switchToFiles,
    switchToJunction,
    resultChip,
} = useDirLinks()

// —— 同步条目列表（目录置顶，文件级同步的文件清单直接铺平展示，不藏二级菜单） ——
type DevSyncKind = 'dir' | 'file'

interface DevSyncRow {
    key: string
    name: string
    kind: DevSyncKind
    mode: string
    target: string
    ok: boolean
    parent?: DirLinkView
}

const syncRows = computed<DevSyncRow[]>(() => {
    const rows: DevSyncRow[] = []
    for (const d of dirLinks.value) {
        rows.push({
            key: 'dir:' + d.project_dir,
            name: d.project_dir,
            kind: 'dir',
            mode: d.mode,
            target: 'minecraft/' + d.instance_dir,
            ok: d.project_exists,
            parent: d,
        })
        if (d.mode === 'files') {
            for (const f of d.files ?? []) {
                rows.push({ key: 'file:' + d.project_dir + '/' + f, name: f, kind: 'file', mode: 'files', target: '', ok: true })
            }
        }
    }
    return rows
})

const syncHeaders = computed(() => [
    { title: t('dev.colEntry'), key: 'name', sortable: false },
    { title: t('dev.colType'), key: 'kind', sortable: false, width: 90 },
    { title: t('dev.colMode'), key: 'mode', sortable: false, width: 90 },
    { title: t('dev.colTarget'), key: 'target', sortable: false, width: 220 },
    { title: t('projects.colAction'), key: 'actions', sortable: false, align: 'end' as const, width: 320 },
])

// 文件行操作：按 key（file:<dir>/<path>）找回所属目录后打开文件选择
function openFileSelectByName(rowKey: string) {
    const dir = rowKey.slice('file:'.length).split('/')[0]
    const dl = dirLinks.value.find(d => d.project_dir === dir)
    if (dl) openFileSelect(dl)
}

// —— mod 差异 ——
const diff = ref<MetaDiff | null>(null)
const diffLoading = ref(false)
const diffBusy = ref('')
const pullOneDialog = ref(false)
const pullOneTarget = ref('')
let diffGeneration = 0
let diffProject = ''

const diffInstanceOnly = computed(() => diff.value?.instance_only ?? [])
const diffProjectOnly = computed(() => diff.value?.project_only ?? [])
const diffVersionDiff = computed(() => diff.value?.version_diff ?? [])
const diffCount = computed(() => diffInstanceOnly.value.length + diffProjectOnly.value.length + diffVersionDiff.value.length)

// —— mod 列表（参考 mod 管理页：v-data-table，差异行置顶、共有 mod 列于其下） ——
type ModDiffKind = 'instance_only' | 'project_only' | 'version_diff' | 'common'

interface DevModRow {
    id: string
    name: string
    side: string
    file: string
    version: string
    instanceVersion: string
    diff: ModDiffKind
}

const modSearch = ref('')
const modOnlyDiff = ref(false)
const modSortBy = ref<{ key: string; order: 'asc' | 'desc' }[]>([{ key: 'name', order: 'asc' }])

const modRows = computed<DevModRow[]>(() => {
    const mods = currentProject.value?.mods ?? []
    const projById = new Map(mods.map(m => [m.id, m]))
    const rows: DevModRow[] = []
    // 项目侧（含共有、项目独有、版本差异）
    for (const m of mods) {
        const vd = diffVersionDiff.value.find(v => v.id === m.id)
        rows.push({
            id: m.id,
            name: m.name || m.id,
            side: m.side,
            file: m.file,
            version: m.version || m.file,
            instanceVersion: vd ? vd.instance_version : (m.version || m.file),
            diff: vd ? 'version_diff' : diffProjectOnly.value.includes(m.id) ? 'project_only' : 'common',
        })
    }
    // 实例独有
    for (const id of diffInstanceOnly.value) {
        if (projById.has(id)) continue
        rows.push({ id, name: id, side: '', file: '', version: '', instanceVersion: '', diff: 'instance_only' })
    }
    // 差异置顶，组内按名称排序（表格列排序在用户点击后覆盖此默认序）
    const order: Record<ModDiffKind, number> = { version_diff: 0, instance_only: 1, project_only: 2, common: 3 }
    return rows.sort((a, b) => order[a.diff] - order[b.diff] || a.name.localeCompare(b.name))
})

const modFiltered = computed(() => {
    const q = modSearch.value.trim().toLowerCase()
    return modRows.value.filter(r => {
        if (modOnlyDiff.value && r.diff === 'common') return false
        if (!q) return true
        return r.name.toLowerCase().includes(q) || r.id.toLowerCase().includes(q)
    })
})

const modHeaders = computed(() => [
    { title: t('projects.colMod'), key: 'name', sortable: true },
    { title: t('projects.colSide'), key: 'side', sortable: true, width: 100 },
    { title: t('dev.colStatus'), key: 'diff', sortable: false, width: 120 },
    { title: t('dev.colProjectVersion'), key: 'version', sortable: true, width: 200 },
    { title: t('dev.colInstanceVersion'), key: 'instanceVersion', sortable: true, width: 200 },
    { title: t('projects.colAction'), key: 'actions', sortable: false, align: 'end' as const, width: 150 },
])

function modStatusChip(kind: ModDiffKind): { label: string; color: string } {
    switch (kind) {
        case 'instance_only':
            return { label: t('dev.statusInstanceOnly'), color: 'primary' }
        case 'project_only':
            return { label: t('dev.statusProjectOnly'), color: 'success' }
        case 'version_diff':
            return { label: t('dev.statusVersionDiff'), color: 'warning' }
        default:
            return { label: t('dev.statusCommon'), color: 'grey' }
    }
}

function modSideText(side: string): string {
    return side ? t('side.' + side) : t('side.unknown')
}

// 状态横幅 chips
const statusChips = computed(() => {
    if (!currentLink.value) return []
    const chips: { text: string; color: string; icon: string }[] = []
    chips.push({
        text: t('dev.status.dirLinks', [dirLinks.value.length]),
        color: dirLinks.value.length > 0 ? 'success' : 'grey',
        icon: 'mdi-folder-sync-outline',
    })
    if (diffLoading.value) {
        chips.push({ text: t('dev.status.diffLoading'), color: 'info', icon: 'mdi-compare-horizontal' })
    } else if (diff.value) {
        chips.push({
            text: diffCount.value > 0 ? t('dev.status.diffCount', [diffCount.value]) : t('dev.status.diffClean'),
            color: diffCount.value > 0 ? 'warning' : 'success',
            icon: diffCount.value > 0 ? 'mdi-compare-horizontal' : 'mdi-check-circle-outline',
        })
    }
    return chips
})

// —— 数据加载 ——
async function loadDiff(propagateError = false) {
    const projectName = focus.value
    const generation = ++diffGeneration
    if (!currentLink.value?.instance_valid) {
        diff.value = null
        diffProject = ''
        return false
    }
    if (diffProject !== projectName) diff.value = null
    diffLoading.value = true
    try {
        const next = await PrismService.MetaDiff(projectName)
        if (generation !== diffGeneration || focus.value !== projectName) return false
        diff.value = next
        diffProject = projectName
        return true
    } catch (e) {
        if (generation !== diffGeneration || focus.value !== projectName) return false
        if (propagateError) throw e
        showSnackbar(errText(e))
        return false
    } finally {
        if (generation === diffGeneration) diffLoading.value = false
    }
}

async function reloadFocus() {
    if (!focus.value) return
    setDlProject(focus.value)
    await Promise.all([refreshDirLinks(), loadDiff()])
}

// 选择 focus：写回路由 query（可深链/刷新保持）
function selectFocus(name: string) {
    if (!name || name === focus.value) return
    focus.value = name
    void router.replace({ query: { ...route.query, project: name } })
}

watch(focus, (next, previous) => {
    if (next !== previous) {
        diffGeneration++
        diff.value = null
        diffProject = ''
    }
    void reloadFocus()
})

async function loadPage() {
    loading.value = true
    loadError.value = ''
    try {
        await Promise.all([loadProjects(), loadOverview()])
        ready.value = true
        const q = String(route.query.project ?? '')
        const initial = q && focusItems.value.some(i => i.value === q) ? q : (links.value.find(l => l.instance_valid)?.project ?? links.value[0]?.project ?? '')
        if (initial !== focus.value) focus.value = initial
        else if (initial) await reloadFocus()
    } catch (e) {
        loadError.value = errText(e)
    } finally {
        loading.value = false
    }
}

onMounted(loadPage)

// —— 后端 mods 监听推送：项目/实例 mods 目录变化时实时刷新差异 ——
// 后端 ServiceStartup 即开始监听全部已关联项目，此处只做订阅；
// 比对失败（data.error 非空）不打扰用户，保留上次结果等手动刷新。
let offModsDiff: (() => void) | undefined
onMounted(() => {
    offModsDiff = Events.On('packgradle:mods-diff', ev => {
        const data = ev.data as ModsWatchEvent
        if (!data || data.project !== focus.value || data.error) return
        diffGeneration++
        diff.value = data.diff
        diffProject = data.project
    })
})
onBeforeUnmount(() => {
    offModsDiff?.()
    offModsDiff = undefined
})

// —— meta 推送/拉取 ——
async function pushMeta() {
    const projectName = focus.value
    if (!projectName) return
    let refreshFailed = false
    await runTask({
        title: t('tasks.metaPush', [projectName]),
        kind: 'meta',
        run: async () => {
            const count = await PrismService.PushMeta(projectName, '')
            try {
                refreshFailed = !(await loadDiff(true))
            } catch (e) {
                refreshFailed = true
                showSnackbar(errText(e), 'warning')
            }
            return t('prism.metaPushed', [count ?? 0])
        },
        warn: () => refreshFailed,
    })
}

const pullOpen = ref(false)
const pullBusy = ref(false)
const pullError = ref('')
const pullOneError = ref('')

function askPullMeta() {
    pullError.value = ''
    pullOpen.value = true
}

async function confirmPullMeta() {
    const projectName = focus.value
    if (!projectName || pullBusy.value) return
    pullBusy.value = true
    pullError.value = ''
    let refreshFailed = false
    try {
        const result = await runTask({
            title: t('tasks.metaPull', [projectName]),
            kind: 'meta',
            run: async () => {
                const count = await PrismService.PullMeta(projectName, '')
                try {
                    const refreshed = await PackwizService.RefreshProject(projectName)
                    if (!refreshed.ok) throw new Error(displayText(refreshed.output))
                    refreshFailed = !(await loadDiff(true))
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
        if (result !== null) pullOpen.value = false
    } finally {
        pullBusy.value = false
    }
}

// —— 单条差异操作 ——
function askPullOne(id: string) {
    pullOneTarget.value = id
    pullOneError.value = ''
    pullOneDialog.value = true
}

async function confirmPullOne() {
    const id = pullOneTarget.value
    const projectName = focus.value
    if (!id || !projectName || diffBusy.value) return
    diffBusy.value = id
    pullOneError.value = ''
    let refreshFailed = false
    try {
        const result = await runTask({
            title: t('tasks.metaPullOne', [id]),
            kind: 'meta',
            run: async () => {
                await PrismService.PullMeta(projectName, id)
                try {
                    const refreshed = await PackwizService.RefreshProject(projectName)
                    if (!refreshed.ok) throw new Error(displayText(refreshed.output))
                    refreshFailed = !(await loadDiff(true))
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
    const projectName = focus.value
    if (!projectName) return
    diffBusy.value = id
    let refreshFailed = false
    try {
        await runTask({
            title: t('tasks.metaPushOne', [id]),
            kind: 'meta',
            run: async () => {
                await PrismService.PushMeta(projectName, id)
                try {
                    refreshFailed = !(await loadDiff(true))
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

function loaderChip(loader: string) {
    return loaderChips[loader] ?? { label: loader, color: 'grey' }
}
</script>

<template>
    <v-container fluid class="pa-6">
        <PageHeader :title="t('dev.title')" :subtitle="t('dev.subtitle')">
            <template #actions>
                <v-select
                    :model-value="focus"
                    :items="focusItems"
                    item-title="title"
                    item-value="value"
                    :label="t('dev.focusLabel')"
                    density="comfortable"
                    hide-details
                    class="focus-select"
                    :disabled="loading || !ready"
                    @update:model-value="selectFocus"
                >
                    <template #item="{ props: itemProps, item }">
                        <v-list-item v-bind="itemProps" :subtitle="item.raw.subtitle">
                            <template #append>
                                <v-icon
                                    :icon="item.raw.linked ? 'mdi-link-variant' : 'mdi-link-variant-off'"
                                    size="small"
                                    :color="item.raw.linked ? 'success' : 'grey'"
                                />
                            </template>
                        </v-list-item>
                    </template>
                </v-select>
            </template>
        </PageHeader>

        <v-progress-linear v-if="loading" indeterminate class="mb-4" />

        <v-alert v-if="loadError" type="error" variant="tonal" class="mb-5">
            <div class="d-flex align-center ga-3">
                <span class="flex-grow-1">{{ loadError }}</span>
                <v-btn size="small" variant="tonal" @click="loadPage">{{ t('common.refresh') }}</v-btn>
            </div>
        </v-alert>

        <!-- 未选择 / 无项目 -->
        <v-card v-if="ready && !loading && focusItems.length === 0" class="py-4">
            <EmptyState icon="mdi-rocket-launch-outline" :title="t('dev.emptyTitle')" :text="t('dev.emptyHint')">
                <template #actions>
                    <v-btn color="primary" prepend-icon="mdi-package-variant-closed" @click="router.push('/projects')">
                        {{ t('dev.goProjects') }}
                    </v-btn>
                </template>
            </EmptyState>
        </v-card>

        <template v-else-if="ready && focus">
            <!-- 同步状态横幅 -->
            <v-card class="mb-5 dev-status" :class="{ 'card-error': currentLink && !currentLink.instance_valid }">
                <v-card-text class="d-flex align-center flex-wrap ga-3 py-3">
                    <v-avatar rounded="lg" size="40" color="primary" variant="tonal">
                        <span class="text-body-1 font-weight-bold">{{ focus.slice(0, 1).toUpperCase() }}</span>
                    </v-avatar>
                    <div style="min-width: 180px">
                        <div class="text-subtitle-2 font-weight-bold">{{ focus }}</div>
                        <div v-if="currentProject && !currentProject.error" class="d-flex flex-wrap ga-1 mt-1">
                            <v-chip
                                v-if="currentProject.modloader"
                                size="x-small"
                                :color="loaderChip(currentProject.modloader).color"
                                variant="flat"
                            >
                                {{ loaderChip(currentProject.modloader).label }} {{ currentProject.modloader_version }}
                            </v-chip>
                            <v-chip v-if="currentProject.minecraft" size="x-small" variant="tonal">{{ currentProject.minecraft }}</v-chip>
                            <v-chip v-if="currentProject.version" size="x-small" variant="tonal">v{{ currentProject.version }}</v-chip>
                        </div>
                    </div>
                    <template v-if="currentLink">
                        <v-icon icon="mdi-arrow-left-right" size="small" class="text-medium-emphasis" />
                        <div style="min-width: 180px">
                            <div class="d-flex align-center ga-2">
                                <v-icon icon="mdi-prism" size="small" color="primary" />
                                <span class="text-subtitle-2">{{ currentLink.instance_name || currentLink.instance_id }}</span>
                                <v-chip v-if="!currentLink.instance_valid" size="x-small" color="error" variant="flat">
                                    {{ t('prism.instanceInvalidChip') }}
                                </v-chip>
                            </div>
                            <div v-if="currentInstance" class="text-caption text-medium-emphasis mt-1">
                                {{ currentInstance.minecraft || t('prism.unknown') }} · {{ currentInstance.modloader || t('prism.loaderVanilla') }}
                            </div>
                        </div>
                        <v-spacer />
                        <div class="d-flex flex-wrap ga-2">
                            <v-chip
                                v-for="chip in statusChips"
                                :key="chip.text"
                                size="small"
                                :color="chip.color"
                                variant="tonal"
                                :prepend-icon="chip.icon"
                            >
                                {{ chip.text }}
                            </v-chip>
                        </div>
                    </template>
                    <template v-else>
                        <v-spacer />
                        <div class="d-flex align-center ga-3">
                            <span class="text-body-2 text-medium-emphasis">{{ t('dev.notLinkedHint') }}</span>
                            <v-btn color="primary" variant="flat" prepend-icon="mdi-link-variant" @click="router.push('/instances')">
                                {{ t('dev.goLink') }}
                            </v-btn>
                        </div>
                    </template>
                </v-card-text>
            </v-card>

            <template v-if="currentLink">
                <!-- meta 快捷操作 + 区块切换 -->
                <div class="d-flex align-center flex-wrap ga-2 mb-4">
                    <v-btn size="small" color="primary" variant="tonal" prepend-icon="mdi-arrow-up-bold-outline" :title="t('prism.metaPushTip')" @click="pushMeta">
                        {{ t('prism.metaPushBtn') }}
                    </v-btn>
                    <v-btn size="small" color="primary" variant="tonal" prepend-icon="mdi-arrow-down-bold-outline" :title="t('prism.metaPullTip')" @click="askPullMeta">
                        {{ t('prism.metaPullBtn') }}
                    </v-btn>
                    <v-spacer />
                    <v-btn-toggle v-model="tab" mandatory density="comfortable" variant="outlined" divided>
                        <v-btn value="dirs" prepend-icon="mdi-folder-sync-outline">
                            {{ t('dev.tabDirs') }}
                            <v-chip size="x-small" variant="tonal" class="ml-2">{{ dirLinks.length }}</v-chip>
                        </v-btn>
                        <v-btn value="mods" prepend-icon="mdi-compare-horizontal">
                            {{ t('dev.tabMods') }}
                            <v-chip v-if="diffCount > 0" size="x-small" color="warning" variant="flat" class="ml-2">{{ diffCount }}</v-chip>
                        </v-btn>
                    </v-btn-toggle>
                </div>

                <!-- 区块一：同步目录 / 文件列表 -->
                <v-card v-if="tab === 'dirs'" class="mb-5">
                    <v-card-title class="d-flex align-center pt-5">
                        <v-icon icon="mdi-folder-sync-outline" color="primary" class="mr-2" />
                        {{ t('dev.dirsTitle') }}
                        <v-spacer />
                        <v-btn color="primary" variant="tonal" size="small" prepend-icon="mdi-link-variant-plus" @click="doLinkAll">
                            {{ t('prism.linkAllBtn') }}
                        </v-btn>
                    </v-card-title>
                    <v-card-text>
                        <div class="text-body-2 text-medium-emphasis mb-3">{{ t('prism.linkAllHint') }}</div>

                        <!-- 一键关联结果 -->
                        <v-list v-if="linkAllResults.length > 0" density="compact" class="mb-3 results-list">
                            <v-list-item
                                v-for="r in linkAllResults"
                                :key="r.name"
                                :title="r.name"
                                :subtitle="r.detail ? displayText(r.detail) : ''"
                            >
                                <template #prepend>
                                    <v-icon :icon="r.is_dir ? 'mdi-folder-outline' : 'mdi-file-outline'" class="mr-2" />
                                </template>
                                <template #append>
                                    <v-chip size="x-small" :color="resultChip(r).color" variant="tonal">
                                        {{ resultChip(r).label }}
                                    </v-chip>
                                </template>
                            </v-list-item>
                        </v-list>

                        <v-progress-linear v-if="dlLoading" indeterminate class="mb-2" />

                        <!-- 同步条目表格：目录行置顶，文件级同步的文件清单直接铺平展示（不藏二级菜单） -->
                        <v-data-table
                            v-if="syncRows.length > 0"
                            :headers="syncHeaders"
                            :items="syncRows"
                            item-value="key"
                            density="comfortable"
                            hover
                            :items-per-page="50"
                            :items-per-page-options="[25, 50, 100, { value: -1, title: t('common.all') }]"
                            class="mods-table mb-2"
                        >
                            <template #[`item.name`]="{ item: row }">
                                <div class="d-flex align-center" :class="{ 'file-row': row.kind === 'file' }">
                                    <v-icon
                                        :icon="row.kind === 'dir' ? 'mdi-folder-outline' : 'mdi-file-outline'"
                                        size="small"
                                        class="mr-2"
                                        :color="row.kind === 'dir' ? 'primary' : undefined"
                                    />
                                    <span :class="{ 'font-weight-bold': row.kind === 'dir' }">{{ row.name }}</span>
                                </div>
                            </template>

                            <template #[`item.kind`]="{ item: row }">
                                <v-chip size="x-small" :color="row.kind === 'dir' ? 'primary' : 'info'" variant="tonal">
                                    {{ row.kind === 'dir' ? t('dev.kindDir') : t('dev.kindFile') }}
                                </v-chip>
                            </template>

                            <template #[`item.mode`]="{ item: row }">
                                <v-chip v-if="row.kind === 'dir'" size="x-small" :color="row.mode === 'files' ? 'info' : 'grey'" variant="tonal">
                                    {{ row.mode === 'files' ? t('prism.dirLinkModeFiles') : t('prism.dirLinkModeJunction') }}
                                </v-chip>
                                <span v-else class="text-caption text-medium-emphasis">{{ t('dev.modeHardlink') }}</span>
                            </template>

                            <template #[`item.target`]="{ item: row }">
                                <span v-if="row.target" class="text-caption">{{ row.target }}</span>
                                <span v-else class="text-caption text-medium-emphasis">—</span>
                            </template>

                            <template #[`item.actions`]="{ item: row }">
                                <template v-if="row.parent">
                                    <v-chip v-if="!row.ok" size="x-small" color="warning" variant="tonal" class="mr-2">
                                        {{ t('prism.parseFailed') }}
                                    </v-chip>
                                    <v-btn v-if="row.mode === 'files'" size="small" variant="text" class="mr-1" @click="openFileSelect(row.parent)">
                                        {{ t('prism.dirLinkFilesBtn') }}
                                    </v-btn>
                                    <v-btn v-if="row.mode !== 'files'" size="small" variant="text" class="mr-1" @click="switchToFiles(row.parent)">
                                        {{ t('prism.dirLinkSwitchFiles') }}
                                    </v-btn>
                                    <v-btn v-if="row.mode === 'files'" size="small" variant="text" class="mr-1" @click="switchToJunction(row.parent)">
                                        {{ t('prism.dirLinkSwitchJunction') }}
                                    </v-btn>
                                    <v-btn v-if="row.mode !== 'files'" size="small" variant="text" class="mr-1" @click="askManualLink(row.parent)">
                                        {{ t('prism.manualLinkBtn') }}
                                    </v-btn>
                                    <v-btn size="small" variant="text" color="error" @click="askRemoveDirLink(row.parent)">
                                        {{ t('prism.dirLinkRemove') }}
                                    </v-btn>
                                </template>
                                <v-btn
                                    v-else
                                    size="small"
                                    variant="text"
                                    prepend-icon="mdi-file-edit-outline"
                                    @click="openFileSelectByName(row.key)"
                                >
                                    {{ t('prism.dirLinkFilesBtn') }}
                                </v-btn>
                            </template>

                        </v-data-table>
                        <div v-else-if="!dlLoading" class="text-body-2 text-medium-emphasis mb-3">
                            {{ t('prism.dirLinkEmpty') }}
                        </div>

                        <!-- 添加目录 -->
                        <div v-if="candidates.length > 0" class="d-flex align-center ga-2">
                            <v-select
                                v-model="selDir"
                                :items="candidates"
                                :label="t('prism.dirLinkCandidate')"
                                density="comfortable"
                                hide-details="auto"
                                style="max-width: 320px"
                            />
                            <v-btn color="primary" variant="tonal" :disabled="!selDir" :loading="adding" @click="addDirLink">
                                {{ t('prism.dirLinkAdd') }}
                            </v-btn>
                        </div>
                        <div v-else-if="!dlLoading" class="text-caption text-medium-emphasis">
                            {{ t('prism.dirLinkNoCandidate') }}
                        </div>
                    </v-card-text>
                </v-card>

                <!-- 区块二：mod 列表（差异置顶，共有 mod 列于其下；参考 mod 管理页 v-data-table） -->
                <v-card v-else class="mb-5">
                    <v-card-title class="d-flex align-center pt-5">
                        <v-icon icon="mdi-compare-horizontal" color="primary" class="mr-2" />
                        {{ t('dev.modsTitle') }}
                        <v-spacer />
                        <span class="text-caption text-medium-emphasis mr-2">{{ t('dev.modsHint') }}</span>
                        <v-btn icon="mdi-refresh" size="small" variant="text" :loading="diffLoading" :title="t('prism.diffRefresh')" @click="loadDiff" />
                    </v-card-title>
                    <v-card-text>
                        <div class="d-flex align-center flex-wrap ga-3 mb-3">
                            <v-text-field
                                v-model="modSearch"
                                :placeholder="t('projects.searchMods')"
                                prepend-inner-icon="mdi-magnify"
                                density="compact"
                                hide-details
                                clearable
                                class="mod-search"
                            />
                            <v-chip
                                :variant="modOnlyDiff ? 'flat' : 'tonal'"
                                :color="modOnlyDiff ? 'primary' : undefined"
                                filter
                                @click="modOnlyDiff = !modOnlyDiff"
                            >
                                {{ t('dev.onlyDiff') }}
                            </v-chip>
                            <v-spacer />
                            <span class="text-caption text-medium-emphasis">{{ t('projects.modsFiltered', [modFiltered.length, modRows.length]) }}</span>
                        </div>

                        <v-progress-linear v-if="diffLoading" indeterminate class="mb-2" />

                        <v-data-table
                            v-else
                            v-model:sort-by="modSortBy"
                            :headers="modHeaders"
                            :items="modFiltered"
                            item-value="id"
                            density="comfortable"
                            hover
                            :items-per-page="50"
                            :items-per-page-options="[25, 50, 100, { value: -1, title: t('common.all') }]"
                            class="mods-table"
                        >
                            <template #[`item.name`]="{ item: row }">
                                <div :class="{ 'font-weight-bold': row.diff !== 'common' }">{{ row.name }}</div>
                                <div class="text-caption text-medium-emphasis">{{ row.id }}</div>
                            </template>

                            <template #[`item.side`]="{ item: row }">
                                <v-chip v-if="row.side" size="x-small" :color="sideColors[row.side] ?? 'grey'" variant="flat">
                                    {{ modSideText(row.side) }}
                                </v-chip>
                                <span v-else class="text-caption text-medium-emphasis">—</span>
                            </template>

                            <template #[`item.diff`]="{ item: row }">
                                <v-chip size="x-small" :color="modStatusChip(row.diff).color" :variant="row.diff === 'common' ? 'tonal' : 'flat'">
                                    {{ modStatusChip(row.diff).label }}
                                </v-chip>
                            </template>

                            <template #[`item.version`]="{ item: row }">
                                <span v-if="row.version" class="text-caption">{{ row.version }}</span>
                                <span v-else class="text-caption text-medium-emphasis">—</span>
                            </template>

                            <template #[`item.instanceVersion`]="{ item: row }">
                                <span v-if="row.diff === 'instance_only'" class="text-caption text-medium-emphasis">{{ t('dev.instanceHasMod') }}</span>
                                <span v-else-if="row.instanceVersion" class="text-caption" :class="{ 'text-warning font-weight-medium': row.diff === 'version_diff' }">
                                    {{ row.instanceVersion }}
                                </span>
                                <span v-else class="text-caption text-medium-emphasis">—</span>
                            </template>

                            <template #[`item.actions`]="{ item: row }">
                                <v-btn
                                    v-if="row.diff === 'instance_only'"
                                    size="small"
                                    variant="tonal"
                                    :loading="diffBusy === row.id"
                                    :disabled="diffBusy !== ''"
                                    @click="askPullOne(row.id)"
                                >
                                    {{ t('prism.metaPullOne') }}
                                </v-btn>
                                <v-btn
                                    v-else-if="row.diff === 'project_only'"
                                    size="small"
                                    variant="tonal"
                                    :loading="diffBusy === row.id"
                                    :disabled="diffBusy !== ''"
                                    @click="pushOne(row.id)"
                                >
                                    {{ t('prism.metaPushOne') }}
                                </v-btn>
                                <template v-else-if="row.diff === 'version_diff'">
                                    <v-btn
                                        size="small"
                                        variant="text"
                                        class="mr-1"
                                        prepend-icon="mdi-arrow-down-bold-outline"
                                        :loading="diffBusy === row.id"
                                        :disabled="diffBusy !== ''"
                                        :title="t('prism.metaPullTip')"
                                        @click="askPullOne(row.id)"
                                    >
                                        {{ t('prism.metaPullOne') }}
                                    </v-btn>
                                    <v-btn
                                        size="small"
                                        variant="text"
                                        prepend-icon="mdi-arrow-up-bold-outline"
                                        :loading="diffBusy === row.id"
                                        :disabled="diffBusy !== ''"
                                        :title="t('prism.metaPushTip')"
                                        @click="pushOne(row.id)"
                                    >
                                        {{ t('prism.metaPushOne') }}
                                    </v-btn>
                                </template>
                            </template>

                        </v-data-table>

                        <div v-if="!diffLoading && modRows.length === 0" class="text-body-2 text-medium-emphasis py-6 text-center">
                            <v-icon icon="mdi-puzzle-outline" size="32" class="mb-2" />
                            <div>{{ t('projects.noMods') }}</div>
                        </div>
                    </v-card-text>
                </v-card>
            </template>
        </template>

        <!-- 一键关联确认（合并 .pgignore 询问为单框） -->
        <v-dialog v-model="linkAllDialog" :persistent="linkingAll" max-width="520">
            <v-card class="dialog-card" elevation="8">
                <v-card-title class="d-flex align-center pt-5">
                    <v-avatar size="34" rounded="md" color="warning" variant="tonal" class="mr-3">
                        <v-icon icon="mdi-link-variant-plus" size="19" />
                    </v-avatar>
                    {{ t('prism.linkAllConfirmTitle') }}
                </v-card-title>
                <v-card-text class="text-body-2">
                    {{ t('prism.linkAllConfirmText', [linkAllProject]) }}
                    <ul class="consequence-list mt-2">
                        <li>{{ t('prism.linkAllC1') }}</li>
                        <li>{{ t('prism.linkAllC2') }}</li>
                        <li v-if="!pgignoreExists" class="text-warning">{{ t('prism.linkAllC3') }}</li>
                    </ul>
                    <v-alert v-if="linkAllError" type="error" variant="tonal" density="compact" class="mt-3">
                        {{ linkAllError }}
                    </v-alert>
                </v-card-text>
                <v-card-actions class="px-5 pb-4">
                    <v-spacer />
                    <v-btn variant="text" :disabled="linkingAll" @click="linkAllDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn v-if="!pgignoreExists" variant="tonal" :disabled="linkingAll" @click="confirmLinkAll(false)">
                        {{ t('prism.pgignoreSkipAndLink') }}
                    </v-btn>
                    <v-btn color="primary" variant="flat" :loading="linkingAll" @click="confirmLinkAll(!pgignoreExists)">
                        {{ pgignoreExists ? t('common.confirm') : t('prism.pgignoreCreateAndLink') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 手动链接确认 -->
        <ConfirmDialog
            v-model="manualLinkDialog"
            :title="t('prism.manualLinkConfirmTitle')"
            :text="t('prism.manualLinkConfirmText', [manualLinkTarget?.project_dir ?? ''])"
            :consequences="[t('prism.manualLinkC1'), t('prism.manualLinkC2')]"
            :confirm-text="t('prism.manualLinkBtn')"
            icon="mdi-alert-outline"
            :loading="manualLinkBusy"
            :error="manualLinkError"
            @confirm="confirmManualLink"
        />

        <!-- 移除目录确认 -->
        <ConfirmDialog
            v-model="removeDirDialog"
            :title="t('prism.dirLinkRemoveTitle')"
            :text="t('prism.dirLinkRemoveText', [removeDirTarget?.project_dir ?? ''])"
            :consequences="[t('prism.dirLinkRemoveC1')]"
            :confirm-text="t('prism.dirLinkRemove')"
            icon="mdi-delete-alert-outline"
            danger
            :loading="removeDirBusy"
            :error="removeDirError"
            @confirm="confirmRemoveDirLink"
        />

        <!-- 文件级同步：文件选择 -->
        <FileSelectDialog
            v-model="fileSelectOpen"
            :project="fileSelectProject"
            :dir="fileSelectDir"
            :files="fileSelectFiles"
            @changed="refreshDirLinks"
        />

        <!-- 拉取 meta 确认（后果四要素） -->
        <ConfirmDialog
            v-model="pullOpen"
            :title="t('prism.metaPullConfirmTitle')"
            :text="t('prism.metaPullConfirmText', [focus])"
            :consequences="[t('prism.metaPullC1'), t('prism.metaPullC2'), t('prism.metaPullC3')]"
            :confirm-text="t('prism.metaPullBtn')"
            icon="mdi-arrow-down-bold-outline"
            icon-color="primary"
            :loading="pullBusy"
            :error="pullError"
            @confirm="confirmPullMeta"
        />

        <!-- 单 mod 拉取确认 -->
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
    </v-container>
</template>

<style scoped>
.focus-select {
    min-width: 260px;
    max-width: 360px;
}
.dev-status {
    border-left: 3px solid rgb(var(--v-theme-primary)) !important;
}
.results-list,
.dir-list {
    border: 1px solid var(--pg-border);
    border-radius: 12px;
}
.consequence-list {
    padding-left: 18px;
    margin: 0;
    color: rgba(var(--v-theme-on-surface), 0.75);
}
.consequence-list li {
    margin-bottom: 3px;
    line-height: 1.5;
}
.mod-search {
    max-width: 280px;
}
/* 文件行缩进，视觉上从属于上方目录行 */
.file-row {
    padding-left: 22px;
    color: rgba(var(--v-theme-on-surface), 0.75);
}
</style>
