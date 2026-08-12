<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Dialogs } from '@wailsio/runtime'
import { PrismService, PackwizService } from '../../bindings/packgradle/internal/service'
import type { Instance, LinkView, DirLinkView, LinkResult } from '../../bindings/packgradle/internal/prism'
import type { PackProject } from '../../bindings/packgradle/internal/packwiz'
import { useSnackbar } from '../composables/useSnackbar'
import { displayText, errText, errorCode } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import { navigate } from '../nav'

const { t } = useI18n()

const instances = ref<Instance[]>([])
const instancesDir = ref('')
const loading = ref(false)
// 自动定位失败时显示手动输入引导
const locateFailed = ref(false)
const failedCode = ref('') // 定位失败的错误码（err.prism.not_found 等）
const failedError = ref('') // 定位失败的错误文本
// 手动指定的实例目录（config 持久化，空串 = 自动定位）
const manualPath = ref('')
const saving = ref(false)
const { snackbar, snackbarMsg, show } = useSnackbar()

// 项目 ↔ 实例 关联
const links = ref<LinkView[]>([])
const projects = ref<PackProject[]>([])
const linkDialog = ref(false)
const selProject = ref('')
const selInstance = ref('')
const linking = ref(false)
const creating = ref(false)

// 目录同步关联
const dirLinkDialog = ref(false)
const dirLinkProject = ref('')
const dirLinks = ref<DirLinkView[]>([])
const candidates = ref<string[]>([])
const selDir = ref('')
// 一键关联结果
const linkAllResults = ref<LinkResult[]>([])
const linkAlling = ref(false)
// .pgignore 缺失询问对话框
const pgignoreDialog = ref(false)
// 手动链接确认对话框
const manualLinkDialog = ref(false)
const manualLinkTarget = ref<DirLinkView | null>(null)
const manualLinking = ref(false)
// 文件级同步：文件选择对话框
const fileSelectDialog = ref(false)
const fileSelectProject = ref('')
const fileSelectDir = ref('')
const allFiles = ref<string[]>([])
const selectedFiles = ref<string[]>([])
const savingFiles = ref(false)

// 加载器 chip：已识别 → 颜色标签；空 → 原版；其余 → 原样文本
function loaderInfo(inst: Instance): { label: string; color?: string } {
    if (!inst.modloader) return { label: t('prism.loaderVanilla') }
    return loaderChips[inst.modloader] ?? { label: inst.modloader }
}

async function load() {
    loading.value = true
    try {
        const dir = await PrismService.InstancesDir()
        instancesDir.value = dir ?? ''
        locateFailed.value = false
        failedError.value = ''
    } catch (e) {
        // 定位失败：展示手动输入引导（含具体错误原因）
        locateFailed.value = true
        failedCode.value = errorCode(e) ?? ''
        failedError.value = errText(e)
        instances.value = []
        instancesDir.value = ''
        return
    } finally {
        loading.value = false
    }

    try {
        instances.value = (await PrismService.ListInstances()) ?? []
    } catch (e) {
        show(errText(e))
        instances.value = []
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
        show(manualPath.value.trim() ? t('prism.manualPathSaved') : t('prism.manualPathCleared'))
        await load()
    } catch (e) {
        show(errText(e))
    } finally {
        saving.value = false
    }
}

// 清除手动路径，恢复自动定位
async function clearManualPath() {
    manualPath.value = ''
    await saveManualPath()
}

// ---- 关联 ----

// 加载可关联的项目列表（排除解析失败的项目）
async function loadProjects() {
    projects.value = ((await PackwizService.ListProjects()) ?? []).filter(p => !p.error)
}

async function loadLinks() {
    links.value = (await PrismService.GetLinks()) ?? []
}

async function openLinkDialog() {
    await loadProjects() // 打开前刷新，保证列表最新
    if (projects.value.length === 0) {
        show(t('prism.noProjectsHint'))
        return
    }
    selProject.value = ''
    selInstance.value = ''
    linkDialog.value = true
}

// 选择项目时自动匹配同名实例（不区分大小写）
function matchInstance(projectName: string): string {
    const name = projectName.toLowerCase()
    return instances.value.find(i => i.id.toLowerCase() === name || i.name.toLowerCase() === name)?.id ?? ''
}

