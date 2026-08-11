<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { EnvService } from '../../bindings/packgradle'
import type { ToolInfo } from '../../bindings/packgradle/models'

const tools = ref<ToolInfo[]>([])
const loading = ref(false)
const configuring = ref(false)
const snackbar = ref(false)
const snackbarMsg = ref('')

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

async function load() {
    loading.value = true
    try {
        tools.value = (await EnvService.Detect()) ?? []
    } finally {
        loading.value = false
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

async function savePath(tool: ToolInfo) {
    try {
        tools.value = (await EnvService.SetToolPath(tool.name, tool.path)) ?? []
        snackbarMsg.value = '已保存 ' + (toolMeta[tool.name]?.title ?? tool.name) + ' 路径'
        snackbar.value = true
    } catch (e) {
        snackbarMsg.value = String(e)
        snackbar.value = true
    }
}

onMounted(load)
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
                                <v-btn size="small" variant="tonal" @click="savePath(tool)">保存</v-btn>
                            </template>
                        </v-text-field>
                    </v-card-text>
                </v-card>
            </v-col>
        </v-row>

        <v-snackbar v-model="snackbar" timeout="4000" location="bottom">
            {{ snackbarMsg }}
        </v-snackbar>
    </div>
</template>
