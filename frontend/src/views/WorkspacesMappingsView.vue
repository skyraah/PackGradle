<script setup lang="ts">
// /workspaces/:id/mappings：受管范围（映射策略）查看与编辑（契约 03 §2.3 GetMappingPolicy /
// UpdateMappingPolicy；UX 原型 §7.4 画板 M-01，票 #108）。
// 头部走共享工作区对象头 WorkspaceObjectHead（变化|受管范围页签反转、受管范围激活，
// 主操作=编辑受管范围，规范五项菜单）；指标条——当前策略、适用资源数（ADR-0002 决议 3 /
// CONTEXT.md：修订号不在 UI 展示，画板 M-01 的 revision 指标不还原，票 #108 偏差记录）。
// 只读策略表 7 列（资源类别/Project 范围/Runtime 范围/方向/Include-Exclude 摘要/物化方式/
// 状态）；「编辑受管范围」进入页面级编辑模式，多条规则统一提交（UpdateMappingPolicy
// 乐观锁：expected_revision 取自读取时的 relation_revision）。
// 修订号是内部一致性字段，页面任何位置不展示数字（ADR-0002 决议 3）。
// collision 证据读快照持久化诊断（GetSnapshotDiagnostics），反映最近一次扫描的策略状态。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { CircleAlert } from '@lucide/vue'
import { Browser, Clipboard } from '@wailsio/runtime'
import { SyncService } from '../api'
import type { DiagnosticDTO, MappingRuleDTO, PolicyDTO, WorkspaceDTO } from '../api'
import { bootstrapped, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errorCode, errText } from '../utils/errors'
import { DIFF_TONES, HEALTH_TONES, latestScanText, toneOf } from '../utils/pageState'
import { availabilityReasonText, canQuickUpdate } from '../utils/plans'
import { useWorkspaceHeadTabs } from '../composables/useWorkspaceHeadTabs'
import WorkspaceObjectHead from '../components/common/WorkspaceObjectHead.vue'
import type { HeadBadge, HeadMenuItem } from '../components/common/WorkspaceObjectHead.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))

// —— 策略查询快照（编辑草稿独立保存，保存成功前不回写）——
const policy = ref<PolicyDTO | null>(null)
const phase = ref<'loading' | 'error' | 'ready'>('loading')
const inflight = ref(false)
const errorMsg = ref('')
const editing = ref(false)
const saving = ref(false)
const confirming = ref(false) // 统一提交前的保存确认条
const staleBanner = ref(false)

// 编辑草稿：可编辑面 = 受管目录（两侧前缀）、方向、排除规则；exclude 以逐行
// 文本编辑（一行一条 glob）。mod 规则前缀恒为 mods（编译器保留），不可删除。
interface DraftRule {
    id: string
    resource_kind: string
    project_prefix: string
    runtime_prefix: string
    include: string[]
    excludeText: string
    direction: string
    materialization: string
    merge_policy: string
    runtime_local: string
    isMod: boolean
}
const draft = ref<DraftRule[]>([])

function toDraft(r: MappingRuleDTO): DraftRule {
    return {
        id: r.id,
        resource_kind: r.resource_kind,
        project_prefix: r.project_prefix,
        runtime_prefix: r.runtime_prefix,
        include: r.include ?? [],
        excludeText: (r.exclude ?? []).join('\n'),
        direction: r.direction,
        materialization: r.materialization,
        merge_policy: r.merge_policy,
        runtime_local: r.runtime_local,
        isMod: r.resource_kind === 'mod',
    }
}

function ruleDTO(d: DraftRule): MappingRuleDTO {
    return {
        id: d.id,
        resource_kind: d.resource_kind,
        project_prefix: d.project_prefix.trim(),
        runtime_prefix: d.runtime_prefix.trim(),
        include: d.include,
        exclude: d.excludeText.split('\n').map(s => s.trim()).filter(Boolean),
        direction: d.direction,
        materialization: d.materialization,
        merge_policy: d.merge_policy,
        runtime_local: d.runtime_local,
    }
}

async function loadPolicy(): Promise<void> {
    inflight.value = true
    try {
        policy.value = await SyncService.GetMappingPolicy(relationID.value)
        draft.value = (policy.value.rules ?? []).map(toDraft)
        phase.value = 'ready'
        errorMsg.value = ''
        staleBanner.value = false
    } catch (e) {
        phase.value = 'error'
        errorMsg.value = errText(e)
    } finally {
        inflight.value = false
    }
    void loadDiagnostics()
}