const matchedInstanceId = computed(() => (selProject.value ? matchInstance(selProject.value) : ''))
const matchHintVisible = computed(() => selProject.value !== '' && matchedInstanceId.value !== '')

async function doLink() {
    if (!selProject.value || !selInstance.value) return
    linking.value = true
    try {
        await PrismService.LinkProject(selProject.value, selInstance.value)
        show(t('prism.linkCreated', [selProject.value, selInstance.value]))
        linkDialog.value = false
        await loadLinks()
    } catch (e) {
        show(errText(e))
    } finally {
        linking.value = false
    }
}

async function doUnlink(project: string) {
    try {
        await PrismService.UnlinkProject(project)
        show(t('prism.linkRemoved', [project]))
        await loadLinks()
    } catch (e) {
        show(errText(e))
    }
}

// 基于项目信息程序创建实例
async function doCreateInstance() {
    if (!selProject.value) return
    creating.value = true
    try {
        const inst = await PrismService.CreateInstance(selProject.value)
        show(t('prism.instanceCreated', [inst.id]))
        await load() // 重扫实例列表
        selInstance.value = inst.id
    } catch (e) {
        show(errText(e))
    } finally {
        creating.value = false
    }
}

// ---- 目录同步关联 ----

async function openDirLinks(link: LinkView) {
    dirLinkProject.value = link.project
    dirLinkDialog.value = true
    await refreshDirLinks()
}

async function refreshDirLinks() {
    dirLinks.value = (await PrismService.ListDirLinks(dirLinkProject.value)) ?? []
    candidates.value = (await PrismService.ListProjectDirs(dirLinkProject.value)) ?? []
    // 已添加的目录从候选中剔除
    const added = new Set(dirLinks.value.map(d => d.project_dir))
    candidates.value = candidates.value.filter(c => !added.has(c))
    selDir.value = ''
}

async function addDirLink() {
    if (!selDir.value) return
    try {
        await PrismService.AddDirLink(dirLinkProject.value, selDir.value)
        show(t('prism.dirLinkAdded', [selDir.value]))
        await refreshDirLinks()
    } catch (e) {
        show(errText(e))
    }
}

async function removeDirLink(dl: DirLinkView) {
    try {
        await PrismService.RemoveDirLink(dl.project, dl.project_dir)
        show(t('prism.dirLinkRemoved', [dl.project_dir]))
        await refreshDirLinks()
    } catch (e) {
        show(errText(e))
    }
}

// 一键关联：项目根下全部未被 .pgignore 忽略的条目建链。
// 未检测到 .pgignore 时弹自定义对话框询问（Wails 原生 Question 在构建版会挂起）。
async function doLinkAll() {
    if (!dirLinkProject.value) return
    const exists = await PrismService.HasPGIgnore(dirLinkProject.value)
    if (!exists) {
        pgignoreDialog.value = true // 确认后走 choosePGIgnore 继续
        return
    }
    await executeLinkAll()
}

// .pgignore 询问结果：create = 生成默认规则后继续，skip = 不生成直接关联
function choosePGIgnore(choice: 'create' | 'skip') {
    pgignoreDialog.value = false
    void applyPGIgnoreChoice(choice)
}

async function applyPGIgnoreChoice(choice: 'create' | 'skip') {
    if (choice === 'create') {
        try {
            await PrismService.EnsurePGIgnore(dirLinkProject.value)
            show(t('prism.pgignoreCreated'))
        } catch (e) {
            show(errText(e))
            return
        }
    }
    await executeLinkAll()
}

// executeLinkAll 执行一键关联并展示结果
async function executeLinkAll() {
    linkAlling.value = true
    try {
        linkAllResults.value = (await PrismService.CreateAllLinks(dirLinkProject.value)) ?? []
        const ok = linkAllResults.value.filter(r => r.status === 'linked' || r.status === 'existing').length
        const skipped = linkAllResults.value.filter(r => r.status === 'skipped').length
        const failed = linkAllResults.value.filter(r => r.status === 'error').length
        const manual = linkAllResults.value.filter(r => r.status === 'manual').length
        if (manual > 0) {
            show(t('prism.linkAllDoneWithManual', [ok, skipped, failed, manual]))
        } else {
            show(t('prism.linkAllDone', [ok, skipped, failed]))
        }
        await refreshDirLinks()
    } catch (e) {
        show(errText(e))
    } finally {
        linkAlling.value = false
    }
}

