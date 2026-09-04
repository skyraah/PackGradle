<script setup lang="ts">
// /workspaces/new：新建工作区（Prepare → Apply 两段式；契约 03、UX 原型 §7.2）。
// 从已登记两端点发起预检并创建 Relation；建议范围默认不勾选，
// 用户确认（CreateRelation）前不写入受管范围。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ProjectService, RuntimeService, SyncService } from '../api'
import type {
    EndpointDTO,
    MappingRuleDTO,
    PreparationCheckDTO,
    RelationDTO,
    RelationPreparationDTO,
} from '../api'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()
const router = useRouter()

const projects = ref<EndpointDTO[]>([])
const runtimes = ref<EndpointDTO[]>([])
const suggestions = ref<MappingRuleDTO[]>([])
const loading = ref(false)

const selectedProject = ref<EndpointDTO | null>(null)
const selectedRuntime = ref<EndpointDTO | null>(null)
// 勾选的建议规则 ID（默认不勾选：确认前不纳入受管）
const checkedSuggestions = ref<Set<string>>(new Set())

const prep = ref<RelationPreparationDTO | null>(null)
const preparing = ref(false)
const creating = ref(false)
const created = ref<RelationDTO | null>(null)

onMounted(load)

async function load() {
    loading.value = true
    try {
        const [ps, rs, ss] = await Promise.all([
            ProjectService.ListProjects(),
            RuntimeService.ListRuntimes(),
            SyncService.ListPolicySuggestions(),
        ])
        projects.value = ps ?? []
        runtimes.value = rs ?? []
        suggestions.value = ss ?? []
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        loading.value = false
    }
}

function toggleEndpoint(side: 'project' | 'runtime', ep: EndpointDTO) {
    // 选择即时生效；换选后已有预检失效，需重新预检
    if (side === 'project') selectedProject.value = ep
    else selectedRuntime.value = ep
    prep.value = null
    created.value = null
}

function toggleSuggestion(id: string) {
    const next = new Set(checkedSuggestions.value)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    checkedSuggestions.value = next
    prep.value = null
}

const canPrepare = computed(() => selectedProject.value != null && selectedRuntime.value != null && !preparing.value)
const checks = computed<PreparationCheckDTO[]>(() => prep.value?.checks ?? [])
const blockingFailed = computed(() =>
    checks.value.some((c) => c.severity === 'blocking' && !c.passed),
)

// prepare 执行预检：展示检查项与受管范围（确认前不落库）
async function prepare() {
    if (!selectedProject.value || !selectedRuntime.value) return
    preparing.value = true
    try {
        prep.value = await SyncService.PrepareRelation({
            project_root: selectedProject.value.root_path,
            // 已登记运行实例的登记输入：后端派生的实例目录（缺值时留空，
            // 由预检检查项可见地报「实例目录不可达」，不静默替换为游戏目录）
            runtime_instance_dir: selectedRuntime.value.instance_dir ?? '',
            policy_set: 'default-v1',
            suggestions: [...checkedSuggestions.value],
        })
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        preparing.value = false
    }
}

