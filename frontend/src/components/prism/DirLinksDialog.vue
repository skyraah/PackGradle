<script setup lang="ts">
// 目录同步关联管理：目录关联列表 + 一键关联（.pgignore 引导）+ 手动链接 +
// 整目录 junction ↔ 文件级同步切换 + 文件选择（子组件 FileSelectDialog）。
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PrismService } from '../../../bindings/packgradle/internal/service'
import type { DirLinkView, LinkResult } from '../../../bindings/packgradle/internal/prism'
import { showSnackbar } from '../../stores/ui'
import { errText, displayText } from '../../utils/errors'
import ConfirmDialog from '../common/ConfirmDialog.vue'
import FileSelectDialog from './FileSelectDialog.vue'

const { t } = useI18n()

const props = defineProps<{
    modelValue: boolean
    project: string
}>()

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
}>()

const loading = ref(false)
const dirLinks = ref<DirLinkView[]>([])
const candidates = ref<string[]>([])
const selDir = ref('')
const adding = ref(false)
// 一键关联
const linkAlling = ref(false)
const linkAllResults = ref<LinkResult[]>([])
const pgignoreDialog = ref(false)
// 手动链接确认
const manualLinkDialog = ref(false)
const manualLinkTarget = ref<DirLinkView | null>(null)
const manualLinking = ref(false)
// 文件级同步：文件选择
const fileSelectOpen = ref(false)
const fileSelectDir = ref('')
const fileSelectFiles = ref<string[]>([])
// 切换模式
const switching = ref('')

watch(
    () => props.modelValue,
    open => {
        if (open) void refreshDirLinks()
    },
)

async function refreshDirLinks() {
    loading.value = true
    try {
        // 两个独立查询并发执行，避免串行往返
        const [linksResult, dirsResult] = await Promise.all([
            PrismService.ListDirLinks(props.project),
            PrismService.ListProjectDirs(props.project),
        ])
        dirLinks.value = linksResult ?? []
        candidates.value = dirsResult ?? []
        // 已添加的目录从候选中剔除
        const added = new Set(dirLinks.value.map(d => d.project_dir))
        candidates.value = candidates.value.filter(c => !added.has(c))
        selDir.value = ''
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        loading.value = false
    }
}

async function addDirLink() {
    if (!selDir.value) return
    adding.value = true
    try {
        await PrismService.AddDirLink(props.project, selDir.value)
        showSnackbar(t('prism.dirLinkAdded', [selDir.value]))
        await refreshDirLinks()
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        adding.value = false
    }
}

async function removeDirLink(dl: DirLinkView) {
    try {
        await PrismService.RemoveDirLink(dl.project, dl.project_dir)
        showSnackbar(t('prism.dirLinkRemoved', [dl.project_dir]))
        await refreshDirLinks()
    } catch (e) {
        showSnackbar(errText(e))
    }
}

// 一键关联：项目根下全部未被 .pgignore 忽略的条目建链
async function doLinkAll() {
    try {
        const exists = await PrismService.HasPGIgnore(props.project)
        if (!exists) {
            pgignoreDialog.value = true
            return
        }
    } catch (e) {
        showSnackbar(errText(e))
        return
    }
    await executeLinkAll()
}

function choosePGIgnore(choice: 'create' | 'skip') {
    pgignoreDialog.value = false
    void applyPGIgnoreChoice(choice)
}

async function applyPGIgnoreChoice(choice: 'create' | 'skip') {
    if (choice === 'create') {
        try {
            await PrismService.EnsurePGIgnore(props.project)
            showSnackbar(t('prism.pgignoreCreated'))
        } catch (e) {
            showSnackbar(errText(e))
            return
        }
    }
    await executeLinkAll()
}

async function executeLinkAll() {
    linkAlling.value = true
    try {
        linkAllResults.value = (await PrismService.CreateAllLinks(props.project)) ?? []
        const ok = linkAllResults.value.filter(r => r.status === 'linked' || r.status === 'existing').length
        const skipped = linkAllResults.value.filter(r => r.status === 'skipped').length
        const failed = linkAllResults.value.filter(r => r.status === 'error').length
        const manual = linkAllResults.value.filter(r => r.status === 'manual').length
        if (manual > 0) {
            showSnackbar(t('prism.linkAllDoneWithManual', [ok, skipped, failed, manual]))
        } else {
            showSnackbar(t('prism.linkAllDone', [ok, skipped, failed]))
        }
        await refreshDirLinks()
    } catch (e) {
        showSnackbar(errText(e))
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
            showSnackbar(displayText(res.detail))
        } else {
            showSnackbar(t('prism.manualLinkDone', [dl.project_dir]))
        }
        await refreshDirLinks()
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        manualLinking.value = false
    }
}

function openFileSelect(dl: DirLinkView) {
    fileSelectDir.value = dl.project_dir
    fileSelectFiles.value = dl.files ?? []
    fileSelectOpen.value = true
}

