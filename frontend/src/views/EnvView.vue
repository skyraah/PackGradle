<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Dialogs } from '@wailsio/runtime'
import { EnvService } from '../../bindings/packgradle/internal/service'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'
import { useSnackbar } from '../composables/useSnackbar'
import { errText } from '../utils/errors'

const { t } = useI18n()

const tools = ref<ToolInfo[]>([])
const loading = ref(false)
const configuring = ref(false)
const { snackbar, snackbarMsg, show } = useSnackbar()
// 弹窗引导：检测完成后若有工具未找到则提示键入安装路径
const missingDialog = ref(false)
const dismissed = ref(false)

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

// 检测结果提示：按来源渲染（config/env/path/default-dir），未找到时提示键入路径
function toolMessage(tool: ToolInfo): string {
    if (!tool.found) return t('tool.not_found')
    return t(`tool.source.${tool.source}`, [tool.path])
}

async function load() {
    loading.value = true
    try {
        tools.value = (await EnvService.Detect()) ?? []
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
    missingDialog.value = false
    dismissed.value = true
}

async function browse(tool: ToolInfo) {
    try {
        const picked = await Dialogs.OpenFile({
            Title: t('env.browseTitle', [toolMeta[tool.name]?.titleKey ?? tool.name]),
            CanChooseFiles: true,
            CanChooseDirectories: true,
        })
        if (picked) tool.path = String(picked)
    } catch {
        // 用户取消选择时 Wails 会以错误形式返回，静默忽略即可
    }
}

async function configure() {
    configuring.value = true
    try {
        const [updated, added] = await EnvService.Configure()
        if (updated) tools.value = updated
        if (added && added.length > 0) {
            show(t('env.pathConfigured', [added.join('; ')]))
        } else {
            show(t('env.pathNoChange'))
        }
    } catch (e) {
        show(errText(e))
    } finally {
        configuring.value = false
    }
}

async function savePath(tool: ToolInfo, closeDialog = false) {
    try {
        tools.value = (await EnvService.SetToolPath(tool.name, tool.path)) ?? []
        show(t('env.pathSaved', [toolMeta[tool.name]?.titleKey ?? tool.name]))
        if (closeDialog) {
            missingDialog.value = false
            dismissed.value = true
        }
    } catch (e) {
        show(errText(e))
    }
}

// 弹窗底部「保存」：逐个保存所有缺失工具的路径后关闭
async function saveAllMissing() {
    for (const tool of [...missingTools.value]) {
        await savePath(tool)
    }
    missingDialog.value = false
    dismissed.value = true
}

// CurseForge API Key 配置
const apiKey = ref('')
const apiKeyVisible = ref(false)
const savingKey = ref(false)

async function saveApiKey() {
    savingKey.value = true
    try {
        await EnvService.SetApiKey(apiKey.value)
        show(apiKey.value ? t('env.apiKeySaved') : t('env.apiKeyCleared'))
    } catch (e) {
        show(errText(e))
    } finally {
        savingKey.value = false
    }
}

onMounted(async () => {
    // 检测与读取 API Key 互不依赖，并发执行
    const [, key] = await Promise.all([load(), EnvService.GetApiKey()])
    apiKey.value = key ?? ''
})
</script>

<template>
    <div>
        <v-row class="align-center mb-4">
            <v-col>
                <h2 class="text-h5">{{ t('env.title') }}</h2>
                <div class="text-body-2 text-medium-emphasis">{{ t('env.subtitle') }}</div>
            </v-col>
            <v-col cols="auto">
                <v-btn variant="text" icon="mdi-refresh" :loading="loading" @click="load" />
                <v-btn
                    color="primary"
                    prepend-icon="mdi-wrench"
                    :loading="configuring"
                    :disabled="!tools.some(t => t.found)"
                    @click="configure"
                >
                    {{ t('env.configureBtn') }}
                </v-btn>
            </v-col>
        </v-row>

        <v-row>
            <v-col v-for="tool in tools" :key="tool.name" cols="12" md="6">
                <v-card :loading="loading">
                    <v-card-title class="d-flex align-center">
                        <v-icon :icon="toolMeta[tool.name]?.icon" class="mr-2" :color="tool.found ? 'success' : 'grey'" />
                        {{ toolMeta[tool.name]?.titleKey ? t(toolMeta[tool.name]!.titleKey) : tool.name }}
                        <v-chip
                            v-if="tool.found"
                            size="x-small"
                            :color="tool.env_ok ? 'success' : 'warning'"
                            class="ml-3"
                        >
                            {{ tool.env_ok ? t('env.inPath') : t('env.notInPath') }}
                        </v-chip>
                        <v-chip v-else size="x-small" color="error" class="ml-3">{{ t('env.notFound') }}</v-chip>
                    </v-card-title>

                    <v-card-text>
                        <div class="text-body-2 text-medium-emphasis mb-2">
                            {{ toolMeta[tool.name]?.hintKey ? t(toolMeta[tool.name]!.hintKey) : '' }}
                        </div>
                        <v-alert
                            :type="tool.found ? 'success' : 'warning'"
                            variant="tonal"
                            density="compact"
                            class="mb-4"
                        >
                            {{ toolMessage(tool) }}
                        </v-alert>
                        <v-text-field
                            v-model="tool.path"
                            :label="toolMeta[tool.name]?.placeholderKey ? t(toolMeta[tool.name]!.placeholderKey) : ''"
                            :placeholder="toolMeta[tool.name]?.placeholderKey ? t(toolMeta[tool.name]!.placeholderKey) : ''"
                            variant="outlined"
                            density="compact"
                            hide-details="auto"
                            clearable
                            @keyup.enter="savePath(tool)"
                        >
                            <template #append>
                                <v-btn size="small" variant="text" icon="mdi-folder-search" :title="t('env.browse')" @click="browse(tool)" />
                                <v-btn size="small" variant="tonal" @click="savePath(tool)">{{ t('env.save') }}</v-btn>
                            </template>
                        </v-text-field>
                    </v-card-text>
                </v-card>
            </v-col>
        </v-row>

        <v-card class="mt-4">
            <v-card-title class="d-flex align-center">
                <v-icon icon="mdi-key-outline" color="amber" class="mr-2" />
                {{ t('env.apiKeyTitle') }}
                <v-chip v-if="apiKey" size="x-small" color="success" class="ml-3">{{ t('env.apiKeyConfigured') }}</v-chip>
                <v-chip v-else size="x-small" color="grey" class="ml-3">{{ t('env.apiKeyNotConfigured') }}</v-chip>
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-2">{{ t('env.apiKeyHint') }}</div>
                <v-text-field
                    v-model="apiKey"
                    :type="apiKeyVisible ? 'text' : 'password'"
                    :label="t('env.apiKeyLabel')"
                    :placeholder="t('env.apiKeyPlaceholder')"
                    variant="outlined"
                    density="compact"
                    hide-details="auto"
                    clearable
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
            <v-card>
                <v-card-title class="d-flex align-center">
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
                            variant="outlined"
                            density="compact"
                            hide-details="auto"
                            @keyup.enter="savePath(tool, true)"
                        >
                            <template #append>
                                <v-btn size="small" variant="text" icon="mdi-folder-search" :title="t('env.browse')" @click="browse(tool)" />
                            </template>
                        </v-text-field>
                    </div>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="closeMissingDialog">{{ t('env.missingDialogCancel') }}</v-btn>
                    <v-btn color="primary" variant="tonal" @click="saveAllMissing">
                        {{ t('env.missingDialogSave') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-snackbar v-model="snackbar" timeout="4000" location="bottom">
            {{ snackbarMsg }}
        </v-snackbar>
    </div>
</template>
