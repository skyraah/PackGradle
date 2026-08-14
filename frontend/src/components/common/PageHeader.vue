<script setup lang="ts">
// 页面统一头部：标题 + 副标题 + 右侧操作区
defineProps<{
    title: string
    subtitle?: string
}>()
</script>

<template>
    <div class="d-flex align-center flex-wrap ga-3 mb-5">
        <div class="flex-grow-1" style="min-width: 220px">
            <h1 class="text-h6 font-weight-bold mb-1">{{ title }}</h1>
            <div v-if="subtitle" class="text-body-2 text-medium-emphasis">{{ subtitle }}</div>
        </div>
        <div v-if="$slots.actions" class="d-flex align-center ga-2">
            <slot name="actions" />
        </div>
    </div>
</template>
