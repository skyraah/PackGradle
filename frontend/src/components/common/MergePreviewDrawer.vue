<script setup lang="ts">
// 合并预览抽屉（契约 07 §3.4/§6，票 #94）：计划页 merged_clean 行「查看合并结果」
// 入口。后端只供 content（合并后全文）/base_content（基线全文）两段——行级
// 绿红黄标注（utils/mergePreview 对两段全文行级计算）与语法高亮（按扩展名
// 分派，未识别退纯文本）都是本抽屉的渲染层职责。
// 「所见即所写」：预览 content 与暂存期重算、最终落盘字节同一确定性逻辑（后端
// GetMergedPreview 直接调 core/merge.Texts）。stale/expired 计划同样可预览（只读）。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MergedPreviewDTO } from '../../../bindings/packgradle/internal/transport/models'
import { SyncService } from '../../api'
import { errText } from '../../utils/errors'
import {
    diffRows,
    highlightLine,
    langOfPath,
    type DiffRow,
    type DiffRowType,
    type HighlightLang,
} from '../../utils/mergePreview'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
    Sheet,
    SheetContent,
    SheetDescription,
    SheetHeader,
    SheetTitle,
} from '@/components/ui/sheet'

const { t } = useI18n()

const props = defineProps<{
    planId: string
    resourceId: string
    /** 展示用相对路径：预览加载前表头即可显示 */
    relativePath?: string
    /** 计划已停用/过期：预览只读、仅供回看的说明横幅 */
    readonlyHint?: boolean
}>()

const open = defineModel<boolean>({ default: false })

const loading = ref(false)
const errorMsg = ref('')
const preview = ref<MergedPreviewDTO | null>(null)

async function load(): Promise<void> {
    if (loading.value) return
    loading.value = true
    errorMsg.value = ''
    try {
        preview.value = await SyncService.GetMergedPreview(props.planId, props.resourceId)
    } catch (e) {
        preview.value = null
        errorMsg.value = errText(e)
    } finally {
        loading.value = false
    }
}

// 打开（或打开中切换行）即拉取预览：实时计算不落库，每次打开现算
watch(
    () => [open.value, props.planId, props.resourceId] as const,
    ([nowOpen]) => {
        if (nowOpen) void load()
    },
    { immediate: true },
)

// 行级标注与高亮语言只随预览数据重算（渲染层纯计算，无副作用）
const rows = computed<DiffRow[]>(() =>
    preview.value ? diffRows(preview.value.base_content, preview.value.content) : [],
)
const lang = computed<HighlightLang>(() =>
    langOfPath(preview.value?.relative_path ?? props.relativePath ?? ''),
)
// 基线行切分与后端口径一致：结尾换行符产生的尾空元素不是一行
const baseLines = computed<string[]>(() => {
    if (!preview.value) return []
    const lines = preview.value.base_content.split('\n')
    if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
    return lines
})
const unchanged = computed(() => rows.value.length > 0 && rows.value.every(r => r.type === 'same'))

const rowTone: Record<DiffRowType, string> = {
    added: 'bg-emerald-500/10',
    removed: 'bg-red-500/10',
    modified: 'bg-amber-500/10',
    same: '',
}

const rowMark: Record<DiffRowType, string> = {
    added: '+',
    removed: '−',
    modified: '~',
    same: ' ',
}
</script>

<template>
    <Sheet v-model:open="open">
        <SheetContent side="right" class="flex w-full flex-col gap-0 sm:max-w-[720px]">
            <SheetHeader class="border-b pb-3">
                <SheetTitle class="flex flex-wrap items-center gap-2">
                    {{ t('plans.mergePreview.title') }}
                    <Badge v-if="preview || relativePath" variant="outline" class="font-mono text-xs">
                        {{ preview?.relative_path ?? relativePath }}
                    </Badge>
                </SheetTitle>
                <SheetDescription class="text-xs">{{ t('plans.mergePreview.subtitle') }}</SheetDescription>
            </SheetHeader>

            <div class="min-h-0 flex-1 overflow-y-auto p-3">
                <!-- 停用/过期计划：预览仍可看（只读），说明横幅（tint 警告语言与页面横幅一致） -->
                <div v-if="readonlyHint" class="bg-tint-warning text-warning mb-3 rounded-md px-3 py-2 text-xs">
                    {{ t('plans.mergePreview.staleHint') }}
                </div>

                <!-- 加载中 -->
                <div v-if="loading" class="flex flex-col gap-2 py-8">
                    <div v-for="i in 6" :key="i" class="h-4 w-full animate-pulse rounded bg-muted"></div>
                </div>

                <!-- 失败：错误码经 errText 翻译（err.merge.not_mergeable 等），可重试 -->
                <div v-else-if="errorMsg" class="flex flex-col items-start gap-3 py-8">
                    <span class="text-destructive text-sm">{{ t('plans.mergePreview.loadFailed') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="loading" @click="load">
                        {{ t('plans.retry') }}
                    </Button>
                </div>

                <template v-else-if="preview">
                    <!-- 合并后全文：行级标注 + 语法高亮（渲染层职责） -->
                    <div class="mb-1 flex flex-wrap items-center gap-2">
                        <span class="text-sm font-medium">{{ t('plans.mergePreview.merged') }}</span>
                        <Badge variant="outline" class="bg-emerald-500/10 text-xs">{{ t('plans.mergePreview.legendAdd') }}</Badge>
                        <Badge variant="outline" class="bg-red-500/10 text-xs">{{ t('plans.mergePreview.legendRemove') }}</Badge>
                        <Badge variant="outline" class="bg-amber-500/10 text-xs">{{ t('plans.mergePreview.legendModify') }}</Badge>
                    </div>
                    <div class="bg-card overflow-x-auto rounded-md border">
                        <div
                            v-for="(row, i) in rows"
                            :key="i"
                            class="flex items-start gap-2 px-2 font-mono text-xs leading-5 whitespace-pre"
                            :class="rowTone[row.type]"
                            :title="row.replaced ? t('plans.mergePreview.base') + ': ' + row.replaced : undefined"
                        >
                            <span class="text-muted-foreground w-3 shrink-0 select-none text-center">{{ rowMark[row.type] }}</span>
                            <span class="min-w-0">
                                <template v-for="(seg, k) in highlightLine(row.text, lang)" :key="k">
                                    <span v-if="seg.cls" :class="seg.cls">{{ seg.text }}</span>
                                    <template v-else>{{ seg.text }}</template>
                                </template>
                            </span>
                        </div>
                        <div v-if="unchanged" class="text-muted-foreground px-2 py-1.5 text-xs">
                            {{ t('plans.mergePreview.unchanged') }}
                        </div>
                    </div>

                    <!-- 基线全文：增删改标注的比对锚点（只读参照，同语法高亮） -->
                    <div class="mt-4 mb-1 flex items-center gap-2">
                        <span class="text-sm font-medium">{{ t('plans.mergePreview.base') }}</span>
                    </div>
                    <div class="bg-card overflow-x-auto rounded-md border">
                        <div v-for="(line, i) in baseLines" :key="i" class="px-2 font-mono text-xs leading-5 whitespace-pre">
                            <template v-for="(seg, k) in highlightLine(line, lang)" :key="k">
                                <span v-if="seg.cls" :class="seg.cls">{{ seg.text }}</span>
                                <template v-else>{{ seg.text }}</template>
                            </template>
                        </div>
                    </div>
                </template>
            </div>
        </SheetContent>
    </Sheet>
</template>
