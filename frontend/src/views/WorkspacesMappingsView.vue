<script setup lang="ts">
// /workspaces/:id/mappings：受管范围（映射策略）查看与编辑（契约 03 §2.3 GetMappingPolicy /
// UpdateMappingPolicy；UX 原型 §7.4）。只读策略表为默认态；「编辑受管范围」进入页面级编辑模式，
// 多条规则统一提交（UpdateMappingPolicy 乐观锁：expected_revision 取自读取时的 relation_revision）。
// 修订号是内部一致性字段，页面任何位置不展示数字（ADR-0002 决议 3）。
// collision 证据读快照持久化诊断（GetSnapshotDiagnostics），反映最近一次扫描的策略状态。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { DiagnosticDTO, MappingRuleDTO, PolicyDTO } from '../api'
import { bootstrapped, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errorCode, errText } from '../utils/errors'
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

const directionOptions = ['bidirectional', 'project_to_runtime', 'runtime_to_project', 'ignore']
const cols = [
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
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部：工作区上下文 + 编辑/返回 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="page-title">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }}
                    </template>
                    <template v-else>{{ t('mappings.title') }}</template>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">{{ t('mappings.subtitle') }}</p>
            </div>
            <div class="flex shrink-0 gap-2">
                <Button v-if="!editing" size="sm" :disabled="phase !== 'ready' || inflight" @click="startEdit">
                    {{ t('mappings.editAction') }}
                </Button>
                <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                    {{ t('mappings.backToList') }}
                </Button>
            </div>
        </div>

        <!-- 工作区不存在：syncCache 引导完成后仍找不到该关系 -->
        <Card v-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('mappings.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('mappings.backToList') }}
                </Button>
            </CardContent>
        </Card>

        <template v-else>
            <!-- 首查 loading：骨架行（保留表头同构） -->
            <Card v-if="phase === 'loading'">
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

                <!-- 策略集徽标 + 状态提示 -->
                <div class="flex flex-wrap items-center gap-2">
                    <Badge variant="outline" class="text-muted-foreground">
                        {{ t('mappings.policySetLabel') }}：{{ policy.policy_id }}
                    </Badge>
                    <Badge v-if="editing" variant="secondary">{{ t('mappings.editingBadge') }}</Badge>
                    <span v-if="hasActiveTask" class="text-xs text-amber-600 dark:text-amber-400">
                        {{ t('mappings.activeTaskHint') }}
                    </span>
                </div>

                <!-- 编辑模式：规则草稿表 -->
                <Card v-if="editing">
                    <CardContent class="py-2">
                        <Table>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
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
                                            <option v-for="d in directionOptions" :key="d" :value="d">
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

                <!-- 只读模式：已生效规则表 -->
                <Card v-else>
                    <CardContent class="py-2">
                        <Table>
                            <TableHeader>
                                <TableRow>
                                    <TableHead v-for="c in cols" :key="c">{{ t(c) }}</TableHead>
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
                                    <TableCell>{{ t('mappings.direction.' + r.direction) }}</TableCell>
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
                                    <TableCell class="text-muted-foreground text-xs">
                                        {{ t('mappings.materialization.' + r.materialization) }}
                                    </TableCell>
                                    <TableCell class="text-muted-foreground text-xs">
                                        {{ t('mappings.merge.' + r.merge_policy) }}
                                    </TableCell>
                                    <TableCell class="text-muted-foreground text-xs">
                                        {{ t('mappings.runtimeLocal.' + r.runtime_local) }}
                                    </TableCell>
                                    <TableCell class="text-muted-foreground text-xs">—</TableCell>
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

                <!-- 建议纳管（尚未生效的模板；编辑模式可一键并入草稿） -->
                <Card v-if="pendingSuggestions.length">
                    <CardContent class="flex flex-col gap-2 py-3">
                        <div class="flex items-center gap-2">
                            <span class="text-sm font-medium">{{ t('mappings.suggestionsTitle') }}</span>
                            <span class="text-muted-foreground text-xs">{{ t('mappings.suggestionsHint') }}</span>
                        </div>
                        <div class="flex flex-wrap gap-2">
                            <div
                                v-for="s in pendingSuggestions"
                                :key="s.id"
                                class="flex items-center gap-2 rounded-md border px-2 py-1"
                            >
                                <div>
                                    <div class="text-sm">{{ t('workspacesNew.suggestions.' + s.id) }}</div>
                                    <div class="text-muted-foreground font-mono text-xs">
                                        {{ s.project_prefix }} → {{ s.runtime_prefix }}
                                    </div>
                                </div>
                                <Button v-if="editing" variant="outline" size="xs" @click="addSuggestion(s)">
                                    {{ t('mappings.suggestionAdd') }}
                                </Button>
                                <Badge v-else variant="secondary">{{ t('mappings.suggestionInactive') }}</Badge>
                            </div>
                        </div>
                    </CardContent>
                </Card>

                <!-- 诊断证据（最近一次扫描的持久化诊断） -->
                <Card>
                    <CardContent class="flex flex-col gap-2 py-3">
                        <div class="flex items-center gap-2">
                            <span class="text-sm font-medium">{{ t('mappings.diagnosticsTitle') }}</span>
                            <span class="text-muted-foreground text-xs">{{ t('mappings.diagnosticsHint') }}</span>
                        </div>
                        <template v-if="diagnosticsState === 'loaded'">
                            <!-- collision：阻断性证据独立列出（哪两条规则并列、命中哪个路径） -->
                            <div
                                v-for="(c, i) in collisions"
                                :key="'c' + i"
                                class="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm"
                            >
                                <div class="text-destructive">{{ t(c.code, c.args ?? []) }}</div>
                                <div class="text-muted-foreground font-mono text-xs">{{ c.relative_path }}</div>
                            </div>
                            <div v-if="otherDiags.length" class="flex flex-wrap gap-1">
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
                            <p v-if="!diagnostics.length" class="text-muted-foreground text-sm">
                                {{ t('mappings.diagnosticsClean') }}
                            </p>
                        </template>
                        <p v-else-if="diagnosticsState === 'notScanned'" class="text-muted-foreground text-sm">
                            {{ t('mappings.diagnosticsNotScanned') }}
                        </p>
                    </CardContent>
                </Card>
            </template>
        </template>
    </div>
</template>
