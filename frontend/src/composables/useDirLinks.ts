// 目录同步管理（DirLinks）的可复用逻辑：从 DirLinksDialog 提炼，
// 供原对话框与「开发版本」页面共用。数据源 PrismService。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { PrismService } from '../api'
import type { DirLinkView, LinkResult } from '../../bindings/packgradle/internal/prism'
import { runTask } from '../stores/taskCenter'
import { showSnackbar } from '../stores/ui'
import { errText, displayText } from '../utils/errors'

export function useDirLinks() {
    const { t } = useI18n()

    // 当前管理目标项目（为空时列表清空且不发起请求）
    const project = ref('')
    const loading = ref(false)
    const dirLinks = ref<DirLinkView[]>([])
    const candidates = ref<string[]>([])
    const selDir = ref('')
    const adding = ref(false)
    const linkAllResults = ref<LinkResult[]>([])

    // 一键关联确认（合并 .pgignore 询问：有 ignore = 普通确认；无 ignore = 同框三选）
    const linkAllDialog = ref(false)
    const linkAllProject = ref('')
    const pgignoreExists = ref(true)
    const linkingAll = ref(false)
    const linkAllError = ref('')
    // 手动链接确认
    const manualLinkDialog = ref(false)
    const manualLinkTarget = ref<DirLinkView | null>(null)
    const manualLinkBusy = ref(false)
    const manualLinkError = ref('')
    // 移除目录确认
    const removeDirDialog = ref(false)
    const removeDirTarget = ref<DirLinkView | null>(null)
    const removeDirBusy = ref(false)
    const removeDirError = ref('')
    // 文件级同步：文件选择
    const fileSelectOpen = ref(false)
    const fileSelectProject = ref('')
    const fileSelectDir = ref('')
    const fileSelectFiles = ref<string[]>([])
    let loadGeneration = 0

    const hasDirLinks = computed(() => dirLinks.value.length > 0)

    function setProject(p: string) {
        if (project.value === p) return
        project.value = p
        loadGeneration++
        dirLinks.value = []
        candidates.value = []
        linkAllResults.value = []
        selDir.value = ''
    }

    async function refreshDirLinks(propagateError = false) {
        const projectName = project.value
        const generation = ++loadGeneration
        if (!projectName) {
            dirLinks.value = []
            candidates.value = []
            return false
        }
        loading.value = true
        try {
            const [linksResult, dirsResult] = await Promise.all([
                PrismService.ListDirLinks(projectName),
                PrismService.ListProjectDirs(projectName),
            ])
            if (generation !== loadGeneration || project.value !== projectName) return false
            dirLinks.value = linksResult ?? []
            candidates.value = dirsResult ?? []
            const added = new Set(dirLinks.value.map(d => d.project_dir))
            candidates.value = candidates.value.filter(c => !added.has(c))
            selDir.value = ''
            return true
        } catch (e) {
            if (generation !== loadGeneration || project.value !== projectName) return false
            if (propagateError) throw e
            showSnackbar(errText(e))
            return false
        } finally {
            if (generation === loadGeneration) loading.value = false
        }
    }

    async function addDirLink() {
        const projectName = project.value
        const dir = selDir.value
        if (!projectName || !dir) return
        adding.value = true
        try {
            await PrismService.AddDirLink(projectName, dir)
            showSnackbar(t('prism.dirLinkAdded', [dir]), 'success')
            await refreshDirLinks()
        } catch (e) {
            showSnackbar(errText(e))
        } finally {
            adding.value = false
        }
    }

    function askRemoveDirLink(dl: DirLinkView) {
        removeDirTarget.value = dl
        removeDirError.value = ''
        removeDirDialog.value = true
    }

    async function confirmRemoveDirLink() {
        const dl = removeDirTarget.value
        if (!dl || removeDirBusy.value) return
        removeDirBusy.value = true
        removeDirError.value = ''
        try {
            await PrismService.RemoveDirLink(dl.project, dl.project_dir)
            showSnackbar(t('prism.dirLinkRemoved', [dl.project_dir]), 'success')
            removeDirDialog.value = false
            removeDirTarget.value = null
            await refreshDirLinks()
        } catch (e) {
            removeDirError.value = errText(e)
        } finally {
            removeDirBusy.value = false
        }
    }

    // 一键关联：先查 .pgignore，统一进确认框（单框完成确认 + ignore 选择）
    async function doLinkAll() {
        const projectName = project.value
        if (!projectName) return
        try {
            pgignoreExists.value = await PrismService.HasPGIgnore(projectName)
        } catch (e) {
            showSnackbar(errText(e))
            return
        }
        if (project.value !== projectName) return
        linkAllProject.value = projectName
        linkAllError.value = ''
        linkAllDialog.value = true
    }

    async function confirmLinkAll(createPGIgnore: boolean) {
        const projectName = linkAllProject.value
        if (!projectName || linkingAll.value) return
        linkingAll.value = true
        linkAllError.value = ''
        let refreshFailed = false
        let resultFailed = false
        try {
            if (createPGIgnore) {
                await PrismService.EnsurePGIgnore(projectName)
                pgignoreExists.value = true
            }
            const result = await runTask({
                title: t('tasks.linkAll', [projectName]),
                kind: 'link',
                run: async () => {
                    const results = (await PrismService.CreateAllLinks(projectName)) ?? []
                    if (project.value === projectName) {
                        linkAllResults.value = results
                        refreshFailed = !(await refreshDirLinks())
                    }
                    const ok = results.filter(r => r.status === 'linked' || r.status === 'existing').length
                    const skipped = results.filter(r => r.status === 'skipped').length
                    const failed = results.filter(r => r.status === 'error').length
                    const manual = results.filter(r => r.status === 'manual').length
                    resultFailed = failed > 0
                    return manual > 0
                        ? t('prism.linkAllDoneWithManual', [ok, skipped, failed, manual])
                        : t('prism.linkAllDone', [ok, skipped, failed])
                },
                warn: () => refreshFailed || resultFailed,
                onError: message => (linkAllError.value = message),
            })
            if (result !== null) {
                linkAllDialog.value = false
                linkAllProject.value = ''
            }
        } catch (e) {
            linkAllError.value = errText(e)
        } finally {
            linkingAll.value = false
        }
    }

    function askManualLink(dl: DirLinkView) {
        manualLinkTarget.value = dl
        manualLinkError.value = ''
        manualLinkDialog.value = true
    }

    async function confirmManualLink() {
        const dl = manualLinkTarget.value
        if (!dl || manualLinkBusy.value) return
        manualLinkBusy.value = true
        manualLinkError.value = ''
        try {
            const res = await PrismService.ManualLinkDir(dl.project, dl.project_dir)
            if (res.status === 'error') {
                manualLinkError.value = displayText(res.detail)
                return
            }
            showSnackbar(t('prism.manualLinkDone', [dl.project_dir]), 'success')
            manualLinkDialog.value = false
            manualLinkTarget.value = null
            await refreshDirLinks()
        } catch (e) {
            manualLinkError.value = errText(e)
        } finally {
            manualLinkBusy.value = false
        }
    }

    function openFileSelect(dl: DirLinkView) {
        fileSelectProject.value = dl.project
        fileSelectDir.value = dl.project_dir
        fileSelectFiles.value = dl.files ?? []
        fileSelectOpen.value = true
    }

    async function switchToFiles(dl: DirLinkView) {
        try {
            await PrismService.SetDirLinkMode(dl.project, dl.project_dir, 'files')
            showSnackbar(t('prism.dirLinkModeFiles'), 'success')
            await refreshDirLinks()
        } catch (e) {
            showSnackbar(errText(e))
        }
    }

    async function switchToJunction(dl: DirLinkView) {
        try {
            await PrismService.SetDirLinkMode(dl.project, dl.project_dir, '')
            showSnackbar(t('prism.dirLinkModeJunction'), 'success')
            await refreshDirLinks()
        } catch (e) {
            showSnackbar(errText(e))
        }
    }

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

    return {
        project,
        loading,
        dirLinks,
        candidates,
        selDir,
        adding,
        linkAllResults,
        hasDirLinks,
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
        setProject,
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
    }
}
