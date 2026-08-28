<script setup lang="ts">
// 首次引导对话框：仅在未检测到全局 config.toml（首次打开）时由 App 弹出。
// 五步步骤条 + 当前步操作直达；完成/跳过后 MarkConfigCreated 落盘 config，之后不再弹出。
// 中途 goStep 不落盘：用户尚未完成任何配置时，下次启动仍会引导。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { EnvService } from '../../api'
import { loadProjects, projects } from '../../stores/projects'
import { useEnv } from '../../stores/env'
import { useInstances } from '../../stores/instances'
import { errText } from '../../utils/errors'
import PageSteps from './PageSteps.vue'
import type { StepItem } from './PageSteps.vue'

const { t } = useI18n()
const router = useRouter()

const open = ref(false)
const ready = ref(false)
const finishing = ref(false)
const finishError = ref('')

const { tools, apiKey, loadTools, loadApiKey } = useEnv()
const { overview, loadOverview } = useInstances()

const steps = computed<StepItem[]>(() => {
    const packwiz = tools.value.find(t => t.name === 'packwiz')?.found ?? false
    const prism = tools.value.find(t => t.name === 'prism-launcher')?.found ?? false
    return [
        { key: 'packwiz', label: t('dashboard.qs.packwiz'), done: packwiz, to: '/settings' },
        { key: 'prism', label: t('dashboard.qs.prism'), done: prism, to: '/settings' },
        { key: 'apiKey', label: t('dashboard.qs.apiKey'), done: !!apiKey.value, to: '/settings' },
        { key: 'project', label: t('dashboard.qs.project'), done: projects.value.length > 0, to: '/projects' },
        { key: 'link', label: t('dashboard.qs.link'), done: (overview.value?.links?.length ?? 0) > 0, to: '/instances' },
    ]
})

const doneCount = computed(() => steps.value.filter(s => s.done).length)
const currentStep = computed(() => steps.value.find(s => !s.done) ?? null)

onMounted(async () => {
    // 已有 config（非首次）→ 不弹；检测失败视为非首次，避免阻塞主界面
    let exists = true
    try {
        exists = await EnvService.ConfigExists()
    } catch {
        /* keep true */
    }
    if (exists) return
    await Promise.allSettled([loadTools(), loadApiKey(), loadProjects(), loadOverview()])
    ready.value = true
    open.value = true
})

async function goStep(step: StepItem) {
    open.value = false
    router.push(step.to)
}

async function finish() {
    if (finishing.value) return
    finishing.value = true
    finishError.value = ''
    try {
        await EnvService.MarkConfigCreated()
        open.value = false
    } catch (e) {
        finishError.value = errText(e)
    } finally {
        finishing.value = false
    }
}
</script>

<template>
    <v-dialog v-model="open" max-width="640" persistent>
        <v-card class="dialog-card" elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-avatar size="34" rounded="md" color="primary" variant="tonal" class="mr-3">
                    <v-icon icon="mdi-rocket-launch-outline" size="19" />
                </v-avatar>
                {{ t('onboarding.title') }}
                <v-spacer />
                <v-chip size="x-small" variant="tonal">{{ doneCount }}/{{ steps.length }}</v-chip>
            </v-card-title>
            <v-card-text>
                <div class="text-body-2 text-medium-emphasis mb-4">{{ t('onboarding.subtitle') }}</div>
                <v-progress-linear
                    :model-value="steps.length ? (doneCount / steps.length) * 100 : 0"
                    color="primary"
                    height="4"
                    rounded
                    class="mb-4"
                />
                <PageSteps v-if="ready" :steps="steps" @go="goStep" />
                <v-progress-linear v-else indeterminate />
                <v-alert v-if="currentStep" type="info" variant="tonal" density="compact" class="mt-4">
                    {{ t('onboarding.currentHint', [currentStep.label]) }}
                </v-alert>
                <v-alert v-if="finishError" type="error" variant="tonal" density="compact" class="mt-3">
                    {{ finishError }}
                </v-alert>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-btn variant="text" :loading="finishing" @click="finish">{{ t('onboarding.skip') }}</v-btn>
                <v-spacer />
                <v-btn v-if="currentStep" variant="tonal" :disabled="finishing" @click="goStep(currentStep)">
                    {{ t('onboarding.goCurrent') }}
                </v-btn>
                <v-btn color="primary" variant="flat" :loading="finishing" :disabled="doneCount < steps.length" @click="finish">
                    {{ t('onboarding.finish') }}
                </v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>