async function switchToFiles(dl: DirLinkView) {
    switching.value = dl.project_dir
    try {
        await PrismService.SetDirLinkMode(dl.project, dl.project_dir, 'files')
        showSnackbar(t('prism.dirLinkModeFiles'))
        await refreshDirLinks()
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        switching.value = ''
    }
}

async function switchToJunction(dl: DirLinkView) {
    switching.value = dl.project_dir
    try {
        await PrismService.SetDirLinkMode(dl.project, dl.project_dir, '')
        showSnackbar(t('prism.dirLinkModeJunction'))
        await refreshDirLinks()
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        switching.value = ''
    }
}

// 一键关联结果的状态 chip
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
</script>

<template>
    <v-dialog :model-value="modelValue" max-width="720" @update:model-value="emit('update:modelValue', $event)">
        <v-card elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-folder-sync-outline" color="primary" class="mr-2" />
                {{ t('prism.dirLinksTitle') }}
                <v-chip size="x-small" variant="tonal" class="ml-2">{{ project }}</v-chip>
            </v-card-title>
            <v-card-text>
                <div class="d-flex align-center mb-3">
                    <div class="text-body-2 text-medium-emphasis flex-grow-1 mr-3">{{ t('prism.linkAllHint') }}</div>
                    <v-btn
                        color="primary"
                        variant="tonal"
                        prepend-icon="mdi-link-variant-plus"
                        :loading="linkAlling"
                        @click="doLinkAll"
                    >
                        {{ t('prism.linkAllBtn') }}
                    </v-btn>
                </div>

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

                <v-progress-linear v-if="loading" indeterminate class="mb-2" />

                <!-- 已关联目录 -->
                <v-list v-if="dirLinks.length > 0" density="compact" class="mb-2 dir-list">
                    <v-list-item v-for="dl in dirLinks" :key="dl.project_dir" :title="dl.project_dir">
                        <template #subtitle>
                            <span class="text-caption text-medium-emphasis">→ minecraft/{{ dl.instance_dir }}</span>
                        </template>
                        <template #append>
                            <v-chip
                                size="x-small"
                                :color="dl.mode === 'files' ? 'info' : 'grey'"
                                variant="tonal"
                                class="mr-2"
                            >
                                {{ dl.mode === 'files' ? t('prism.dirLinkModeFiles') : t('prism.dirLinkModeJunction') }}
                            </v-chip>
                            <v-chip v-if="!dl.project_exists" size="x-small" color="warning" variant="tonal" class="mr-2">
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
                                :loading="switching === dl.project_dir"
                                @click="switchToFiles(dl)"
                            >
                                {{ t('prism.dirLinkSwitchFiles') }}
                            </v-btn>
                            <v-btn
                                v-if="dl.mode === 'files'"
                                size="small"
                                variant="text"
                                class="mr-1"
                                :loading="switching === dl.project_dir"
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
                            <v-btn size="small" variant="text" color="error" @click="removeDirLink(dl)">
                                {{ t('prism.dirLinkRemove') }}
                            </v-btn>
                        </template>
                    </v-list-item>
                </v-list>
                <div v-else-if="!loading" class="text-body-2 text-medium-emphasis mb-3">
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
                <div v-else-if="!loading" class="text-caption text-medium-emphasis">
                    {{ t('prism.dirLinkNoCandidate') }}
                </div>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-spacer />
                <v-btn variant="tonal" @click="emit('update:modelValue', false)">{{ t('common.close') }}</v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>

    <!-- .pgignore 缺失询问 -->
    <v-dialog v-model="pgignoreDialog" max-width="520">
        <v-card elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-file-question-outline" color="warning" class="mr-2" />
                {{ t('prism.pgignoreMissingTitle') }}
            </v-card-title>
            <v-card-text>{{ t('prism.pgignoreMissingText') }}</v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-spacer />
                <v-btn variant="text" @click="pgignoreDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                <v-btn variant="tonal" @click="choosePGIgnore('skip')">{{ t('prism.pgignoreSkipAndLink') }}</v-btn>
                <v-btn color="primary" variant="flat" @click="choosePGIgnore('create')">
                    {{ t('prism.pgignoreCreateAndLink') }}
                </v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>

    <!-- 手动链接确认 -->
    <ConfirmDialog
        v-model="manualLinkDialog"
        :title="t('prism.manualLinkConfirmTitle')"
        :text="t('prism.manualLinkConfirmText', [manualLinkTarget?.project_dir ?? ''])"
        :confirm-text="t('prism.manualLinkBtn')"
        :loading="manualLinking"
        icon="mdi-alert-outline"
        @confirm="confirmManualLink"
    />

    <!-- 文件级同步：文件选择 -->
    <FileSelectDialog
        v-model="fileSelectOpen"
        :project="project"
        :dir="fileSelectDir"
        :files="fileSelectFiles"
        @changed="refreshDirLinks"
    />
</template>

<style scoped>
.results-list,
.dir-list {
    border: 1px solid rgba(255, 255, 255, 0.08);
    border-radius: 10px;
}
</style>
