<script lang="ts">
// 复用方视图模型：后端确认要求投影（code 渲染为文案由调用方预处理）
export interface ConfirmRequirementVM {
    label: string
    count: number
}

export default {}
</script>

<script setup lang="ts">
// 危险操作确认弹窗（UX 原型 §8 confirmDialog 的 danger 变体 + §4 .danger-strip）：
// 红色左边条 + 四要素正文——动作、对象、后果、可逆性（05 文档 A2 原则）。
// 重绑定确认（#109）、端点移除与回滚确认（#110）共用；后果行沿原型 pf-row 形态
// （警示图标 + 逐条文案）。组件只管呈现与确认事件，动作语义与文案由调用方组装。
// 票 #111 评审收敛：回滚确认（原独立 RestoreConfirmDialog）并入本组件——四要素行
// 按需渲染（只有后果行的调用方省略 action/target/reversibility）、标题副题
// （description）、页脚前附加内容（#extra 插槽）与知情勾选门（ackLabel，勾选后
// 确认按钮才可用，每次打开重置）均为可选。
import { computed, ref, watch } from 'vue'
import { TriangleAlertIcon } from '@lucide/vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog'

const props = withDefaults(
    defineProps<{
        /** 弹窗标题（如「确认重新绑定？」；回滚确认按确切度区分） */
        title: string
        /** 标题下副题（回滚确认的目标说明句，已含目标提交 id）；缺省不渲染 */
        description?: string
        /** 要素一 · 动作：做什么（一句话）；缺省不渲染该行 */
        action?: string
        /** 要素二 · 对象：作用在哪（名称 / 路径）；缺省不渲染该行 */
        target?: string
        /** 要素三 · 后果：逐条列出（警示行） */
        consequences?: string[]
        /** 要素四 · 可逆性：如何回退；缺省不渲染该行 */
        reversibility?: string
        /** 确认按钮文案（如「确认重新绑定」） */
        confirmLabel: string
        /** 确认进行中：按钮禁用防重复提交 */
        confirming?: boolean
        /** 知情勾选门文案（req.restore_acknowledge）：提供即在页脚前渲染勾选行，
            勾选后确认按钮才可用，每次打开重置（回滚确认，#110） */
        ackLabel?: string
    }>(),
    {
        description: '',
        action: '',
        target: '',
        consequences: () => [],
        reversibility: '',
        confirming: false,
        ackLabel: '',
    },
)

const emit = defineEmits<{ confirm: [] }>()

// 具名 open 模型：调用方以 v-model:open 控制（与内层 reka Dialog 同名透传）
const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()

// 知情勾选：每次打开重置（防上次会话的勾选残留直接放行）
const ack = ref(false)
watch(open, v => {
    if (v) ack.value = false
})

// 确认按钮可用性：确认进行中禁用；带知情勾选门时未勾选禁用
const confirmDisabled = computed(() => props.confirming || (props.ackLabel !== '' && !ack.value))

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
                <div class="min-w-0">
                    <DialogTitle class="text-base">{{ title }}</DialogTitle>
                    <DialogDescription v-if="description" class="text-muted-foreground text-xs">
                        {{ description }}
                    </DialogDescription>
                    <DialogDescription v-else class="sr-only">{{ title }}</DialogDescription>
                </div>
            </DialogHeader>

            <div class="flex flex-col gap-3 px-5 py-4 text-sm">
                <div v-if="action" class="flex items-start justify-between gap-3">
                    <span class="text-muted-foreground shrink-0 text-xs">{{ t('confirm.action') }}</span>
                    <span class="text-right font-medium">{{ action }}</span>
                </div>
                <div v-if="target" class="flex items-start justify-between gap-3">
                    <span class="text-muted-foreground shrink-0 text-xs">{{ t('confirm.target') }}</span>
                    <span class="text-right font-medium break-all">{{ target }}</span>
                </div>
                <div v-if="consequences.length" class="flex flex-col gap-0.5">
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
                <div v-if="reversibility" class="flex items-start justify-between gap-3">
                    <span class="text-muted-foreground shrink-0 text-xs">{{ t('confirm.reversibility') }}</span>
                    <span class="text-right">{{ reversibility }}</span>
                </div>

                <!-- 页脚前附加内容（回滚确认：partial 后果提示 + 后端确认要求投影） -->
                <slot name="extra" />

                <!-- 知情勾选门（可选）：确认按钮在勾选前禁用 -->
                <label v-if="ackLabel" class="mt-1 flex cursor-pointer items-start gap-2 text-sm">
                    <Checkbox v-model="ack" class="mt-0.5" />
                    <span>{{ ackLabel }}</span>
                </label>
            </div>

            <DialogFooter class="border-t px-5 py-3">
                <Button variant="ghost" size="sm" :disabled="confirming" @click="cancel">
                    {{ t('confirm.cancel') }}
                </Button>
                <Button variant="destructive" size="sm" :disabled="confirmDisabled" @click="emit('confirm')">
                    {{ confirmLabel }}
                </Button>
            </DialogFooter>
        </DialogContent>
    </Dialog>
</template>
