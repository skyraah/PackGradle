<script setup lang="ts">
// 通用确认对话框：替代各页面重复的确认弹窗与 Wails 原生 Question（构建版会挂起）
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = withDefaults(
    defineProps<{
        modelValue: boolean
        title: string
        text?: string
        confirmText?: string
        cancelText?: string
        confirmColor?: string
        loading?: boolean
        icon?: string
        iconColor?: string
        persistent?: boolean
    }>(),
    {
        text: '',
        confirmText: '',
        cancelText: '',
        confirmColor: 'primary',
        loading: false,
        icon: 'mdi-help-circle-outline',
        iconColor: 'warning',
        persistent: false,
    },
)

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
    (e: 'confirm'): void
}>()

function close() {
    emit('update:modelValue', false)
}
</script>

<template>
    <v-dialog
        :model-value="modelValue"
        :persistent="props.persistent"
        max-width="480"
        @update:model-value="emit('update:modelValue', $event)"
    >
        <v-card elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon v-if="icon" :icon="icon" :color="iconColor" class="mr-2" />
                {{ title }}
            </v-card-title>
            <v-card-text v-if="text" class="text-body-2">{{ text }}</v-card-text>
            <v-card-text v-else-if="$slots.text" class="text-body-2"><slot name="text" /></v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-spacer />
                <v-btn variant="text" :disabled="loading" @click="close">
                    {{ cancelText || t('common.cancel') }}
                </v-btn>
                <v-btn :color="confirmColor" variant="flat" :loading="loading" @click="emit('confirm')">
                    {{ confirmText || t('common.confirm') }}
                </v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>
