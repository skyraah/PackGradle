<script lang="ts">
// 复用方视图模型：后端确认要求投影（code 渲染为文案由调用方预处理）
export interface ConfirmRequirementVM {
    label: string
    count: number
}

export default {}
</script>

<script setup lang="ts">
// 回滚四要素确认对话框（UX 原型 P3 H-04 confirmDialog，票 #110）：
// 标题按确切度区分（原样/部分），四条后果逐条可见——①删除损失面（条数 + 警示
// 行计数）②CF 重取失败语义（整场退出可重试不进崩溃恢复）③回滚永远需人工确认
// ④成功产生新回滚记录、历史不改写；allow_partial 决议附 partial 后果提示。
// 知情勾选是确认按钮的门（req.restore_acknowledge，后端 confirmation_requirements
// 投影同屏）；确认经 emit('confirm') 交调用方执行（resolve+confirm 链）。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { TriangleAlert } from '@lucide/vue'
import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'

const props = withDefaults(
    defineProps<{
        /** true = 部分回滚（allow_partial），标题与后果提示随之切换 */
        partial?: boolean
        /** 目标说明句（已含目标提交 id） */
        targetLine?: string
        /** 四要素①：将删除的行数 */
        deleteCount?: number
        /** 四要素①：其中含不可找回/不留存警示的行数 */
        lossWarnCount?: number
        /** partial 后果提示的未恢复项计数 */
        partialRemain?: number
        /** 后端确认要求投影（restore_acknowledge 恒在） */
        requirements?: ConfirmRequirementVM[]
        /** 确认请求执行中（按钮禁用防重入） */
        busy?: boolean
    }>(),
    {
        partial: false,
        targetLine: '',
        deleteCount: 0,
        lossWarnCount: 0,
        partialRemain: 0,
        requirements: () => [],
        busy: false,
    },
)

const emit = defineEmits<{
    confirm: []
}>()

const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()

// 知情勾选：每次打开重置（防上次会话的勾选残留直接放行）
const ack = ref(false)
watch(open, v => {
    if (v) ack.value = false
})

const title = computed(() => (props.partial ? t('restore.confirmTitlePartial') : t('restore.confirmTitleExact')))

function onConfirm(): void {
    if (!ack.value || props.busy) return
    emit('confirm')
}
</script>

<template>
    <AlertDialog v-model:open="open">
        <AlertDialogContent class="sm:max-w-xl">
            <AlertDialogHeader>
                <AlertDialogTitle>{{ title }}</AlertDialogTitle>
                <AlertDialogDescription v-if="targetLine">{{ targetLine }}</AlertDialogDescription>
            </AlertDialogHeader>

            <div class="flex flex-col gap-2 text-sm">
                <!-- 四要素（H-04 确认框，逐条可见） -->
                <div class="rounded-md border p-2">
                    <div class="flex items-start gap-2">
                        <TriangleAlert class="text-warning mt-0.5 size-3.5 flex-none" aria-hidden="true" />
                        <span>① {{ deleteCount > 0 ? t('restore.confirm.deleteLoss', [deleteCount, lossWarnCount]) : t('restore.confirm.noDelete') }}</span>
                    </div>
                </div>
                <div class="rounded-md border p-2">
                    <div>② {{ t('restore.confirm.cfFailure') }}</div>
                </div>
                <div class="rounded-md border p-2">
                    <div>③ {{ t('restore.confirm.manualOnly') }}</div>
                </div>
                <div class="rounded-md border p-2">
                    <div>④ {{ t('restore.confirm.newRecord') }}</div>
                </div>
                <!-- partial 后果提示（allow_partial 决议时） -->
                <div v-if="partial" class="text-muted-foreground text-xs">
                    {{ t('restore.confirm.partialNote', [partialRemain]) }}
                </div>
                <!-- 后端确认要求投影（恒非空，restore_acknowledge 恒在） -->
                <div class="text-muted-foreground text-xs">{{ t('restore.confirm.reqTitle') }}</div>
                <div class="flex flex-col gap-1">
                    <div v-for="req in requirements" :key="req.label" class="flex items-center justify-between gap-2">
                        <span class="text-xs">{{ req.label }}</span>
                        <Badge variant="outline" class="text-muted-foreground">{{ t('plans.resourceCount', [req.count]) }}</Badge>
                    </div>
                </div>
                <!-- 知情勾选：确认按钮门 -->
                <label class="mt-1 flex cursor-pointer items-start gap-2 text-sm">
                    <input v-model="ack" type="checkbox" class="accent-current" />
                    <span>{{ t('req.restore_acknowledge') }}</span>
                </label>
            </div>

            <AlertDialogFooter>
                <AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel>
                <AlertDialogAction
                    :disabled="!ack || busy"
                    :class="busy ? 'pointer-events-none opacity-50' : ''"
                    @click="onConfirm"
                >
                    {{ t('restore.confirmSubmit') }}
                </AlertDialogAction>
            </AlertDialogFooter>
        </AlertDialogContent>
    </AlertDialog>
</template>
