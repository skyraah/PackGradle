<script setup lang="ts">
// 危险操作确认弹窗（UX 原型 §8 confirmDialog 的 danger 变体 + §4 .danger-strip）：
// 红色左边条 + 四要素正文——动作、对象、后果、可逆性（05 文档 A2 原则）。
// 重绑定确认（#109）与回滚确认（#110）共用；后果行沿原型 pf-row 形态（警示图标 +
// 逐条文案）。组件只管呈现与确认事件，动作语义与文案由调用方组装。
import { TriangleAlertIcon } from '@lucide/vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog'

const props = defineProps<{
    /** 弹窗标题（如「确认重新绑定？」） */
    title: string
    /** 要素一 · 动作：做什么（一句话） */
    action: string
    /** 要素二 · 对象：作用在哪（名称 / 路径） */
    target: string
    /** 要素三 · 后果：逐条列出（警示行） */
    consequences: string[]
    /** 要素四 · 可逆性：如何回退 */
    reversibility: string
    /** 确认按钮文案（如「确认重新绑定」） */
    confirmLabel: string
    /** 确认进行中：按钮禁用防重复提交 */
    confirming?: boolean
}>()

const emit = defineEmits<{ confirm: [] }>()

// 具名 open 模型：调用方以 v-model:open 控制（与内层 reka Dialog 同名透传）
const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()

function cancel() {
    if (props.confirming) return
    open.value = false
}
</script>

<template>
    <Dialog v-model:open="open">
        <DialogContent class="border-l-destructive max-w-lg gap-0 overflow-hidden border-l-[3px] p-0">
            <DialogHeader class="flex-row items-center gap-2 border-b px-5 py-3.5">
                <TriangleAlertIcon class="text-destructive size-4 shrink-0" />
                <DialogTitle class="text-base">{{ title }}</DialogTitle>
                <DialogDescription class="sr-only">{{ title }}</DialogDescription>
            </DialogHeader>

            <div class="flex flex-col gap-3 px-5 py-4 text-sm">
                <div class="flex items-start justify-between gap-3">
                    <span class="text-muted-foreground shrink-0 text-xs">{{ t('confirm.action') }}</span>
                    <span class="text-right font-medium">{{ action }}</span>
                </div>
                <div class="flex items-start justify-between gap-3">
                    <span class="text-muted-foreground shrink-0 text-xs">{{ t('confirm.target') }}</span>
                    <span class="text-right font-medium break-all">{{ target }}</span>
                </div>
                <div class="flex flex-col gap-0.5">
                    <span class="text-muted-foreground text-xs">{{ t('confirm.consequences') }}</span>
                    <div
                        v-for="c in consequences"
                        :key="c"
                        class="flex items-start gap-2 py-1"
                    >
                        <TriangleAlertIcon class="text-warning mt-0.5 size-3.5 shrink-0" />
                        <span>{{ c }}</span>
                    </div>
                </div>
                <div class="flex items-start justify-between gap-3">
                    <span class="text-muted-foreground shrink-0 text-xs">{{ t('confirm.reversibility') }}</span>
                    <span class="text-right">{{ reversibility }}</span>
                </div>
            </div>

            <DialogFooter class="border-t px-5 py-3">
                <Button variant="ghost" size="sm" :disabled="confirming" @click="cancel">
                    {{ t('confirm.cancel') }}
                </Button>
                <Button variant="destructive" size="sm" :disabled="confirming" @click="emit('confirm')">
                    {{ confirmLabel }}
                </Button>
            </DialogFooter>
        </DialogContent>
    </Dialog>
</template>
