<script setup lang="ts">
// /workspaces/new：新建工作区向导（UX 原型 §7 向导画板 N-01..N-05）。
// 四步骨架：选择项目源 → 选择运行实例 → 选择受管范围 → 确认并创建；左侧 210px
// 步骤导航（当前步主色点、已完成步成功色点 + 对勾，≤900px 转纵向）。
// 语义沿契约 03：建议范围默认不勾选，确认（CreateRelation）前不写入受管范围；
// 创建确认走居中弹窗；创建成功进入 N-05 创建进度页——创建后立即 StartScan
// （原型横幅承诺「创建后将立即扫描两侧端点」），扫描为后台任务、由任务中心
// 追踪，本页以 GetWorkspace 轮询推进三段步骤，完成后出现「查看初始化预览」。
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
    CheckIcon,
    CircleAlertIcon,
    ClockIcon,
    InfoIcon,
    LoaderCircleIcon,
    PlusIcon,
    ShieldCheckIcon,
} from '@lucide/vue'
import { ProjectService, RuntimeService, SyncService } from '../api'
import type {
    EndpointDTO,
    EndpointHealthDTO,
    MappingRuleDTO,
    PreparationCheckDTO,
    RelationDTO,
    RelationPreparationDTO,
    WorkspaceDTO,
} from '../api'
import CandidateCard from '../components/common/CandidateCard.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { showSnackbar } from '../stores/ui'
import { triggerRequery } from '../stores/syncCache'
import { errText } from '../utils/errors'
import { HEALTH_TONES, PAGE_LIMIT, toneOf, type BadgeTone } from '../utils/pageState'

const { t } = useI18n()
const router = useRouter()

// —— 向导四步（步骤导航与各步标题共用同一组文案）——
const STEP_KEYS = [
    'workspacesNew.step1Title',
    'workspacesNew.step2Title',
    'workspacesNew.step3Title',
    'workspacesNew.step4Title',
] as const

// —— 候选数据 ——
const projects = ref<EndpointDTO[]>([])
const runtimes = ref<EndpointDTO[]>([])
const suggestions = ref<MappingRuleDTO[]>([])
const existingWorkspaces = ref<WorkspaceDTO[]>([])
const loading = ref(false)
// endpoint_id -> 健康结果（读接口；取不到则不渲染健康徽章）
const health = reactive(new Map<string, EndpointHealthDTO>())

// —— 向导状态（沿原型 resetWizard：step/src/rt/pol/pre/creating/done）——
const step = ref(0)
const selectedProject = ref<EndpointDTO | null>(null)
const selectedRuntime = ref<EndpointDTO | null>(null)
const selectedPolicy = ref<MappingRuleDTO | null>(null)
const precheckState = ref<'idle' | 'running' | 'done'>('idle')
const prep = ref<RelationPreparationDTO | null>(null)
const confirmOpen = ref(false)

// —— N-05 创建进度 ——
const created = ref<RelationDTO | null>(null)
const scanDone = ref(false)
const scanFailed = ref(false)
let pollTimer: ReturnType<typeof setInterval> | undefined

const checks = computed<PreparationCheckDTO[]>(() => prep.value?.checks ?? [])
const blockingFailed = computed(() =>
    checks.value.some((c) => c.severity === 'blocking' && !c.passed),
)
const pairLabel = computed(() =>
    created.value
        ? `${created.value.project.display_name} ↔ ${created.value.runtime.display_name}`
        : '',
)

onMounted(load)
onBeforeUnmount(stopScanPolling)

async function load() {
    loading.value = true
    try {
        const [ps, rs, ss, ws] = await Promise.all([
            ProjectService.ListProjects(),
            RuntimeService.ListRuntimes(),
            SyncService.ListPolicySuggestions(),
            SyncService.ListWorkspaces('', PAGE_LIMIT),
        ])
        projects.value = ps ?? []
        runtimes.value = rs ?? []
        suggestions.value = ss ?? []
        existingWorkspaces.value = ws?.items ?? []
        // 健康徽章为补充信息：并行拉取、逐个落位，失败静默（无徽章）
        void loadHealth(projects.value, (id) => ProjectService.GetProjectHealth(id))
        void loadHealth(runtimes.value, (id) => RuntimeService.GetRuntimeHealth(id))
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        loading.value = false
    }
}

