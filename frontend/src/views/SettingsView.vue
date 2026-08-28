<script setup lang="ts">
// 设置页：开发工具检测（packwiz / Prism Launcher）+ PATH 配置 + CurseForge API Key。
// 交互升级：路径输入 dirty 判断 + 失败 inline 错误；API Key 本地副本编辑，保存才写缓存。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { EnvService, isMockEnabled, setMockEnabled } from '../api'
import { pickToolPath } from '../utils/dialogs'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'
import { useEnv } from '../stores/env'
import { runTask } from '../stores/taskCenter'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import PageHeader from '../components/common/PageHeader.vue'
import ConfirmDialog from '../components/common/ConfirmDialog.vue'

const { t } = useI18n()
const { tools, apiKey, loadTools, setTools, loadApiKey, saveApiKey: persistApiKey } = useEnv()

const loading = ref(false)
const configuring = ref(false)
const savingTool = ref('')
const savingMissing = ref(false)
const missingDialog = ref(false)
const dismissed = ref(false)
const toolsBusy = computed(() => loading.value || configuring.value || savingTool.value !== '' || savingMissing.value)

// 输入框 dirty 状态与 inline 错误（key: 工具名 / 'apiKey'）
const editedPaths = ref<Record<string, string>>({})
const fieldErrors = ref<Record<string, string>>({})

const toolMeta: Record<string, { titleKey: string; icon: string; hintKey: string; placeholderKey: string }> = {
    'packwiz': {
        titleKey: 'tool.packwiz.title',
        icon: 'mdi-package-variant-closed',
        hintKey: 'tool.packwiz.hint',
        placeholderKey: 'tool.packwiz.placeholder',
    },
    'prism-launcher': {
        titleKey: 'tool.prism-launcher.title',
        icon: 'mdi-launch',
        hintKey: 'tool.prism-launcher.hint',
        placeholderKey: 'tool.prism-launcher.placeholder',
    },
}

const missingTools = computed(() => tools.value.filter(t => !t.found))

function toolTitle(tool: ToolInfo): string {
    const key = toolMeta[tool.name]?.titleKey
    return key ? t(key) : tool.name
}

function toolMessage(tool: ToolInfo): string {
    if (!tool.found) return t('tool.not_found')
    return t('tool.source.' + tool.source, [tool.path])
}

function toolChip(tool: ToolInfo): { color: string; label: string } {
    if (!tool.found) return { color: 'error', label: t('env.notFound') }
    return tool.env_ok
        ? { color: 'success', label: t('env.inPath') }
        : { color: 'warning', label: t('env.notInPath') }
}

// 输入中的路径（未保存编辑覆盖检测值）
function pathModel(tool: ToolInfo) {
    return editedPaths.value[tool.name] ?? tool.path
}

function isDirty(tool: ToolInfo): boolean {
    const v = editedPaths.value[tool.name]
    return v !== undefined && v !== tool.path
}

function onPathInput(tool: ToolInfo, v: string) {
    editedPaths.value[tool.name] = v
    delete fieldErrors.value[tool.name]
}

async function load(force = false) {
    if (loading.value) return
    loading.value = true
    try {
        await loadTools(force)
        editedPaths.value = {}
        fieldErrors.value = {}
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        loading.value = false
    }
    maybePromptMissing()
}

function maybePromptMissing() {
    if (dismissed.value) return
    if (missingTools.value.length > 0) missingDialog.value = true
}

function closeMissingDialog() {
    if (savingMissing.value || savingTool.value) return
    missingDialog.value = false
    dismissed.value = true
}

// 浏览：系统对话框选择路径（可执行文件或所在目录），填入输入框待保存；取消则不动
async function browsePath(tool: ToolInfo) {
    const picked = await pickToolPath()
    if (!picked) return
    onPathInput(tool, picked)
}

