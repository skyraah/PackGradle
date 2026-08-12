<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Dialogs } from '@wailsio/runtime'
import { PackwizService } from '../../bindings/packgradle/internal/service'
import type { PackProject, ModInfo, UpdateCheckResult } from '../../bindings/packgradle/internal/packwiz'
import { useSnackbar } from '../composables/useSnackbar'
import { useApiKeyGuide } from '../composables/useApiKeyGuide'
import { errText, displayText } from '../utils/errors'
import { isCfMod, cfReleaseKey, cfDateText, loaderChips, sideColors } from '../utils/cf'

const { t } = useI18n()

const projects = ref<PackProject[]>([])
const loading = ref(false)
const importing = ref(false)
const expanded = ref<string | null>(null)
const { snackbar, snackbarMsg, show } = useSnackbar()
const refreshing = ref<string | null>(null)
const refreshOutput = ref('')
const outputDialog = ref(false)
// 移除项目确认对话框（替代 Wails 原生 Question）
const removeDialog = ref(false)
const removing = ref<PackProject | null>(null)

// —— CurseForge 版本获取 ——
const fetching = ref<string | null>(null) // 单行获取中的 mod id
const fetchingAll = ref<string | null>(null) // 批量获取中的项目名
const { apiKeyDialog, handleError, goConfigApiKey } = useApiKeyGuide()

function loaderChip(loader: string) {
    return loaderChips[loader] ?? { label: loader, color: 'grey' }
}

// side 中文标签（side.* 翻译键，缺失时显示未知）
function sideText(mod: ModInfo): string {
    return mod.side ? t(`side.${mod.side}`) : t('side.unknown')
}

// releaseType 中文标签（cf.release.* 翻译键）
function releaseText(tp: number): string {
    const key = cfReleaseKey(tp)
    return key ? t(key) : ''
}

// 项目解析失败原因（错误码 JSON 文本）→ 用户可读文本
function projectError(proj: PackProject): string {
    return displayText(proj.error)
}

async function load() {
    loading.value = true
    try {
        projects.value = (await PackwizService.ListProjects()) ?? []
    } finally {
        loading.value = false
    }
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
        show(t('projects.imported', [proj.name, (proj.mods ?? []).length]))
        await load()
        expanded.value = proj.name
    } catch (e) {
        show(t('projects.importFailed', [errText(e)]))
    } finally {
        importing.value = false
    }
}

async function removeProject(proj: PackProject) {
    // Wails 原生 Question 对话框在构建版会挂起（Promise 不返回），改用自定义确认对话框
    removing.value = proj
    removeDialog.value = true
}

// 确认移除：执行删除并刷新列表
async function confirmRemove() {
    const proj = removing.value
    if (!proj) return
    removeDialog.value = false
    removing.value = null
    try {
        const list = await PackwizService.RemoveProject(proj.name)
        projects.value = list ?? []
        if (expanded.value === proj.name) expanded.value = null
    } catch (e) {
        show(t('projects.removeFailed', [errText(e)]))
    }
}

async function refreshProject(proj: PackProject) {
    refreshing.value = proj.name
    try {
        const result = await PackwizService.RefreshProject(proj.name)
        outputTitle.value = t('projects.refreshOutputTitle')
        refreshOutput.value = displayText(result.output || (result.ok ? t('projects.outputSuccess', ['packwiz refresh']) : t('projects.outputFailed')))
        outputDialog.value = true
        await load()
    } finally {
        refreshing.value = null
    }
}

async function fetchModVersion(proj: PackProject, mod: ModInfo) {
    fetching.value = mod.id
    try {
        const updated = await PackwizService.FetchModVersion(proj.name, mod.id)
        const target = proj.mods?.find(m => m.id === mod.id)
        if (target && updated) Object.assign(target, updated)
        show(t('projects.versionFetched', [updated?.name ?? mod.name]))
    } catch (e) {
        handleError(e, show)
    } finally {
        fetching.value = null
    }
}

async function fetchAllVersions(proj: PackProject) {
    fetchingAll.value = proj.name
    try {
        const results = (await PackwizService.FetchAllModVersions(proj.name)) ?? []
        const ok = results.filter(r => r.ok).length
        show(t('projects.versionsFetched', [ok, results.length]))
        await load()
    } catch (e) {
        handleError(e, show)
    } finally {
        fetchingAll.value = null
    }
}

