<script setup lang="ts">
// /workspaces/:id/rebind：重绑（契约 03 §2.4 PrepareRebind/ApplyRebind；UX 原型
// R-01..R-03 画板，票 #109）。双栏布局：左「当前登记」kv 面板，右「候选端点」
// 候选卡列表（同侧已登记端点，radio + 路径 + 指纹摘要 + 健康徽章，复用 #106
// CandidateCard）；下方「预检（PrepareRebind）」面板随选中候选即时刷新——
// 可证明等价（baseline_inheritance=inherit）出全绿行，不可证明出后果说明行。
// 确认走 DangerConfirmDialog 危险弹窗（红色左边条 + 四要素正文，#110 回滚复用）；
// ApplyRebind 成功后触发受控重查并进入变化页（ADR-0003 单事务，恒 reinitialize）。
// 候选健康读 stores/endpoints 会话缓存（切页往返不丢，票 #109），进页自动补查。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
    BoxIcon,
    CheckIcon,
    CircleAlertIcon,
    FolderIcon,
    LoaderCircleIcon,
    PlusIcon,
    ShieldCheckIcon,
} from '@lucide/vue'
import { ProjectService, RuntimeService, SyncService } from '../api'
import type { EndpointDTO, RebindPreparationDTO } from '../api'
import CandidateCard from '../components/common/CandidateCard.vue'
import DangerConfirmDialog from '../components/common/DangerConfirmDialog.vue'
import { useEndpointPage } from '../composables/useEndpointPage'
import { bootstrapped, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText, errorCode } from '../utils/errors'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))

// —— 工作区上下文（syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)
const relationLabel = computed(() => {
    const rel = wsRow.value?.relation
    return rel ? `${rel.project.display_name} ↔ ${rel.runtime.display_name}` : ''
})

// —— 候选端点（一次只重绑一侧；两侧各装配一个页面组合式函数，
//      健康结果落在同一张会话缓存上，切侧零开销）——
const side = ref<'project' | 'runtime'>('project')
const projectsPage = useEndpointPage({
    list: () => ProjectService.ListProjects(),
    register: rootPath => ProjectService.RegisterProject({ root_path: rootPath }),
    health: endpointID => ProjectService.GetProjectHealth(endpointID),
})
const runtimesPage = useEndpointPage({
    list: () => RuntimeService.ListRuntimes(),
    register: rootPath => RuntimeService.RegisterRuntime({ root_path: rootPath }),
    health: endpointID => RuntimeService.GetRuntimeHealth(endpointID),
})
const activePage = computed(() => (side.value === 'project' ? projectsPage : runtimesPage))
const candidates = computed(() => activePage.value.registered.value)
const sideLabel = computed(() => t(side.value === 'project' ? 'rebind.sideProject' : 'rebind.sideRuntime'))

// 当前登记：按侧别取对应端点（重绑页打开时 syncCache 可能仍在引导，wsRow 可为空）
const currentEndpoint = computed(() => {
    const rel = wsRow.value?.relation
    if (!rel) return null
    return side.value === 'project' ? rel.project : rel.runtime
})

// 最后成功扫描：按侧别取最新快照的采集时间（UX 原型 R-01 当前登记栏）
const lastScanAt = computed(() => {
    const ws = wsRow.value
    if (!ws) return ''
    const snap = side.value === 'project' ? ws.latest_project_snapshot : ws.latest_runtime_snapshot
    if (!snap?.captured_at) return ''
    const at = Date.parse(snap.captured_at)
    return Number.isNaN(at) ? '' : new Date(at).toLocaleString()
})

onMounted(async () => {
    await Promise.all([projectsPage.loadRegistered(), runtimesPage.loadRegistered()])
    // 候选健康徽章进页自动补查：会话缓存已查过的直接命中（静默，失败不打扰）
    projectsPage.ensureHealth(projectsPage.registered.value)
    runtimesPage.ensureHealth(runtimesPage.registered.value)
})

function switchSide(next: 'project' | 'runtime') {
    if (side.value === next) return
    side.value = next
    selectedId.value = ''
    prep.value = null
}

// 候选卡副行：登记路径 + 指纹摘要（对比结论与原型一致：与登记一致/不一致）
function candidatePath(cand: EndpointDTO): string {
    return cand.root_path
}

// 绑定指纹摘要：完整哈希不适合直读，展示前后一致性即可（对比结论见预检分支）
function fingerprintSummary(fp: string): string {
    if (!fp) return '—'
    return fp.slice(0, 15) + '…'
}