// —— 建议纳管（后端建议模板中尚未生效的部分；编辑模式下可一键并入草稿）——
const suggestions = ref<MappingRuleDTO[]>([])
const activeIDs = computed(() => {
    const src = editing.value ? draft.value : (policy.value?.rules ?? [])
    return new Set(src.map(r => r.id))
})
const pendingSuggestions = computed(() => suggestions.value.filter(s => !activeIDs.value.has(s.id)))

function addSuggestion(s: MappingRuleDTO): void {
    if (activeIDs.value.has(s.id)) return
    draft.value = [...draft.value, toDraft(s)]
}

function removeRule(idx: number): void {
    draft.value = draft.value.filter((_, i) => i !== idx)
}

function startEdit(): void {
    draft.value = (policy.value?.rules ?? []).map(toDraft)
    staleBanner.value = false
    editing.value = true
}

function cancelEdit(): void {
    editing.value = false
    confirming.value = false
}

// —— 保存：统一提交（确认条 → UpdateMappingPolicy 乐观锁）——
async function save(): Promise<void> {
    if (!policy.value || saving.value) return
    saving.value = true
    try {
        const saved = await SyncService.UpdateMappingPolicy({
            relation_id: relationID.value,
            expected_revision: policy.value.relation_revision ?? 0,
            rules: draft.value.map(ruleDTO),
        })
        policy.value = saved
        draft.value = (saved.rules ?? []).map(toDraft)
        editing.value = false
        confirming.value = false
        staleBanner.value = false
        showSnackbar(t('mappings.savedToast'), 'success')
        triggerRequery() // 策略修改递增关系修订（旧计划立即过期），走受控重查刷新列表缓存
    } catch (e) {
        if (errorCode(e) === 'err.mapping.stale_revision') {
            // 乐观锁失败：本地编辑基于旧版本，退出编辑并引导重新加载
            staleBanner.value = true
            editing.value = false
            confirming.value = false
        } else {
            // 编译失败等：留在编辑模式，用户按错误定位修复后重试
            showSnackbar(errText(e), 'error')
        }
    } finally {
        saving.value = false
    }
}

// —— 工作区上下文（读 syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)
const hasActiveTask = computed(() => !!wsRow.value?.state.active_task_id)

// —— 对象头视图模型（副行徽章 / 适配器 / 最近扫描，口径与变化页一致）——
const healthBadge = computed<HeadBadge | null>(() => {
    const w = wsRow.value
    if (!w) return null
    return { label: t('workspaces.health.' + w.relation.health), tone: toneOf(HEALTH_TONES, w.relation.health) }
})
const diffBadge = computed<HeadBadge | null>(() => {
    const w = wsRow.value
    if (!w) return null
    return { label: t('workspaces.diffState.' + w.state.diff_state), tone: toneOf(DIFF_TONES, w.state.diff_state) }
})
const adaptersText = computed(() => {
    const w = wsRow.value
    return w ? `${w.relation.project.adapter} · ${w.relation.runtime.adapter}` : ''
})
const lastScanText = computed(() => {
    const w = wsRow.value
    if (!w) return ''
    return latestScanText(w.latest_project_snapshot?.captured_at, w.latest_runtime_snapshot?.captured_at)
})

// 页签「变化 | 受管范围 | 历史」三常驻（拍板 Q8-a）：受管范围页激活（画板 M-01，票 #108）
const tabs = useWorkspaceHeadTabs(() => relationID.value, 'mappings')

// 主操作 = 编辑受管范围（画板 M-01 注记：编辑能力已实现时才出现入口，P1 只读表为默认态；
// 编辑中主操作让位给页内保存/放弃操作条）
const headPrimary = computed(() => {
    if (editing.value) return null
    return { label: t('mappings.editAction'), disabled: phase.value !== 'ready' || inflight.value }
})

// —— 「更多」菜单（规范五项，与变化页同口径；行为本页接线）——
const updating = ref(false)