// —— packwiz 更新检查（复用 packwiz 官方 update 命令）——
const checking = ref<string | null>(null) // 检查中的项目名
const checkingProj = ref<PackProject | null>(null) // 检查结果对应的项目
const checkResult = ref<UpdateCheckResult | null>(null)
const checkDialog = ref(false)
const updatingAll = ref(false) // 正在应用全部更新
const outputTitle = ref('') // 命令输出对话框标题

// 检查：运行 `packwiz update --all` 并喂入 "n"，只列出可更新项不实际应用
async function checkUpdates(proj: PackProject) {
    checking.value = proj.name
    checkingProj.value = proj
    try {
        const result = await PackwizService.CheckUpdates(proj.name)
        checkResult.value = result
        checkDialog.value = true
        const upd = result?.updates?.length ?? 0
        const err = result?.errors?.length ?? 0
        show(err > 0 ? t('projects.checkDoneWithErrors', [upd, err]) : t('projects.checkDone', [upd]))
    } catch (e) {
        show(errText(e))
    } finally {
        checking.value = null
    }
}

// 应用更新：更新全部有更新的 mod（packwiz update --all -y）
async function applyAllUpdates() {
    const proj = checkingProj.value
    if (!proj) return
    updatingAll.value = true
    try {
        const result = await PackwizService.UpdateMods(proj.name, '')
        outputTitle.value = t('projects.updateOutputTitle')
        refreshOutput.value = displayText(result.output || (result.ok ? t('projects.outputSuccess', ['packwiz update']) : t('projects.outputFailed')))
        outputDialog.value = true
        checkDialog.value = false
        await load()
    } catch (e) {
        show(errText(e))
    } finally {
        updatingAll.value = false
    }
}

onMounted(load)
</script>

