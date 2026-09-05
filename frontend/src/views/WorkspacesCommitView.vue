<script setup lang="ts">
// /workspaces/:id/history/:commit_id：同步记录详情（契约 05 §3.5 GetCommit；
// UX 原型 P3 H-02，票 #110）。页头＝mono 记录 id + 完整性徽章（exact 绿 /
// partial 琥珀带剩余数）+「返回历史」+ 主按钮「回滚到此状态」；head 记录禁选
// 显信息横幅（该记录的结果即工作区现状，回滚到当前＝空操作）。
// 回滚入口（契约 06 §9，票 #61）：本页主操作是全产品回滚唯一入口（restore_preview
// 门控，feature 未点亮不渲染；prepare_restore availability 不可用置灰显后端原因码）。
// 记录数据为本页查询快照（changes 全量不分页），工作区上下文与 history_view 门控
// 读 stores/syncCache 投影。变更表列「前 → 后」为后端联表表示摘要，null 显「—」。
// 状态机互斥：loading / error / gate / empty / ready（沿 changes 页先例）；
// err.commit.not_found 落错误态错误条（记录不存在或跨关系）。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { History, Info } from '@lucide/vue'
import { SyncService } from '../api'
import type { CommitDTO, CommitSummaryDTO } from '../api'
import { bootstrapped, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { availabilityReasonText, canPrepareRestore } from '../utils/plans'
import { completenessTone, formatTime, resolvePageState, type QueryPhase } from '../utils/pageState'
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

// 完整性徽章文案：partial 带剩余数（H-02：partial · 剩余 N）
const completenessLabel = computed(() => {
    const s = summary.value
    if (!s) return ''
    return s.completeness === 'partial'
        ? t('history.completenessRemaining', [s.remaining_change_count])
        : t('history.completeness.' + s.completeness)
})

// —— 计数条（H-02 五枚）：变更资源 / 创建 / 修改 / 删除 / 剩余差异，全为行投影
// 聚合的纯渲染 ——
const counts = computed(() => {
    const rows = commit.value?.changes ?? []
    return {
        total: rows.length,
        create: rows.filter(r => r.change_kind === 'create').length,
        modify: rows.filter(r => r.change_kind === 'modify').length,
        remove: rows.filter(r => r.change_kind === 'delete').length,
        remaining: summary.value?.remaining_change_count ?? 0,
    }
})

const cols = ['history.commit.colResource', 'history.commit.colChange', 'history.commit.colProject', 'history.commit.colRuntime']

// —— 跳过清单（票 #63 剔除语义透出面）：本场剔出的取数失败行（err.download.*
// /hash_format_unsupported/content_unavailable），原因码直查 locale（与错误条
// 同一套键；缺键渲染键名本身便于发现遗漏）。——
const skipped = computed(() => commit.value?.skipped ?? [])

// —— 用户决议清单（票 #100，ADR-0013 §1）：「已忽略」（随提交合成 ignore 规则，
// 恢复入口在受管范围页）与「手动处理」（本次吸收进基线）分列展示——与上方
// skipped 的物化取数剔除项是两个清单。——
const ignored = computed(() => commit.value?.ignored ?? [])
const manual = computed(() => commit.value?.manual ?? [])

// —— 决议分列块（评审 S5 抽取）：ignored/manual 两段近似模板收敛为单一渲染
// 子块，section 对象即 props（locale 键名 / padding 类差异），渲染结果与分列
// 实现逐类名一致。——
interface DecisionSection {
    key: 'ignored' | 'manual'
    titleKey: string
    hintKey: string
    rows: NonNullable<CommitDTO['ignored']>
    sectionClass: string
}
const decisionSections = computed<DecisionSection[]>(() => {
    const sections: DecisionSection[] = []
    if (ignored.value.length > 0) {
        sections.push({
            key: 'ignored',
            titleKey: 'history.commit.ignoredTitle',
            hintKey: 'history.commit.ignoredHint',
            rows: ignored.value,
            sectionClass: 'border-b px-2 py-2 pb-3',
        })
    }
    if (manual.value.length > 0) {
        sections.push({
            key: 'manual',
            titleKey: 'history.commit.manualTitle',
            hintKey: 'history.commit.manualHint',
            rows: manual.value,
            // 与 ignored 同现时补顶距（原 manual 块的 :class 条件）
            sectionClass: ignored.value.length > 0 ? 'px-2 py-2 pt-3' : 'px-2 py-2',
        })
    }
    return sections
})

// 重试跳过项分两路（#62 遗留项收口；#86 单调用化）：
// - 授权模式开：升级走快速更新同一后端用例（SyncService.QuickUpdate，契约 07
//   §3.1 唯一口径）——链在后端阻塞收口，前端按三态承接：no_diff → 「已是最新」；
//   apply_started → 任务中心/变化页；awaiting_confirmation → 待确认计划页。
// - 未授权：保持「重新开始」最小语义（重新触发扫描，用户走既有 prepare_sync
//   流，新计划天然只剩未更新项；不新造部分重试机制）。成功后回工作区列表并提示。
const retrying = ref(false)
async function retrySkipped(): Promise<void> {
    if (retrying.value) return
    retrying.value = true
    try {
        const ws = wsRow.value
        if (ws?.authorized_apply === true) {
            const res = await SyncService.QuickUpdate(relationID.value)
            triggerRequery()
            if (res.outcome === 'no_diff') {
                showSnackbar(t('workspaces.quickUpdate.upToDate'), 'success')
            } else if (res.outcome === 'apply_started') {
                showSnackbar(t('workspaces.quickUpdate.directToast'), 'success')
                void router.push('/workspaces/' + relationID.value + '/changes')
            } else {
                showSnackbar(t('workspaces.quickUpdate.manualToast'), 'warning')
                void router.push('/workspaces/' + relationID.value + '/plans/' + res.plan_id)
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
            <!-- 页头（H-02）：mono 记录 id + 完整性徽章；右侧「返回历史」+ 主按钮 -->
            <div class="flex items-start justify-between gap-4">
                <div class="min-w-0">
                    <h1 class="page-title flex flex-wrap items-center gap-2">
                        <span v-if="summary" class="truncate font-mono" :title="summary.commit_id">{{ summary.commit_id }}</span>
                        <template v-else>{{ t('history.commit.title') }}</template>
                        <Badge
                            v-if="summary"
                            :variant="completenessTone(summary.completeness).variant"
                            :title="summary.completeness === 'partial' ? t('history.remainingCount', [summary.remaining_change_count]) : undefined"
                        >
                            {{ completenessLabel }}
                        </Badge>
                    </h1>
                    <p class="text-muted-foreground mt-1 text-sm">
                        <template v-if="summary">{{ t('history.kind.' + summary.kind) }} · {{ t('history.commit.created') }} {{ formatTime(summary.created_at) }}</template>
                        <template v-if="wsRow"> · </template>
                        <template v-if="wsRow">
                            {{ wsRow.relation.project.display_name }}
                            <span class="text-muted-foreground">↔</span>
                            {{ wsRow.relation.runtime.display_name }}
                        </template>
                    </p>
                </div>
                <div class="flex shrink-0 flex-wrap justify-end gap-2">
                    <Button variant="ghost" size="sm" @click="router.push('/workspaces/' + relationID + '/history')">
                        {{ t('history.commit.backToHistory') }}
                    </Button>
                    <!-- 回滚入口（全产品唯一，契约 06 §9）：restore_preview 门控 + head 禁选 -->
                    <Button
                        v-if="restoreEntryVisible"
                        size="sm"
                        :disabled="!restoreReady || preparing"
                        :title="restoreReady ? undefined : restoreReason"
                        @click="prepareRestore"
                    >
                        <History class="size-3.5" aria-hidden="true" />
                        {{ t('restore.entryPrimary') }}
                    </Button>
                </div>
            </div>

            <!-- head 禁选横幅（H-02 / 票 #61）：该记录的结果即工作区现状，回滚到当前＝空操作 -->
            <div
                v-if="wsRow && summary && isHead"
                class="flex items-center gap-2.5 rounded-lg border border-tint-primary bg-tint-primary px-3.5 py-2.5 text-[12.5px]"
            >
                <Info class="text-primary size-4 flex-none" aria-hidden="true" />
                <div class="min-w-0 flex-1">
                    <span class="font-bold">{{ t('restore.headBannerTitle') }}</span>
                    <span> · {{ t('restore.headBannerHint') }}</span>
                </div>
            </div>

            <!-- restore_preview 点亮但 prepare_restore 不可用：显后端原因码（唯一门控同源） -->
            <div
                v-else-if="wsRow && summary && wsRow.features.restore_preview === true && !restoreReady"
                class="flex flex-wrap items-center gap-2.5 rounded-lg border border-tint-warning bg-tint-warning px-3.5 py-2.5 text-[12.5px]"
            >
                <Info class="text-warning size-4 flex-none" aria-hidden="true" />
                <div class="min-w-0 flex-1">
                    <span class="font-bold">{{ t('restore.entryUnavailable') }}</span>
                    <template v-if="restoreReason">
                        <span> · {{ restoreReason }}</span>
                    </template>
                </div>
            </div>

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
                <!-- 计数条（H-02 五枚）+ 来源计划 -->
                <div class="flex flex-wrap items-center gap-2">
                    <Badge variant="outline" class="text-muted-foreground">
                        {{ t('history.commit.countTotal') }} <b class="text-foreground">{{ counts.total }}</b>
                    </Badge>
                    <Badge variant="outline" class="text-muted-foreground">
                        {{ t('history.commit.countCreate') }} <b class="text-foreground">{{ counts.create }}</b>
                    </Badge>
                    <Badge variant="outline" class="text-muted-foreground">
                        {{ t('history.commit.countModify') }} <b class="text-foreground">{{ counts.modify }}</b>
                    </Badge>
                    <Badge variant="outline" class="text-muted-foreground">
                        {{ t('history.commit.countDelete') }} <b class="text-foreground">{{ counts.remove }}</b>
                    </Badge>
                    <Badge variant="outline" class="text-muted-foreground">
                        {{ t('history.commit.countRemaining') }} <b class="text-foreground">{{ counts.remaining }}</b>
                    </Badge>
                    <template v-if="commit?.plan_id">
                        <span class="text-faint">|</span>
                        <button
                            class="text-primary max-w-48 truncate font-mono text-xs hover:underline"
                            :title="commit.plan_id"
                            @click="router.push('/workspaces/' + relationID + '/plans/' + commit.plan_id)"
                        >
                            {{ t('history.commit.plan') }} {{ commit.plan_id }}
                        </button>
                    </template>
                    <span v-else class="text-muted-foreground text-xs">{{ t('history.commit.planMissing') }}</span>
                </div>

                <!-- 逐资源变更表（H-02 四列：资源、变更、Project、Runtime） -->
                <Card>
                    <CardContent class="py-2">
                        <div class="text-muted-foreground flex items-center justify-between gap-2 px-2 py-2 text-xs">
                            <span class="text-foreground text-sm font-medium">{{ t('history.commit.changesTitle') }}</span>
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
                                    <TableCell class="max-w-80 truncate font-mono text-sm font-medium" :title="row.resource_id">
                                        {{ row.resource_id }}
                                    </TableCell>
                                    <TableCell>
                                        <Badge variant="outline" plain>{{ t('history.change.' + row.change_kind) }}</Badge>
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
                                </TableRow>
                            </TableBody>
                        </Table>
                    </CardContent>
                </Card>

                <!-- 用户决议清单（票 #100，ADR-0013）：已忽略 / 手动处理分列 -->
                <Card v-if="decisionSections.length > 0">
                    <CardContent class="py-2">
                        <div v-for="section in decisionSections" :key="section.key" :class="section.sectionClass">
                            <div class="flex flex-col">
                                <span class="font-medium text-sm text-foreground">{{ t(section.titleKey) }}（{{ section.rows.length }}）</span>
                                <span class="text-muted-foreground text-xs">{{ t(section.hintKey) }}</span>
                            </div>
                            <div class="mt-2 flex flex-col gap-1">
                                <span
                                    v-for="row in section.rows"
                                    :key="row.resource_id"
                                    class="max-w-96 truncate font-mono text-xs"
                                    :title="row.resource_id"
                                >{{ row.resource_id }}</span>
                            </div>
                        </div>
                    </CardContent>
                </Card>

                <!-- 跳过清单（票 #63 剔除语义）：本场剔出的取数失败行 + 重试入口。
                     授权模式开走快速更新单调用（#86，进行中统一 busy），否则重扫最小语义 -->
                <Card v-if="skipped.length > 0">
                    <CardContent class="py-2">
                        <div class="flex items-center justify-between gap-2 px-2 py-2">
                            <div class="flex flex-col">
                                <span class="text-foreground text-sm font-medium">{{ t('history.commit.skippedTitle') }}（{{ skipped.length }}）</span>
                                <span class="text-muted-foreground text-xs">{{ t('history.commit.skippedHint') }}</span>
                            </div>
                            <Button size="sm" variant="outline" :disabled="retrying" @click="retrySkipped">
                                {{ retrying ? t('workspaces.quickUpdate.busy') : t('history.commit.retrySkipped') }}
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