// create 确认创建：成功即进入已建工作区（创建流交付后由事件管线驱动列表/状态刷新）
async function create() {
    if (!prep.value) return
    creating.value = true
    try {
        created.value = await SyncService.CreateRelation(prep.value.preparation_id)
        showSnackbar(t('workspacesNew.createdToast'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        creating.value = false
    }
}

function checkVariant(c: PreparationCheckDTO): 'default' | 'destructive' | 'secondary' {
    if (c.passed) return 'default'
    return c.severity === 'blocking' ? 'destructive' : 'secondary'
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4 p-4 text-foreground">
        <div>
            <h1 class="page-title">{{ t('workspacesNew.title') }}</h1>
            <p class="text-muted-foreground mt-1 text-sm">{{ t('workspacesNew.subtitle') }}</p>
        </div>

        <!-- 已建工作区（应用成功进入工作区） -->
        <Card v-if="created">
            <CardHeader>
                <CardTitle>{{ t('workspacesNew.createdTitle') }}</CardTitle>
                <CardDescription>{{ t('workspacesNew.createdNote') }}</CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-2 text-sm">
                <div class="font-medium">
                    {{ created.project.display_name }} <span class="text-muted-foreground">↔</span>
                    {{ created.runtime.display_name }}
                </div>
                <div class="text-muted-foreground text-xs" :title="created.project.root_path">
                    {{ t('endpoints.colPath') }}（{{ t('workspacesNew.step1Title') }}）：{{ created.project.root_path }}
                </div>
                <div class="text-muted-foreground text-xs" :title="created.runtime.root_path">
                    {{ t('endpoints.colPath') }}（{{ t('workspacesNew.step2Title') }}）：{{ created.runtime.root_path }}
                </div>
                <div class="flex items-center gap-2">
                    <Badge variant="outline">{{ created.policy_set }}</Badge>
                    <Button size="sm" variant="outline" @click="router.push('/workspaces')">
                        {{ t('workspacesNew.goList') }}
                    </Button>
                </div>
            </CardContent>
        </Card>

        <!-- 步骤 1：选择项目源 -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('workspacesNew.step1Title') }}</CardTitle>
            </CardHeader>
            <CardContent>
                <div v-if="projects.length" class="flex flex-col gap-1">
                    <button
                        v-for="p in projects"
                        :key="p.id"
                        type="button"
                        class="hover:bg-muted flex items-center justify-between rounded-md border px-3 py-2 text-left text-sm"
                        :class="selectedProject?.id === p.id ? 'border-primary bg-muted/50' : ''"
                        @click="toggleEndpoint('project', p)"
                    >
                        <span class="font-medium">{{ p.display_name }}</span>
                        <span class="text-muted-foreground max-w-96 truncate text-xs" :title="p.root_path">{{ p.root_path }}</span>
                    </button>
                </div>
                <p v-else class="text-muted-foreground text-sm">
                    {{ loading ? t('endpoints.loading') : t('workspacesNew.noProjects') }}
                </p>
            </CardContent>
        </Card>

        <!-- 步骤 2：选择运行实例 -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('workspacesNew.step2Title') }}</CardTitle>
            </CardHeader>
            <CardContent>
                <div v-if="runtimes.length" class="flex flex-col gap-1">
                    <button
                        v-for="r in runtimes"
                        :key="r.id"
                        type="button"
                        class="hover:bg-muted flex items-center justify-between rounded-md border px-3 py-2 text-left text-sm"
                        :class="selectedRuntime?.id === r.id ? 'border-primary bg-muted/50' : ''"
                        @click="toggleEndpoint('runtime', r)"
                    >
                        <span class="font-medium">{{ r.display_name }}</span>
                        <span class="text-muted-foreground max-w-96 truncate text-xs" :title="r.root_path">{{ r.root_path }}</span>
                    </button>
                </div>
                <p v-else class="text-muted-foreground text-sm">
                    {{ loading ? t('endpoints.loading') : t('workspacesNew.noRuntimes') }}
                </p>
            </CardContent>
        </Card>

        <!-- 步骤 3：选择受管范围（建议默认不勾选） -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('workspacesNew.step3Title') }}</CardTitle>
                <CardDescription>{{ t('workspacesNew.step3Desc') }}</CardDescription>
            </CardHeader>
            <CardContent>
                <div class="flex flex-col gap-1">
                    <button
                        v-for="s in suggestions"
                        :key="s.id"
                        type="button"
                        class="hover:bg-muted flex items-center justify-between rounded-md border px-3 py-2 text-left text-sm"
                        :class="checkedSuggestions.has(s.id) ? 'border-primary bg-muted/50' : ''"
                        @click="toggleSuggestion(s.id)"
                    >
                        <span class="font-medium">
                            {{ checkedSuggestions.has(s.id) ? '☑' : '☐' }}
                            {{ t('workspacesNew.suggestions.' + s.id) }}
                        </span>
                        <span class="text-muted-foreground text-xs">
                            {{ t('workspacesNew.suggestionMeta', [t('workspacesNew.kind.' + s.resource_kind), t('workspacesNew.direction.' + s.direction), t('workspacesNew.materialization.' + s.materialization)]) }}
                        </span>
                    </button>
                </div>
                <p class="text-muted-foreground mt-3 text-xs">{{ t('workspacesNew.basePolicyNote') }}</p>
            </CardContent>
        </Card>

        <!-- 步骤 4：预检并创建 -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('workspacesNew.step4Title') }}</CardTitle>
                <CardDescription>{{ t('workspacesNew.step4Desc') }}</CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-3">
                <div class="flex items-center gap-2">
                    <Button :disabled="!canPrepare" @click="prepare">
                        {{ preparing ? t('workspacesNew.preparing') : t('workspacesNew.prepareBtn') }}
                    </Button>
                    <Button
                        v-if="prep && !blockingFailed"
                        :disabled="creating"
                        @click="create"
                    >
                        {{ creating ? t('workspacesNew.creating') : t('workspacesNew.createBtn') }}
                    </Button>
                </div>

                <template v-if="prep">
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
                                    <Badge :variant="checkVariant(c)">
                                        {{ c.passed ? t('workspacesNew.checkPassed') : c.severity === 'blocking' ? t('workspacesNew.checkBlocking') : t('workspacesNew.checkWarning') }}
                                    </Badge>
                                </TableCell>
                                <TableCell class="text-muted-foreground text-xs">{{ c.detail }}</TableCell>
                            </TableRow>
                        </TableBody>
                    </Table>

                    <div>
                        <p class="mb-1 text-sm font-medium">{{ t('workspacesNew.policyPreview') }}</p>
                        <ul class="text-muted-foreground list-inside list-disc text-xs">
                            <li v-for="r in prep.policy.rules ?? []" :key="r.id">
                                {{ r.id }}（{{ t('workspacesNew.kind.' + r.resource_kind) }} ·
                                {{ r.direction }} · {{ t('workspacesNew.materialization.' + r.materialization) }}）
                            </li>
                        </ul>
                    </div>
                </template>
            </CardContent>
        </Card>
    </div>
</template>
