<script setup lang="ts">
// /workspaces/:id/history：同步历史（契约 05 §3.5 ListCommits；UX 原型 §7.7）。
// 历史数据为本页查询快照（created_at DESC 游标分页），工作区上下文（关系名/
// features.history_view 门控）读 stores/syncCache 投影，页面不做第二处取数。
// 状态机互斥：loading / error / gate / empty / ready / refreshing（沿 changes 页先例）。
// 失败执行不进入历史（进任务/恢复），故空态即「尚无已提交的同步」。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { CommitSummaryDTO } from '../api'
import { bootstrapped, workspaces } from '../stores/syncCache'
import { errText } from '../utils/errors'
import {
    completenessTone,
    formatTime,
    NEUTRAL,
    PAGE_LIMIT,
    resolvePageState,
    type BadgeTone,
    type QueryPhase,
} from '../utils/pageState'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))

// —— 查询快照（旧数据在刷新失败时保留）——
const items = ref<CommitSummaryDTO[]>([])
const nextCursor = ref('')
// 查询生命周期：phase 是互斥主状态；inflight 只在已有快照时投影为 refreshing
const phase = ref<QueryPhase>('loading')
const inflight = ref(false)
const errorMsg = ref('')

// —— 工作区上下文（读 syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)

// feature 门控（契约 03 §2.1：feature=false 前端不渲染）：history_view 未点亮时
// 页面不渲染内容（入口不渲染由 T11 的列表行承接）
const gated = computed(() => wsRow.value !== undefined && wsRow.value?.features.history_view !== true)

const pageState = computed(() => resolvePageState(phase.value, gated.value, items.value.length > 0))
// 重查失败保留旧快照时的提示（成功后清空；契约 04 受控重查的失败语义）
const refreshing = computed(() => inflight.value && items.value.length > 0)
const refreshFailed = computed(() => phase.value === 'ready' && errorMsg.value !== '')

let querySeq = 0

async function queryPage(cursor: string): Promise<void> {
    const seq = ++querySeq
    inflight.value = true
    try {
        const page = await SyncService.ListCommits(relationID.value, cursor, PAGE_LIMIT)
        if (seq !== querySeq) return
        items.value = cursor ? [...items.value, ...(page.items ?? [])] : (page.items ?? [])
        nextCursor.value = page.next_cursor ?? ''
        phase.value = 'ready'
        errorMsg.value = ''
    } catch (e) {
        if (seq !== querySeq) return
        if (phase.value === 'ready') {
            // 已有查询快照：保留旧数据，仅提示（契约 04 受控重查的失败语义）
            errorMsg.value = errText(e)
        } else {
            phase.value = 'error'
            errorMsg.value = errText(e)
        }
    } finally {
        if (seq === querySeq) inflight.value = false
    }
}

const reload = () => void queryPage('')
const loadMore = () => void queryPage(nextCursor.value)

// 路由切换工作区 → 全量重查
watch(relationID, reload, { immediate: true })

// —— 展示辅助（色调/时间/相位状态机收敛于 utils/pageState）——
const kindTones: Record<string, BadgeTone> = {
    initialize: NEUTRAL,
    sync: NEUTRAL,
    restore: NEUTRAL,
}

