<script setup lang="ts">
// 文件级同步的文件选择对话框：从实例侧读取文件列表 + 当前勾选。
// 保存 = 勾选文件移动到项目目录并硬链接回实例侧。
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PrismService } from '../../../bindings/packgradle/internal/service'
import type { LinkResult } from '../../../bindings/packgradle/internal/prism'
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
    /** 保存成功，父级应刷新目录关联列表 */
    (e: 'changed'): void
}>()

const loading = ref(false)
const saving = ref(false)
const allFiles = ref<string[]>([])
const selectedFiles = ref<string[]>([])

watch(
    () => props.modelValue,
    async open => {
        if (!open) return
        loading.value = true
        try {
            allFiles.value = (await PrismService.ListInstanceDirFiles(props.project, props.dir)) ?? []
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
    saving.value = true
    try {
        const results =
            (await PrismService.SelectInstanceFiles(props.project, props.dir, selectedFiles.value)) ?? []
        const ok = results.filter(r => r.status === 'linked').length
        const skipped = results.filter(r => r.status === 'skipped').length
        const failed = results.filter(r => r.status === 'error').length
        if (failed > 0) {
            const detail = (results.find(r => r.status === 'error') as LinkResult | undefined)?.detail
            showSnackbar(t('prism.dirLinkSelectDoneWithErrors', [ok, skipped, failed, detail ? ' ' + detail : '']))
        } else {
            showSnackbar(t('prism.dirLinkSelectDone', [ok, skipped]))
        }
        emit('changed')
        emit('update:modelValue', false)
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        saving.value = false
    }
}
</script>

<template>
    <v-dialog :model-value="modelValue" max-width="560" @update:model-value="emit('update:modelValue', $event)">
        <v-card elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-file-check-outline" color="primary" class="mr-2" />
                {{ t('prism.dirLinkFilesTitle') }} · {{ dir }}
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-3">
                    {{ t('prism.dirLinkFilesHint', [dir]) }}
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
                <v-btn variant="text" @click="emit('update:modelValue', false)">{{ t('prism.linkCancel') }}</v-btn>
                <v-btn color="primary" variant="flat" :loading="saving" @click="save">{{ t('common.save') }}</v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>

<style scoped>
.file-list {
    border: 1px solid rgba(255, 255, 255, 0.08);
    border-radius: 10px;
}
</style>