async function loadHealth(list: EndpointDTO[], check: (id: string) => Promise<EndpointHealthDTO>) {
    await Promise.allSettled(
        list.map(async (ep) => {
            try {
                health.set(ep.id, await check(ep.id))
            } catch {
                // 健康检查失败 → 无结果不渲染徽章（页面主流程不受影响）
            }
        }),
    )
}

// 端点健康 DTO → 工作区健康语义键（ok=正常 / missing=端点失效 /
// identity_mismatch=需要重绑），色调取 pageState 的 HEALTH_TONES 单源（票 #102）
const ENDPOINT_HEALTH_KEYS: Record<string, string> = {
    ok: 'healthy',
    missing: 'endpoint_missing',
    identity_mismatch: 'rebind_required',
}

function endpointHealthBadge(id: string): { label: string; tone: BadgeTone } | null {
    const h = health.get(id)
    if (!h) return null
    const key = ENDPOINT_HEALTH_KEYS[h.status]
    if (!key) return null
    return { label: t('workspaces.health.' + key), tone: toneOf(HEALTH_TONES, key) }
}

// —— 重复组合 / 工作区计数（以已登记端点根路径匹配 ListWorkspaces 投影；
//      后端预检的重复 pair 检查仍是唯一准绳，chip 为前置提示）——
function projectRefs(p: EndpointDTO): number {
    return existingWorkspaces.value.filter((w) => w.relation.project.root_path === p.root_path).length
}

function isPairTaken(r: EndpointDTO): boolean {
    const p = selectedProject.value
    if (!p) return false
    return existingWorkspaces.value.some(
        (w) => w.relation.project.root_path === p.root_path && w.relation.runtime.root_path === r.root_path,
    )
}

// —— 步间流转：换选即失效预检（与旧两段式一致的失效语义）——
function resetPreparation() {
    prep.value = null
    precheckState.value = 'idle'
}

function pickSource(p: EndpointDTO) {
    if (selectedProject.value?.id === p.id) return
    selectedProject.value = p
    selectedRuntime.value = null
    selectedPolicy.value = null
    resetPreparation()
}

function pickRuntime(r: EndpointDTO) {
    if (selectedRuntime.value?.id === r.id) return
    selectedRuntime.value = r
    selectedPolicy.value = null
    resetPreparation()
}

function pickPolicy(s: MappingRuleDTO) {
    if (selectedPolicy.value?.id === s.id) return
    selectedPolicy.value = s
    resetPreparation()
}

function goNext() {
    if (step.value < 3) step.value += 1
}

function goBack() {
    if (step.value > 0) step.value -= 1
}

function cancel() {
    void router.push('/workspaces')
}

// —— 预检（PrepareRelation）：只读端点；建议范围仅展示不勾选（原型拍板语义，
//      勾选行为依赖正式 Mapping 契约），故 suggestions 传空 ——
async function prepare() {
    if (!selectedProject.value || !selectedRuntime.value) return
    precheckState.value = 'running'
    try {
        prep.value = await SyncService.PrepareRelation({
            project_root: selectedProject.value.root_path,
            // 已登记运行实例的登记输入：后端派生的实例目录（缺值时留空，
            // 由预检检查项可见地报「实例目录不可达」，不静默替换为游戏目录）
            runtime_instance_dir: selectedRuntime.value.instance_dir ?? '',
            policy_set: 'default-v1',
            suggestions: [],
        })
        precheckState.value = 'done'
    } catch (e) {
        showSnackbar(errText(e), 'error')
        resetPreparation()
    }
}

// —— 创建（CreateRelation 消费预检）：确认弹窗二次确认后执行；成功即转入
//      N-05 进度页并立即发起扫描（后台任务由任务中心追踪，本页轮询推进）——
async function create() {
    if (!prep.value) return
    confirmOpen.value = false
    try {
        created.value = await SyncService.CreateRelation(prep.value.preparation_id)
        showSnackbar(t('workspacesNew.createdToast'), 'success')
        triggerRequery()
    } catch (e) {
        showSnackbar(errText(e), 'error')
        return
    }
    scanDone.value = false
    scanFailed.value = false
    void startScan()
}

async function startScan() {
    if (!created.value) return
    try {
        await SyncService.StartScan(created.value.relation_id)
    } catch (e) {
        // 扫描发起失败不阻塞进度页：说明文案指引用户回列表处理（任务中心仍可追踪）
        showSnackbar(errText(e), 'error')
    }
    startScanPolling()
}

