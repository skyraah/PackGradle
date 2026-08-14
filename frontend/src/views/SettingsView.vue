<script setup lang="ts">
// 设置页：开发工具检测（packwiz / Prism Launcher）+ PATH 配置 + CurseForge API Key。
// 检测结果与 API Key 走共享缓存（stores/env），工作台与设置页数据一致。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Dialogs } from '@wailsio/runtime'
import { EnvService } from '../../bindings/packgradle/internal/service'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'
import { useEnv } from '../stores/env'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import PageHeader from '../components/common/PageHeader.vue'

const { t } = useI18n()
const { tools, apiKey, loadTools, setTools, loadApiKey } = useEnv()

const loading = ref(false)
const configuring = ref(false)
const savingTool = ref('')
const savingMissing = ref(false)
// 弹窗引导：检测完成后若有工具未找到则提示键入安装路径
const missingDialog = ref(false)
const dismissed = ref(false)
const toolsBusy = computed(() => loading.value || configuring.value || savingTool.value !== '' || savingMissing.value)

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

// 检测结果提示：按来源渲染（config/env/path/default-dir），未找到时提示键入路径
function toolMessage(tool: ToolInfo): string {
    if (!tool.found) return t('tool.not_found')
    return t('tool.source.' + tool.source, [tool.path])
}

// 工具状态 chip
function toolChip(tool: ToolInfo): { color: string; label: string } {
    if (!tool.found) return { color: 'error', label: t('env.notFound') }
    return tool.env_ok
        ? { color: 'success', label: t('env.inPath') }
        : { color: 'warning', label: t('env.notInPath') }
}

async function load(force = false) {
    if (loading.value) return
    loading.value = true
    try {
        await loadTools(force)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        loading.value = false
    }
    maybePromptMissing()
}

// 未找到工具时弹出引导框；用户主动关闭后本次会话不再打扰
function maybePromptMissing() {
    if (dismissed.value) return
    if (missingTools.value.length > 0) {
        missingDialog.value = true
    }
}

function closeMissingDialog() {
    if (savingMissing.value || savingTool.value) return
    missingDialog.value = false
    dismissed.value = true
}

async function browse(tool: ToolInfo) {
    try {
        const picked = await Dialogs.OpenFile({
            Title: t('env.browseTitle', [toolTitle(tool)]),
            CanChooseFiles: true,
            CanChooseDirectories: true,
        })
        if (picked) tool.path = String(picked)
    } catch {
        // 用户取消选择时 Wails 会以错误形式返回，静默忽略即可
    }
}

async function configure() {
    if (toolsBusy.value) return
    configuring.value = true
    try {
        const [updated, added] = await EnvService.Configure()
        if (updated) setTools(updated)
        if (added && added.length > 0) {
            showSnackbar(t('env.pathConfigured', [added.join('; ')]), 'success')
        } else {
            showSnackbar(t('env.pathNoChange'))
        }
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
        setTools(await EnvService.SetToolPath(tool.name, tool.path))
        showSnackbar(t('env.pathSaved', [toolTitle(tool)]), 'success')
        if (closeDialog && missingTools.value.length === 0) {
            missingDialog.value = false
            dismissed.value = true
        }
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        savingTool.value = ''
    }
}

// 弹窗底部「保存」：逐个保存所有缺失工具的路径后关闭
async function saveAllMissing() {
    if (toolsBusy.value) return
    savingMissing.value = true
    try {
        const targets = [...missingTools.value]
        for (const tool of targets) {
            setTools(await EnvService.SetToolPath(tool.name, tool.path))
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

// CurseForge API Key
const apiKeyVisible = ref(false)
const savingKey = ref(false)

async function saveApiKey() {
    if (savingKey.value) return
    savingKey.value = true
    try {
        await EnvService.SetApiKey(apiKey.value)
        showSnackbar(apiKey.value.trim() ? t('env.apiKeySaved') : t('env.apiKeyCleared'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        savingKey.value = false
    }
}

onMounted(async () => {
    // 检测与读取 API Key 互不依赖，并发执行
    const [, keyResult] = await Promise.allSettled([load(), loadApiKey()])
    if (keyResult.status === 'rejected') showSnackbar(errText(keyResult.reason), 'error')
})
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
                    @click="load(true)"
                />
                <v-btn
                    color="primary"
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
                        <section class="tool-card surface-tile">
                            <div>
                                <div class="d-flex align-center mb-3">
                                    <v-avatar
                                        rounded="lg"
                                        size="44"
                                        :color="tool.found ? 'success' : 'grey'"
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
                                    <v-chip size="small" :color="toolChip(tool).color" variant="tonal">
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
                                    v-model="tool.path"
                                    :label="toolMeta[tool.name]?.placeholderKey ? t(toolMeta[tool.name]!.placeholderKey) : ''"
                                    density="comfortable"
                                    hide-details="auto"
                                    clearable
                                    :disabled="configuring || savingMissing"
                                    @keyup.enter="savePath(tool)"
                                >
                                    <template #append>
                                        <v-btn
                                            size="small"
                                            variant="text"
                                            icon="mdi-folder-search"
                                            :title="t('env.browse')"
                                            :disabled="toolsBusy"
                                            @click="browse(tool)"
                                        />
                                        <v-btn
                                            size="small"
                                            variant="tonal"
                                            :loading="savingTool === tool.name"
                                            :disabled="toolsBusy && savingTool !== tool.name"
                                            @click="savePath(tool)"
                                        >
                                            {{ t('env.save') }}
                                        </v-btn>
                                    </template>
                                </v-text-field>
                            </div>
                        </section>
                    </v-col>
                </v-row>
            </v-card-text>
        </v-card>

        <!-- CurseForge API Key -->
        <v-card>
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-key-outline" color="amber" class="mr-2" />
                {{ t('env.apiKeyTitle') }}
                <v-chip v-if="apiKey" size="x-small" color="success" variant="tonal" class="ml-3">
                    {{ t('env.apiKeyConfigured') }}
                </v-chip>
                <v-chip v-else size="x-small" color="grey" variant="tonal" class="ml-3">
                    {{ t('env.apiKeyNotConfigured') }}
                </v-chip>
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-3">{{ t('env.apiKeyHint') }}</div>
                <v-text-field
                    v-model="apiKey"
                    :type="apiKeyVisible ? 'text' : 'password'"
                    :label="t('env.apiKeyLabel')"
                    :placeholder="t('env.apiKeyPlaceholder')"
                    density="comfortable"
                    hide-details="auto"
                    clearable
                    style="max-width: 520px"
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
                        <v-btn size="small" variant="tonal" :loading="savingKey" @click="saveApiKey">{{ t('env.save') }}</v-btn>
                    </template>
                </v-text-field>
            </v-card-text>
        </v-card>

        <!-- 未找到工具时的引导弹窗 -->
        <v-dialog
            v-model="missingDialog"
            max-width="560"
            persistent
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
                            v-model="tool.path"
                            :placeholder="toolMeta[tool.name]?.placeholderKey ? t(toolMeta[tool.name]!.placeholderKey) : ''"
                            density="comfortable"
                            hide-details="auto"
                            @keyup.enter="savePath(tool, true)"
                        >
                            <template #append>
                                <v-btn size="small" variant="text" icon="mdi-folder-search" :title="t('env.browse')" @click="browse(tool)" />
                            </template>
                        </v-text-field>
                    </div>
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
