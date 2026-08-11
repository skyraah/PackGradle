<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Dialogs } from '@wailsio/runtime'
import { EnvService } from '../../bindings/packgradle'
import type { ToolInfo } from '../../bindings/packgradle/models'

const tools = ref<ToolInfo[]>([])
const loading = ref(false)
const configuring = ref(false)
const snackbar = ref(false)
const snackbarMsg = ref('')
// 弹窗引导：检测完成后若有工具未找到则提示键入安装路径
const missingDialog = ref(false)
const dismissed = ref(false)

const toolMeta: Record<string, { title: string; icon: string; hint: string; placeholder: string }> = {
    'packwiz': {
        title: 'packwiz',
        icon: 'mdi-package-variant-closed',
        hint: 'packwiz 整合包管理器（CLI）',
        placeholder: 'packwiz.exe 路径（留空自动检测）',
    },
    'prism-launcher': {
        title: 'Prism Launcher',
        icon: 'mdi-launch',
        hint: 'Prism Launcher 启动器',
        placeholder: 'PrismLauncher.exe 路径（留空自动检测）',
    },
}

const missingTools = computed(() => tools.value.filter(t => !t.found))

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
            Title: `选择 ${toolMeta[tool.name]?.title} 路径（可选手柄或所在目录）`,
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
        const [updated, msg] = await EnvService.Configure()
        if (updated) tools.value = updated
        snackbarMsg.value = msg ?? ''
        snackbar.value = true
    } catch (e) {
        snackbarMsg.value = String(e)
        snackbar.value = true
    } finally {
        configuring.value = false
    }
}

async function savePath(tool: ToolInfo, closeDialog = false) {
    try {
        tools.value = (await EnvService.SetToolPath(tool.name, tool.path)) ?? []
        snackbarMsg.value = '已保存 ' + (toolMeta[tool.name]?.title ?? tool.name) + ' 路径'
        snackbar.value = true
        if (closeDialog) {
            missingDialog.value = false
            dismissed.value = true
        }
    } catch (e) {
        snackbarMsg.value = String(e)
        snackbar.value = true
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
        snackbarMsg.value = apiKey.value ? '已保存 CurseForge API Key' : '已清除 CurseForge API Key'
        snackbar.value = true
    } catch (e) {
        snackbarMsg.value = String(e)
        snackbar.value = true
    } finally {
        savingKey.value = false
    }
}

onMounted(async () => {
    await load()
    apiKey.value = (await EnvService.GetApiKey()) ?? ''
})
</script>

<template>
    <div>
        <v-row class="align-center mb-4">
            <v-col>
                <h2 class="text-h5">环境配置</h2>
                <div class="text-body-2 text-medium-emphasis">
                    自动检测 packwiz 与 Prism Launcher 的装载状态，一键将工具目录加入系统 PATH
                </div>
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
                    一键配置环境变量
                </v-btn>
            </v-col>
        </v-row>

        <v-row>
            <v-col v-for="tool in tools" :key="tool.name" cols="12" md="6">
                <v-card :loading="loading">
                    <v-card-title class="d-flex align-center">
                        <v-icon :icon="toolMeta[tool.name]?.icon" class="mr-2" :color="tool.found ? 'success' : 'grey'" />
                        {{ toolMeta[tool.name]?.title ?? tool.name }}
                        <v-chip
                            v-if="tool.found"
                            size="x-small"
                            :color="tool.env_ok ? 'success' : 'warning'"
                            class="ml-3"
                        >
                            {{ tool.env_ok ? '已在 PATH' : '未加入 PATH' }}
                        </v-chip>
                        <v-chip v-else size="x-small" color="error" class="ml-3">未检测到</v-chip>
                    </v-card-title>

                    <v-card-text>
                        <div class="text-body-2 text-medium-emphasis mb-2">{{ toolMeta[tool.name]?.hint }}</div>
                        <v-alert
                            :type="tool.found ? 'success' : 'warning'"
                            variant="tonal"
                            density="compact"
                            class="mb-4"
                        >
                            {{ tool.message }}
                        </v-alert>
                        <v-text-field
                            v-model="tool.path"
                            :label="toolMeta[tool.name]?.placeholder"
                            :placeholder="toolMeta[tool.name]?.placeholder"
                            variant="outlined"
                            density="compact"
                            hide-details="auto"
                            clearable
                            @keyup.enter="savePath(tool)"
                        >
                            <template #append>
                                <v-btn size="small" variant="text" icon="mdi-folder-search" title="浏览" @click="browse(tool)" />
                                <v-btn size="small" variant="tonal" @click="savePath(tool)">保存</v-btn>
                            </template>
                        </v-text-field>
                    </v-card-text>
                </v-card>
            </v-col>
        </v-row>

        <v-card class="mt-4">
            <v-card-title class="d-flex align-center">
                <v-icon icon="mdi-key-outline" color="amber" class="mr-2" />
                CurseForge API Key
                <v-chip v-if="apiKey" size="x-small" color="success" class="ml-3">已配置</v-chip>
                <v-chip v-else size="x-small" color="grey" class="ml-3">未配置</v-chip>
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-2">
                    用于按需查询 mod 版本与更新信息（如获取 mod 版本号），可在 CurseForge 开发者后台免费申请。
                </div>
                <v-text-field
                    v-model="apiKey"
                    :type="apiKeyVisible ? 'text' : 'password'"
                    label="CurseForge API Key（留空并保存可清除）"
                    placeholder="粘贴你的 API Key"
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
                            title="显示/隐藏"
                            @click="apiKeyVisible = !apiKeyVisible"
                        />
                        <v-btn size="small" variant="tonal" :loading="savingKey" @click="saveApiKey">保存</v-btn>
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
                    未找到工具，请输入安装路径
                </v-card-title>
                <v-card-text>
                    <div v-for="tool in missingTools" :key="tool.name" class="mb-4">
                        <div class="text-subtitle-2 mb-1">
                            <v-icon :icon="toolMeta[tool.name]?.icon" size="small" class="mr-1" />
                            {{ toolMeta[tool.name]?.title ?? tool.name }}
                        </div>
                        <v-text-field
                            v-model="tool.path"
                            :placeholder="toolMeta[tool.name]?.placeholder"
                            variant="outlined"
                            density="compact"
                            hide-details="auto"
                            @keyup.enter="savePath(tool, true)"
                        >
                            <template #append>
                                <v-btn size="small" variant="text" icon="mdi-folder-search" title="浏览" @click="browse(tool)" />
                            </template>
                        </v-text-field>
                    </div>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="text" @click="closeMissingDialog">取消</v-btn>
                    <v-btn color="primary" variant="tonal" @click="saveAllMissing">
                        保存
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-snackbar v-model="snackbar" timeout="4000" location="bottom">
            {{ snackbarMsg }}
        </v-snackbar>
    </div>
</template>