function candidateFingerprint(cand: EndpointDTO): string {
    const base = `${t('rebind.fpShort')} ${fingerprintSummary(cand.binding_fingerprint)}`
    const cur = currentEndpoint.value
    if (!cur) return base
    const matched = cand.binding_fingerprint === cur.binding_fingerprint
    return `${base}（${t(matched ? 'rebind.fpMatch' : 'rebind.fpMismatch')}）`
}

// —— 预检与应用：选中候选即预检（R-02/R-03 两分支随候选切换即时刷新）——
const selectedId = ref('')
const selectedCandidate = computed(() => candidates.value.find(c => c.id === selectedId.value) ?? null)
const prep = ref<RebindPreparationDTO | null>(null)
const preparing = ref(false)
const applying = ref(false)
let prepareSeq = 0

// 新端点根路径取值沿候选登记投影：project 为 pack.toml 所在目录；runtime 为
// 实例目录（instance_dir，契约 PrepareRebind 输入口径；缺值留空让预检可见报错）
function rootPathFor(cand: EndpointDTO): string {
    return side.value === 'project' ? cand.root_path : cand.instance_dir ?? ''
}

function pick(cand: EndpointDTO) {
    if (selectedId.value === cand.id) return
    selectedId.value = cand.id
    void prepareSelected()
}

async function prepareSelected() {
    const cand = selectedCandidate.value
    if (!cand || applying.value) return
    const seq = ++prepareSeq
    preparing.value = true
    prep.value = null
    try {
        const result = await SyncService.PrepareRebind({
            relation_id: relationID.value,
            side: side.value,
            root_path: rootPathFor(cand),
        })
        if (seq === prepareSeq) prep.value = result
    } catch (e) {
        if (seq === prepareSeq) {
            prep.value = null
            showSnackbar(errText(e), 'error')
        }
    } finally {
        if (seq === prepareSeq) preparing.value = false
    }
}

// 可证明等价：后端结论 baseline_inheritance=inherit（指纹一致），Baseline 可继承
const provable = computed(() => prep.value?.baseline_inheritance === 'inherit')
const checks = computed(() => prep.value?.checks ?? [])
const blockingFailed = computed(() =>
    checks.value.some(c => c.severity === 'blocking' && !c.passed),
)
// 预检有效性：blocking 全过且未过期（过期由前端隐藏主操作，后端守卫兜底）
const prepExpired = computed(() => {
    const exp = prep.value ? Date.parse(prep.value.expires_at) : NaN
    return prep.value !== null && (Number.isNaN(exp) || Date.now() >= exp)
})
const canConfirm = computed(() =>
    prep.value !== null && !preparing.value && !applying.value && !blockingFailed.value && !prepExpired.value,
)

// 预检行（原型 pf-row 形态）：检查项逐条 + 分支收尾行
interface PreflightRow {
    key: string
    tone: 'ok' | 'warn' | 'err'
    text: string
    detail?: string
}
const preflightRows = computed<PreflightRow[]>(() => {
    if (!prep.value) return []
    const rows: PreflightRow[] = checks.value.map((c, i) => ({
        key: `${c.code}:${i}`,
        tone: c.passed ? 'ok' : c.severity === 'blocking' ? 'err' : 'warn',
        text: t(c.code, c.args ?? []),
        detail: c.detail,
    }))
    if (provable.value) {
        rows.push({ key: 'baseline', tone: 'ok', text: t('rebind.checkBaselineInherit') })
        rows.push({
            key: 'plans',
            tone: 'ok',
            text: t('rebind.checkAffectedPlans', [prep.value.invalidated_plan_count]),
        })
    } else {
        rows.push({ key: 'consequence', tone: 'err', text: t('rebind.consequenceReset') })
    }
    return rows
})

// —— 危险确认弹窗（四要素：动作、对象、后果、可逆性）——
const confirmOpen = ref(false)
const confirmTarget = computed(() => {
    const cand = selectedCandidate.value
    if (!cand) return ''
    return t('rebind.confirmTarget', [relationLabel.value, sideLabel.value, cand.display_name, candidatePath(cand)])
})
const confirmConsequences = computed(() =>
    provable.value
        ? [
              t('rebind.consequenceInherit1'),
              t('rebind.consequenceInherit2', [prep.value?.invalidated_plan_count ?? 0]),
          ]
        : [t('rebind.consequenceReset1'), t('rebind.consequenceReset2')],
)
const confirmReversibility = computed(() =>
    t(provable.value ? 'rebind.reversibleInherit' : 'rebind.reversibleReset'),
)