<template>
    <div>
        <v-row class="align-center mb-4">
            <v-col>
                <h2 class="text-h5">{{ t('projects.title') }}</h2>
                <div class="text-body-2 text-medium-emphasis">{{ t('projects.subtitle') }}</div>
            </v-col>
            <v-col cols="auto">
                <v-btn variant="text" icon="mdi-refresh" :loading="loading" @click="load" />
                <v-btn color="primary" prepend-icon="mdi-folder-open" :loading="importing" @click="importProject">
                    {{ t('projects.importBtn') }}
                </v-btn>
            </v-col>
        </v-row>

        <v-alert
            v-if="projects.length === 0 && !loading"
            type="info"
            variant="tonal"
            class="mb-4"
            prepend-icon="mdi-information-outline"
        >
            {{ t('projects.empty') }}
        </v-alert>

        <v-progress-linear v-if="loading" indeterminate class="mb-4" />

        <v-card v-for="proj in projects" :key="proj.name" class="mb-4">
            <v-list-item @click="expanded = expanded === proj.name ? null : proj.name">
                <template #prepend>
                    <v-avatar rounded="lg" color="primary" variant="tonal">
                        <v-icon icon="mdi-package-variant-closed" />
                    </v-avatar>
                </template>
                <template #title>
                    {{ proj.name }}
                    <v-chip v-if="proj.error" size="x-small" color="error" class="ml-2">{{ t('projects.parseFailed') }}</v-chip>
                </template>
                <template #subtitle>
                    <span v-if="!proj.error">
                        <v-chip
                            v-if="proj.modloader"
                            size="x-small"
                            :color="loaderChip(proj.modloader).color"
                            variant="tonal"
                            class="mr-2"
                        >
                            {{ loaderChip(proj.modloader).label }} {{ proj.modloader_version }}
                        </v-chip>
                        <v-chip v-if="proj.minecraft" size="x-small" variant="tonal" class="mr-2">
                            {{ t('projects.minecraft', [proj.minecraft]) }}
                        </v-chip>
                        <v-chip v-if="proj.version" size="x-small" variant="tonal" class="mr-2">
                            v{{ proj.version }}
                        </v-chip>
                        <v-chip v-if="proj.author" size="x-small" variant="tonal">{{ t('projects.author', [proj.author]) }}</v-chip>
                        <span class="ml-2 text-caption text-medium-emphasis">{{ t('projects.modCount', [(proj.mods ?? []).length]) }}</span>
                    </span>
                    <span v-else class="text-error">{{ projectError(proj) }}</span>
                </template>
                <template #append>
                    <v-btn
                        v-if="!proj.error"
                        icon="mdi-update"
                        variant="text"
                        size="small"
                        :title="t('projects.tooltipCheckUpdates')"
                        :loading="checking === proj.name"
                        @click.stop="checkUpdates(proj)"
                    />
                    <v-btn
                        v-if="!proj.error"
                        icon="mdi-cloud-download"
                        variant="text"
                        size="small"
                        :title="t('projects.tooltipFetchAll')"
                        :loading="fetchingAll === proj.name"
                        @click.stop="fetchAllVersions(proj)"
                    />
                    <v-btn
                        v-if="!proj.error"
                        icon="mdi-refresh"
                        variant="text"
                        size="small"
                        :title="t('projects.tooltipRefresh')"
                        :loading="refreshing === proj.name"
                        @click.stop="refreshProject(proj)"
                    />
                    <v-btn
                        icon="mdi-delete-outline"
                        variant="text"
                        size="small"
                        color="error"
                        :title="t('projects.tooltipRemove')"
                        @click.stop="removeProject(proj)"
                    />
                </template>
            </v-list-item>

            <v-expand-transition>
                <div v-if="expanded === proj.name && !proj.error">
                    <v-divider />
                    <v-table density="compact">
                        <thead>
                            <tr>
                                <th>{{ t('projects.colMod') }}</th>
                                <th class="w-25">{{ t('projects.colSide') }}</th>
                                <th class="w-30">{{ t('projects.colFile') }}</th>
                                <th class="w-25">{{ t('projects.colVersion') }}</th>
                                <th class="text-right">{{ t('projects.colAction') }}</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-for="mod in proj.mods ?? []" :key="mod.id">
                                <td>
                                    {{ mod.name || mod.id }}
                                    <div class="text-caption text-medium-emphasis">{{ mod.id }}</div>
                                </td>
                                <td>
                                    <v-chip
                                        size="x-small"
                                        :color="sideColors[mod.side] ?? 'grey'"
                                        variant="tonal"
                                    >
                                        {{ sideText(mod) }}
                                    </v-chip>
                                </td>
                                <td class="text-caption">{{ mod.file || '—' }}</td>
                                <td class="text-caption">
                                    <!-- 本地版本优先；CurseForge displayName 与文件名一致时不再重复显示，改为发布日期 -->
                                    <span v-if="mod.version" :title="mod.cf_version || ''">{{ mod.version }}</span>
                                    <span v-else-if="mod.cf_version && mod.cf_version !== mod.file">{{ mod.cf_version }}</span>
                                    <span v-else-if="mod.cf_version">{{ t('projects.published') }} {{ cfDateText(mod.cf_file_date) || '—' }}</span>
                                    <span v-else>—</span>
                                    <div v-if="mod.cf_version && mod.cf_version !== mod.file" class="text-medium-emphasis">
                                        {{ releaseText(mod.cf_release_type) }}
                                        <template v-if="mod.cf_release_type && mod.cf_file_date"> · </template>{{ cfDateText(mod.cf_file_date) }}
                                    </div>
                                    <div v-else-if="mod.cf_version && releaseText(mod.cf_release_type)" class="text-medium-emphasis">
                                        {{ releaseText(mod.cf_release_type) }}
                                    </div>
                                </td>
                                <td class="text-right">
                                    <v-btn
                                        v-if="isCfMod(mod)"
                                        icon="mdi-cloud-download-outline"
                                        size="x-small"
                                        variant="text"
                                        :loading="fetching === mod.id"
                                        :disabled="fetchingAll !== null"
                                        :title="mod.cf_version ? t('projects.tooltipRefetch') : t('projects.tooltipFetch')"
                                        @click="fetchModVersion(proj, mod)"
                                    />
                                </td>
                            </tr>
                            <tr v-if="(proj.mods ?? []).length === 0">
                                <td colspan="5" class="text-center text-medium-emphasis">{{ t('projects.noMods') }}</td>
                            </tr>
                        </tbody>
                    </v-table>
                </div>
            </v-expand-transition>
        </v-card>

        <v-dialog v-model="outputDialog" max-width="640">
            <v-card>
                <v-card-title class="text-subtitle-1">{{ outputTitle }}</v-card-title>
                <v-card-text>
                    <pre class="text-body-2 refresh-output">{{ refreshOutput }}</pre>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="tonal" @click="outputDialog = false">{{ t('projects.close') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-dialog v-model="checkDialog" max-width="640">
            <v-card>
                <v-card-title class="text-subtitle-1">
                    <v-icon icon="mdi-update" class="mr-1" />
                    {{ t('projects.checkDialogTitle') }}
                </v-card-title>
                <v-card-text>
                    <v-alert
                        v-if="!checkResult?.ok"
                        type="error"
                        variant="tonal"
                        density="compact"
                        class="mb-3"
                    >
                        {{ t('projects.checkFailed') }}
                    </v-alert>
                    <v-alert
                        v-else-if="(checkResult?.updates?.length ?? 0) === 0 && (checkResult?.errors?.length ?? 0) === 0"
                        type="success"
                        variant="tonal"
                        density="compact"
                        class="mb-3"
                    >
                        {{ t('projects.allUpToDate') }}
                    </v-alert>
                    <v-list v-if="(checkResult?.updates?.length ?? 0) > 0" density="compact" class="mb-3">
                        <v-list-subheader>{{ t('projects.hasUpdates', [checkResult?.updates?.length]) }}</v-list-subheader>
                        <v-list-item v-for="u in checkResult?.updates ?? []" :key="u.name">
                            <v-list-item-title class="text-body-2">{{ u.name }}</v-list-item-title>
                            <v-list-item-subtitle class="text-caption">
                                {{ u.current_file }}
                                <v-icon icon="mdi-arrow-right" size="x-small" />
                                <span class="text-primary">{{ u.latest_file }}</span>
                            </v-list-item-subtitle>
                        </v-list-item>
                    </v-list>
                    <v-list v-if="(checkResult?.errors?.length ?? 0) > 0" density="compact" class="mb-3">
                        <v-list-subheader>{{ t('projects.failedSkipped', [checkResult?.errors?.length]) }}</v-list-subheader>
                        <v-list-item v-for="e in checkResult?.errors ?? []" :key="e.name + e.error">
                            <v-list-item-title class="text-caption">
                                {{ e.name }}：<span class="text-error">{{ displayText(e.error) }}</span>
                            </v-list-item-title>
                        </v-list-item>
                    </v-list>
                    <pre class="text-body-2 refresh-output">{{ checkResult?.output }}</pre>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="checkDialog = false">{{ t('projects.close') }}</v-btn>
                    <v-btn
                        v-if="(checkResult?.updates?.length ?? 0) > 0"
                        color="primary"
                        variant="tonal"
                        :loading="updatingAll"
                        @click="applyAllUpdates"
                    >
                        {{ t('projects.applyAll') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-dialog v-model="apiKeyDialog" max-width="480">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-key-alert-outline" color="warning" class="mr-2" />
                    {{ t('projects.apiKeyDialogTitle') }}
                </v-card-title>
                <v-card-text>{{ t('projects.apiKeyDialogText') }}</v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="apiKeyDialog = false">{{ t('projects.close') }}</v-btn>
                    <v-btn color="primary" variant="tonal" @click="goConfigApiKey">{{ t('projects.goConfigureApiKey') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 移除项目确认对话框（Wails 原生 Question 在构建版挂起，用自定义对话框替代） -->
        <v-dialog v-model="removeDialog" max-width="440">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-alert-outline" color="warning" class="mr-2" />
                    {{ t('projects.removeTitle') }}
                </v-card-title>
                <v-card-text>{{ t('projects.removeMessage', [removing?.name ?? '']) }}</v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="removeDialog = false">{{ t('projects.cancel') }}</v-btn>
                    <v-btn color="error" variant="tonal" @click="confirmRemove">{{ t('projects.removeBtn') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-snackbar v-model="snackbar" timeout="4000" location="bottom">
            {{ snackbarMsg }}
        </v-snackbar>
    </div>
</template>

<style scoped>
.refresh-output {
    max-height: 320px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
    background: rgb(var(--v-theme-surface-variant));
    border-radius: 6px;
    padding: 12px;
}
</style>
