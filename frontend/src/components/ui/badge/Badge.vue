<script setup lang="ts">
import type { PrimitiveProps } from "reka-ui"
import type { HTMLAttributes } from "vue"
import type { BadgeVariants } from "."
import { reactiveOmit } from "@vueuse/core"
import { Primitive } from "reka-ui"
import { cn } from "@/lib/utils"
import { badgeVariants } from "."

const props = defineProps<PrimitiveProps & {
  variant?: BadgeVariants["variant"]
  /** 去掉 st-* 变体的前置圆点（原型 .st.plain::before{display:none}；对 shadcn 四变体无作用） */
  plain?: boolean
  /** st-run 变体前置圆点呼吸动画（原型 .pulse / @keyframes pp；对其他变体无作用） */
  pulse?: boolean
  class?: HTMLAttributes["class"]
}>()

const delegatedProps = reactiveOmit(props, "class", "plain", "pulse")
</script>

<template>
  <Primitive
    data-slot="badge"
    :class="cn(badgeVariants({ variant }), props.plain && 'before:hidden', props.pulse && 'before:animate-pp', props.class)"
    v-bind="delegatedProps"
  >
    <slot />
  </Primitive>
</template>