// apply 确认重绑：成功后触发受控重查并进入工作区变化页（UX 原型 R 画板）
async function apply() {
    if (!canConfirm.value || !prep.value) return
    applying.value = true
    try {
        await SyncService.ApplyRebind(prep.value.preparation_id)
        confirmOpen.value = false
        triggerRequery()
        showSnackbar(t('rebind.successToast'), 'success')
        await router.push('/workspaces/' + relationID.value + '/changes')
    } catch (e) {
        confirmOpen.value = false
        // 失败保留候选端点与预检证据；预检过期/已消费时清空证据引导重新预检
        const code = errorCode(e)
        if (code === 'err.relation.rebind_prep_expired' || code === 'err.relation.rebind_prep_consumed') {
            prep.value = null
        }
        showSnackbar(errText(e), 'error')
    } finally {
        applying.value = false
    }
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部：工作区上下文 + 返回变化页 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="page-title">
                    <template v-if="wsRow">{{ t('rebind.titlePattern', [relationLabel]) }}</template>
                    <template v-else>{{ t('rebind.title') }}</template>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">{{ t('rebind.subtitle') }}</p>
            </div>
            <Button
                variant="outline"
                size="sm"
                :disabled="applying"
                @click="router.push('/workspaces/' + relationID + '/changes')"
            >
                {{ t('rebind.backToChanges') }}
            </Button>
        </div>

        <!-- 工作区不存在：syncCache 引导完成后仍找不到该关系 -->
        <Card v-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('rebind.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('changes.backToList') }}
                </Button>
            </CardContent>
        </Card>

        <template v-else>
            <!-- 双栏：当前登记 / 候选端点（UX 原型 R-01 rebind-grid） -->
            <div class="grid gap-4 md:grid-cols-2">
                <Card>
                    <CardHeader>
                        <CardTitle class="flex items-center gap-2">
                            <FolderIcon class="text-muted-foreground size-4" />
                            {{ t('rebind.currentTitle') }}
                        </CardTitle>
                        <CardDescription>{{ t('rebind.currentNote') }}</CardDescription>
                    </CardHeader>
                    <CardContent v-if="currentEndpoint" class="flex flex-col gap-1.5 text-sm">
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('endpoints.colName') }}</span>
                            <span class="font-medium">{{ currentEndpoint.display_name }}</span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('endpoints.colAdapter') }}</span>
                            <Badge variant="outline">{{ currentEndpoint.adapter }}</Badge>
                        </div>
                        <div class="flex items-start justify-between gap-3">
                            <span class="text-muted-foreground shrink-0 text-xs">{{ t('endpoints.colPath') }}</span>
                            <span class="max-w-72 truncate text-right font-mono text-xs" :title="currentEndpoint.root_path">
                                {{ currentEndpoint.root_path }}
                            </span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('rebind.lastScan') }}</span>
                            <span class="text-muted-foreground text-xs">{{ lastScanAt || '—' }}</span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('rebind.fingerprintLabel') }}</span>
                            <span class="font-mono text-xs" :title="currentEndpoint.binding_fingerprint">
                                {{ fingerprintSummary(currentEndpoint.binding_fingerprint) }}
                            </span>
                        </div>
                    </CardContent>
                    <CardContent v-else class="text-muted-foreground text-sm">{{ t('endpoints.loading') }}</CardContent>
                </Card>

                <Card>
                    <CardHeader class="flex flex-row items-center justify-between space-y-0">
                        <CardTitle class="flex items-center gap-2">
                            <BoxIcon class="text-muted-foreground size-4" />
                            {{ t('rebind.candidateTitle') }}
                        </CardTitle>
                        <div class="flex gap-1.5">
                            <Button
                                size="sm"
                                :variant="side === 'project' ? 'default' : 'outline'"
                                :disabled="applying"
                                @click="switchSide('project')"
                            >
                                {{ t('rebind.sideProject') }}
                            </Button>
                            <Button
                                size="sm"
                                :variant="side === 'runtime' ? 'default' : 'outline'"
                                :disabled="applying"
                                @click="switchSide('runtime')"
                            >
                                {{ t('rebind.sideRuntime') }}
                            </Button>
                        </div>
                    </CardHeader>
                    <CardContent class="flex flex-col gap-2">
                        <template v-if="candidates.length">
                            <CandidateCard
                                v-for="cand in candidates"
                                :key="cand.id"
                                :name="cand.display_name"
                                :sub="candidatePath(cand)"
                                mono
                                :selected="selectedId === cand.id"
                                :disabled="applying"
                                @select="pick(cand)"
                            >
                                <template #sub>
                                    <span class="text-faint mt-px block truncate font-mono text-xs">
                                        {{ candidateFingerprint(cand) }}
                                    </span>
                                </template>
                                <template #badge>
                                    <Badge
                                        :variant="activePage.healthBadge(cand.id).tone.variant"
                                        :class="activePage.healthBadge(cand.id).tone.class"
                                    >{{ activePage.healthBadge(cand.id).label }}</Badge>
                                </template>
                            </CandidateCard>
                        </template>
                        <div v-else class="flex flex-col items-start gap-2 py-2">
                            <span class="text-muted-foreground text-sm">
                                {{ activePage.loadingList ? t('endpoints.loading') : t('rebind.candidatesEmpty', [sideLabel]) }}
                            </span>
                            <Button
                                v-if="!activePage.loadingList"
                                variant="ghost"
                                size="sm"
                                @click="router.push(side === 'project' ? '/sources' : '/runtimes')"
                            >
                                <PlusIcon class="size-3.5" />{{ t('rebind.goRegister', [sideLabel]) }}
                            </Button>
                        </div>
                    </CardContent>
                </Card>
            </div>

            <!-- 预检（PrepareRebind）：随选中候选即时刷新；未选给提示 -->
            <Card>
                <CardHeader>
                    <CardTitle class="flex items-center gap-2">
                        <ShieldCheckIcon class="text-muted-foreground size-4" />
                        {{ t('rebind.prepTitle') }}
                    </CardTitle>
                    <CardDescription>{{ t('rebind.prepDesc') }}</CardDescription>
                </CardHeader>
                <CardContent class="flex flex-col">
                    <p v-if="!selectedCandidate" class="text-muted-foreground py-2 text-sm">
                        {{ t('rebind.noCandidateHint') }}
                    </p>
                    <p v-else-if="preparing" class="text-muted-foreground flex items-center gap-2 py-2 text-sm">
                        <LoaderCircleIcon class="size-3.5 animate-spin" />
                        {{ t('rebind.preparing') }}
                    </p>
                    <template v-else-if="prep">
                        <!-- 检查行（ok/warn/err 三色，两分支随候选切换） -->
                        <div
                            v-for="row in preflightRows"
                            :key="row.key"
                            class="flex items-start gap-2.5 border-border/60 border-b py-2 text-[13px] last:border-b-0"
                            :title="row.detail || undefined"
                        >
                            <CheckIcon v-if="row.tone === 'ok'" class="text-success mt-0.5 size-3.5 shrink-0" />
                            <CircleAlertIcon v-else-if="row.tone === 'warn'" class="text-warning mt-0.5 size-3.5 shrink-0" />
                            <CircleAlertIcon v-else class="text-error mt-0.5 size-3.5 shrink-0" />
                            <span>{{ row.text }}</span>
                        </div>

                        <!-- 右下主操作：确认重新绑定（危险弹窗二次确认） -->
                        <div class="mt-3 flex items-center justify-end gap-3">
                            <span v-if="prepExpired" class="text-xs text-amber-600 dark:text-amber-400">
                                {{ t('rebind.prepExpired') }}
                            </span>
                            <Button
                                v-if="prepExpired"
                                variant="outline"
                                size="sm"
                                :disabled="preparing || applying"
                                @click="prepareSelected"
                            >
                                {{ t('rebind.prepareBtn') }}
                            </Button>
                            <span v-else-if="blockingFailed" class="text-destructive text-xs">
                                {{ t('rebind.blockedHint') }}
                            </span>
                            <Button v-else :disabled="!canConfirm" @click="confirmOpen = true">
                                {{ applying ? t('rebind.applying') : t('rebind.applyBtn') }}
                            </Button>
                        </div>
                    </template>
                </CardContent>
            </Card>
        </template>

        <!-- 危险确认：红色左边条 + 四要素正文（#110 回滚流程复用同一组件） -->
        <DangerConfirmDialog
            v-model:open="confirmOpen"
            :title="t('rebind.confirmTitle')"
            :action="t('rebind.confirmAction', [sideLabel])"
            :target="confirmTarget"
            :consequences="confirmConsequences"
            :reversibility="confirmReversibility"
            :confirm-label="t('rebind.applyBtn')"
            :confirming="applying"
            @confirm="apply"
        />
    </div>
</template>