// 手动链接：实例侧已有内容时确认后复制并入并建链
function askManualLink(dl: DirLinkView) {
    manualLinkTarget.value = dl
    manualLinkDialog.value = true
}

async function confirmManualLink() {
    const dl = manualLinkTarget.value
    if (!dl) return
    manualLinkDialog.value = false
    manualLinkTarget.value = null
    manualLinking.value = true
    try {
        const res = await PrismService.ManualLinkDir(dl.project, dl.project_dir)
        if (res.status === 'error') {
            show(displayText(res.detail))
        } else {
            show(t('prism.manualLinkDone', [dl.project_dir]))
        }
        await refreshDirLinks()
    } catch (e) {
        show(errText(e))
    } finally {
        manualLinking.value = false
    }
}

// 文件级同步：打开文件选择对话框（加载项目目录全部文件 + 当前勾选）
async function openFileSelect(dl: DirLinkView) {
    fileSelectProject.value = dl.project
    fileSelectDir.value = dl.project_dir
    allFiles.value = (await PrismService.ListDirFiles(dl.project, dl.project_dir)) ?? []
    selectedFiles.value = [...(dl.files ?? [])]
    fileSelectDialog.value = true
}

// 保存文件清单（自动切换为文件级同步并重建链接）
async function saveFileSelect() {
    savingFiles.value = true
    try {
        await PrismService.SetDirLinkFiles(fileSelectProject.value, fileSelectDir.value, selectedFiles.value)
        show(t('prism.dirLinkFilesSaved', [fileSelectDir.value]))
        fileSelectDialog.value = false
        await refreshDirLinks()
    } catch (e) {
        show(errText(e))
    } finally {
        savingFiles.value = false
    }
}

// 从文件级切回整目录 junction
async function switchToJunction(dl: DirLinkView) {
    try {
        await PrismService.SetDirLinkMode(dl.project, dl.project_dir, '')
        show(t('prism.dirLinkModeJunction'))
        await refreshDirLinks()
    } catch (e) {
        show(errText(e))
    }
}

// 文件勾选切换
function toggleFile(f: string, checked: boolean | null) {
    if (checked) {
        if (!selectedFiles.value.includes(f)) selectedFiles.value.push(f)
    } else {
        selectedFiles.value = selectedFiles.value.filter(x => x !== f)
    }
}

// 一键关联结果的状态 chip 信息
function resultChip(r: LinkResult): { color: string; label: string } {
    switch (r.status) {
        case 'linked':
            return { color: 'success', label: t('prism.linkResult.linked') }
        case 'existing':
            return { color: 'info', label: t('prism.linkResult.existing') }
        case 'manual':
            return { color: 'warning', label: t('prism.linkResult.manual') }
        case 'skipped':
            return { color: 'warning', label: t('prism.linkResult.skipped') }
        default:
            return { color: 'error', label: t('prism.linkResult.error') }
    }
}

const instanceCountText = computed(() => t('prism.instanceCount', [instances.value.length]))

onMounted(async () => {
    manualPath.value = (await PrismService.GetInstancesPath()) ?? ''
    await loadProjects()
    await load()
    await loadLinks()
})
</script>

