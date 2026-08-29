<script setup lang="ts">
// /workspaces/:id/changes：资源级变更浏览（契约 03 §2.2 GetChanges；UX 原型 §7.3）。
// 变更数据为本页查询快照（GetChanges 读时计算，不进 syncCache 全量缓存）；
// 工作区上下文（关系名/活跃任务）仍读 stores/syncCache，页面不做第二处取数。
// 状态机互斥：loading / error / empty / filteredEmpty / ready / refreshing；
// 刷新失败保留旧查询快照（契约 04 §2.4：查询失败静默保留旧数据，等下次触发重试）。
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { ChangeDTO, ChangesSummaryDTO } from '../api'
import { bootstrapped, tasks, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { canPrepareSync, prepareSync } from '../utils/plans'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))

// 单页行数（与后端 MaxPageLimit 对齐）
const PAGE_LIMIT = 200
const PREFIX_DEBOUNCE_MS = 300

// —— 查询快照（旧数据在刷新失败时保留）——
const items = ref<ChangeDTO[]>([])
const summary = ref<ChangesSummaryDTO | null>(null)
const nextCursor = ref('')
// 查询生命周期：phase 是互斥主状态；inflight 只在已有快照时投影为 refreshing
const phase = ref<'loading' | 'error' | 'ready'>('loading')
const inflight = ref(false)
const errorMsg = ref('')

// —— 筛选（变更即整表重查，cursor 归零；summary 由后端保证不受筛选影响）——
const classification = ref('')
const kind = ref('')
const prefix = ref('')

const pageState = computed<'loading' | 'error' | 'empty' | 'filteredEmpty' | 'ready' | 'refreshing'>(() => {
    if (phase.value === 'loading') return 'loading'
    if (phase.value === 'error') return 'error'
    if (inflight.value) return 'refreshing'
    if (!items.value.length) return hasFilters.value ? 'filteredEmpty' : 'empty'
    return 'ready'
})
const hasFilters = computed(() => classification.value !== '' || kind.value !== '' || prefix.value !== '')

// —— 工作区上下文（读 syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)

let querySeq = 0
let prefixTimer: ReturnType<typeof setTimeout> | undefined

