<script setup lang="ts">
// /sources：Packwiz 项目源端点管理（UX 原型 E-01 画板，票 #109；契约 03 §2.5）。
// 只管理端点登记与解析健康，没有跨端操作入口。七列表格（项目源/根路径/适配器/
// 解析健康/关联工作区/最近检查/更多），行点击开 400px 详情抽屉（字段/诊断/技术
// 信息三分区）；登记走对话框（「登记并选中」）；移除登记走危险确认弹窗。
// 健康结果读写 stores/endpoints 会话缓存（票 #109）：进页自动补查缓存缺失的
// 端点，切页往返已查健康不丢；关联工作区计数取 syncCache 工作区投影（按登记
// 根路径匹配，沿新建向导先例）。发现（按父目录搜索）保留为表格下方的辅助区。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
    CheckIcon,
    CircleAlertIcon,
    PlusIcon,
    XIcon,
} from '@lucide/vue'
import { ProjectService } from '../api'
import type { EndpointDTO, ProjectCandidateDTO } from '../api'
import DangerConfirmDialog from '../components/common/DangerConfirmDialog.vue'
import { useEndpointPage } from '../composables/useEndpointPage'
import { workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { pickDirectory } from '../utils/dialogs'
import { formatTime } from '../utils/pageState'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
    Sheet,
    SheetContent,
    SheetDescription,
    SheetFooter,
    SheetHeader,
    SheetTitle,
} from '@/components/ui/sheet'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()

const page = useEndpointPage({
    list: () => ProjectService.ListProjects(),
    register: rootPath => ProjectService.RegisterProject({ root_path: rootPath }),
    health: endpointID => ProjectService.GetProjectHealth(endpointID),
})
const { registered, loadingList, registering, health, loadRegistered, register, checkHealth, ensureHealth, healthOf, healthBadge } = page

const candidates = ref<ProjectCandidateDTO[]>([])
const parentDir = ref('')
const discovering = ref(false)

onMounted(async () => {
    await loadRegistered()
    ensureHealth(registered.value)
})

async function discover() {
    if (!parentDir.value.trim()) return
    discovering.value = true
    try {
        candidates.value = (await ProjectService.DiscoverProjects(parentDir.value.trim())) ?? []
    } catch (e) {
        candidates.value = []
        showSnackbar(errText(e), 'error')
    } finally {
        discovering.value = false
    }
}

// registerFromPath 登记后联动重发现（刷新候选的 registered 状态）
async function registerFromPath(rootPath: string) {
    const ep = await register(rootPath)
    if (ep) {
        discover()
    }
}

// —— 关联工作区计数：按登记根路径匹配工作区投影（新建向导先例）——
function refsCount(ep: EndpointDTO): number {
    return workspaces.value.filter(
        w => w.relation.project.root_path === ep.root_path || w.relation.runtime.root_path === ep.root_path,
    ).length
}

function checkedAtText(ep: EndpointDTO): string {
    return formatTime(healthOf(ep.id)?.checked_at)
}

// —— 登记对话框（「登记并选中」：成功后健康补查 + 打开详情抽屉）——
const regOpen = ref(false)
const regPath = ref('')

async function pickRegDir() {
    const picked = await pickDirectory(t('rebind.pathDialogTitle'))
    if (picked) regPath.value = picked
}

async function confirmRegister() {
    const path = regPath.value.trim()
    if (!path || registering.value) return
    const ep = await register(path)
    if (!ep) return
    regOpen.value = false
    regPath.value = ''
    void checkHealth(ep)
    openDrawer(ep)
}

// —— 详情抽屉（400px；字段 / 诊断 / 技术信息）——
const drawerOpen = ref(false)
const drawerEp = ref<EndpointDTO | null>(null)
const drawerChecking = computed(() => drawerEp.value !== null && health.get(drawerEp.value.id) === 'checking')

function openDrawer(ep: EndpointDTO) {
    drawerEp.value = ep
    drawerOpen.value = true
}

