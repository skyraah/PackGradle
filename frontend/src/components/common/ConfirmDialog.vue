<script setup lang="ts">
// 通用确认对话框：替代各页面重复的确认弹窗与 Wails 原生 Question（构建版会挂起）。
// 支持 danger 视觉变体与 consequences 后果列表（四要素文案：动作+对象+后果+可逆性）。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = withDefaults(
    defineProps<{
        modelValue: boolean
        title: string
        text?: string
        /** 后果列表：逐条展示在文本下方（替代塞进一段长文） */
        consequences?: string[]
        confirmText?: string
        cancelText?: string
        confirmColor?: string
        loading?: boolean
        error?: string
        icon?: string
        iconColor?: string
        persistent?: boolean
        /** 危险操作视觉：左侧红条 + 图标底色 */
        danger?: boolean
    }>(),
    {
        text: '',
        consequences: () => [],
        confirmText: '',
        cancelText: '',
        confirmColor: 'primary',
        loading: false,
        error: '',
        icon: 'mdi-help-circle-outline',
        iconColor: 'warning',
        persistent: false,
        danger: false,
    },
)

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
    (e: 'confirm'): void
}>()

const effectiveIconColor = computed(() => (props.danger ? 'error' : props.iconColor))
const effectiveConfirmColor = computed(() => (props.danger ? 'error' : props.confirmColor))

function close() {
    if (props.loading) return
    emit('update:modelValue', false)
}

function updateOpen(v: boolean) {
    if (!v && props.loading) return
    emit('update:modelValue', v)
}
</script>

<template>
    <v-dialog
        :model-value="modelValue"
        :persistent="props.persistent || loading"
        max-width="480"
        @update:model-value="updateOpen"
    >
        <v-card class="dialog-card" :class="{ 'confirm-danger': danger }" elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-avatar v-if="icon" size="34" rounded="md" :color="effectiveIconColor" variant="tonal" class="mr-3">
                    <v-icon :icon="icon" size="19" />
                </v-avatar>
                {{ title }}
            </v-card-title>
            <v-card-text v-if="text || consequences.length > 0 || $slots.text" class="text-body-2">
                <slot name="text">{{ text }}</slot>
                <ul v-if="consequences.length > 0" class="consequence-list mt-2">
                    <li v-for="(c, i) in consequences" :key="i">{{ c }}</li>
                </ul>
            </v-card-text>
            <v-alert v-if="error" type="error" variant="tonal" density="compact" class="mx-5 mb-3">
                {{ error }}
            </v-alert>
            <v-card-actions class="px-5 pb-4">
                <v-spacer />
                <v-btn variant="text" :disabled="loading" @click="close">
                    {{ cancelText || t('common.cancel') }}
                </v-btn>
                <v-btn :color="effectiveConfirmColor" variant="flat" :loading="loading" @click="emit('confirm')">
                    {{ confirmText || t('common.confirm') }}
                </v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>

<style scoped>
.confirm-danger {
    border-left: 4px solid rgb(var(--v-theme-error)) !important;
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