function startScanPolling() {
    stopScanPolling()
    pollTimer = setInterval(async () => {
        if (!created.value) return
        try {
            const ws = await SyncService.GetWorkspace(created.value.relation_id)
            if (ws.state.scan_state === 'ready') {
                scanDone.value = true
                stopScanPolling()
                triggerRequery()
            } else if (ws.state.scan_state === 'failed') {
                scanFailed.value = true
                stopScanPolling()
            }
        } catch {
            // 单轮查询失败下轮重试；离开页面时 onBeforeUnmount 统一清理
        }
    }, 2000)
}

function stopScanPolling() {
    if (pollTimer !== undefined) {
        clearInterval(pollTimer)
        pollTimer = undefined
    }
}

function backToList() {
    void router.push('/workspaces')
}

// 初始化预览 = 变化页（新建工作区首次扫描后的 P-01 初始化预览画板在此承接）
function viewInitPreview() {
    if (created.value) void router.push('/workspaces/' + created.value.relation_id + '/changes')
}

// tonal 按钮（原型 .btn-tonal：tint-primary 底 + 主色字），覆盖 ghost 的 hover 底色
const tonalClass = 'bg-tint-primary text-primary hover:bg-tint-primary/80'
</script>

<template>
    <!-- N-05：创建进度（560px 居中；完成后出现「查看初始化预览」） -->
    <div v-if="created" class="mx-auto w-full max-w-[560px] p-4 text-foreground">
        <div class="mt-[10vh]">
            <h1 class="page-title">{{ t('workspacesNew.creatingTitle') }}</h1>
            <p class="text-muted-foreground mt-1 text-xs">{{ pairLabel }}</p>
        </div>

        <div class="my-5 flex flex-col">
            <!-- 段一：创建工作区（CreateRelation 返回即完成） -->
            <div class="flex items-start gap-3 py-2.5" :class="created ? 'text-foreground' : 'text-muted-foreground'">
                <span
                    class="flex size-6 shrink-0 items-center justify-center rounded-full"
                    :class="created ? 'bg-tint-success text-success' : 'bg-surface-2 text-muted-foreground'"
                >
                    <CheckIcon v-if="created" class="size-3.5" />
                    <LoaderCircleIcon v-else class="size-3.5 animate-spin" />
                </span>
                <div>
                    <div class="text-sm">{{ t('workspacesNew.phaseCreate') }}</div>
                    <div class="text-faint text-[11.5px]">
                        {{ scanDone ? t('workspacesNew.phaseCreateDone') : t('workspacesNew.phaseCreateWait') }}
                    </div>
                </div>
            </div>

            <!-- 段二：扫描两侧端点（后台任务；任务中心追踪） -->
            <div class="flex items-start gap-3 py-2.5 text-foreground">
                <span
                    class="flex size-6 shrink-0 items-center justify-center rounded-full"
                    :class="scanDone || scanFailed ? 'bg-tint-success text-success' : 'bg-tint-primary text-primary'"
                >
                    <CheckIcon v-if="scanDone || scanFailed" class="size-3.5" />
                    <LoaderCircleIcon v-else class="size-3.5 animate-spin" />
                </span>
                <div>
                    <div class="text-sm">{{ t('workspacesNew.phaseScan') }}</div>
                    <div class="text-faint text-[11.5px]">
                        {{
                            scanDone
                                ? t('workspacesNew.phaseScanDone')
                                : scanFailed
                                  ? t('workspacesNew.phaseScanFailed')
                                  : t('workspacesNew.phaseScanRunning')
                        }}
                    </div>
                </div>
            </div>

            <!-- 段三：生成初始化预览（扫描完成后可查看） -->
            <div class="flex items-start gap-3 py-2.5" :class="scanDone ? 'text-foreground' : 'text-muted-foreground'">
                <span
                    class="flex size-6 shrink-0 items-center justify-center rounded-full"
                    :class="scanDone ? 'bg-tint-success text-success' : 'bg-surface-2 text-muted-foreground'"
                >
                    <CheckIcon v-if="scanDone" class="size-3.5" />
                    <ClockIcon v-else class="size-3.5" />
                </span>
                <div>
                    <div class="text-sm">{{ t('workspacesNew.phasePreview') }}</div>
                    <div class="text-faint text-[11.5px]">
                        {{ scanDone ? t('workspacesNew.phasePreviewDone') : t('workspacesNew.phasePreviewWait') }}
                    </div>
                </div>
            </div>
        </div>

        <div class="flex flex-wrap gap-2">
            <Button variant="outline" @click="backToList">{{ t('workspacesNew.backToList') }}</Button>
            <Button v-if="scanDone" @click="viewInitPreview">{{ t('workspacesNew.viewInitPreview') }}</Button>
        </div>
    </div>

    <!-- N-01..N-04：向导主体（980px；左 210px 步骤导航 + 右内容，≤900px 转纵向） -->
    <div v-else class="mx-auto flex w-full max-w-[980px] flex-col gap-4 p-4 text-foreground">
        <div class="flex items-start justify-between gap-3">
            <div>
                <h1 class="page-title">{{ t('workspacesNew.title') }}</h1>
                <p class="text-muted-foreground mt-1 text-sm">{{ t('workspacesNew.subtitle') }}</p>
            </div>
            <Button variant="ghost" size="sm" @click="cancel">{{ t('workspacesNew.cancel') }}</Button>
        </div>

        <div class="grid grid-cols-1 gap-[22px] min-[900px]:grid-cols-[210px_1fr]">
            <!-- 步骤导航：当前步主色底、已完成步成功色底 + 对勾 -->
            <nav
                class="flex gap-1.5 border-border border-b pb-3 min-[900px]:flex-col min-[900px]:gap-0 min-[900px]:border-r min-[900px]:border-b-0 min-[900px]:pb-0 min-[900px]:pr-[18px]"
                :aria-label="t('workspacesNew.title')"
            >
                <div
                    v-for="(key, i) in STEP_KEYS"
                    :key="key"
                    class="flex items-start gap-2.5 rounded-lg px-2 py-2 text-sm min-[900px]:py-[9px]"
                    :class="
                        i === step
                            ? 'bg-surface-2 font-semibold text-foreground'
                            : 'text-muted-foreground'
                    "
                >
                    <span
                        class="flex size-[22px] shrink-0 items-center justify-center rounded-full text-[11.5px] font-bold"
                        :class="
                            i < step
                                ? 'bg-tint-success text-success'
                                : i === step
                                  ? 'bg-primary text-primary-foreground'
                                  : 'bg-surface-2 text-muted-foreground'
                        "
                    >
                        <CheckIcon v-if="i < step" class="size-3" />
                        <template v-else>{{ i + 1 }}</template>
                    </span>
                    <span>{{ t(key) }}</span>
                </div>
            </nav>

            <div>
                <!-- N-01：选择项目源 -->
                <section v-if="step === 0" class="flex flex-col gap-2">
                    <h2 class="mb-2 text-base font-semibold">{{ t('workspacesNew.step1Title') }}</h2>
                    <template v-if="projects.length">
                        <CandidateCard
                            v-for="p in projects"
                            :key="p.id"
                            :name="p.display_name"
                            :sub="p.root_path"
                            mono
                            :selected="selectedProject?.id === p.id"
                            :chip="t('workspacesNew.wsCountChip', [projectRefs(p)])"
                            @select="pickSource(p)"
                        >
                            <template #badge>
                                <Badge
                                    v-if="endpointHealthBadge(p.id)"
                                    :variant="endpointHealthBadge(p.id)!.tone.variant"
                                    :class="endpointHealthBadge(p.id)!.tone.class"
                                >{{ endpointHealthBadge(p.id)!.label }}</Badge>
                            </template>
                        </CandidateCard>
                    </template>
                    <p v-else class="text-muted-foreground text-sm">
                        {{ loading ? t('endpoints.loading') : t('workspacesNew.noProjects') }}
                    </p>

                    <div class="mt-2">
                        <Button size="sm" variant="ghost" :class="tonalClass" @click="router.push('/sources')">
                            <PlusIcon class="size-3.5" />{{ t('workspacesNew.registerSource') }}
                        </Button>
                    </div>

                    <div class="mt-4 flex justify-end">
                        <Button :disabled="!selectedProject" @click="goNext">{{ t('workspacesNew.next') }}</Button>
                    </div>
                </section>

                <!-- N-02：选择运行实例（重复 pair 禁用挂「已建立工作区」chip） -->
                <section v-else-if="step === 1" class="flex flex-col gap-2">
                    <h2 class="text-base font-semibold">{{ t('workspacesNew.step2Title') }}</h2>
                    <p class="mb-2 text-muted-foreground text-xs">
                        {{ t('workspacesNew.step2SourcePrefix', [selectedProject?.display_name ?? t('workspacesNew.noSourcePicked')]) }}
                    </p>
                    <template v-if="runtimes.length">
                        <CandidateCard
                            v-for="r in runtimes"
                            :key="r.id"
                            :name="r.display_name"
                            :sub="r.root_path + ' · ' + r.adapter"
                            mono
                            :selected="selectedRuntime?.id === r.id"
                            :disabled="isPairTaken(r)"
                            :disabled-title="t('workspacesNew.pairTakenTitle')"
                            :chip="isPairTaken(r) ? t('workspacesNew.pairTakenChip') : undefined"
                            @select="pickRuntime(r)"
                        >
                            <template #badge>
                                <Badge
                                    v-if="endpointHealthBadge(r.id)"
                                    :variant="endpointHealthBadge(r.id)!.tone.variant"
                                    :class="endpointHealthBadge(r.id)!.tone.class"
                                >{{ endpointHealthBadge(r.id)!.label }}</Badge>
                            </template>
                        </CandidateCard>
                    </template>
                    <p v-else class="text-muted-foreground text-sm">
                        {{ loading ? t('endpoints.loading') : t('workspacesNew.noRuntimes') }}
                    </p>

                    <div class="mt-2">
                        <Button size="sm" variant="ghost" :class="tonalClass" @click="router.push('/runtimes')">
                            <PlusIcon class="size-3.5" />{{ t('workspacesNew.registerRuntime') }}
                        </Button>
                    </div>

                    <div class="mt-4 flex items-center justify-between">
                        <Button variant="ghost" size="sm" @click="goBack">{{ t('workspacesNew.back') }}</Button>
                        <Button :disabled="!selectedRuntime" @click="goNext">{{ t('workspacesNew.next') }}</Button>
                    </div>
                </section>

                <!-- N-03：选择受管范围（MappingPolicy 候选卡 + 建议范围 chip 组，
                          不默认勾选——原型拍板语义） -->
                <section v-else-if="step === 2" class="flex flex-col gap-2">
                    <h2 class="text-base font-semibold">{{ t('workspacesNew.step3Title') }}</h2>
                    <p class="mb-2 text-muted-foreground text-xs">{{ t('workspacesNew.step3Desc') }}</p>
                    <CandidateCard
                        v-for="s in suggestions"
                        :key="s.id"
                        :name="t('workspacesNew.suggestions.' + s.id)"
                        :sub="t('workspacesNew.suggestionMeta', [t('workspacesNew.kind.' + s.resource_kind), t('workspacesNew.direction.' + s.direction), t('workspacesNew.materialization.' + s.materialization)])"
                        :selected="selectedPolicy?.id === s.id"
                        @select="pickPolicy(s)"
                    />

                    <div class="mt-3.5 rounded-lg border bg-card px-4 py-3.5">
                        <h3 class="flex items-center gap-2 text-[13px] font-semibold">
                            <InfoIcon class="text-primary size-4" />{{ t('workspacesNew.suggestedScopeTitle') }}
                        </h3>
                        <div class="mt-2.5 flex flex-wrap gap-1.5">
                            <span
                                v-for="s in suggestions"
                                :key="s.id"
                                class="inline-flex h-6 items-center rounded-md bg-surface-2 px-2.5 text-[11.5px] font-semibold text-muted-foreground"
                            >{{ s.project_prefix }}</span>
                        </div>
                        <p class="text-faint mt-2 text-[11.5px]">{{ t('workspacesNew.suggestedScopeNote') }}</p>
                    </div>

                    <div class="mt-4 flex items-center justify-between">
                        <Button variant="ghost" size="sm" @click="goBack">{{ t('workspacesNew.back') }}</Button>
                        <Button :disabled="!selectedPolicy" @click="goNext">{{ t('workspacesNew.next') }}</Button>
                    </div>
                </section>

                <!-- N-04：确认并创建（kv 摘要 + info 横幅 + 预检面板） -->
                <section v-else class="flex flex-col gap-2">
                    <h2 class="mb-2 text-base font-semibold">{{ t('workspacesNew.step4Title') }}</h2>

                    <!-- kv 摘要卡 -->
                    <dl class="grid grid-cols-[120px_1fr] gap-x-3 gap-y-1 rounded-lg border bg-card px-4 py-3.5 text-[12.5px]">
                        <dt class="text-muted-foreground">{{ t('workspacesNew.kvWorkspace') }}</dt>
                        <dd class="font-semibold">
                            {{ selectedProject?.display_name }} ↔ {{ selectedRuntime?.display_name }}
                        </dd>
                        <dt class="text-muted-foreground">{{ t('workspacesNew.kvProjectPath') }}</dt>
                        <dd class="font-mono text-xs">{{ selectedProject?.root_path }}</dd>
                        <dt class="text-muted-foreground">{{ t('workspacesNew.kvRuntimePath') }}</dt>
                        <dd class="font-mono text-xs">{{ selectedRuntime?.root_path }}</dd>
                        <dt class="text-muted-foreground">{{ t('workspacesNew.kvAdapters') }}</dt>
                        <dd>{{ selectedProject?.adapter }} · {{ selectedRuntime?.adapter }}</dd>
                        <dt class="text-muted-foreground">{{ t('workspacesNew.kvScope') }}</dt>
                        <dd>
                            {{ selectedPolicy ? t('workspacesNew.suggestions.' + selectedPolicy.id) : '—' }}{{ t('workspacesNew.kvScopeSuffix') }}
                        </dd>
                    </dl>

                    <!-- info 横幅 -->
                    <div class="flex items-center gap-2.5 rounded-lg border border-tint-primary bg-tint-primary px-3.5 py-2.5 text-[12.5px]">
                        <InfoIcon class="text-primary size-4 shrink-0" />
                        <div class="min-w-0 flex-1">{{ t('workspacesNew.confirmBanner') }}</div>
                    </div>

                    <!-- 预检面板：idle 运行预检 / running 呼吸态 / done 结果行列表 -->
                    <div class="mt-1 rounded-lg border bg-card px-4 py-3.5">
                        <h3 class="mb-2.5 flex items-center gap-2 text-[13px] font-semibold">
                            <ShieldCheckIcon class="size-4" />{{ t('workspacesNew.precheckTitle') }}
                        </h3>

                        <template v-if="precheckState === 'idle'">
                            <Button size="sm" variant="ghost" :class="tonalClass" @click="prepare">
                                {{ t('workspacesNew.runPrecheck') }}
                            </Button>
                            <p class="text-faint mt-2 text-[11.5px]">{{ t('workspacesNew.precheckHint') }}</p>
                        </template>

                        <Badge v-else-if="precheckState === 'running'" variant="st-run" pulse>
                            {{ t('workspacesNew.precheckRunning') }}
                        </Badge>

                        <template v-else>
                            <div class="divide-border divide-y">
                                <div
                                    v-for="c in checks"
                                    :key="c.code + (c.args?.[0] ?? '')"
                                    class="flex items-start gap-2 py-1.5 text-xs"
                                >
                                    <CheckIcon v-if="c.passed" class="text-success mt-px size-3.5 shrink-0" />
                                    <CircleAlertIcon
                                        v-else
                                        class="mt-px size-3.5 shrink-0"
                                        :class="c.severity === 'blocking' ? 'text-error' : 'text-warning'"
                                    />
                                    <span>
                                        {{ t(c.code, c.args ?? []) }}<template v-if="c.detail">：{{ c.detail }}</template>
                                    </span>
                                </div>
                            </div>
                            <p v-if="!checks.length" class="text-muted-foreground text-xs">
                                {{ t('workspacesNew.precheckEmpty') }}
                            </p>

                            <div class="mt-3 flex items-center justify-end gap-2">
                                <Button variant="ghost" size="sm" @click="goBack">{{ t('workspacesNew.back') }}</Button>
                                <Button :disabled="blockingFailed" :title="blockingFailed ? t('workspacesNew.precheckBlockedTitle') : undefined" @click="confirmOpen = true">
                                    <PlusIcon class="size-4" />{{ t('workspacesNew.createBtn') }}
                                </Button>
                            </div>
                        </template>
                    </div>
                </section>
            </div>
        </div>
    </div>

    <!-- 创建确认：居中弹窗（取消文字钮 + 确认主钮） -->
    <Dialog v-model:open="confirmOpen">
        <DialogContent class="sm:max-w-md">
            <DialogHeader>
                <DialogTitle>{{ t('workspacesNew.confirmTitle') }}</DialogTitle>
                <DialogDescription>
                    {{ t('workspacesNew.confirmDesc', [selectedProject?.display_name ?? '', selectedRuntime?.display_name ?? '']) }}
                </DialogDescription>
            </DialogHeader>
            <DialogFooter class="gap-2">
                <Button variant="ghost" @click="confirmOpen = false">{{ t('workspacesNew.cancel') }}</Button>
                <Button @click="create">{{ t('workspacesNew.createBtn') }}</Button>
            </DialogFooter>
        </DialogContent>
    </Dialog>
</template>