function diagLines(ep: EndpointDTO): { key: string; tone: 'ok' | 'warn' | 'err'; text: string }[] {
    const h = healthOf(ep.id)
    if (!h) return []
    return [
        {
            key: 'path',
            tone: h.path_exists ? 'ok' : 'err',
            text: t(h.path_exists ? 'endpoints.diagPathOk' : 'endpoints.diagPathMissing'),
        },
        {
            key: 'fp',
            tone: h.fingerprint_matches ? 'ok' : 'warn',
            text: t(h.fingerprint_matches ? 'endpoints.diagFpOk' : 'endpoints.diagFpMismatch'),
        },
    ]
}

function techLines(ep: EndpointDTO): string {
    return [
        `${t('endpoints.techEndpointId')}: ${ep.id}`,
        `${t('endpoints.techFingerprint')}: ${ep.binding_fingerprint || '—'}`,
        `${t('endpoints.techAdapterIdentity')}: ${ep.adapter_identity || '—'}`,
        `${t('endpoints.techCheckedAt')}: ${healthOf(ep.id)?.checked_at || '—'}`,
    ].join('\n')
}

// —— 移除登记（危险确认；当前版本后端未提供移除接口，确认后明确告知）——
const removeOpen = ref(false)
const removeTarget = ref<EndpointDTO | null>(null)