// 快速更新（契约 06）：一次点击单调用——链在后端；awaiting_confirmation → 待确认计划页
async function runQuickUpdate(): Promise<void> {
    if (updating.value) return
    updating.value = true
    try {
        const res = await SyncService.QuickUpdate(relationID.value)
        triggerRequery()
        if (res.outcome === 'no_diff') {
            showSnackbar(t('workspaces.quickUpdate.upToDate'), 'success')
        } else if (res.outcome === 'apply_started') {
            showSnackbar(t('workspaces.quickUpdate.directToast'), 'success')
        } else {
            showSnackbar(t('workspaces.quickUpdate.manualToast'), 'warning')
            await router.push('/workspaces/' + relationID.value + '/plans/' + res.plan_id)
        }
    } catch (e) {
        showSnackbar(errText(e), 'error')
        triggerRequery()
    } finally {
        updating.value = false
    }
}

// 打开端点位置：项目源根目录交系统打开（同变化页 open-endpoint 口径）
async function openEndpoint(): Promise<void> {
    const path = wsRow.value?.relation.project.root_path
    if (!path) return
    try {
        await Browser.OpenURL(path)
    } catch (e) {
        showSnackbar(t('changes.openEndpoint.failed') + '：' + errText(e), 'error')
    }
}

// 复制诊断信息：读双端最新快照的持久化诊断 + 工作区状态摘要写入系统剪贴板
// （与变化页 copy-diagnostics 同源同格式）
function buildDiagnosticText(w: WorkspaceDTO, diags: DiagnosticDTO[]): string {
    const lines = [
        t('changes.diag.header'),
        t('changes.diag.workspace', [w.relation.project.display_name, w.relation.runtime.display_name]),
        t('changes.diag.relation', [w.relation.relation_id]),
        t('changes.diag.state', [
            t('workspaces.health.' + w.relation.health),
            t('workspaces.scanState.' + w.state.scan_state),
            t('workspaces.baselineState.' + w.state.baseline_state),
            t('workspaces.diffState.' + w.state.diff_state),
        ]),
    ]
    if (!diags.length) {
        lines.push(t('changes.diag.clean'))
        return lines.join('\n')
    }
    lines.push(t('changes.diag.list', [diags.length]))
    for (const d of diags) {
        lines.push(`- [${d.severity}] ${d.code}${d.detail ? ' · ' + d.detail : ''}`)
    }
    return lines.join('\n')
}

