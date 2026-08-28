<script setup lang="ts">
// 空状态占位：图标 + 标题 + 说明 + 可选操作
defineProps<{
    icon: string
    title: string
    text?: string
}>()
</script>

<template>
    <div class="d-flex flex-column align-center justify-center text-center py-10 px-4">
        <v-icon :icon="icon" size="56" class="text-medium-emphasis mb-3" />
        <div class="text-subtitle-1 font-weight-medium mb-1">{{ title }}</div>
        <div v-if="text" class="text-body-2 text-medium-emphasis mb-4" style="max-width: 420px">{{ text }}</div>
        <div v-if="$slots.actions" class="d-flex ga-2 flex-wrap justify-center">
            <slot name="actions" />
        </div>
    </div>
</template>
