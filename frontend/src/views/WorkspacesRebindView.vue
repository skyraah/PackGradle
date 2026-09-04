<script setup lang="ts">
// /workspaces/:id/rebind：重绑（契约 03 §2.4 PrepareRebind/ApplyRebind；UX 原型 §7.6）。
// 工作区上下文（关系名/availability）读 stores/syncCache 投影；预检为本页动作
// （PrepareRebind 结果查询快照），确认走 ApplyRebind（ADR-0003 单事务，恒 reinitialize）。
// 布局：左栏当前登记 / 右栏候选端点（侧别切换 + 新路径 + 目录选择器）+ 下方预检区。
// 主操作「确认重绑」只在预检有效时出现；执行中锁定提交区，成功后进入变化页；
// 失败保留候选端点与预检证据（同一预检可重试直至过期）。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SyncService } from '../api'
import type { PreparationCheckDTO, RebindPreparationDTO } from '../api'
import { bootstrapped, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText, errorCode } from '../utils/errors'
import { pickDirectory } from '../utils/dialogs'
import { checkTone } from '../utils/pageState'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))

// —— 工作区上下文（syncCache 投影，不二次取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)

// —— 候选端点（一次只重绑一侧）——
const side = ref<'project' | 'runtime'>('project')
const rootPath = ref('')

// 当前登记：按侧别取对应端点（重绑页打开时 syncCache 可能仍在引导，wsRow 可为空）
const currentEndpoint = computed(() => {
    const rel = wsRow.value?.relation
    if (!rel) return null
    return side.value === 'project' ? rel.project : rel.runtime
})

// 最近成功扫描：按侧别取最新快照的采集时间（UX 原型 §7.6 当前登记栏）
const lastScanAt = computed(() => {
    const ws = wsRow.value
    if (!ws) return ''
    const snap = side.value === 'project' ? ws.latest_project_snapshot : ws.latest_runtime_snapshot
    if (!snap?.captured_at) return ''
    const at = Date.parse(snap.captured_at)
    return Number.isNaN(at) ? '' : new Date(at).toLocaleString()
})

function switchSide(next: 'project' | 'runtime') {
    if (side.value === next) return
    side.value = next
    // 换侧后已有预检失效，需重新预检
    prep.value = null
}

// —— 预检与应用 ——
const prep = ref<RebindPreparationDTO | null>(null)
const preparing = ref(false)
const applying = ref(false)

const checks = computed<PreparationCheckDTO[]>(() => prep.value?.checks ?? [])
const blockingFailed = computed(() =>
    checks.value.some(c => c.severity === 'blocking' && !c.passed),
)
// 预检有效性：blocking 全过且未过期（过期由前端隐藏主操作，后端守卫兜底）
const prepExpired = computed(() => {
    const exp = prep.value ? Date.parse(prep.value.expires_at) : NaN
    return prep.value !== null && (Number.isNaN(exp) || Date.now() >= exp)
})
const canPrepare = computed(() => rootPath.value.trim() !== '' && !preparing.value && !applying.value)
const canApply = computed(() => prep.value !== null && !blockingFailed.value && !prepExpired.value && !applying.value)

async function chooseDirectory() {
    const picked = await pickDirectory(t('rebind.pathDialogTitle'))
    if (picked) rootPath.value = picked
}

async function prepare() {
    if (!canPrepare.value) return
    preparing.value = true
    try {
        prep.value = await SyncService.PrepareRebind({
            relation_id: relationID.value,
            side: side.value,
            root_path: rootPath.value.trim(),
        })
    } catch (e) {
        prep.value = null
        showSnackbar(errText(e), 'error')
    } finally {
        preparing.value = false
    }
}