const cols = ['history.colTime', 'history.colKind', 'history.colCompleteness', 'history.colRemaining', 'history.colCommit']
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部：工作区上下文 + 返回 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="text-xl font-semibold">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }}
                    </template>
                    <template v-else>{{ t('history.title') }}</template>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">{{ t('history.subtitle') }}</p>
            </div>
            <div class="flex shrink-0 gap-2">
                <Button variant="outline" size="sm" :disabled="inflight" @click="reload">
                    {{ t('history.refresh') }}
                </Button>
                <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                    {{ t('history.backToList') }}
                </Button>
            </div>
        </div>

        <!-- 工作区不存在：syncCache 引导完成后仍找不到该关系 -->
        <Card v-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('history.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('history.backToList') }}
                </Button>
            </CardContent>
        </Card>

        <template v-else>
            <!-- 首查 loading：骨架行（保留表头同构） -->
            <Card v-if="pageState === 'loading'">
                <CardContent class="py-2">
                    <Table>
                        <TableHeader>
                            <TableRow>
                                <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                            </TableRow>
                        </TableHeader>
                        <TableBody>
                            <TableRow v-for="i in 3" :key="i">
                                <TableCell v-for="c in cols" :key="c">
                                    <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                </TableCell>
                            </TableRow>
                        </TableBody>
                    </Table>
                </CardContent>
            </Card>

            <!-- 首查/重查失败且无旧快照：错误态可重试 -->
            <Card v-else-if="pageState === 'error'">
                <CardContent class="flex items-center justify-between gap-3 py-4">
                    <span class="text-destructive text-sm">{{ t('history.errorTitle') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="reload">{{ t('history.retry') }}</Button>
                </CardContent>
            </Card>

            <!-- feature 门控：history_view 未点亮（契约 03 §2.1） -->
            <Card v-else-if="pageState === 'gate'">
                <CardContent class="text-muted-foreground py-8 text-center text-sm">
                    {{ t('history.featureOff') }}
                </CardContent>
            </Card>

            <template v-else>
                <!-- 重查中 / 重查失败（保留旧快照）提示 -->
                <div v-if="refreshing || refreshFailed" class="flex flex-wrap items-center gap-2">
                    <Badge v-if="refreshing" variant="secondary">{{ t('history.refreshing') }}</Badge>
                    <span v-if="refreshFailed" class="text-destructive text-xs">{{ t('history.refreshFailed') }}：{{ errorMsg }}</span>
                </div>

                <Card>
                    <CardContent class="py-2">
                        <!-- 空态：尚无已提交的同步（失败执行不进入历史，进任务/恢复） -->
                        <div v-if="pageState === 'empty'" class="text-muted-foreground py-10 text-center text-sm">
                            {{ t('history.empty') }}
                        </div>

                        <Table v-else>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                                    <TableHead class="text-right">{{ t('history.colActions') }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="row in items" :key="row.commit_id">
                                    <TableCell class="text-muted-foreground text-sm">{{ formatTime(row.created_at) }}</TableCell>
                                    <TableCell>
                                        <Badge :variant="(kindTones[row.kind] ?? NEUTRAL).variant">
                                            {{ t('history.kind.' + row.kind) }}
                                        </Badge>
                                    </TableCell>
                                    <TableCell>
                                        <Badge :variant="completenessTone(row.completeness).variant" :class="completenessTone(row.completeness).class">
                                            {{ t('history.completeness.' + row.completeness) }}
                                        </Badge>
                                    </TableCell>
                                    <TableCell>
                                        <span v-if="row.completeness === 'partial'" class="text-amber-600 text-sm dark:text-amber-400">
                                            {{ t('history.remainingCount', [row.remaining_change_count]) }}
                                        </span>
                                        <span v-else class="text-muted-foreground">—</span>
                                    </TableCell>
                                    <TableCell class="max-w-72 truncate font-mono text-xs" :title="row.commit_id">
                                        {{ row.commit_id }}
                                    </TableCell>
                                    <TableCell class="text-right">
                                        <Button
                                            variant="outline"
                                            size="xs"
                                            @click="router.push('/workspaces/' + relationID + '/history/' + row.commit_id)"
                                        >
                                            {{ t('history.openDetail') }}
                                        </Button>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>

                        <!-- 页脚：已展示计数 + 加载更多 -->
                        <div v-if="pageState === 'ready'" class="flex items-center justify-between gap-2 py-2">
                            <span class="text-muted-foreground text-xs">{{ t('history.shownOf', [items.length]) }}</span>
                            <Button v-if="nextCursor" variant="outline" size="sm" :disabled="inflight" @click="loadMore">
                                {{ t('history.loadMore') }}
                            </Button>
                        </div>
                    </CardContent>
                </Card>
            </template>
        </template>
    </div>
</template>
