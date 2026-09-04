<script setup lang="ts">
// 候选卡（UX 原型 workspace-ux-prototype.html §7 .cand）：radio 圆点 + 名称 +
// 副行（mono 可选）+ 徽章/chip 尾缀。新建工作区向导（#106）与重绑定页（#108）
// 共用的候选选择形态；组件只管呈现与选择态，数据与选中语义由调用方组装。
// - selected：主色描边 + 同色 1px 外圈（.cand.sel），radio 填充主色
// - disabled + disabledTitle：重复 pair 等不可选组合，整卡 55% 透明禁点
//   （.cand.dis），title 保留悬停原因说明
// - chip：尾部中性 chip 文案（「N 个工作区」/「已建立工作区」）
// - #badge 插槽：健康徽章位（Badge st-* 变体）；#default 追加尾部内容
import { computed } from 'vue'
import { cn } from '@/lib/utils'

const props = defineProps<{
    name: string
    /** 副行文案（路径 / 元信息）；空则不渲染 */
    sub?: string
    /** 副行使用 mono 字形（路径场景，原型 .rowsub.mono） */
    mono?: boolean
    selected?: boolean
    disabled?: boolean
    /** 禁用态整卡悬停说明（如重复 pair 原因） */
    disabledTitle?: string
    /** 尾部 chip 文案 */
    chip?: string
}>()

const emit = defineEmits<{ select: [] }>()

const rootClass = computed(() =>
    cn(
        'focus-visible:ring-ring/50 flex w-full items-center gap-3 rounded-lg border bg-card px-3.5 py-3 text-left text-sm outline-none transition-colors focus-visible:border-ring focus-visible:ring-3 disabled:cursor-not-allowed disabled:opacity-55',
        props.disabled
            ? 'cursor-not-allowed'
            : 'hover:bg-surface-2 cursor-pointer',
        props.selected && 'border-primary shadow-[0_0_0_1px_var(--primary)]',
    ),
)

const radioClass = computed(() =>
    cn(
        'size-4 shrink-0 rounded-full border-2 border-faint',
        props.selected && 'border-primary bg-primary shadow-[inset_0_0_0_3px_var(--card)]',
    ),
)
</script>

<template>
    <button
        type="button"
        :class="rootClass"
        :disabled="disabled"
        :title="disabled ? disabledTitle : undefined"
        @click="emit('select')"
    >
        <span :class="radioClass" aria-hidden="true"></span>
        <span class="min-w-0 flex-1">
            <span class="block truncate font-semibold">{{ name }}</span>
            <span
                v-if="sub"
                class="text-faint mt-px block truncate text-[11.5px]"
                :class="mono ? 'font-mono text-xs' : ''"
            >{{ sub }}</span>
            <slot name="sub"></slot>
        </span>
        <slot name="badge"></slot>
        <span
            v-if="chip"
            class="inline-flex h-6 shrink-0 items-center rounded-md bg-surface-2 px-2.5 text-[11.5px] font-semibold text-muted-foreground"
        >{{ chip }}</span>
        <slot></slot>
    </button>
</template>