async function queryPage(cursor: string): Promise<void> {
    const seq = ++querySeq
    inflight.value = true
    try {
        const page = await SyncService.GetChanges({
            relation_id: relationID.value,
            classification: classification.value || undefined,
            resource_kind: kind.value || undefined,
            path_prefix: prefix.value || undefined,
            cursor: cursor || undefined,
            limit: PAGE_LIMIT,
        })
        if (seq !== querySeq) return // 已被更新的查询作废（快速切换筛选）
        if (cursor) {
            items.value = [...items.value, ...(page.items ?? [])]
        } else {
            items.value = page.items ?? []
            summary.value = page.summary
        }
        nextCursor.value = page.next_cursor ?? ''
        phase.value = 'ready'
        errorMsg.value = ''
    } catch (e) {
        if (seq !== querySeq) return
        if (phase.value === 'ready') {
            // 已有查询快照：保留旧数据，仅提示（契约 04 受控重查的失败语义）
            showSnackbar(t('changes.refreshFailed') + '：' + errText(e), 'error')
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
const resetFilters = () => {
    classification.value = ''
    kind.value = ''
    prefix.value = ''
}

watch([classification, kind], reload)
watch(prefix, () => {
    clearTimeout(prefixTimer)
    prefixTimer = setTimeout(reload, PREFIX_DEBOUNCE_MS)
})
onUnmounted(() => clearTimeout(prefixTimer))

// 路由切换工作区 → 全量重查
watch(relationID, reload, { immediate: true })

// —— 生成同步计划（T11 计划页承接）：availability 唯一门控，逻辑收敛于 utils/plans ——
const preparing = ref(false)
const canPrepareSyncNow = computed(() => canPrepareSync(wsRow.value))

async function prepareSyncPlan(): Promise<void> {
    const ws = wsRow.value
    if (!ws || preparing.value) return
    preparing.value = true
    try {
        const plan = await prepareSync(ws)
        await router.push('/workspaces/' + relationID.value + '/plans/' + plan.plan_id)
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        preparing.value = false
    }
}

// 活跃任务收敛（扫描结束）→ 重查一次变更；开始新任务不重查（读时计算，完成后才变）。
// 触发时 syncCache 已随其自身管线更新，这里只补本页查询快照。
watch(
    () => wsRow.value?.state.active_task_id ?? '',
    (now, prev) => {
        if (prev && !now) reload()
    },
)

// 行内未完结任务（当前页关系上的活跃任务，来自 syncCache）
const activeTask = computed(() => {
    const id = wsRow.value?.state.active_task_id
    return id ? (tasks.value.get(id) ?? null) : null
})

// —— 展示辅助 ——
interface BadgeTone {
    variant: 'default' | 'secondary' | 'destructive' | 'outline'
    class?: string
}
const OK: BadgeTone = { variant: 'outline', class: 'text-emerald-600 dark:text-emerald-400' }
const WARN: BadgeTone = { variant: 'outline', class: 'text-amber-600 dark:text-amber-400' }
const NEUTRAL: BadgeTone = { variant: 'outline' }
const BUSY: BadgeTone = { variant: 'secondary' }
const BAD: BadgeTone = { variant: 'destructive' }

const classTones: Record<string, BadgeTone> = {
    noop: NEUTRAL,
    converged: OK,
    adopt_equal: OK,
    init_choice: WARN,
    project_to_runtime: BUSY,
    runtime_to_project: BUSY,
    remove_runtime_candidate: WARN,
    remove_project_candidate: WARN,
    conflict_modify: BAD,
    conflict_delete_modify: BAD,
}

function classTone(c: string): BadgeTone {
    return classTones[c] ?? NEUTRAL
}

function humanSize(bytes?: number): string {
    if (!bytes || bytes <= 0) return ''
    const units = ['B', 'KB', 'MB', 'GB']
    let v = bytes
    let i = 0
    while (v >= 1024 && i < units.length - 1) {
        v /= 1024
        i++
    }
    return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

const summaryChips = computed(() => {
    const s = summary.value
    if (!s) return []
    return [
        { key: 'changes.summary.total', count: s.total },
        { key: 'changes.summary.noop', count: s.noop_count },
        { key: 'changes.summary.converged', count: s.converged_count },
        { key: 'changes.summary.adoptEqual', count: s.adopt_equal_count },
        { key: 'changes.summary.initChoice', count: s.init_choice_count },
        { key: 'changes.summary.create', count: s.create_count },
        { key: 'changes.summary.modify', count: s.modify_count },
        { key: 'changes.summary.delete', count: s.delete_count },
        { key: 'changes.summary.conflict', count: s.conflict_count },
    ]
})

// 分类枚举单源：classTones 的键序即筛选下拉的选项序
const classOptions = Object.keys(classTones)

const cols = ['changes.colResource', 'changes.colProject', 'changes.colBaseline', 'changes.colRuntime', 'changes.colVerdict']

const kindOptions = ['mod', 'text_file', 'binary_file']
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部：工作区上下文 + 返回/重查 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="text-xl font-semibold">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }}
                    </template>
                    <template v-else>{{ t('changes.title') }}</template>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">
                    {{ t('changes.subtitle') }}
                    <template v-if="activeTask"> · {{ t(activeTask.message_key, activeTask.message_args ?? []) }}</template>
                </p>
            </div>
            <div class="flex shrink-0 gap-2">
                <Button v-if="canPrepareSyncNow" variant="outline" size="sm" :disabled="preparing" @click="prepareSyncPlan">
                    {{ t('changes.planAction') }}
                </Button>
                <Button variant="outline" size="sm" :disabled="inflight" @click="reload">
                    {{ t('changes.refresh') }}
                </Button>
                <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                    {{ t('changes.backToList') }}
                </Button>
            </div>
        </div>

        <!-- 工作区不存在：syncCache 引导完成后仍找不到该关系 -->
        <Card v-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('changes.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('changes.backToList') }}
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

            <!-- 首查/重查失败且无旧快照：错误态可重试 -->
            <Card v-else-if="pageState === 'error'">
                <CardContent class="flex items-center justify-between gap-3 py-4">
                    <span class="text-destructive text-sm">{{ t('changes.errorTitle') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="reload">{{ t('changes.retry') }}</Button>
                </CardContent>
            </Card>

            <template v-else>
                <!-- 摘要（全量计数）+ 筛选条 -->
                <div class="flex flex-wrap items-center gap-2">
                    <Badge v-for="chip in summaryChips" :key="chip.key" variant="outline" class="text-muted-foreground">
                        {{ t(chip.key) }} {{ chip.count }}
                    </Badge>
                    <Badge v-if="pageState === 'refreshing'" variant="secondary">{{ t('changes.refreshing') }}</Badge>
                </div>

                <div class="flex flex-wrap items-center gap-2">
                    <select v-model="classification" class="border-input h-9 rounded-md border bg-transparent px-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3">
                        <option value="">{{ t('changes.class.all') }}</option>
                        <option v-for="c in classOptions" :key="c" :value="c">{{ t('changes.class.' + c) }}</option>
                    </select>
                    <select v-model="kind" class="border-input h-9 rounded-md border bg-transparent px-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3">
                        <option value="">{{ t('changes.filter.kindAll') }}</option>
                        <option v-for="k in kindOptions" :key="k" :value="k">{{ t('changes.kind.' + k) }}</option>
                    </select>
                    <Input v-model="prefix" class="w-64" :placeholder="t('changes.filter.prefixPlaceholder')" />
                    <span class="text-muted-foreground text-xs">{{ t('changes.filterNote') }}</span>
                </div>

                <Card>
                    <CardContent class="py-2">
                        <!-- 空态（无筛选）/ 筛选空态 -->
                        <div v-if="pageState === 'empty' || pageState === 'filteredEmpty'" class="flex flex-col items-center gap-3 py-10">
                            <p class="text-muted-foreground text-sm">
                                {{ t(pageState === 'empty' ? 'changes.empty' : 'changes.filteredEmpty') }}
                            </p>
                            <Button v-if="pageState === 'filteredEmpty'" variant="outline" size="sm" @click="resetFilters">
                                {{ t('changes.resetFilters') }}
                            </Button>
                        </div>

                        <Table v-else>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="row in items" :key="row.resource_id">
                                    <TableCell>
                                        <div class="max-w-80 truncate font-medium" :title="row.relative_path || row.resource_id">
                                            {{ row.relative_path || row.resource_id }}
                                        </div>
                                        <div class="mt-1 flex flex-wrap items-center gap-1">
                                            <Badge variant="outline" class="text-muted-foreground">{{ t('changes.kind.' + row.resource_kind) }}</Badge>
                                            <Badge
                                                v-for="d in row.diagnostics"
                                                :key="d.code + (d.args?.join('|') ?? '')"
                                                variant="outline"
                                                class="text-amber-600 dark:text-amber-400"
                                                :title="d.detail"
                                            >
                                                {{ t(d.code, d.args ?? []) }}
                                            </Badge>
                                        </div>
                                    </TableCell>
                                    <TableCell>
                                        <template v-if="row.project">
                                            <div>{{ row.project.format }}</div>
                                            <div class="text-muted-foreground text-xs">{{ humanSize(row.project.content?.size) }}</div>
                                        </template>
                                        <span v-else class="text-muted-foreground">—</span>
                                    </TableCell>
                                    <TableCell>
                                        <template v-if="row.base">
                                            <div>{{ row.base.format }}</div>
                                            <div class="text-muted-foreground text-xs">{{ humanSize(row.base.content?.size) }}</div>
                                        </template>
                                        <span v-else class="text-muted-foreground">{{ t('changes.baselineMissing') }}</span>
                                    </TableCell>
                                    <TableCell>
                                        <template v-if="row.runtime">
                                            <div>{{ row.runtime.format }}</div>
                                            <div class="text-muted-foreground text-xs">{{ humanSize(row.runtime.content?.size) }}</div>
                                        </template>
                                        <span v-else class="text-muted-foreground">—</span>
                                    </TableCell>
                                    <TableCell>
                                        <div class="flex flex-col items-start gap-1">
                                            <Badge :variant="classTone(row.classification).variant" :class="classTone(row.classification).class">
                                                {{ t('changes.class.' + row.classification) }}
                                            </Badge>
                                            <Badge v-for="c in row.conflicts" :key="c.kind" variant="destructive">
                                                {{ t('changes.conflict.' + c.kind) }}
                                            </Badge>
                                        </div>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>

                        <!-- 页脚：已展示计数 + 加载更多 -->
                        <div v-if="pageState === 'ready' || pageState === 'refreshing'" class="flex items-center justify-between gap-2 py-2">
                            <span class="text-muted-foreground text-xs">
                                {{ t('changes.shownOf', [items.length, summary?.total ?? 0]) }}
                            </span>
                            <Button v-if="nextCursor" variant="outline" size="sm" :disabled="inflight" @click="loadMore">
                                {{ t('changes.loadMore') }}
                            </Button>
                        </div>
                    </CardContent>
                </Card>
            </template>
        </template>
    </div>
</template>
