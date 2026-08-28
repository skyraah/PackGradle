<script setup lang="ts">
// 页面统一头部：标题 + 副标题 + 右侧操作区
defineProps<{
    title: string
    subtitle?: string
}>()
</script>

<template>
    <header class="page-header mb-5">
        <div class="page-header-copy">
            <h1 class="text-h5 font-weight-bold mb-1">{{ title }}</h1>
            <div v-if="subtitle" class="text-body-2 text-medium-emphasis">{{ subtitle }}</div>
        </div>
        <div v-if="$slots.actions" class="page-header-actions">
            <slot name="actions" />
        </div>
    </header>
</template>

<style scoped>
.page-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
}

.page-header-copy {
    flex: 1 1 auto;
    min-width: 220px;
}

.page-header-actions {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
    gap: 8px;
}

@media (max-width: 700px) {
    .page-header {
        flex-direction: column;
    }

    .page-header-actions {
        width: 100%;
        flex-wrap: wrap;
    }
}
</style>
