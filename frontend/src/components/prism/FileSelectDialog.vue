<script setup lang="ts">
// 文件级同步的文件选择对话框：从实例侧读取文件列表 + 当前勾选。
// 保存 = 勾选文件移动到项目目录并硬链接回实例侧。
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PrismService } from '../../api'
import { showSnackbar } from '../../stores/ui'
import { errText } from '../../utils/errors'

const { t } = useI18n()

const props = defineProps<{
    modelValue: boolean
    project: string
    dir: string
    /** 当前已纳入同步的文件清单（初始勾选） */
    files: string[]
}>()

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
    (e: 'changed'): void
}>()

const loading = ref(false)
const saving = ref(false)
const allFiles = ref<string[]>([])
const selectedFiles = ref<string[]>([])
const targetProject = ref('')
const targetDir = ref('')

watch(
    () => props.modelValue,
    async open => {
        if (!open) return
        targetProject.value = props.project
        targetDir.value = props.dir
        loading.value = true
        try {
            allFiles.value = (await PrismService.ListInstanceDirFiles(targetProject.value, targetDir.value)) ?? []
            selectedFiles.value = [...props.files]
        } catch (e) {
            showSnackbar(errText(e))
            emit('update:modelValue', false)
        } finally {
            loading.value = false
        }
    },
)

function toggleFile(f: string, checked: boolean | null) {
    if (checked) {
        if (!selectedFiles.value.includes(f)) selectedFiles.value.push(f)
    } else {
        selectedFiles.value = selectedFiles.value.filter(x => x !== f)
    }
}

async function save() {
    if (saving.value) return
    saving.value = true
    try {
        const results = (await PrismService.SelectInstanceFiles(targetProject.value, targetDir.value, selectedFiles.value)) ?? []
        const ok = results.filter(r => r.status === 'linked').length
        const skipped = results.filter(r => r.status === 'skipped').length
        showSnackbar(t('prism.dirLinkSelectDone', [ok, skipped]), 'success')
        emit('changed')
        emit('update:modelValue', false)
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        saving.value = false
    }
}

function updateOpen(v: boolean) {
    if (!v && saving.value) return
    emit('update:modelValue', v)
}
</script>

<template>
    <v-dialog :model-value="modelValue" :persistent="saving" max-width="560" @update:model-value="updateOpen">
        <v-card class="dialog-card" elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-file-check-outline" color="primary" class="mr-2" />
                {{ t('prism.dirLinkFilesTitle') }} · {{ targetDir }}
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-3">
                    {{ t('prism.dirLinkFilesHint', [targetDir]) }}
                </div>
                <div class="d-flex align-center mb-2 ga-2">
                    <v-btn size="small" variant="tonal" @click="selectedFiles = [...allFiles]">
                        {{ t('prism.selectAll') }}
                    </v-btn>
                    <v-btn size="small" variant="text" @click="selectedFiles = []">
                        {{ t('prism.clearAll') }}
                    </v-btn>
                    <span class="text-caption text-medium-emphasis">
                        {{ selectedFiles.length }}/{{ allFiles.length }}
                    </span>
                </div>
                <v-progress-linear v-if="loading" indeterminate class="mb-2" />
                <v-list v-else-if="allFiles.length > 0" density="compact" max-height="320" class="overflow-y-auto file-list">
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
                <div v-else-if="!loading" class="text-caption text-medium-emphasis">
                    {{ t('prism.dirLinkNoCandidate') }}
                </div>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-spacer />
                <v-btn variant="text" :disabled="saving" @click="updateOpen(false)">{{ t('prism.linkCancel') }}</v-btn>
                <v-btn color="primary" variant="flat" :loading="saving" @click="save">{{ t('common.save') }}</v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>

<style scoped>
.file-list {
    border: 1px solid var(--pg-border);
    border-radius: 12px;
}
</style>
