<script setup lang="ts">
// /workspaces/:id/history/:commit_id：同步记录详情（契约 05 §3.5 GetCommit；
// UX 原型 §7.8）。记录数据为本页查询快照（changes 全量不分页），工作区上下文
// 与 history_view 门控读 stores/syncCache 投影。
// 变更表列「前 → 后」为后端联表表示摘要（representationSummary），null 显「—」。
// 回滚入口（契约 06 §9，票 #61）：本页主操作「回滚到此状态」是全产品回滚唯一
// 入口（restore_preview 门控，feature 未点亮不渲染；prepare_restore availability
// 不可用置灰显后端原因码）；head 提交禁选＝UI 防误触（head=历史首条，后端
// availability 不含 commit 维度，空差异计划本就合法），以信息横幅说明。
// 状态机互斥：loading / error / gate / empty / ready（沿 changes 页先例）；
// err.commit.not_found 落错误态错误条（记录不存在或跨关系）。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { CommitDTO, CommitSummaryDTO } from '../api'
import { bootstrapped, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { availabilityReasonText, canPrepareRestore } from '../utils/plans'
import { runQuickUpdate } from '../utils/quickUpdate'
import type { QuickUpdatePhase } from '../utils/quickUpdate'
import {
    completenessTone,
    formatTime,
    resolvePageState,
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
const commitID = computed(() => String(route.params.commit_id ?? ''))

// —— 查询快照 ——
const commit = ref<CommitDTO | null>(null)
const phase = ref<QueryPhase>('loading')
const inflight = ref(false)
const errorMsg = ref('')
let querySeq = 0

async function loadCommit(): Promise<void> {
    const seq = ++querySeq
    inflight.value = true
    try {
        const c = await SyncService.GetCommit(relationID.value, commitID.value)
        if (seq !== querySeq) return
        commit.value = c
        phase.value = 'ready'
        errorMsg.value = ''
    } catch (e) {
        if (seq !== querySeq) return
        commit.value = null
        phase.value = 'error'
        errorMsg.value = errText(e)
    } finally {
        if (seq === querySeq) inflight.value = false
    }
}

watch([relationID, commitID], () => void loadCommit(), { immediate: true })

// —— 工作区上下文（读 syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)
const gated = computed(() => wsRow.value !== undefined && wsRow.value?.features.history_view !== true)

const pageState = computed(() =>
    resolvePageState(phase.value, gated.value, (commit.value?.changes ?? []).length > 0),
)

const summary = computed<CommitSummaryDTO | null>(() => commit.value?.summary ?? null)

// —— 展示辅助（色调/时间/相位状态机收敛于 utils/pageState）——
// 表示摘要「前 → 后」：null 显「—」（契约 05 §3.5）
function rep(s?: string | null): string {
    return s ?? '—'
}

const cols = ['history.commit.colResource', 'history.commit.colProject', 'history.commit.colRuntime', 'history.commit.colOperation']

// —— 跳过清单（票 #63 剔除语义透出面）：本场剔出的取数失败行（err.download.*
// /hash_format_unsupported/content_unavailable），原因码直查 locale（与错误条
// 同一套键；缺键渲染键名本身便于发现遗漏）。——
const skipped = computed(() => commit.value?.skipped ?? [])

// 重试跳过项分两路（#62 遗留项收口）：
// - 授权模式开：升级走快速更新同一编排（utils/quickUpdate，唯一口径 Q7）——
//   requirements 空时免确认直达 apply（committed → 任务中心/变化页），否则转
//   待确认计划页（manual）；本页零特判，编排细节与免确认判定全在编排内。
// - 未授权：保持「重新开始」最小语义（重新触发扫描，用户走既有 prepare_sync
//   流，新计划天然只剩未更新项；不新造部分重试机制）。成功后回工作区列表并提示。
const retrying = ref(false)
const retryPhase = ref<QuickUpdatePhase | ''>('')
async function retrySkipped(): Promise<void> {
    if (retrying.value) return
    retrying.value = true
    try {
        const ws = wsRow.value
        if (ws?.authorized_apply === true) {
            const outcome = await runQuickUpdate(ws, phase => {
                retryPhase.value = phase
            })
            if (outcome.kind === 'committed') {
                showSnackbar(t('workspaces.quickUpdate.directToast'), 'success')
                void router.push('/workspaces/' + relationID.value + '/changes')
            } else {
                showSnackbar(t('workspaces.quickUpdate.manualToast'), 'warning')
                void router.push('/workspaces/' + relationID.value + '/plans/' + outcome.planID)
            }
            return
        }
        await SyncService.StartScan(relationID.value)
        showSnackbar(t('history.commit.retryQueued'), 'success')
        void router.push('/workspaces')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        retrying.value = false
        retryPhase.value = ''
    }
}

// —— 回滚入口（契约 06 §9，票 #61）——
// head 判定：历史首页首条（created_at DESC）即 head（提交不可变、head 恒在
// 保留窗口内）。查询失败按「非 head」处理——head 禁选是防误触增强，不因探测
// 失败阻断合法回滚；对 head 发起 PrepareRestore 也只是空差异计划，后端合法。
const headCommitID = ref('')
async function loadHead(): Promise<void> {
    try {
        const page = await SyncService.ListCommits(relationID.value, '', 1)
        headCommitID.value = page.items?.[0]?.commit_id ?? ''
    } catch {
        headCommitID.value = ''
    }
}

const isHead = computed(() => headCommitID.value !== '' && headCommitID.value === commitID.value)
// 入口渲染门控：restore_preview feature 点亮（feature=false 不渲染入口）且非 head
//（head 禁选＝UI 防误触，改显横幅说明）；可用性再由 prepare_restore availability 推导
const restoreEntryVisible = computed(
    () => wsRow.value?.features.restore_preview === true && !isHead.value,
)
const restoreReady = computed(() => canPrepareRestore(wsRow.value))
const restoreReason = computed(() => availabilityReasonText(wsRow.value, 'prepare_restore'))

const preparing = ref(false)
async function prepareRestore(): Promise<void> {
    if (preparing.value || !restoreReady.value) return
    preparing.value = true
    try {
        const plan = await SyncService.PrepareRestore({
            relation_id: relationID.value,
            commit_id: commitID.value,
        })
        showSnackbar(t('restore.prepared'), 'success')
        await router.push('/workspaces/' + relationID.value + '/plans/restore/' + plan.plan_id)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        preparing.value = false
    }
}

watch([relationID, commitID], () => void loadHead(), { immediate: true })
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部：记录类型 + 完整性 + 时间 + 剩余差异 + 来源计划 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="flex items-center gap-2 text-xl font-semibold">
                    {{ summary ? t('history.kind.' + summary.kind) : t('history.commit.title') }}
                    <Badge v-if="summary" :variant="completenessTone(summary.completeness).variant" :class="completenessTone(summary.completeness).class">
                        {{ t('history.completeness.' + summary.completeness) }}
                    </Badge>
                    <Badge v-if="summary && summary.completeness === 'partial'" variant="outline" class="text-amber-600 dark:text-amber-400">
                        {{ t('history.remainingCount', [summary.remaining_change_count]) }}
                    </Badge>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }} ·
                    </template>
                    <template v-if="summary">{{ t('history.commit.created') }} {{ formatTime(summary.created_at) }}</template>
                    <template v-if="summary?.commit_id"> · </template>
                    <span v-if="summary?.commit_id" class="font-mono text-xs" :title="summary.commit_id">{{ summary.commit_id }}</span>
                </p>
            </div>
            <div class="flex shrink-0 flex-wrap justify-end gap-2">
                <!-- 回滚入口（全产品唯一，契约 06 §9）：restore_preview 门控 + head 禁选 -->
                <Button
                    v-if="restoreEntryVisible"
                    size="sm"
                    :disabled="!restoreReady || preparing"
                    :title="restoreReady ? undefined : restoreReason"
                    @click="prepareRestore"
                >
                    {{ t('restore.entryPrimary') }}
                </Button>
                <Button v-if="commit?.plan_id" variant="outline" size="sm" @click="router.push('/workspaces/' + relationID + '/plans/' + commit.plan_id)">
                    {{ t('history.commit.plan') }}
                </Button>
                <Button variant="ghost" size="sm" @click="router.push('/workspaces/' + relationID + '/history')">
                    {{ t('history.commit.backToHistory') }}
                </Button>
            </div>
        </div>

        <!-- head 禁选横幅（票 #61）：该记录的结果即工作区现状，回滚到当前＝空操作 -->
        <Card v-if="wsRow && summary && isHead">
            <CardContent class="text-muted-foreground flex items-center gap-2 py-3 text-sm">
                <span class="text-foreground font-medium">{{ t('restore.headBannerTitle') }}</span>
                <span>— {{ t('restore.headBannerHint') }}</span>
            </CardContent>
        </Card>

        <!-- restore_preview 点亮但 prepare_restore 不可用：显后端原因码（唯一门控同源） -->
        <Card v-else-if="wsRow && summary && wsRow.features.restore_preview === true && !restoreReady">
            <CardContent class="flex flex-wrap items-center justify-between gap-3 py-3">
                <span class="text-amber-600 text-sm dark:text-amber-400">
                    {{ t('restore.entryUnavailable') }}<template v-if="restoreReason">：{{ restoreReason }}</template>
                </span>
            </CardContent>
        </Card>

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
                            <TableRow v-for="i in 5" :key="i">
                                <TableCell v-for="c in cols" :key="c">
                                    <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                </TableCell>
                            </TableRow>
                        </TableBody>
                    </Table>
                </CardContent>
            </Card>

            <!-- 查询失败：错误态可重试（err.commit.not_found 渲染为本地化错误条） -->
            <Card v-else-if="pageState === 'error'">
                <CardContent class="flex items-center justify-between gap-3 py-4">
                    <span class="text-destructive text-sm">{{ t('history.commit.errorTitle') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="loadCommit">{{ t('history.retry') }}</Button>
                </CardContent>
            </Card>

            <!-- feature 门控：history_view 未点亮（契约 03 §2.1） -->
            <Card v-else-if="pageState === 'gate'">
                <CardContent class="text-muted-foreground py-8 text-center text-sm">
                    {{ t('history.featureOff') }}
                </CardContent>
            </Card>

            <template v-else-if="summary">
                <!-- 摘要条：完整性 / 剩余差异 / 来源计划 -->
                <div class="flex flex-wrap items-center gap-2">
                    <Badge variant="outline" class="text-muted-foreground">{{ t('history.commit.remaining') }} {{ summary.remaining_change_count }}</Badge>
                    <Badge v-if="commit?.plan_id" variant="outline" class="text-muted-foreground">
                        {{ t('history.commit.plan') }} <span class="max-w-48 truncate font-mono" :title="commit.plan_id">{{ commit.plan_id }}</span>
                    </Badge>
                    <span v-else class="text-muted-foreground text-xs">{{ t('history.commit.planMissing') }}</span>
                </div>

                <!-- 逐资源变更表 -->
                <Card>
                    <CardContent class="py-2">
                        <div class="text-muted-foreground flex items-center justify-between gap-2 px-2 py-2 text-xs">
                            <span class="font-medium text-sm text-foreground">{{ t('history.commit.changesTitle') }}</span>
                            <span>{{ t('history.shownOf', [commit?.changes?.length ?? 0]) }}</span>
                        </div>

                        <!-- 空态：该记录没有逐资源变更 -->
                        <div v-if="pageState === 'empty'" class="text-muted-foreground py-10 text-center text-sm">
                            {{ t('history.commit.changesEmpty') }}
                        </div>

                        <Table v-else>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="row in commit?.changes ?? []" :key="row.resource_id">
                                    <TableCell class="max-w-80 truncate font-medium" :title="row.resource_id">
                                        {{ row.resource_id }}
                                    </TableCell>
                                    <TableCell class="max-w-72 truncate font-mono text-xs" :title="rep(row.project_before) + ' → ' + rep(row.project_after)">
                                        <span class="text-muted-foreground">{{ rep(row.project_before) }}</span>
                                        <span class="text-muted-foreground"> → </span>
                                        {{ rep(row.project_after) }}
                                    </TableCell>
                                    <TableCell class="max-w-72 truncate font-mono text-xs" :title="rep(row.runtime_before) + ' → ' + rep(row.runtime_after)">
                                        <span class="text-muted-foreground">{{ rep(row.runtime_before) }}</span>
                                        <span class="text-muted-foreground"> → </span>
                                        {{ rep(row.runtime_after) }}
                                    </TableCell>
                                    <TableCell>
                                        <Badge variant="outline">{{ t('history.change.' + row.change_kind) }}</Badge>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>
                    </CardContent>
                </Card>

                <!-- 跳过清单（票 #63 剔除语义）：本场剔出的取数失败行 + 重试入口。
                     授权模式开走快速更新编排（进行中显编排阶段），否则重扫最小语义 -->
                <Card v-if="skipped.length > 0">
                    <CardContent class="py-2">
                        <div class="flex items-center justify-between gap-2 px-2 py-2">
                            <div class="flex flex-col">
                                <span class="font-medium text-sm text-foreground">{{ t('history.commit.skippedTitle') }}（{{ skipped.length }}）</span>
                                <span class="text-muted-foreground text-xs">{{ t('history.commit.skippedHint') }}</span>
                            </div>
                            <Button size="sm" variant="outline" :disabled="retrying" @click="retrySkipped">
                                {{ retrying && retryPhase ? t('workspaces.quickUpdate.phase.' + retryPhase) : t('history.commit.retrySkipped') }}
                            </Button>
                        </div>
                        <Table>
                            <TableHeader>
                                <TableRow>
                                    <TableHead>{{ t('history.commit.colResource') }}</TableHead>
                                    <TableHead>{{ t('history.commit.colReason') }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="row in skipped" :key="row.resource_id">
                                    <TableCell class="max-w-96 truncate font-mono text-xs" :title="row.resource_id">
                                        {{ row.resource_id }}
                                    </TableCell>
                                    <TableCell class="text-sm">
                                        <span class="text-amber-600 dark:text-amber-400">{{ t(row.reason_code, row.reason_args ?? []) }}</span>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>
                    </CardContent>
                </Card>
            </template>
        </template>
    </div>
</template>
