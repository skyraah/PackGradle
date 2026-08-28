<script setup lang="ts">
// 关联项目对话框：选择 packwiz 项目 + Prism 实例（同名自动匹配），支持程序创建实例。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PrismService } from '../../api'
import type { Instance } from '../../../bindings/packgradle/internal/prism'
import { loadProjects, projects } from '../../stores/projects'
import { loadOverview } from '../../stores/instances'
import { runTask } from '../../stores/taskCenter'
import { showSnackbar } from '../../stores/ui'
import { errText } from '../../utils/errors'

const { t } = useI18n()

const props = defineProps<{
    modelValue: boolean
    instances: Instance[]
}>()

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
    (e: 'changed'): void
}>()

const selProject = ref('')
const selInstance = ref('')
const linking = ref(false)
const creating = ref(false)
const preparing = ref(false)
const linkError = ref('')

const linkableProjects = computed(() => projects.value.filter(p => !p.error))

watch(
    () => props.modelValue,
    async open => {
        if (!open) return
        preparing.value = true
        try {
            await loadProjects(true)
        } catch (e) {
            showSnackbar(errText(e))
            emit('update:modelValue', false)
            preparing.value = false
            return
        }
        preparing.value = false
        if (linkableProjects.value.length === 0) {
            showSnackbar(t('prism.noProjectsHint'))
            emit('update:modelValue', false)
            return
        }
        selProject.value = ''
        selInstance.value = ''
        linkError.value = ''
    },
)

function matchInstance(projectName: string): string {
    const name = projectName.toLowerCase()
    return props.instances.find(i => i.id.toLowerCase() === name || i.name.toLowerCase() === name)?.id ?? ''
}

const matchedInstanceId = computed(() => (selProject.value ? matchInstance(selProject.value) : ''))
const matchHintVisible = computed(() => selProject.value !== '' && matchedInstanceId.value !== '')

async function doLink() {
    const projectName = selProject.value
    const instanceID = selInstance.value
    if (!projectName || !instanceID || linking.value || creating.value) return
    linking.value = true
    linkError.value = ''
    let refreshFailed = false
    try {
        const result = await runTask({
            title: t('tasks.link', [projectName, instanceID]),
            kind: 'link',
            run: async () => {
                await PrismService.LinkProject(projectName, instanceID)
                try {
                    await loadOverview(true)
                } catch (e) {
                    refreshFailed = true
                    showSnackbar(errText(e), 'warning')
                }
                return t('prism.linkCreated', [projectName, instanceID])
            },
            warn: () => refreshFailed,
            onError: message => (linkError.value = message),
        })
        if (result !== null) {
            emit('update:modelValue', false)
            emit('changed')
        }
    } finally {
        linking.value = false
    }
}

async function doCreateInstance() {
    const projectName = selProject.value
    if (!projectName || creating.value || linking.value) return
    creating.value = true
    try {
        const inst = await PrismService.CreateInstance(projectName)
        try {
            await loadOverview(true)
        } catch (e) {
            showSnackbar(errText(e), 'warning')
        }
        showSnackbar(t('prism.instanceCreated', [inst.id]), 'success')
        emit('changed')
        selInstance.value = inst.id
    } catch (e) {
        // 实例已存在/版本缺失等：后端结构化错误直接提示
        showSnackbar(errText(e), 'error')
    } finally {
        creating.value = false
    }
}

function updateOpen(v: boolean) {
    if (!v && (linking.value || creating.value)) return
    emit('update:modelValue', v)
}
</script>

<template>
    <v-dialog
        :model-value="modelValue"
        :persistent="linking || creating"
        max-width="560"
        @update:model-value="updateOpen"
    >
        <v-card class="dialog-card" elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-link-variant" color="primary" class="mr-2" />
                {{ t('prism.linkDialogTitle') }}
            </v-card-title>
            <v-card-text>
                <v-progress-linear v-if="preparing" indeterminate class="mb-3" />
                <v-select
                    v-model="selProject"
                    :items="linkableProjects"
                    item-title="name"
                    item-value="name"
                    :label="t('prism.linkProject')"
                    density="comfortable"
                    hide-details="auto"
                    class="mb-4"
                    :disabled="linking || creating"
                    @update:model-value="selInstance = matchedInstanceId"
                />
                <v-alert v-if="matchHintVisible" type="info" variant="tonal" density="compact" class="mb-3">
                    {{ t('prism.matchHint') }}
                </v-alert>
                <v-select
                    v-model="selInstance"
                    :items="instances"
                    item-title="name"
                    item-value="id"
                    :label="t('prism.linkInstance')"
                    density="comfortable"
                    hide-details="auto"
                    :disabled="linking || creating"
                />
                <div v-if="instances.length === 0" class="text-caption text-medium-emphasis mt-3">
                    {{ t('prism.createInstanceHint') }}
                </div>
                <v-alert v-if="linkError" type="error" variant="tonal" density="compact" class="mt-3">
                    {{ linkError }}
                </v-alert>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-btn v-if="selProject" variant="tonal" :loading="creating" :disabled="linking" @click="doCreateInstance">
                    {{ t('prism.createInstanceBtn') }}
                </v-btn>
                <v-spacer />
                <v-btn variant="text" :disabled="linking || creating" @click="updateOpen(false)">{{ t('prism.linkCancel') }}</v-btn>
                <v-btn
                    color="primary"
                    variant="flat"
                    :loading="linking"
                    :disabled="creating || !selProject || !selInstance"
                    @click="doLink"
                >
                    {{ t('prism.linkSubmit') }}
                </v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>
