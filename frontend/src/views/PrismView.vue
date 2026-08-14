<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Dialogs } from '@wailsio/runtime'
import { PrismService, PackwizService } from '../../bindings/packgradle/internal/service'
import type { Instance, LinkView, DirLinkView, LinkResult, MetaDiff } from '../../bindings/packgradle/internal/prism'
import { useSnackbar } from '../composables/useSnackbar'
import { useProjects } from '../composables/useProjects'
import { displayText, errText, parseAppErr } from '../utils/errors'
import { loaderChips } from '../utils/cf'
import { navigate, bumpProjectsVersion } from '../nav'

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

// 项目 ↔ 实例 关联（项目列表走共享缓存，避免与项目页重复解析）
const { projects, loadProjects, invalidateProjects } = useProjects()
const linkableProjects = computed(() => projects.value.filter(p => !p.error))
const links = ref<LinkView[]>([])
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
// meta 推送/拉取
const metaBusy = ref('') // 操作中的项目名（推送/拉取共用 loading）
const pullConfirmDialog = ref(false)
const pullConfirmTarget = ref<LinkView | null>(null)
// meta 差异
const diffDialog = ref(false)
const diffProject = ref('')
const diff = ref<MetaDiff | null>(null)
const diffLoading = ref(false)
const diffBusy = ref('') // 差异对话框中单操作中的 mod id
const pullOneDialog = ref(false)
const pullOneTarget = ref('')

// 加载器 chip：已识别 → 颜色标签；空 → 原版；其余 → 原样文本
function loaderInfo(inst: Instance): { label: string; color?: string } {
    if (!inst.modloader) return { label: t('prism.loaderVanilla') }
    return loaderChips[inst.modloader] ?? { label: inst.modloader }
}