async function configure() {
    if (toolsBusy.value) return
    configuring.value = true
    try {
        const [updated, added] = await EnvService.Configure()
        if (updated) setTools(updated)
        showSnackbar(added && added.length > 0 ? t('env.pathConfigured', [added.join('; ')]) : t('env.pathNoChange'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        configuring.value = false
    }
}

async function savePath(tool: ToolInfo, closeDialog = false) {
    if (savingTool.value || savingMissing.value) return
    savingTool.value = tool.name
    try {
        const list = await EnvService.SetToolPath(tool.name, pathModel(tool))
        setTools(list)
        delete editedPaths.value[tool.name]
        delete fieldErrors.value[tool.name]
        showSnackbar(t('env.pathSaved', [toolTitle(tool)]), 'success')
        if (closeDialog && missingTools.value.length === 0) {
            missingDialog.value = false
            dismissed.value = true
        }
    } catch (e) {
        // 保存失败：错误 inline 显示在输入框（反馈位置与视线一致）
        fieldErrors.value[tool.name] = errText(e)
    } finally {
        savingTool.value = ''
    }
}

async function saveAllMissing() {
    if (toolsBusy.value) return
    savingMissing.value = true
    try {
        const targets = [...missingTools.value]
        for (const tool of targets) {
            setTools(await EnvService.SetToolPath(tool.name, pathModel(tool)))
        }
        if (missingTools.value.length > 0) {
            showSnackbar(t('env.missingPathsStillMissing', [missingTools.value.length]), 'warning')
            return
        }
        showSnackbar(t('env.missingPathsSaved', [targets.length]), 'success')
        missingDialog.value = false
        dismissed.value = true
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        savingMissing.value = false
    }
}

// —— CurseForge API Key：本地副本编辑，保存才写缓存 ——
const apiKeyVisible = ref(false)
const savingKey = ref(false)
const apiKeyDraft = ref('')
const apiKeyLoaded = ref(false)

const apiKeyDirty = computed(() => apiKeyLoaded.value && apiKeyDraft.value !== apiKey.value)

async function saveApiKey() {
    if (savingKey.value) return
    savingKey.value = true
    try {
        await persistApiKey(apiKeyDraft.value)
        delete fieldErrors.value['apiKey']
        showSnackbar(apiKeyDraft.value.trim() ? t('env.apiKeySaved') : t('env.apiKeyCleared'), 'success')
    } catch (e) {
        fieldErrors.value['apiKey'] = errText(e)
    } finally {
        savingKey.value = false
    }
}

onMounted(async () => {
    const [, keyResult] = await Promise.allSettled([load(), loadApiKey()])
    if (keyResult.status === 'rejected') showSnackbar(errText(keyResult.reason), 'error')
    apiKeyDraft.value = apiKey.value
    apiKeyLoaded.value = true
})

// —— Mock 数据层开关：确认后写入 localStorage 并刷新（各 store 缓存需重载） ——
const mockOn = ref(isMockEnabled())
const mockDialog = ref(false)
const mockPending = ref(false)

function askMock(v: boolean) {
    mockPending.value = v
    mockDialog.value = true
}

function confirmMock() {
    mockDialog.value = false
    mockOn.value = mockPending.value
    setMockEnabled(mockPending.value)
    setTimeout(() => window.location.reload(), 300)
}
</script>

<template>
    <v-container fluid class="pa-6">
        <PageHeader :title="t('settings.title')" :subtitle="t('settings.subtitle')">
            <template #actions>
                <v-btn
                    variant="text"
                    icon="mdi-refresh"
                    :loading="loading"
                    :disabled="configuring || savingMissing || savingTool !== ''"
                    :title="t('common.refresh')"
                    @click="load(true)"
                />
                <v-btn
                    color="primary"
                    class="primary-action"
                    prepend-icon="mdi-wrench"
                    :loading="configuring"
                    :disabled="loading || savingMissing || savingTool !== '' || !tools.some(t => t.found)"
                    @click="configure"
                >
                    {{ t('env.configureBtn') }}
                </v-btn>
            </template>
        </PageHeader>

        <!-- 开发工具 -->
        <v-card class="mb-5">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-tools" color="primary" class="mr-2" />
                {{ t('settings.toolsTitle') }}
            </v-card-title>
            <v-card-text class="text-body-2 text-medium-emphasis pb-0">{{ t('settings.toolsHint') }}</v-card-text>
            <v-card-text>
                <v-progress-linear v-if="loading" indeterminate class="mb-4" />
                <v-row>
                    <v-col v-for="tool in tools" :key="tool.name" cols="12" md="6">
                        <section class="tool-card surface-tile" :class="{ 'card-error': !tool.found }">
                            <div>
                                <div class="d-flex align-center mb-3">
                                    <v-avatar
                                        rounded="lg"
                                        size="44"
                                        :color="tool.found ? 'success' : 'error'"
                                        variant="tonal"
                                        class="mr-3"
                                    >
                                        <v-icon :icon="toolMeta[tool.name]?.icon" size="24" />
                                    </v-avatar>
                                    <div class="flex-grow-1" style="min-width: 0">
                                        <div class="text-subtitle-2 font-weight-medium">
                                            {{ toolMeta[tool.name]?.titleKey ? t(toolMeta[tool.name]!.titleKey) : tool.name }}
                                        </div>
                                        <div class="text-caption text-medium-emphasis">
                                            {{ toolMeta[tool.name]?.hintKey ? t(toolMeta[tool.name]!.hintKey) : '' }}
                                        </div>
                                    </div>
                                    <v-chip size="small" :color="toolChip(tool).color" variant="flat">
                                        {{ toolChip(tool).label }}
                                    </v-chip>
                                </div>
                                <v-alert
                                    :type="tool.found ? 'success' : 'warning'"
                                    variant="tonal"
                                    density="compact"
                                    class="mb-3"
                                >
                                    {{ toolMessage(tool) }}
                                </v-alert>
                                <v-text-field
                                    :model-value="pathModel(tool)"
                                    :label="toolMeta[tool.name]?.placeholderKey ? t(toolMeta[tool.name]!.placeholderKey) : ''"
                                    density="comfortable"
                                    hide-details="auto"
                                    clearable
                                    :error-messages="fieldErrors[tool.name] ?? []"
                                    :disabled="configuring || savingMissing"
                                    @update:model-value="(v: string) => onPathInput(tool, v ?? '')"
                                    @keyup.enter="savePath(tool)"
                                >
                                    <template #append>
                                        <v-btn
                                            size="small"
                                            variant="text"
                                            icon="mdi-folder-search"
                                            :title="t('env.browse')"
                                            :disabled="toolsBusy"
                                            @click="browsePath(tool)"
                                        />
                                        <v-btn
                                            size="small"
                                            variant="tonal"
                                            :loading="savingTool === tool.name"
                                            :disabled="(toolsBusy && savingTool !== tool.name) || !isDirty(tool)"
                                            @click="savePath(tool)"
                                        >
                                            {{ t('env.save') }}
                                        </v-btn>
                                    </template>
                                </v-text-field>
                                <div v-if="isDirty(tool)" class="text-caption text-warning mt-1">
                                    {{ t('env.unsavedChanges') }}
                                </div>
                            </div>
                        </section>
                    </v-col>
                </v-row>
            </v-card-text>
        </v-card>

        <!-- CurseForge API Key -->
        <v-card class="mb-5">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-key-outline" color="amber" class="mr-2" />
                {{ t('env.apiKeyTitle') }}
                <v-chip v-if="apiKey" size="x-small" color="success" variant="flat" class="ml-3">
                    {{ t('env.apiKeyConfigured') }}
                </v-chip>
                <v-chip v-else size="x-small" color="grey" variant="tonal" class="ml-3">
                    {{ t('env.apiKeyNotConfigured') }}
                </v-chip>
                <v-chip v-if="apiKeyDirty" size="x-small" color="warning" variant="tonal" class="ml-2">
                    {{ t('env.unsavedChanges') }}
                </v-chip>
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-3">{{ t('env.apiKeyHint') }}</div>
                <v-text-field
                    v-model="apiKeyDraft"
                    :type="apiKeyVisible ? 'text' : 'password'"
                    :label="t('env.apiKeyLabel')"
                    :placeholder="t('env.apiKeyPlaceholder')"
                    density="comfortable"
                    hide-details="auto"
                    clearable
                    :error-messages="fieldErrors['apiKey'] ?? []"
                    style="max-width: 520px"
                    @update:model-value="delete fieldErrors['apiKey']"
                    @keyup.enter="saveApiKey"
                >
                    <template #append>
                        <v-btn
                            size="small"
                            variant="text"
                            :icon="apiKeyVisible ? 'mdi-eye-off-outline' : 'mdi-eye-outline'"
                            :title="t('env.showHide')"
                            @click="apiKeyVisible = !apiKeyVisible"
                        />
                        <v-btn size="small" variant="tonal" :loading="savingKey" :disabled="!apiKeyDirty" @click="saveApiKey">
                            {{ t('env.save') }}
                        </v-btn>
                    </template>
                </v-text-field>
            </v-card-text>
        </v-card>

        <!-- 开发者选项：Mock 数据层 -->
        <v-card>
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-flask-outline" color="secondary" class="mr-2" />
                {{ t('settings.devTitle') }}
                <v-chip v-if="mockOn" size="x-small" color="secondary" variant="flat" class="ml-3">MOCK</v-chip>
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-1">{{ t('mock.hint') }}</div>
                <v-switch
                    :model-value="mockOn"
                    color="secondary"
                    hide-details
                    :label="t('mock.switchLabel')"
                    @update:model-value="(v: boolean | null) => askMock(!!v)"
                />
            </v-card-text>
        </v-card>

        <!-- Mock 切换确认 -->
        <ConfirmDialog
            v-model="mockDialog"
            :title="t('mock.confirmTitle')"
            :text="t('mock.confirmText')"
            :consequences="mockPending ? [t('mock.cOn1'), t('mock.cOn2')] : [t('mock.cOff1')]"
            :confirm-text="mockPending ? t('mock.enable') : t('mock.disable')"
            icon="mdi-flask-outline"
            icon-color="secondary"
            @confirm="confirmMock"
        />

        <!-- 未找到工具时的引导弹窗 -->
        <v-dialog
            v-model="missingDialog"
            max-width="560"
            @click:outside="closeMissingDialog"
            @keydown.esc="closeMissingDialog"
        >
            <v-card class="dialog-card" elevation="8">
                <v-card-title class="d-flex align-center pt-5">
                    <v-icon icon="mdi-help-circle-outline" color="warning" class="mr-2" />
                    {{ t('env.missingDialogTitle') }}
                </v-card-title>
                <v-card-text>
                    <div v-for="tool in missingTools" :key="tool.name" class="mb-4">
                        <div class="text-subtitle-2 mb-1">
                            <v-icon :icon="toolMeta[tool.name]?.icon" size="small" class="mr-1" />
                            {{ toolMeta[tool.name]?.titleKey ? t(toolMeta[tool.name]!.titleKey) : tool.name }}
                        </div>
                        <v-text-field
                            :model-value="pathModel(tool)"
                            :placeholder="toolMeta[tool.name]?.placeholderKey ? t(toolMeta[tool.name]!.placeholderKey) : ''"
                            :error-messages="fieldErrors[tool.name]"
                            density="comfortable"
                            hide-details="auto"
                            @update:model-value="(v: string) => onPathInput(tool, v ?? '')"
                            @keyup.enter="savePath(tool, true)"
                        >
                            <template #append>
                                <v-btn size="small" variant="text" icon="mdi-folder-search" :title="t('env.browse')" @click="browsePath(tool)" />
                            </template>
                        </v-text-field>
                    </div>
                    <div class="text-caption text-medium-emphasis">{{ t('env.missingDialogHint') }}</div>
                </v-card-text>
                <v-card-actions class="px-5 pb-4">
                    <v-spacer />
                    <v-btn variant="text" :disabled="savingMissing || savingTool !== ''" @click="closeMissingDialog">
                        {{ t('env.missingDialogCancel') }}
                    </v-btn>
                    <v-btn color="primary" variant="flat" :loading="savingMissing" :disabled="savingTool !== ''" @click="saveAllMissing">
                        {{ t('env.missingDialogSave') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>
    </v-container>
</template>

<style scoped>
.tool-card {
    height: 100%;
}
</style>