function confirmRemove() {
    removeOpen.value = false
    showSnackbar(t('endpoints.removeUnsupported', [removeTarget.value?.display_name ?? '']), 'warning')
    removeTarget.value = null
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 text-foreground">
        <!-- 页头：标题 + 登记入口（E-01） -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="page-title">{{ t('sources.title') }}</h1>
                <p class="text-muted-foreground mt-1 text-sm">{{ t('sources.subtitle') }}</p>
            </div>
            <Button size="sm" @click="regOpen = true">
                <PlusIcon class="size-3.5" />{{ t('sources.registerAction') }}
            </Button>
        </div>

        <!-- 已登记项目源：七列表格，行点击开详情抽屉 -->
        <Card>
            <CardContent class="py-2">
                <Table>
                    <TableHeader>
                        <TableRow>
                            <TableHead>{{ t('sources.colName') }}</TableHead>
                            <TableHead>{{ t('endpoints.colPath') }}</TableHead>
                            <TableHead>{{ t('endpoints.colAdapter') }}</TableHead>
                            <TableHead>{{ t('sources.colHealth') }}</TableHead>
                            <TableHead class="text-right">{{ t('endpoints.colRefs') }}</TableHead>
                            <TableHead>{{ t('endpoints.colChecked') }}</TableHead>
                            <TableHead class="text-right">{{ t('endpoints.colMore') }}</TableHead>
                        </TableRow>
                    </TableHeader>
                    <TableBody>
                        <template v-if="loadingList">
                            <TableRow v-for="i in 3" :key="'sk' + i">
                                <TableCell v-for="c in 7" :key="c">
                                    <div class="h-4 w-full animate-pulse rounded bg-muted"></div>
                                </TableCell>
                            </TableRow>
                        </template>
                        <TableRow v-else-if="!registered.length">
                            <TableCell :colspan="7">
                                <p class="text-muted-foreground py-8 text-center text-sm">{{ t('sources.registeredEmpty') }}</p>
                            </TableCell>
                        </TableRow>
                        <TableRow
                            v-for="ep in registered"
                            :key="ep.id"
                            class="cursor-pointer"
                            @click="openDrawer(ep)"
                        >
                            <TableCell class="font-medium">{{ ep.display_name }}</TableCell>
                            <TableCell class="max-w-60 truncate font-mono text-xs" :title="ep.root_path">
                                {{ ep.root_path }}
                            </TableCell>
                            <TableCell>
                                <Badge variant="outline">{{ ep.adapter }}</Badge>
                            </TableCell>
                            <TableCell>
                                <Badge :variant="healthBadge(ep.id).tone.variant" :class="healthBadge(ep.id).tone.class">
                                    {{ healthBadge(ep.id).label }}
                                </Badge>
                            </TableCell>
                            <TableCell class="text-right tabular-nums">{{ refsCount(ep) }}</TableCell>
                            <TableCell class="text-muted-foreground text-xs">{{ checkedAtText(ep) }}</TableCell>
                            <TableCell class="text-right" @click.stop>
                                <Button
                                    variant="ghost"
                                    size="icon"
                                    class="text-destructive size-7"
                                    :title="t('endpoints.removeAction')"
                                    @click="removeTarget = ep; removeOpen = true"
                                >
                                    <XIcon class="size-3.5" />
                                </Button>
                            </TableCell>
                        </TableRow>
                    </TableBody>
                </Table>
            </CardContent>
        </Card>

        <!-- 发现与登记（按父目录搜索候选；保留既有能力） -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('sources.candidatesTitle') }}</CardTitle>
                <CardDescription>{{ t('sources.subtitle') }}</CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
                <div class="flex items-center gap-2">
                    <div class="flex w-96 max-w-full flex-col gap-1">
                        <label class="text-xs font-medium" for="sources-parent-dir">{{ t('sources.parentLabel') }}</label>
                        <Input
                            id="sources-parent-dir"
                            v-model="parentDir"
                            :placeholder="t('sources.parentPlaceholder')"
                            @keydown.enter="discover"
                        />
                    </div>
                    <Button :disabled="discovering || !parentDir.trim()" class="mt-5" @click="discover">
                        {{ t('sources.discoverBtn') }}
                    </Button>
                </div>

                <Table v-if="candidates.length">
                    <TableHeader>
                        <TableRow>
                            <TableHead>{{ t('endpoints.colName') }}</TableHead>
                            <TableHead>{{ t('endpoints.colPath') }}</TableHead>
                            <TableHead>{{ t('endpoints.colMC') }}</TableHead>
                            <TableHead>{{ t('endpoints.colLoader') }}</TableHead>
                            <TableHead>{{ t('endpoints.colAction') }}</TableHead>
                        </TableRow>
                    </TableHeader>
                    <TableBody>
                        <TableRow v-for="c in candidates" :key="c.root_path">
                            <TableCell class="font-medium">{{ c.display_name }}</TableCell>
                            <TableCell class="text-muted-foreground max-w-72 truncate text-xs" :title="c.root_path">
                                {{ c.root_path }}
                            </TableCell>
                            <TableCell>{{ c.minecraft || '—' }}</TableCell>
                            <TableCell>{{ c.modloader || '—' }}</TableCell>
                            <TableCell>
                                <Badge v-if="c.registered" variant="secondary">{{ t('endpoints.registeredBadge') }}</Badge>
                                <Button v-else variant="outline" size="sm" :disabled="registering" @click="registerFromPath(c.root_path)">
                                    {{ t('sources.registerBtn') }}
                                </Button>
                            </TableCell>
                        </TableRow>
                    </TableBody>
                </Table>
                <p v-else-if="parentDir.trim()" class="text-muted-foreground text-sm">{{ t('sources.candidatesEmpty') }}</p>
            </CardContent>
        </Card>

        <!-- 登记对话框：根路径（+ 目录选择），确认按钮「登记并选中」 -->
        <Dialog v-model:open="regOpen">
            <DialogContent class="sm:max-w-md">
                <DialogHeader>
                    <DialogTitle>{{ t('endpoints.regDialogTitle', [t('sources.title')]) }}</DialogTitle>
                    <DialogDescription>{{ t('endpoints.regDialogNote') }}</DialogDescription>
                </DialogHeader>
                <div class="flex flex-col gap-1.5">
                    <label class="text-xs font-medium" for="sources-reg-path">{{ t('endpoints.rootPathField') }}</label>
                    <div class="flex gap-2">
                        <Input
                            id="sources-reg-path"
                            v-model="regPath"
                            :placeholder="t('endpoints.pathPlaceholder')"
                            @keydown.enter="confirmRegister"
                        />
                        <Button variant="outline" :disabled="registering" @click="pickRegDir">
                            {{ t('endpoints.pickDir') }}
                        </Button>
                    </div>
                </div>
                <DialogFooter>
                    <Button variant="ghost" size="sm" :disabled="registering" @click="regOpen = false">
                        {{ t('confirm.cancel') }}
                    </Button>
                    <Button size="sm" :disabled="registering || !regPath.trim()" @click="confirmRegister">
                        {{ t('endpoints.regConfirmBtn') }}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>

        <!-- 详情抽屉（400px）：字段 / 诊断 / 技术信息 -->
        <Sheet v-model:open="drawerOpen">
            <SheetContent side="right" class="flex w-[400px] flex-col gap-0 sm:max-w-[calc(100vw-68px)]">
                <SheetHeader class="flex-row items-center gap-2 border-b pb-3">
                    <SheetTitle class="flex min-w-0 items-center gap-2">
                        <span class="truncate">{{ drawerEp?.display_name }}</span>
                        <Badge
                            v-if="drawerEp"
                            :variant="healthBadge(drawerEp.id).tone.variant"
                            :class="healthBadge(drawerEp.id).tone.class"
                        >{{ healthBadge(drawerEp.id).label }}</Badge>
                    </SheetTitle>
                    <SheetDescription class="sr-only">{{ drawerEp?.display_name }}</SheetDescription>
                </SheetHeader>

                <div v-if="drawerEp" class="min-h-0 flex-1 overflow-y-auto p-4">
                    <div class="flex flex-col gap-1.5 text-sm">
                        <div class="flex items-start justify-between gap-3">
                            <span class="text-muted-foreground shrink-0 text-xs">{{ t('endpoints.colPath') }}</span>
                            <span class="text-right font-mono text-xs break-all">{{ drawerEp.root_path }}</span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('endpoints.colAdapter') }}</span>
                            <Badge variant="outline">{{ drawerEp.adapter }}</Badge>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('sources.colHealth') }}</span>
                            <Badge :variant="healthBadge(drawerEp.id).tone.variant" :class="healthBadge(drawerEp.id).tone.class">
                                {{ healthBadge(drawerEp.id).label }}
                            </Badge>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('endpoints.colRefs') }}</span>
                            <span>{{ t('endpoints.refsCount', [refsCount(drawerEp)]) }}</span>
                        </div>
                        <div class="flex items-center justify-between gap-3">
                            <span class="text-muted-foreground text-xs">{{ t('endpoints.colChecked') }}</span>
                            <span class="text-muted-foreground text-xs">{{ checkedAtText(drawerEp) }}</span>
                        </div>
                    </div>

                    <h4 class="mt-5 mb-1.5 text-xs font-semibold">{{ t('endpoints.drawerDiag') }}</h4>
                    <p v-if="drawerChecking" class="text-muted-foreground text-sm">{{ t('endpoints.diagChecking') }}</p>
                    <p v-else-if="!diagLines(drawerEp).length" class="text-muted-foreground text-sm">
                        {{ t('endpoints.diagUnchecked') }}
                    </p>
                    <div v-else class="flex flex-col gap-1.5 text-sm">
                        <div
                            v-for="line in diagLines(drawerEp)"
                            :key="line.key"
                            class="flex items-center gap-2"
                        >
                            <CheckIcon v-if="line.tone === 'ok'" class="text-success size-3.5 shrink-0" />
                            <CircleAlertIcon
                                v-else
                                class="size-3.5 shrink-0"
                                :class="line.tone === 'warn' ? 'text-warning' : 'text-error'"
                            />
                            <span>{{ line.text }}</span>
                        </div>
                    </div>

                    <h4 class="mt-5 mb-1.5 text-xs font-semibold">{{ t('endpoints.drawerTech') }}</h4>
                    <div class="selectable rounded-md bg-surface-2 p-2.5 font-mono text-[11px] break-all whitespace-pre-wrap text-muted-foreground">
                        {{ techLines(drawerEp) }}
                    </div>
                </div>

                <SheetFooter class="border-t">
                    <Button
                        variant="outline"
                        size="sm"
                        :disabled="!drawerEp || drawerChecking"
                        @click="drawerEp && checkHealth(drawerEp)"
                    >
                        {{ t('endpoints.checkBtn') }}
                    </Button>
                    <Button size="sm" @click="drawerOpen = false">{{ t('endpoints.drawerClose') }}</Button>
                </SheetFooter>
            </SheetContent>
        </Sheet>

        <!-- 移除登记：危险确认弹窗（红色左边条 + 四要素） -->
        <DangerConfirmDialog
            v-model:open="removeOpen"
            :title="t('endpoints.removeTitle')"
            :action="t('endpoints.removeAction')"
            :target="removeTarget?.display_name ?? ''"
            :consequences="[t('endpoints.removeConsequence1'), t('endpoints.removeConsequence2')]"
            :reversibility="t('endpoints.removeReversibility')"
            :confirm-label="t('endpoints.removeAction')"
            @confirm="confirmRemove"
        />
    </div>
</template>
