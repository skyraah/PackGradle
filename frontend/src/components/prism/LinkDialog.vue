<script setup lang="ts">
// 关联项目对话框：选择 packwiz 项目 + Prism 实例（同名自动匹配），
// 支持基于项目信息程序创建实例。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PrismService } from '../../../bindings/packgradle/internal/service'
import type { Instance } from '../../../bindings/packgradle/internal/prism'
import { loadProjects, projects } from '../../stores/projects'
import { showSnackbar } from '../../stores/ui'
import { errText } from '../../utils/errors'

const { t } = useI18n()

const props = defineProps<{
    modelValue: boolean
    instances: Instance[]
}>()

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
    /** 关联或创建实例成功，父级应刷新 Overview */
    (e: 'changed'): void
}>()

const selProject = ref('')
const selInstance = ref('')
const linking = ref(false)
const creating = ref(false)
const preparing = ref(false)

const linkableProjects = computed(() => projects.value.filter(p => !p.error))

// 打开前强制刷新项目列表（排除解析失败的项目）
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
    },
)

// 选择项目时自动匹配同名实例（不区分大小写）
function matchInstance(projectName: string): string {
    const name = projectName.toLowerCase()
    return props.instances.find(i => i.id.toLowerCase() === name || i.name.toLowerCase() === name)?.id ?? ''
}

const matchedInstanceId = computed(() => (selProject.value ? matchInstance(selProject.value) : ''))
const matchHintVisible = computed(() => selProject.value !== '' && matchedInstanceId.value !== '')

async function doLink() {
    if (!selProject.value || !selInstance.value) return
    linking.value = true
    try {
        await PrismService.LinkProject(selProject.value, selInstance.value)
        showSnackbar(t('prism.linkCreated', [selProject.value, selInstance.value]))
        emit('update:modelValue', false)
        emit('changed')
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        linking.value = false
    }
}

async function doCreateInstance() {
    if (!selProject.value) return
    creating.value = true
    try {
        const inst = await PrismService.CreateInstance(selProject.value)
        showSnackbar(t('prism.instanceCreated', [inst.id]))
        emit('changed') // 父级重扫实例列表
        selInstance.value = inst.id
    } catch (e) {
        showSnackbar(errText(e))
    } finally {
        creating.value = false
    }
}
</script>

<template>
    <v-dialog :model-value="modelValue" max-width="560" @update:model-value="emit('update:modelValue', $event)">
        <v-card elevation="8">
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
                />
                <div v-if="instances.length === 0" class="text-caption text-medium-emphasis mt-3">
                    {{ t('prism.createInstanceHint') }}
                </div>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-btn v-if="selProject" variant="tonal" :loading="creating" @click="doCreateInstance">
                    {{ t('prism.createInstanceBtn') }}
                </v-btn>
                <v-spacer />
                <v-btn variant="text" @click="emit('update:modelValue', false)">{{ t('prism.linkCancel') }}</v-btn>
                <v-btn
                    color="primary"
                    variant="flat"
                    :loading="linking"
                    :disabled="!selProject || !selInstance"
                    @click="doLink"
                >
                    {{ t('prism.linkSubmit') }}
                </v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>