// apply 确认重绑：成功后触发受控重查并进入工作区变化页（UX 原型 §7.6）
async function apply() {
    if (!canApply.value || !prep.value) return
    applying.value = true
    try {
        await SyncService.ApplyRebind(prep.value.preparation_id)
        triggerRequery()
        showSnackbar(t('rebind.successToast'), 'success')
        await router.push('/workspaces/' + relationID.value + '/changes')
    } catch (e) {
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

// 绑定指纹摘要：完整哈希不适合直读，展示前后一致性即可（对比结论见 fingerprint_changed）
function fingerprintSummary(fp: string): string {
    if (!fp) return '—'
    return fp.slice(0, 15) + '…'
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部：工作区上下文 + 返回 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="page-title">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }}
                    </template>
                    <template v-else>{{ t('rebind.title') }}</template>
                </h1>
                <p class="text-muted-foreground mt-1 text-sm">{{ t('rebind.subtitle') }}</p>
            </div>
            <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                {{ t('changes.backToList') }}
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
            <!-- 双栏：当前登记 / 候选端点（UX 原型 §7.6） -->
            <div class="grid gap-4 md:grid-cols-2">
                <Card>
                    <CardHeader>
                        <CardTitle>{{ t('rebind.currentTitle') }}</CardTitle>
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
                            <span class="max-w-72 truncate text-right text-xs" :title="currentEndpoint.root_path">
                                {{ currentEndpoint.root_path }}
                            </span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('rebind.fingerprintLabel') }}</span>
                            <span class="font-mono text-xs" :title="currentEndpoint.binding_fingerprint">
                                {{ fingerprintSummary(currentEndpoint.binding_fingerprint) }}
                            </span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('rebind.lastScan') }}</span>
                            <span class="text-muted-foreground text-xs">{{ lastScanAt || '—' }}</span>
                        </div>
                    </CardContent>
                    <CardContent v-else class="text-muted-foreground text-sm">{{ t('endpoints.loading') }}</CardContent>
                </Card>

                <Card>
                    <CardHeader>
                        <CardTitle>{{ t('rebind.candidateTitle') }}</CardTitle>
                        <CardDescription>{{ t('rebind.sideNote') }}</CardDescription>
                    </CardHeader>
                    <CardContent class="flex flex-col gap-3">
                        <div class="flex gap-2">
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
                        <div class="flex items-end gap-2">
                            <div class="flex w-full max-w-md flex-col gap-1">
                                <label class="text-xs font-medium" for="rebind-root-path">
                                    {{ side === 'project' ? t('rebind.projectPathLabel') : t('rebind.runtimePathLabel') }}
                                </label>
                                <Input
                                    id="rebind-root-path"
                                    v-model="rootPath"
                                    :placeholder="t('endpoints.pathPlaceholder')"
                                    :disabled="applying"
                                    @keydown.enter="prepare"
                                />
                            </div>
                            <Button variant="outline" class="mb-0.5" :disabled="applying" @click="chooseDirectory">
                                {{ t('rebind.pickDir') }}
                            </Button>
                            <Button class="mb-0.5" :disabled="!canPrepare" @click="prepare">
                                {{ preparing ? t('rebind.preparing') : t('rebind.prepareBtn') }}
                            </Button>
                        </div>
                    </CardContent>
                </Card>
            </div>

            <!-- 预检区：检查项 + 指纹对比 + 基线说明 + 影响计数 -->
            <Card v-if="prep">
                <CardHeader>
                    <CardTitle>{{ t('rebind.prepTitle') }}</CardTitle>
                    <CardDescription>{{ t('rebind.prepDesc') }}</CardDescription>
                </CardHeader>
                <CardContent class="flex flex-col gap-3">
                    <!-- 候选端点证据（UX 原型 §7.6 右栏：名称/适配器/路径/指纹摘要） -->
                    <div class="flex flex-col gap-1 text-sm">
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('rebind.newEndpoint') }}</span>
                            <span class="font-medium">{{ prep.new_endpoint.display_name }}</span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('endpoints.colAdapter') }}</span>
                            <Badge variant="outline">{{ prep.new_endpoint.adapter }}</Badge>
                        </div>
                        <div class="flex items-start justify-between gap-3">
                            <span class="text-muted-foreground shrink-0 text-xs">{{ t('endpoints.colPath') }}</span>
                            <span class="max-w-72 truncate text-right text-xs" :title="prep.new_endpoint.root_path">
                                {{ prep.new_endpoint.root_path }}
                            </span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('rebind.fingerprintLabel') }}</span>
                            <span class="font-mono text-xs" :title="prep.new_endpoint.binding_fingerprint">
                                {{ fingerprintSummary(prep.new_endpoint.binding_fingerprint) }}
                            </span>
                        </div>
                    </div>
                    <div class="flex flex-wrap items-center gap-2 text-sm">
                        <Badge :variant="prep.fingerprint_changed ? 'secondary' : 'outline'">
                            {{ prep.fingerprint_changed ? t('rebind.fingerprintChanged') : t('rebind.fingerprintUnchanged') }}
                        </Badge>
                        <Badge variant="secondary">{{ t('rebind.invalidatedPlans', [prep.invalidated_plan_count]) }}</Badge>
                    </div>
                    <p class="text-muted-foreground text-xs">{{ t('rebind.baselineNote') }}</p>

                    <Table>
                        <TableHeader>
                            <TableRow>
                                <TableHead>{{ t('workspacesNew.colCheck') }}</TableHead>
                                <TableHead>{{ t('workspacesNew.colResult') }}</TableHead>
                                <TableHead>{{ t('workspacesNew.colDetail') }}</TableHead>
                            </TableRow>
                        </TableHeader>
                        <TableBody>
                            <TableRow v-for="c in checks" :key="c.code + (c.args?.[0] ?? '')">
                                <TableCell class="font-medium">{{ t(c.code, c.args ?? []) }}</TableCell>
                                <TableCell>
                                    <Badge :variant="checkTone(c.passed, c.severity).variant">
                                        {{ c.passed ? t('workspacesNew.checkPassed') : c.severity === 'blocking' ? t('workspacesNew.checkBlocking') : t('workspacesNew.checkWarning') }}
                                    </Badge>
                                </TableCell>
                                <TableCell class="text-muted-foreground text-xs">{{ c.detail }}</TableCell>
                            </TableRow>
                        </TableBody>
                    </Table>

                    <!-- 主操作：只在预检有效时出现（UX 原型 §7.6）；过期引导重新预检 -->
                    <div>
                        <Button v-if="!blockingFailed && !prepExpired" :disabled="!canApply" @click="apply">
                            {{ applying ? t('rebind.applying') : t('rebind.applyBtn') }}
                        </Button>
                        <span v-else-if="prepExpired" class="text-amber-600 text-sm">{{ t('rebind.prepExpired') }}</span>
                        <span v-else class="text-destructive text-sm">{{ t('rebind.blockedHint') }}</span>
                    </div>
                </CardContent>
            </Card>
        </template>
    </div>
</template>
