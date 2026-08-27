<script setup lang="ts">
// 横向步骤条：工作台快速开始清单用。
// 完成步骤亮绿打勾，当前步骤高亮描边，点击跳转对应页面。
import { useI18n } from 'vue-i18n'

export interface StepItem {
    key: string
    label: string
    done: boolean
    to: string
}

defineProps<{
    steps: StepItem[]
}>()

const emit = defineEmits<{
    (e: 'go', step: StepItem): void
}>()

const { t } = useI18n()
</script>

<template>
    <div class="page-steps d-flex align-stretch">
        <div
            v-for="(step, i) in steps"
            :key="step.key"
            class="step-item"
            :class="{ 'step-done': step.done, 'step-current': !step.done && (i === 0 || steps[i - 1].done) }"
            role="button"
            tabindex="0"
            @click="emit('go', step)"
            @keyup.enter="emit('go', step)"
        >
            <div class="step-dot">
                <v-icon v-if="step.done" icon="mdi-check" size="16" />
                <span v-else class="step-num">{{ i + 1 }}</span>
            </div>
            <div class="step-label text-caption">{{ step.label }}</div>
            <div v-if="i < steps.length - 1" class="step-line" :class="{ 'line-done': step.done }" />
        </div>
    </div>
</template>

<style scoped>
.page-steps {
    gap: 0;
}
.step-item {
    position: relative;
    display: flex;
    flex: 1 1 0;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 10px 4px 12px;
    cursor: pointer;
    border-radius: 8px;
    transition: background-color 120ms ease;
}
.step-item:hover {
    background: var(--pg-layer-hover);
}
.step-dot {
    display: grid;
    width: 26px;
    height: 26px;
    color: rgb(var(--v-theme-on-surface));
    background: var(--pg-layer);
    border: 1.5px solid var(--pg-border-strong);
    border-radius: 50%;
    place-items: center;
    z-index: 1;
}
.step-num {
    font-size: 12px;
    font-weight: 600;
}
.step-done .step-dot {
    color: rgb(var(--v-theme-on-success));
    background: rgb(var(--v-theme-success));
    border-color: rgb(var(--v-theme-success));
}
.step-current .step-dot {
    border-color: rgb(var(--v-theme-primary));
    box-shadow: 0 0 0 3px var(--pg-nav-active);
}
.step-label {
    text-align: center;
    line-height: 1.3;
}
.step-done .step-label {
    color: rgb(var(--v-theme-success));
}
.step-current .step-label {
    color: rgb(var(--v-theme-primary));
    font-weight: 600;
}
.step-line {
    position: absolute;
    top: 23px;
    left: calc(50% + 16px);
    width: calc(100% - 32px);
    height: 2px;
    background: var(--pg-border-strong);
}
.step-line.line-done {
    background: rgb(var(--v-theme-success));
}
</style>