// 页面数据装载/刷新：Overview 一次调用返回实例目录 + 实例列表 + 关联视图
// （后端只扫描一次实例目录，替代此前的 InstancesDir/ListInstances/GetLinks 三次往返）
async function load() {
    loading.value = true
    try {
        const ov = await PrismService.Overview()
        instancesDir.value = ov.instances_dir ?? ''
        instances.value = ov.instances ?? []
        links.value = ov.links ?? []
        if (ov.locate_error) {
            // 定位失败：展示手动输入引导（含具体错误原因）
            locateFailed.value = true
            failedCode.value = parseAppErr(ov.locate_error)?.code ?? ''
            failedError.value = displayText(ov.locate_error)
        } else {
            locateFailed.value = false
            failedError.value = ''
        }
    } catch (e) {
        show(errText(e))
        instances.value = []
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

// 加载可关联的项目列表（共享缓存；打开对话框前强制刷新，排除解析失败的项目）
async function openLinkDialog() {
    try {
        await loadProjects(true)
    } catch (e) {
        show(errText(e))
        return
    }
    if (linkableProjects.value.length === 0) {
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
        await load() // Overview 一并刷新实例与关联视图
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
        await load()
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
    // 两个独立查询并发执行，避免串行往返
    const [linksResult, dirsResult] = await Promise.all([
        PrismService.ListDirLinks(dirLinkProject.value),
        PrismService.ListProjectDirs(dirLinkProject.value),
    ])
    dirLinks.value = linksResult ?? []
    candidates.value = dirsResult ?? []
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

// 文件级同步：打开文件选择对话框（从实例侧读取文件列表 + 当前勾选）
async function openFileSelect(dl: DirLinkView) {
    fileSelectProject.value = dl.project
    fileSelectDir.value = dl.project_dir
    allFiles.value = (await PrismService.ListInstanceDirFiles(dl.project, dl.project_dir)) ?? []
    selectedFiles.value = [...(dl.files ?? [])]
    fileSelectDialog.value = true
}

// 保存：勾选文件移动到项目目录并硬链接同步
async function saveFileSelect() {
    savingFiles.value = true
    try {
        const results = (await PrismService.SelectInstanceFiles(fileSelectProject.value, fileSelectDir.value, selectedFiles.value)) ?? []
        const ok = results.filter(r => r.status === 'linked').length
        const skipped = results.filter(r => r.status === 'skipped').length
        show(t('prism.dirLinkSelectDone', [ok, skipped]))
        fileSelectDialog.value = false
        await refreshDirLinks()
    } catch (e) {
        show(errText(e))
    } finally {
        savingFiles.value = false
    }
}

// 从整目录切到文件级同步
async function switchToFiles(dl: DirLinkView) {
    try {
        await PrismService.SetDirLinkMode(dl.project, dl.project_dir, 'files')
        show(t('prism.dirLinkModeFiles'))
        await refreshDirLinks()
    } catch (e) {
        show(errText(e))
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

// meta 推送：项目 mod 元数据 → 实例 mods/.index（Prism 兼容格式）
async function pushMeta(link: LinkView) {
    metaBusy.value = link.project
    try {
        const count = await PrismService.PushMeta(link.project, '')
        show(t('prism.metaPushed', [count ?? 0]))
    } catch (e) {
        show(errText(e))
    } finally {
        metaBusy.value = ''
    }
}

// meta 拉取：弹确认框（覆盖项目同名 pw.toml）后从实例 .index 拉回
function askPullMeta(link: LinkView) {
    pullConfirmTarget.value = link
    pullConfirmDialog.value = true
}

async function confirmPullMeta() {
    const link = pullConfirmTarget.value
    if (!link) return
    pullConfirmDialog.value = false
    pullConfirmTarget.value = null
    metaBusy.value = link.project
    try {
        const count = await PrismService.PullMeta(link.project, '')
        show(t('prism.metaPulled', [count ?? 0]))
        await refreshProjectIndex(link.project) // refresh 使 index.toml 收录新条目，差异才正确
        await load() // 刷新当前页（实例/关联列表）
        bumpProjectsVersion() // 拉取改变了项目 mods，通知项目列表刷新
        invalidateProjects() // 共享项目缓存失效（下次 loadProjects 重新拉取）
    } catch (e) {
        show(errText(e))
    } finally {
        metaBusy.value = ''
    }
}

// meta 差异：打开对话框时重新计算并刷新缓存
async function openMetaDiff(link: LinkView) {
    diffProject.value = link.project
    diffDialog.value = true
    await refreshDiff()
}

async function refreshDiff() {
    diffLoading.value = true
    try {
        diff.value = await PrismService.MetaDiff(diffProject.value)
    } catch (e) {
        show(errText(e))
    } finally {
        diffLoading.value = false
    }
}

// 差异视图中的单 mod 拉取（确认后执行）
function askPullOne(id: string) {
    pullOneTarget.value = id
    pullOneDialog.value = true
}

async function confirmPullOne() {
    const id = pullOneTarget.value
    if (!id) return
    pullOneDialog.value = false
    pullOneTarget.value = ''
    diffBusy.value = id
    try {
        await PrismService.PullMeta(diffProject.value, id)
        show(t('prism.metaOneDone', [t('prism.metaPullOne'), id]))
        await refreshProjectIndex(diffProject.value) // refresh 使 index.toml 收录新条目，差异才正确
        await load() // 刷新当前页（实例/关联列表）
        bumpProjectsVersion() // 拉取改变了项目 mods，通知项目列表刷新
        invalidateProjects() // 共享项目缓存失效（下次 loadProjects 重新拉取）
        await refreshDiff()
    } catch (e) {
        show(errText(e))
    } finally {
        diffBusy.value = ''
    }
}

// refreshProjectIndex 执行 packwiz refresh 收录新拉取的 pw.toml（差异以 index.toml 为权威）。
// 失败时提示，不阻断主流程。
async function refreshProjectIndex(project: string) {
    try {
        const result = await PackwizService.RefreshProject(project)
        if (result && !result.ok) {
            show(t('prism.metaRefreshFailed'))
        }
    } catch (e) {
        show(t('prism.metaRefreshFailed') + ': ' + errText(e))
    }
}

// 差异视图中的单 mod 推送
async function pushOne(id: string) {
    diffBusy.value = id
    try {
        await PrismService.PushMeta(diffProject.value, id)
        show(t('prism.metaOneDone', [t('prism.metaPushOne'), id]))
        await refreshDiff()
    } catch (e) {
        show(errText(e))
    } finally {
        diffBusy.value = ''
    }
}

// 差异计算时间展示
function diffFetchedText(): string {
    const ts = diff.value?.fetched_at
    if (!ts) return ''
    return t('prism.metaFetchedAt', [ts])
}

// 差异三区（模板安全访问：null 时为空数组）
const diffInstanceOnly = computed(() => diff.value?.instance_only ?? [])
const diffProjectOnly = computed(() => diff.value?.project_only ?? [])
const diffVersionDiff = computed(() => diff.value?.version_diff ?? [])
const hasDiff = computed(
    () => diffInstanceOnly.value.length > 0 || diffProjectOnly.value.length > 0 || diffVersionDiff.value.length > 0,
)

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
    // 三个独立查询并发执行：手动路径、项目列表（共享缓存）、页面总览
    const [path] = await Promise.all([
        PrismService.GetInstancesPath(),
        loadProjects().catch(() => []),
        load(),
    ])
    manualPath.value = path ?? ''
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
                        <v-btn
                            size="small"
                            variant="tonal"
                            class="mr-1"
                            :loading="metaBusy === link.project"
                            :disabled="metaBusy !== ''"
                            @click="pushMeta(link)"
                        >
                            {{ t('prism.metaPushBtn') }}
                        </v-btn>
                        <v-btn
                            size="small"
                            variant="tonal"
                            class="mr-1"
                            :loading="metaBusy === link.project"
                            :disabled="metaBusy !== ''"
                            @click="askPullMeta(link)"
                        >
                            {{ t('prism.metaPullBtn') }}
                        </v-btn>
                        <v-btn size="small" variant="tonal" class="mr-1" @click="openMetaDiff(link)">
                            {{ t('prism.metaDiffBtn') }}
                        </v-btn>
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
                        :items="linkableProjects"
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
                                    v-if="dl.mode === 'files'"
                                    size="small"
                                    variant="text"
                                    class="mr-1"
                                    @click="openFileSelect(dl)"
                                >
                                    {{ t('prism.dirLinkFilesBtn') }}
                                </v-btn>
                                <v-btn
                                    v-if="dl.mode !== 'files'"
                                    size="small"
                                    variant="text"
                                    class="mr-1"
                                    @click="switchToFiles(dl)"
                                >
                                    {{ t('prism.dirLinkSwitchFiles') }}
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

        <!-- 拉取 meta 确认对话框：覆盖项目同名 pw.toml -->
        <v-dialog v-model="pullConfirmDialog" max-width="520">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-alert-outline" color="warning" class="mr-2" />
                    {{ t('prism.metaPullConfirmTitle') }}
                </v-card-title>
                <v-card-text>{{ t('prism.metaPullConfirmText') }}</v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="pullConfirmDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn color="primary" variant="tonal" @click="confirmPullMeta">
                        {{ t('prism.metaPullBtn') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- meta 差异对话框：每次打开时重新计算并刷新缓存 -->
        <v-dialog v-model="diffDialog" max-width="640">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-compare-horizontal" color="primary" class="mr-2" />
                    {{ t('prism.metaDiffTitle') }} · {{ diffProject }}
                    <v-chip v-if="diffFetchedText()" size="x-small" variant="tonal" class="ml-3">
                        {{ diffFetchedText() }}
                    </v-chip>
                </v-card-title>
                <v-card-text>
                    <div class="text-body-2 text-medium-emphasis mb-2">{{ t('prism.metaDiffHint') }}</div>
                    <div v-if="hasDiff">
                        <!-- 实例独有：可拉取 -->
                        <v-list-subheader v-if="diffInstanceOnly.length > 0" class="text-caption text-primary">
                            {{ t('prism.metaDiffInstanceOnly') }}（{{ diffInstanceOnly.length }}）
                        </v-list-subheader>
                        <v-list-item
                            v-for="id in diffInstanceOnly"
                            :key="'i' + id"
                            density="compact"
                            :title="id"
                        >
                            <template #append>
                                <v-btn
                                    size="small"
                                    variant="tonal"
                                    :loading="diffBusy === id"
                                    :disabled="diffBusy !== ''"
                                    @click="askPullOne(id)"
                                >
                                    {{ t('prism.metaPullOne') }}
                                </v-btn>
                            </template>
                        </v-list-item>

                        <!-- 项目独有：可推送 -->
                        <v-list-subheader v-if="diffProjectOnly.length > 0" class="text-caption text-success">
                            {{ t('prism.metaDiffProjectOnly') }}（{{ diffProjectOnly.length }}）
                        </v-list-subheader>
                        <v-list-item
                            v-for="id in diffProjectOnly"
                            :key="'p' + id"
                            density="compact"
                            :title="id"
                        >
                            <template #append>
                                <v-btn
                                    size="small"
                                    variant="tonal"
                                    :loading="diffBusy === id"
                                    :disabled="diffBusy !== ''"
                                    @click="pushOne(id)"
                                >
                                    {{ t('prism.metaPushOne') }}
                                </v-btn>
                            </template>
                        </v-list-item>

                        <!-- 版本差异 -->
                        <v-list-subheader v-if="diffVersionDiff.length > 0" class="text-caption text-warning">
                            {{ t('prism.metaDiffVersionDiff') }}（{{ diffVersionDiff.length }}）
                        </v-list-subheader>
                        <v-list-item
                            v-for="v in diffVersionDiff"
                            :key="'v' + v.id"
                            density="compact"
                            :title="v.id"
                            :subtitle="`项目 ${v.project_version} → 实例 ${v.instance_version}`"
                        />
                    </div>
                    <div v-else class="text-body-2 text-medium-emphasis">
                        {{ t('prism.metaDiffEmpty') }}
                    </div>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="diffDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 差异视图中的单 mod 拉取确认 -->
        <v-dialog v-model="pullOneDialog" max-width="480">
            <v-card>
                <v-card-title class="d-flex align-center">
                    <v-icon icon="mdi-alert-outline" color="warning" class="mr-2" />
                    {{ t('prism.metaPullOneConfirmTitle') }}
                </v-card-title>
                <v-card-text>{{ t('prism.metaPullOneConfirmText', [pullOneTarget]) }}</v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="pullOneDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn color="primary" variant="tonal" @click="confirmPullOne">
                        {{ t('prism.metaPullOne') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-snackbar v-model="snackbar" timeout="4000" location="bottom">
            {{ snackbarMsg }}
        </v-snackbar>
    </div>
</template>