async function copyDiagnostics(): Promise<void> {
    const w = wsRow.value
    if (!w) return
    try {
        const ids = [w.latest_project_snapshot?.snapshot_id, w.latest_runtime_snapshot?.snapshot_id].filter(
            (s): s is string => !!s,
        )
        let diags: DiagnosticDTO[] = []
        if (ids.length) {
            const lists = await Promise.all(ids.map(id => SyncService.GetSnapshotDiagnostics(relationID.value, id)))
            diags = lists.flat().filter((d): d is DiagnosticDTO => !!d)
        }
        await Clipboard.SetText(buildDiagnosticText(w, diags))
        showSnackbar(t('changes.copyDiagnostics.ok'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    }
}

const menuItems = computed<HeadMenuItem[]>(() => [
    {
        id: 'quick-update',
        label: updating.value ? t('workspaces.quickUpdate.busy') : t('objHead.menu.quickUpdate'),
        disabled: !canQuickUpdate(wsRow.value) || updating.value,
        title: availabilityReasonText(wsRow.value, 'quick_update') || undefined,
    },
    { id: 'rebind', label: t('objHead.menu.rebind') },
    { id: 'settings', label: t('objHead.menu.settings') },
    { id: 'open-endpoint', label: t('objHead.menu.openEndpoint') },
    { id: 'copy-diagnostics', label: t('objHead.menu.copyDiagnostics') },
])

function onMenu(id: string): void {
    if (id === 'quick-update') void runQuickUpdate()
    else if (id === 'rebind') void router.push('/workspaces/' + relationID.value + '/rebind')
    else if (id === 'settings') void router.push('/workspaces/' + relationID.value + '/settings')
    else if (id === 'open-endpoint') void openEndpoint()
    else if (id === 'copy-diagnostics') void copyDiagnostics()
}

// —— 诊断证据（快照持久化诊断：mapping collision 与扫描诊断）——
const diagnostics = ref<DiagnosticDTO[]>([])
const diagnosticsState = ref<'pending' | 'notScanned' | 'loaded'>('pending')
const collisions = computed(() => diagnostics.value.filter(d => d.code === 'diag.mapping.collision'))
const otherDiags = computed(() => diagnostics.value.filter(d => d.code !== 'diag.mapping.collision'))

async function loadDiagnostics(): Promise<void> {
    diagnosticsState.value = 'pending'
    diagnostics.value = []
    const ids = [
        wsRow.value?.latest_project_snapshot?.snapshot_id,
        wsRow.value?.latest_runtime_snapshot?.snapshot_id,
    ].filter((s): s is string => !!s)
    if (!ids.length) {
        diagnosticsState.value = 'notScanned'
        return
    }
    try {
        const lists = await Promise.all(ids.map(id => SyncService.GetSnapshotDiagnostics(relationID.value, id)))
        diagnostics.value = lists.flat().filter((d): d is DiagnosticDTO => !!d)
        diagnosticsState.value = 'loaded'
    } catch {
        // 证据加载失败不打断页面主功能：保持 pending（不渲染证据区内容）
    }
}

// 路由切换工作区 → 全量重查
watch(relationID, loadPolicy, { immediate: true })
// 引导完成/重扫后快照变化 → 刷新证据（证据反映最近一次扫描）
watch(
    () => [wsRow.value?.latest_project_snapshot?.snapshot_id, wsRow.value?.latest_runtime_snapshot?.snapshot_id],
    () => {
        if (phase.value === 'ready') void loadDiagnostics()
    },
)
// 建议模板非关键路径：失败静默（仅建议区不可用）
void SyncService.ListPolicySuggestions()
    .then(ss => (suggestions.value = ss ?? []))
    .catch(() => (suggestions.value = []))

// —— 指标条（画板 M-01：当前策略 / 适用资源；修订号按 ADR-0002 决议 3 不展示）——
const resourceCountText = computed(() => {
    const w = wsRow.value
    const n = w?.latest_project_snapshot?.resource_count ?? w?.latest_runtime_snapshot?.resource_count
    return typeof n === 'number' ? n.toLocaleString() : '—'
})

// —— 表列（编辑模式 9 列草稿表 / 只读 7 列生效表，画板 M-01）——
const editCols = [
    'mappings.colKind',
    'mappings.colProject',
    'mappings.colRuntime',
    'mappings.colDirection',
    'mappings.colFilters',
    'mappings.colMaterialization',
    'mappings.colMerge',
    'mappings.colRuntimeLocal',
    'mappings.colActions',
]
const viewCols = [
    'mappings.colKind',
    'mappings.colProject',
    'mappings.colRuntime',
    'mappings.colDirection',
    'mappings.colFilters',
    'mappings.colMaterialization',
    'mappings.colStatus',
]

// 规则状态（画板 M-01 状态列）：碰撞诊断涉及该规则前缀 → 规则冲突（阻断），否则生效中。
// 碰撞证据的 args = 两条互相冲突的规则 glob（diag.mapping.collision 契约）。
const collisionPatterns = computed(() => {
    const set = new Set<string>()
    for (const d of collisions.value) for (const a of d.args ?? []) set.add(a)
    return set
})
function ruleCollided(r: MappingRuleDTO): boolean {
    return collisionPatterns.value.has(r.project_prefix) || collisionPatterns.value.has(r.runtime_prefix)
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- syncCache 引导失败：统一错误态可重试（否则卡骨架无出口） -->
        <Card v-if="!bootstrapped && !wsRow">
            <CardContent class="flex flex-col gap-2 py-3">
                <div v-for="i in 2" :key="i" class="h-4 w-64 animate-pulse rounded bg-muted"></div>
            </CardContent>
        </Card>

        <!-- 工作区不存在：syncCache 引导完成后仍找不到该关系 -->
        <Card v-else-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('mappings.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('mappings.backToList') }}
                </Button>
            </CardContent>
        </Card>

        <template v-else>
            <!-- 对象头（共享组件）：页签反转为受管范围激活，主操作=编辑受管范围 -->
            <WorkspaceObjectHead
                v-if="wsRow"
                :project="wsRow.relation.project.display_name"
                :runtime="wsRow.relation.runtime.display_name"
                :health-badge="healthBadge"
                :diff-badge="diffBadge"
                :adapters="adaptersText"
                :last-scan="lastScanText"
                :primary-action="headPrimary"
                :menu-items="menuItems"
                :tabs="tabs"
                active-tab="mappings"
                @primary="startEdit"
                @menu="onMenu"
            />
            <h1 v-else class="page-title">{{ t('mappings.title') }}</h1>

            <!-- 首查 loading：骨架行（保留表头同构） -->
            <Card v-if="phase === 'loading'">
                <CardContent class="py-2">
                    <Table>
                        <TableHeader>
                            <TableRow>
                                <TableHead v-for="c in viewCols" :key="c">{{ t(c) }}</TableHead>
                            </TableRow>
                        </TableHeader>
                        <TableBody>
                            <TableRow v-for="i in 3" :key="i">
                                <TableCell v-for="c in viewCols" :key="c">
                                    <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                </TableCell>
                            </TableRow>
                        </TableBody>
                    </Table>
                </CardContent>
            </Card>

            <!-- 首查失败：错误态可重试 -->
            <Card v-else-if="phase === 'error'">
                <CardContent class="flex items-center justify-between gap-3 py-4">
                    <span class="text-destructive text-sm">{{ t('mappings.errorTitle') }}：{{ errorMsg }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="loadPolicy">
                        {{ t('mappings.retry') }}
                    </Button>
                </CardContent>
            </Card>

            <template v-else-if="policy">
                <!-- 乐观锁冲突横幅（不带修订号数字，ADR-0002 决议 3） -->
                <div v-if="staleBanner" class="flex items-center justify-between gap-3 rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2">
                    <span class="text-sm text-amber-600 dark:text-amber-400">{{ t('mappings.staleBanner') }}</span>
                    <Button variant="outline" size="sm" :disabled="inflight" @click="loadPolicy">
                        {{ t('mappings.staleReload') }}
                    </Button>
                </div>

                <!-- 保存确认条（统一提交前的一次确认） -->
                <div v-if="confirming" class="flex flex-wrap items-center justify-between gap-3 rounded-md border px-3 py-2">
                    <span class="text-sm">{{ t('mappings.confirmText') }}</span>
                    <div class="flex gap-2">
                        <Button size="sm" :disabled="saving" @click="save">{{ t('mappings.confirmSave') }}</Button>
                        <Button variant="ghost" size="sm" :disabled="saving" @click="confirming = false">
                            {{ t('mappings.confirmCancel') }}
                        </Button>
                    </div>
                </div>

                <!-- 指标条（画板 M-01：当前策略 / 适用资源；修订号按 ADR-0002 不展示） -->
                <div
                    class="flex flex-wrap items-center gap-x-5 gap-y-1.5 rounded-lg border border-border bg-card px-3 py-2 text-xs text-muted-foreground"
                >
                    <span>
                        {{ t('mappings.metric.policy') }}
                        <b class="text-foreground font-semibold">{{ policy.policy_id }}</b>
                    </span>
                    <span>
                        {{ t('mappings.metric.resources') }}
                        <b class="text-foreground font-semibold">{{ resourceCountText }}</b>
                    </span>
                    <Badge v-if="editing" variant="secondary">{{ t('mappings.editingBadge') }}</Badge>
                    <span v-if="hasActiveTask" class="text-xs text-amber-600 dark:text-amber-400">
                        {{ t('mappings.activeTaskHint') }}
                    </span>
                </div>

                <!-- 编辑模式：规则草稿表（可编辑能力不变） -->
                <Card v-if="editing">
                    <CardContent class="py-2">
                        <Table>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in editCols" :key="c">{{ t(c) }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="(r, idx) in draft" :key="r.id">
                                    <TableCell>
                                        <Badge variant="outline">{{ t('mappings.kind.' + r.resource_kind) }}</Badge>
                                        <div class="text-muted-foreground mt-1 font-mono text-xs">{{ r.id }}</div>
                                    </TableCell>
                                    <TableCell>
                                        <Input
                                            v-model="r.project_prefix"
                                            class="w-36"
                                            :disabled="r.isMod"
                                            :title="r.isMod ? t('mappings.modPrefixFixed') : ''"
                                        />
                                    </TableCell>
                                    <TableCell>
                                        <Input
                                            v-model="r.runtime_prefix"
                                            class="w-36"
                                            :disabled="r.isMod"
                                            :title="r.isMod ? t('mappings.modPrefixFixed') : ''"
                                        />
                                    </TableCell>
                                    <TableCell>
                                        <select
                                            v-model="r.direction"
                                            class="border-input h-9 rounded-md border bg-transparent px-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                                        >
                                            <option v-for="d in ['bidirectional', 'project_to_runtime', 'runtime_to_project', 'ignore']" :key="d" :value="d">
                                                {{ t('mappings.direction.' + d) }}
                                            </option>
                                        </select>
                                    </TableCell>
                                    <TableCell>
                                        <textarea
                                            v-model="r.excludeText"
                                            rows="2"
                                            class="border-input min-w-44 rounded-md border bg-transparent px-2 py-1 text-sm shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                                            :placeholder="t('mappings.excludePlaceholder')"
                                        ></textarea>
                                    </TableCell>
                                    <TableCell class="text-muted-foreground text-xs">
                                        {{ t('mappings.materialization.' + r.materialization) }}
                                    </TableCell>
                                    <TableCell class="text-muted-foreground text-xs">
                                        {{ t('mappings.merge.' + r.merge_policy) }}
                                    </TableCell>
                                    <TableCell class="text-muted-foreground text-xs">
                                        {{ t('mappings.runtimeLocal.' + r.runtime_local) }}
                                    </TableCell>
                                    <TableCell>
                                        <Button
                                            v-if="!r.isMod"
                                            variant="ghost"
                                            size="sm"
                                            class="text-destructive"
                                            @click="removeRule(idx)"
                                        >
                                            {{ t('mappings.removeRule') }}
                                        </Button>
                                        <span v-else class="text-muted-foreground text-xs">—</span>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>
                        <p class="text-muted-foreground px-2 py-2 text-xs">{{ t('mappings.editHint') }}</p>
                    </CardContent>
                </Card>

                <!-- 只读模式：已生效规则表 7 列（画板 M-01；状态列按碰撞诊断着色） -->
                <Card v-else>
                    <CardContent class="py-2">
                        <Table>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in viewCols" :key="c">{{ t(c) }}</TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="r in policy.rules ?? []" :key="r.id">
                                    <TableCell>
                                        <Badge variant="outline">{{ t('mappings.kind.' + r.resource_kind) }}</Badge>
                                        <div class="text-muted-foreground mt-1 font-mono text-xs">{{ r.id }}</div>
                                    </TableCell>
                                    <TableCell class="font-mono text-sm">{{ r.project_prefix || '—' }}</TableCell>
                                    <TableCell class="font-mono text-sm">{{ r.runtime_prefix || '—' }}</TableCell>
                                    <TableCell class="text-xs whitespace-nowrap">{{ t('mappings.direction.' + r.direction) }}</TableCell>
                                    <TableCell>
                                        <div
                                            v-if="(r.exclude?.length ?? 0) + (r.include?.length ?? 0) > 0"
                                            class="max-w-52 truncate font-mono text-xs"
                                            :title="[...(r.include ?? []), ...(r.exclude ?? [])].join('\n')"
                                        >
                                            <template v-if="(r.include ?? []).length">in: {{ r.include!.join(' ') }}</template>
                                            <template v-if="(r.include ?? []).length && (r.exclude ?? []).length"> · </template>
                                            <template v-if="(r.exclude ?? []).length">ex: {{ r.exclude!.join(' ') }}</template>
                                        </div>
                                        <span v-else class="text-muted-foreground text-xs">{{ t('mappings.noFilters') }}</span>
                                    </TableCell>
                                    <TableCell class="text-xs whitespace-nowrap">
                                        {{ t('mappings.materialization.' + r.materialization) }}
                                    </TableCell>
                                    <TableCell>
                                        <Badge v-if="ruleCollided(r)" variant="st-err" :title="t('diag.mapping.collision', [...(collisions[0]?.args ?? [])])">
                                            {{ t('mappings.status.collision') }}
                                        </Badge>
                                        <Badge v-else variant="st-ok">{{ t('mappings.status.healthy') }}</Badge>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>
                    </CardContent>
                </Card>

                <!-- 编辑模式操作条 -->
                <div v-if="editing" class="flex flex-wrap items-center gap-2">
                    <Button :disabled="saving" @click="confirming = true">{{ t('mappings.saveAction') }}</Button>
                    <Button variant="outline" :disabled="saving" @click="cancelEdit">
                        {{ t('mappings.cancelEdit') }}
                    </Button>
                </div>

                <!-- 建议纳管（画板 M-01：区表——资源类别/方向/默认物化/说明/操作） -->
                <Card v-if="pendingSuggestions.length">
                    <CardContent class="flex flex-col gap-2 py-3">
                        <div class="flex items-center gap-2">
                            <span class="text-sm font-medium">{{ t('mappings.suggestionsTitle') }}</span>
                            <span class="text-muted-foreground text-xs">{{ t('mappings.suggestionsHint') }}</span>
                        </div>
                        <Table>
                            <TableHeader>
                                <TableRow>
                                    <TableHead>{{ t('mappings.colKind') }}</TableHead>
                                    <TableHead>{{ t('mappings.colDirection') }}</TableHead>
                                    <TableHead>{{ t('mappings.colDefaultMaterialization') }}</TableHead>
                                    <TableHead>{{ t('mappings.colDescription') }}</TableHead>
                                    <TableHead class="text-right"></TableHead>
                                </TableRow>
                            </TableHeader>
                            <TableBody>
                                <TableRow v-for="s in pendingSuggestions" :key="s.id">
                                    <TableCell class="font-mono text-xs font-semibold">{{ s.id }}</TableCell>
                                    <TableCell class="text-xs whitespace-nowrap">{{ t('mappings.direction.' + s.direction) }}</TableCell>
                                    <TableCell class="text-xs whitespace-nowrap">{{ t('mappings.materialization.' + s.materialization) }}</TableCell>
                                    <TableCell class="text-xs">
                                        {{ t('workspacesNew.suggestions.' + s.id) }}
                                        <span class="text-muted-foreground">· {{ t('mappings.kind.' + s.resource_kind) }}</span>
                                    </TableCell>
                                    <TableCell class="text-right whitespace-nowrap">
                                        <Button v-if="editing" variant="outline" size="xs" @click="addSuggestion(s)">
                                            {{ t('mappings.suggestionAdd') }}
                                        </Button>
                                        <Badge v-else variant="secondary">{{ t('mappings.suggestionInactive') }}</Badge>
                                    </TableCell>
                                </TableRow>
                            </TableBody>
                        </Table>
                    </CardContent>
                </Card>

                <!-- 诊断面板（画板 M-01：mapping collision 为阻断性错误，面板错误描边） -->
                <div class="rounded-lg border bg-card px-3.5 py-3" :class="collisions.length ? 'border-tint-error' : ''">
                    <div class="mb-1 flex flex-wrap items-baseline gap-2">
                        <span class="text-sm font-medium">{{ t('mappings.diagnosticsTitle') }}</span>
                        <span class="text-muted-foreground text-xs">{{ t('mappings.diagnosticsBlocking') }}</span>
                    </div>
                    <template v-if="diagnosticsState === 'loaded'">
                        <!-- collision：阻断性证据独立列出（哪两条规则并列、命中哪个路径） -->
                        <div
                            v-for="(c, i) in collisions"
                            :key="'c' + i"
                            class="flex items-start gap-2.5 border-t py-2 text-[12.5px] first:border-t-0"
                        >
                            <CircleAlert class="text-error mt-0.5 size-4 flex-none" aria-hidden="true" />
                            <div class="min-w-0">
                                <span><b class="font-semibold">{{ t('mappings.collisionLabel') }}</b> · {{ t(c.code, c.args ?? []) }}</span>
                                <div v-if="c.relative_path" class="text-muted-foreground font-mono text-xs">{{ c.relative_path }}</div>
                            </div>
                        </div>
                        <div v-if="otherDiags.length" class="flex flex-wrap gap-1 border-t pt-2">
                            <Badge
                                v-for="(d, i) in otherDiags"
                                :key="'d' + i"
                                variant="outline"
                                class="text-amber-600 dark:text-amber-400"
                                :title="d.detail"
                            >
                                {{ t(d.code, d.args ?? []) }}
                                <template v-if="d.relative_path"> · {{ d.relative_path }}</template>
                            </Badge>
                        </div>
                        <p v-if="!diagnostics.length" class="text-muted-foreground border-t py-2 text-sm">
                            {{ t('mappings.diagnosticsClean') }}
                        </p>
                    </template>
                    <p v-else-if="diagnosticsState === 'notScanned'" class="text-muted-foreground border-t py-2 text-sm">
                        {{ t('mappings.diagnosticsNotScanned') }}
                    </p>
                </div>
            </template>
        </template>
    </div>
</template>