<template>
    <div>
        <v-row class="align-center mb-4">
            <v-col>
                <h2 class="text-h5">{{ t('prism.title') }}</h2>
                <div class="text-body-2 text-medium-emphasis">{{ t('prism.subtitle') }}</div>
            </v-col>
            <v-col cols="auto">
                <v-btn
                    v-if="!locateFailed"
                    color="primary"
                    prepend-icon="mdi-link-variant"
                    class="mr-2"
                    @click="openLinkDialog"
                >
                    {{ t('prism.linkBtn') }}
                </v-btn>
                <v-btn variant="text" icon="mdi-refresh" :loading="loading" @click="load" />
            </v-col>
        </v-row>

        <!-- 实例目录设置：任何状态都可查看/修改（留空 = 自动检测） -->
        <v-card class="mb-4">
            <v-card-title class="d-flex align-center">
                <v-icon icon="mdi-folder-cog-outline" class="mr-2" color="primary" />
                {{ t('prism.instancesDirTitle') }}
                <v-chip size="x-small" variant="tonal" class="ml-3">
                    {{ t('prism.instancesDirCurrent', [instancesDir || t('prism.autoDetect')]) }}
                </v-chip>
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-3">{{ t('prism.instancesDirHint') }}</div>
                <!-- 定位失败的具体原因（not_found 附去配置按钮） -->
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
                            @click="navigate('env')"
                        >
                            {{ t('prism.goConfigure') }}
                        </v-btn>
                    </div>
                </v-alert>
                <v-text-field
                    v-model="manualPath"
                    :label="t('prism.manualPathLabel')"
                    variant="outlined"
                    density="compact"
                    hide-details="auto"
                    clearable
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

        <!-- 实例列表 -->
        <v-card class="mb-4" :loading="loading">
            <v-card-title class="d-flex align-center">
                <v-icon icon="mdi-prism" class="mr-2" color="primary" />
                {{ t('prism.colName') }}
            </v-card-title>

            <v-card-text v-if="instances.length === 0 && !loading" class="text-medium-emphasis">
                {{ locateFailed ? '' : t('prism.empty') }}
            </v-card-text>

            <v-list v-else>
                <v-list-item
                    v-for="inst in instances"
                    :key="inst.id"
                    :title="inst.name"
                    :subtitle="inst.path"
                >
                    <template #prepend>
                        <v-icon icon="mdi-folder-outline" class="mr-2" color="primary" />
                    </template>
                    <template #append>
                        <v-chip v-if="inst.group" size="x-small" variant="outlined" class="mr-2">
                            {{ inst.group }}
                        </v-chip>
                        <v-chip
                            size="x-small"
                            :color="loaderInfo(inst).color ?? undefined"
                            variant="tonal"
                            class="mr-2"
                        >
                            {{ loaderInfo(inst).label }}
                        </v-chip>
                        <v-chip v-if="inst.minecraft" size="x-small" variant="tonal" class="mr-2">
                            {{ inst.minecraft }}
                        </v-chip>
                        <v-chip v-if="inst.error" size="x-small" color="error" variant="tonal">
                            {{ t('prism.parseFailed') }}
                        </v-chip>
                    </template>
                    <template v-if="inst.error" #default>
                        <div class="text-caption text-error mt-1">
                            {{ displayText(inst.error) }}
                        </div>
                    </template>
                </v-list-item>
            </v-list>
        </v-card>

        <!-- 关联列表 -->
        <v-card>
            <v-card-title class="d-flex align-center">
                <v-icon icon="mdi-link-variant" class="mr-2" color="primary" />
                {{ t('prism.linksTitle') }}
            </v-card-title>

            <v-card-text v-if="links.length === 0" class="text-medium-emphasis">
                {{ t('prism.linksEmpty') }}
            </v-card-text>

            <v-list v-else>
                <v-list-item
                    v-for="link in links"
                    :key="link.project"
                    :title="link.project"
                    :subtitle="link.project_path"
                >
                    <template #prepend>
                        <v-icon icon="mdi-package-variant-closed" class="mr-2" color="primary" />
                    </template>
                    <template #append>
                        <v-icon icon="mdi-arrow-right" size="small" class="mr-2" />
                        <v-chip
                            size="small"
                            :color="link.instance_valid ? 'success' : 'error'"
                            variant="tonal"
                            class="mr-2"
                        >
                            {{ link.instance_name || link.instance_id }}
                        </v-chip>
                        <v-chip v-if="!link.instance_valid" size="x-small" color="error" variant="outlined" class="mr-2">
                            {{ t('prism.instanceInvalidChip') }}
                        </v-chip>
                        <v-btn size="small" variant="tonal" class="mr-1" @click="openDirLinks(link)">
                            {{ t('prism.dirLinkBtn') }}
                        </v-btn>
                        <v-btn size="small" variant="text" @click="doUnlink(link.project)">
                            {{ t('prism.unlinkBtn') }}
                        </v-btn>
                    </template>
                </v-list-item>
            </v-list>
        </v-card>

        <!-- 关联对话框：项目 + 实例（自动匹配）+ 程序创建 -->
        <v-dialog v-model="linkDialog" max-width="560">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-link-variant" color="primary" class="mr-2" />
                    {{ t('prism.linkDialogTitle') }}
                </v-card-title>
                <v-card-text>
                    <v-select
                        v-model="selProject"
                        :items="projects"
                        item-title="name"
                        item-value="name"
                        :label="t('prism.linkProject')"
                        variant="outlined"
                        density="compact"
                        hide-details="auto"
                        class="mb-4"
                        @update:model-value="selInstance = matchedInstanceId"
                    />
                    <v-alert
                        v-if="matchHintVisible"
                        type="info"
                        variant="tonal"
                        density="compact"
                        class="mb-3"
                    >
                        {{ t('prism.matchHint') }}
                    </v-alert>
                    <v-select
                        v-model="selInstance"
                        :items="instances"
                        item-title="name"
                        item-value="id"
                        :label="t('prism.linkInstance')"
                        variant="outlined"
                        density="compact"
                        hide-details="auto"
                    />
                    <div v-if="instances.length === 0" class="text-caption text-medium-emphasis mt-3">
                        {{ t('prism.createInstanceHint') }}
                    </div>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn
                        v-if="selProject"
                        variant="text"
                        :loading="creating"
                        @click="doCreateInstance"
                    >
                        {{ t('prism.createInstanceBtn') }}
                    </v-btn>
                    <v-btn variant="text" @click="linkDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn
                        color="primary"
                        variant="tonal"
                        :loading="linking"
                        :disabled="!selProject || !selInstance"
                        @click="doLink"
                    >
                        {{ t('prism.linkSubmit') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 目录同步关联对话框 -->
        <v-dialog v-model="dirLinkDialog" max-width="640">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-folder-sync-outline" color="primary" class="mr-2" />
                    {{ t('prism.dirLinksTitle') }} · {{ dirLinkProject }}
                </v-card-title>
                <v-card-text>
                    <div class="d-flex align-center mb-3">
                        <div class="text-body-2 text-medium-emphasis flex-grow-1">{{ t('prism.linkAllHint') }}</div>
                        <v-btn
                            color="primary"
                            prepend-icon="mdi-link-variant-plus"
                            :loading="linkAlling"
                            @click="doLinkAll"
                        >
                            {{ t('prism.linkAllBtn') }}
                        </v-btn>
                    </div>

                    <!-- 一键关联结果 -->
                    <v-list v-if="linkAllResults.length > 0" density="compact" class="mb-3">
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

                    <v-list v-if="dirLinks.length > 0" density="compact" class="mb-2">
                        <v-list-item
                            v-for="dl in dirLinks"
                            :key="dl.project_dir"
                            :title="dl.project_dir"
                            :subtitle="`→ minecraft/${dl.instance_dir}`"
                        >
                            <template #append>
                                <v-chip
                                    size="x-small"
                                    :color="dl.mode === 'files' ? 'info' : 'grey'"
                                    variant="tonal"
                                    class="mr-2"
                                >
                                    {{ dl.mode === 'files' ? t('prism.dirLinkModeFiles') : t('prism.dirLinkModeJunction') }}
                                </v-chip>
                                <v-chip
                                    v-if="!dl.project_exists"
                                    size="x-small"
                                    color="warning"
                                    variant="tonal"
                                    class="mr-2"
                                >
                                    {{ t('prism.parseFailed') }}
                                </v-chip>
                                <v-btn
                                    size="small"
                                    variant="text"
                                    class="mr-1"
                                    @click="openFileSelect(dl)"
                                >
                                    {{ t('prism.dirLinkFilesBtn') }}
                                </v-btn>
                                <v-btn
                                    v-if="dl.mode === 'files'"
                                    size="small"
                                    variant="text"
                                    class="mr-1"
                                    @click="switchToJunction(dl)"
                                >
                                    {{ t('prism.dirLinkSwitchJunction') }}
                                </v-btn>
                                <v-btn
                                    v-if="dl.mode !== 'files'"
                                    size="small"
                                    variant="text"
                                    class="mr-1"
                                    @click="askManualLink(dl)"
                                >
                                    {{ t('prism.manualLinkBtn') }}
                                </v-btn>
                                <v-btn size="small" variant="text" @click="removeDirLink(dl)">
                                    {{ t('prism.dirLinkRemove') }}
                                </v-btn>
                            </template>
                        </v-list-item>
                    </v-list>
                    <div v-else class="text-body-2 text-medium-emphasis mb-3">
                        {{ t('prism.dirLinkEmpty') }}
                    </div>
                    <v-select
                        v-if="candidates.length > 0"
                        v-model="selDir"
                        :items="candidates"
                        :label="t('prism.dirLinkCandidate')"
                        variant="outlined"
                        density="compact"
                        hide-details="auto"
                    />
                    <div v-else class="text-caption text-medium-emphasis">
                        {{ t('prism.dirLinkNoCandidate') }}
                    </div>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="dirLinkDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn
                        color="primary"
                        variant="tonal"
                        :disabled="!selDir"
                        @click="addDirLink"
                    >
                        {{ t('prism.dirLinkAdd') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- .pgignore 缺失询问（自定义对话框，替代 Wails 原生 Question） -->
        <v-dialog v-model="pgignoreDialog" max-width="520">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-file-question-outline" color="warning" class="mr-2" />
                    {{ t('prism.pgignoreMissingTitle') }}
                </v-card-title>
                <v-card-text>{{ t('prism.pgignoreMissingText') }}</v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="pgignoreDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn variant="tonal" @click="choosePGIgnore('skip')">{{ t('prism.pgignoreSkipAndLink') }}</v-btn>
                    <v-btn color="primary" variant="tonal" @click="choosePGIgnore('create')">
                        {{ t('prism.pgignoreCreateAndLink') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 手动链接确认对话框：实例侧已有内容时确认复制并入后建链 -->
        <v-dialog v-model="manualLinkDialog" max-width="520">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-alert-outline" color="warning" class="mr-2" />
                    {{ t('prism.manualLinkConfirmTitle') }}
                </v-card-title>
                <v-card-text>{{ t('prism.manualLinkConfirmText', [manualLinkTarget?.project_dir ?? '']) }}</v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="manualLinkDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn color="primary" variant="tonal" :loading="manualLinking" @click="confirmManualLink">
                        {{ t('prism.manualLinkBtn') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 文件级同步：文件选择对话框 -->
        <v-dialog v-model="fileSelectDialog" max-width="560">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-file-check-outline" color="primary" class="mr-2" />
                    {{ t('prism.dirLinkFilesTitle') }} · {{ fileSelectDir }}
                </v-card-title>
                <v-card-text>
                    <div class="text-body-2 text-medium-emphasis mb-3">
                        {{ t('prism.dirLinkFilesHint', [fileSelectDir]) }}
                    </div>
                    <div class="mb-2">
                        <v-btn size="small" variant="tonal" class="mr-2" @click="selectedFiles = [...allFiles]">
                            {{ t('prism.selectAll') }}
                        </v-btn>
                        <v-btn size="small" variant="text" @click="selectedFiles = []">
                            {{ t('prism.clearAll') }}
                        </v-btn>
                    </div>
                    <v-list v-if="allFiles.length > 0" density="compact" max-height="320" class="overflow-y-auto">
                        <v-list-item v-for="f in allFiles" :key="f">
                            <template #prepend>
                                <v-checkbox
                                    :model-value="selectedFiles.includes(f)"
                                    density="compact"
                                    hide-details
                                    @update:model-value="(v: boolean | null) => toggleFile(f, v)"
                                />
                            </template>
                            <v-list-item-title class="text-body-2">{{ f }}</v-list-item-title>
                        </v-list-item>
                    </v-list>
                    <div v-else class="text-caption text-medium-emphasis">
                        {{ t('prism.dirLinkNoCandidate') }}
                    </div>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="fileSelectDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn color="primary" variant="tonal" :loading="savingFiles" @click="saveFileSelect">
                        {{ t('prism.save') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-snackbar v-model="snackbar" timeout="4000" location="bottom">
            {{ snackbarMsg }}
        </v-snackbar>
    </div>
</template>
