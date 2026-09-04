<script setup lang="ts">
// 回滚确认对话框（UX 原型 P3 H-04 confirmDialog，票 #110）：危险确认弹窗的回滚
// 形态薄壳（票 #111 评审收敛，共享组件见 DangerConfirmDialog）。标题按确切度区分
// （原样/部分），后果四条逐条可见——删除损失面（条数 + 警示行计数）、CF 重取失败
// 语义（整场退出可重试不进崩溃恢复）、回滚永远需人工确认、成功产生新回滚记录且
// 历史不改写；allow_partial 决议附 partial 后果提示，后端确认要求投影与知情勾选门
// （req.restore_acknowledge）经 #extra / ackLabel 承接；确认经 emit('confirm') 交
// 调用方执行（resolve+confirm 链）。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import DangerConfirmDialog from './DangerConfirmDialog.vue'
import type { ConfirmRequirementVM } from './DangerConfirmDialog.vue'
import { Badge } from '@/components/ui/badge'

const props = withDefaults(
    defineProps<{
        /** true = 部分回滚（allow_partial），标题与后果提示随之切换 */
        partial?: boolean
        /** 目标说明句（已含目标提交 id） */
        targetLine?: string
        /** 后果一：将删除的行数 */
        deleteCount?: number
        /** 后果一：其中含不可找回/不留存警示的行数 */
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

const title = computed(() => (props.partial ? t('restore.confirmTitlePartial') : t('restore.confirmTitleExact')))

// 后果四条（H-04 确认框，逐条可见；次序即语义）
const consequenceLines = computed<string[]>(() => [
    props.deleteCount > 0
        ? t('restore.confirm.deleteLoss', [props.deleteCount, props.lossWarnCount])
        : t('restore.confirm.noDelete'),
    t('restore.confirm.cfFailure'),
    t('restore.confirm.manualOnly'),
    t('restore.confirm.newRecord'),
])
</script>

<template>
    <DangerConfirmDialog
        v-model:open="open"
        :title="title"
        :description="targetLine"
        :consequences="consequenceLines"
        :confirm-label="t('restore.confirmSubmit')"
        :confirming="busy"
        :ack-label="t('req.restore_acknowledge')"
        @confirm="emit('confirm')"
    >
        <template #extra>
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
        </template>
    </DangerConfirmDialog>
</template>
