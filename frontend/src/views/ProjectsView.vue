<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Dialogs } from '@wailsio/runtime'
import { PackwizService } from '../../bindings/packgradle'
import type { PackProject } from '../../bindings/packgradle/models'

const projects = ref<PackProject[]>([])
const loading = ref(false)
const importing = ref(false)
const expanded = ref<string | null>(null)
const snackbar = ref(false)
const snackbarMsg = ref('')
const refreshing = ref<string | null>(null)
const refreshOutput = ref('')
const outputDialog = ref(false)

const loaderChips: Record<string, { label: string; color: string }> = {
    fabric: { label: 'Fabric', color: 'orange' },
    forge: { label: 'Forge', color: 'green' },
    neoforge: { label: 'NeoForge', color: 'blue' },
    quilt: { label: 'Quilt', color: 'pink' },
    liteloader: { label: 'LiteLoader', color: 'teal' },
}

const sideColors: Record<string, string> = {
    client: 'blue',
    server: 'orange',
    both: 'green',
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
            Title: '选择 pack.toml',
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
        snackbarMsg.value = `已导入项目「${proj.name}」（${(proj.mods ?? []).length} 个 mod）`
        snackbar.value = true
        await load()
        expanded.value = proj.name
    } catch (e) {
        snackbarMsg.value = '导入失败: ' + String(e)
        snackbar.value = true
    } finally {
        importing.value = false
    }
}

async function removeProject(proj: PackProject) {
    let confirmed: string
    try {
        confirmed = await Dialogs.Question({
            Title: '确认移除',
            Message: `确定从列表中移除项目「${proj.name}」吗？（不会删除磁盘上的文件）`,
            Buttons: [
                { Label: '移除' },
                { Label: '取消', IsCancel: true },
            ],
        })
    } catch {
        return // 用户取消对话框，静默忽略
    }
    if (confirmed !== 'Yes' && confirmed !== '移除') return
    projects.value = (await PackwizService.RemoveProject(proj.name)) ?? []
    if (expanded.value === proj.name) expanded.value = null
}

async function refreshProject(proj: PackProject) {
    refreshing.value = proj.name
    try {
        const result = await PackwizService.RefreshProject(proj.name)
        refreshOutput.value = result.output || (result.ok ? 'packwiz refresh 执行成功（无输出）' : '执行失败')
        outputDialog.value = true
        await load()
    } finally {
        refreshing.value = null
    }
}

function loaderChip(loader: string) {
    return loaderChips[loader] ?? { label: loader, color: 'grey' }
}

onMounted(load)
</script>

<template>
    <div>
        <v-row class="align-center mb-4">
            <v-col>
                <h2 class="text-h5">项目管理</h2>
                <div class="text-body-2 text-medium-emphasis">导入 pack.toml，以视图管理你的 packwiz 项目与 mod</div>
            </v-col>
            <v-col cols="auto">
                <v-btn variant="text" icon="mdi-refresh" :loading="loading" @click="load" />
                <v-btn color="primary" prepend-icon="mdi-folder-open" :loading="importing" @click="importProject">
                    导入 pack.toml
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
            尚未导入任何项目。点击右上角「导入 pack.toml」，选择你的整合包项目根目录下的 pack.toml 文件。
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
                    <v-chip v-if="proj.error" size="x-small" color="error" class="ml-2">解析失败</v-chip>
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
                            Minecraft {{ proj.minecraft }}
                        </v-chip>
                        <v-chip v-if="proj.version" size="x-small" variant="tonal" class="mr-2">
                            v{{ proj.version }}
                        </v-chip>
                        <v-chip v-if="proj.author" size="x-small" variant="tonal">作者: {{ proj.author }}</v-chip>
                        <span class="ml-2 text-caption text-medium-emphasis">{{ (proj.mods ?? []).length }} 个 mod</span>
                    </span>
                    <span v-else class="text-error">{{ proj.error }}</span>
                </template>
                <template #append>
                    <v-btn
                        v-if="!proj.error"
                        icon="mdi-refresh"
                        variant="text"
                        size="small"
                        :loading="refreshing === proj.name"
                        title="packwiz refresh"
                        @click.stop="refreshProject(proj)"
                    />
                    <v-btn
                        icon="mdi-delete-outline"
                        variant="text"
                        size="small"
                        color="error"
                        title="移除项目"
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
                                <th>mod</th>
                                <th class="w-25">side</th>
                                <th class="w-30">文件</th>
                                <th class="w-25">版本</th>
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
                                        {{ mod.side_cn }}
                                    </v-chip>
                                </td>
                                <td class="text-caption">{{ mod.file || '—' }}</td>
                                <td class="text-caption">{{ mod.version || '—' }}</td>
                            </tr>
                            <tr v-if="(proj.mods ?? []).length === 0">
                                <td colspan="4" class="text-center text-medium-emphasis">项目还没有 mods 目录</td>
                            </tr>
                        </tbody>
                    </v-table>
                </div>
            </v-expand-transition>
        </v-card>

        <v-dialog v-model="outputDialog" max-width="640">
            <v-card>
                <v-card-title class="text-subtitle-1">packwiz refresh 输出</v-card-title>
                <v-card-text>
                    <pre class="text-body-2 refresh-output">{{ refreshOutput }}</pre>
                </v-card-text>
                <v-card-actions>
                    <v-spacer />
                    <v-btn variant="tonal" @click="outputDialog = false">关闭</v-btn>
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
