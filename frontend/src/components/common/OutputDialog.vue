<script setup lang="ts">
// 命令输出查看器：packwiz refresh / update 的 CLI 输出展示
import { useI18n } from 'vue-i18n'
import { showSnackbar } from '../../stores/ui'

const { t } = useI18n()

const props = defineProps<{
    modelValue: boolean
    title: string
    output?: string
}>()

const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
}>()

async function copyOutput() {
    try {
        await navigator.clipboard.writeText(props.output || t('common.noOutput'))
        showSnackbar(t('common.copied'), 'success')
    } catch {
        showSnackbar(t('common.copyFailed'), 'warning')
    }
}
</script>

<template>
    <v-dialog :model-value="modelValue" max-width="680" @update:model-value="emit('update:modelValue', $event)">
        <v-card class="dialog-card" elevation="8">
            <v-card-title class="d-flex align-center pt-5">
                <v-icon icon="mdi-console" color="primary" class="mr-2" />
                {{ title }}
            </v-card-title>
            <v-card-text>
                <pre class="output-pre text-body-2">{{ output || t('common.noOutput') }}</pre>
            </v-card-text>
            <v-card-actions class="px-5 pb-4">
                <v-btn variant="text" prepend-icon="mdi-content-copy" @click="copyOutput">
                    {{ t('common.copyOutput') }}
                </v-btn>
                <v-spacer />
                <v-btn variant="tonal" @click="emit('update:modelValue', false)">{{ t('common.close') }}</v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>

<style scoped>
.output-pre {
    max-height: 360px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
    background: rgb(var(--v-theme-background));
    border: 1px solid rgba(255, 255, 255, 0.08);
    border-radius: 10px;
    padding: 12px;
}
</style>
