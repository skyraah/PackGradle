<script setup lang="ts">
// 目录同步关联管理对话框：逻辑已提炼至 composables/useDirLinks（与「开发版本」页面共用），
// 本组件仅负责对话框壳与布局。操作经任务中心。
import { watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useDirLinks } from '../../composables/useDirLinks'
import { displayText } from '../../utils/errors'
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

const {
    project,
    loading,
    dirLinks,
    candidates,
    selDir,
    adding,
    linkAllResults,
    linkAllDialog,
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
} = useDirLinks()

watch(
    () => props.modelValue,
    open => {
        if (open) {
            setProject(props.project)
            void refreshDirLinks()
        }
    },
)
</script>

<template>
    <v-dialog :model-value="modelValue" max-width="720" @update:model-value="emit('update:modelValue', $event)">
        <v-card class="dialog-card" elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-folder-sync-outline" color="primary" class="mr-2" />
                {{ t('prism.dirLinksTitle') }}
                <v-chip size="x-small" variant="tonal" class="ml-2">{{ project }}</v-chip>
            </v-card-title>
            <v-card-text>
                <div class="d-flex align-center mb-3">
                    <div class="text-body-2 text-medium-emphasis flex-grow-1 mr-3">{{ t('prism.linkAllHint') }}</div>
                    <v-btn color="primary" variant="tonal" prepend-icon="mdi-link-variant-plus" @click="doLinkAll">
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
                            <v-chip size="x-small" :color="dl.mode === 'files' ? 'info' : 'grey'" variant="tonal" class="mr-2">
                                {{ dl.mode === 'files' ? t('prism.dirLinkModeFiles') : t('prism.dirLinkModeJunction') }}
                            </v-chip>
                            <v-chip v-if="!dl.project_exists" size="x-small" color="warning" variant="tonal" class="mr-2">
                                {{ t('prism.parseFailed') }}
                            </v-chip>
                            <v-btn v-if="dl.mode === 'files'" size="small" variant="text" class="mr-1" @click="openFileSelect(dl)">
                                {{ t('prism.dirLinkFilesBtn') }}
                            </v-btn>
                            <v-btn v-if="dl.mode !== 'files'" size="small" variant="text" class="mr-1" @click="switchToFiles(dl)">
                                {{ t('prism.dirLinkSwitchFiles') }}
                            </v-btn>
                            <v-btn v-if="dl.mode === 'files'" size="small" variant="text" class="mr-1" @click="switchToJunction(dl)">
                                {{ t('prism.dirLinkSwitchJunction') }}
                            </v-btn>
                            <v-btn v-if="dl.mode !== 'files'" size="small" variant="text" class="mr-1" @click="askManualLink(dl)">
                                {{ t('prism.manualLinkBtn') }}
                            </v-btn>
                            <v-btn size="small" variant="text" color="error" @click="askRemoveDirLink(dl)">
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

        <!-- 一键关联确认（合并 .pgignore 询问为单框） -->
        <v-dialog v-model="linkAllDialog" :persistent="linkingAll" max-width="520">
            <v-card class="dialog-card" elevation="8">
                <v-card-title class="d-flex align-center pt-5">
                    <v-avatar size="34" rounded="md" color="warning" variant="tonal" class="mr-3">
                        <v-icon icon="mdi-link-variant-plus" size="19" />
                    </v-avatar>
                    {{ t('prism.linkAllConfirmTitle') }}
                </v-card-title>
                <v-card-text class="text-body-2">
                    {{ t('prism.linkAllConfirmText', [project]) }}
                    <ul class="consequence-list mt-2">
                        <li>{{ t('prism.linkAllC1') }}</li>
                        <li>{{ t('prism.linkAllC2') }}</li>
                        <li v-if="!pgignoreExists" class="text-warning">{{ t('prism.linkAllC3') }}</li>
                    </ul>
                    <v-alert v-if="linkAllError" type="error" variant="tonal" density="compact" class="mt-3">
                        {{ linkAllError }}
                    </v-alert>
                </v-card-text>
                <v-card-actions class="px-5 pb-4">
                    <v-spacer />
                    <v-btn variant="text" :disabled="linkingAll" @click="linkAllDialog = false">{{ t('prism.linkCancel') }}</v-btn>
                    <v-btn v-if="!pgignoreExists" variant="tonal" :disabled="linkingAll" @click="confirmLinkAll(false)">
                        {{ t('prism.pgignoreSkipAndLink') }}
                    </v-btn>
                    <v-btn color="primary" variant="flat" :loading="linkingAll" @click="confirmLinkAll(!pgignoreExists)">
                        {{ pgignoreExists ? t('common.confirm') : t('prism.pgignoreCreateAndLink') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 手动链接确认（数据移动后果） -->
        <ConfirmDialog
            v-model="manualLinkDialog"
            :title="t('prism.manualLinkConfirmTitle')"
            :text="t('prism.manualLinkConfirmText', [manualLinkTarget?.project_dir ?? ''])"
            :consequences="[t('prism.manualLinkC1'), t('prism.manualLinkC2')]"
            :confirm-text="t('prism.manualLinkBtn')"
            icon="mdi-alert-outline"
            :loading="manualLinkBusy"
            :error="manualLinkError"
            @confirm="confirmManualLink"
        />

        <!-- 移除目录确认 -->
        <ConfirmDialog
            v-model="removeDirDialog"
            :title="t('prism.dirLinkRemoveTitle')"
            :text="t('prism.dirLinkRemoveText', [removeDirTarget?.project_dir ?? ''])"
            :consequences="[t('prism.dirLinkRemoveC1')]"
            :confirm-text="t('prism.dirLinkRemove')"
            icon="mdi-delete-alert-outline"
            danger
            :loading="removeDirBusy"
            :error="removeDirError"
            @confirm="confirmRemoveDirLink"
        />

        <!-- 文件级同步：文件选择 -->
        <FileSelectDialog
            v-model="fileSelectOpen"
            :project="project"
            :dir="fileSelectDir"
            :files="fileSelectFiles"
            @changed="refreshDirLinks"
        />
    </v-dialog>
</template>

<style scoped>
.results-list,
.dir-list {
    border: 1px solid var(--pg-border);
    border-radius: 12px;
}
.consequence-list {
    padding-left: 18px;
    margin: 0;
    color: rgba(var(--v-theme-on-surface), 0.75);
}
.consequence-list li {
    margin-bottom: 3px;
    line-height: 1.5;
}
</style>
