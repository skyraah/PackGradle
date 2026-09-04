<script setup lang="ts">
// 设置页（画板 SET-01 对齐，票 #107）：全宽分区 + 顶边框分隔（原型 §7.11/.set-sec），
// 行结构 = 150px 标签 + 值区 + 操作区（健康徽章 + 小按钮），不再使用卡片嵌套。
// 五分区——适配器与工具、凭据、本地存储、保留策略、诊断；外观（主题/语言）为
// P1 既有能力，以同一分区语言缀于五分区之后（不占画板分区位）。
// 适配器与工具/凭据消费既有 EnvService 绑定（Detect/SetToolPath/GetApiKey/SetApiKey）；
// 凭据只显示已配置状态与 credential reference，不回显完整 secret（05 原型 §7.11）。
// 本地存储消费 SettingsService.GetStorageStats（#90 只读数据面）。
// 诊断三按钮——导出脱敏诊断包与查看日志目录待后端服务面就绪（绑定未生成），
// 点击提示占位；复制版本信息走前端剪贴板（原型 copyDiag 同语义）。
// dev 构建另有 mock 数据层分区（settings/DevMockCard，__DEV__ 门控异步装载，
// 生产构建该组件连同 mock.* 文案引用整体裁剪）。
// 保留策略节（契约 06 §9 原型 SET-02，票 #65）：五参数编辑 +「立即回收空间」，
// 行为不变仅重排（范围校验唯一口径在后端 err.settings.retention_invalid，前端只拦
// 「无法上 wire 的输入」，越界值照常提交以呈现后端整体拒绝文案）。
import { computed, defineAsyncComponent, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, ExternalLink, Folder, Trash2 } from '@lucide/vue'
import { EnvService, SettingsService } from '../api'
import type { RetentionSettingsDTO, UpdateRetentionSettingsDTO } from '../api'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'
import type { StorageStatsDTO } from '../../bindings/packgradle/internal/transport/models'
import { triggerRequery } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { setThemePref, themePref } from '../stores/theme'
import type { ThemePref } from '../stores/theme'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select'

const { t, locale } = useI18n()

// 语言偏好持久化（packgradle.locale）：P1 仅 zh-CN，切换逻辑就位后新语言包即插即用
try {
    const saved = localStorage.getItem('packgradle.locale')
    if (saved) locale.value = saved
} catch {
    // localStorage 不可用时保持默认 zh-CN
}

// —— 主题：跟随系统 / 浅色 / 深色（stores/theme 持久化 + 落 html.dark） ——
const themeOptions: { value: ThemePref; labelKey: string }[] = [
    { value: 'system', labelKey: 'settings.themeSystem' },
    { value: 'light', labelKey: 'settings.themeLight' },
    { value: 'dark', labelKey: 'settings.themeDark' },
]

// —— 语言：P1 仅 zh-CN 一套文案，选择即应用（后续语言包就位后此列表自然扩展） ——
const languageOptions = [{ value: 'zh-CN', labelKey: 'settings.languageZh' }]

function onLocaleChange(v: unknown) {
    locale.value = String(v)
    try {
        localStorage.setItem('packgradle.locale', String(v))
    } catch {
        // 持久化失败不阻断本次会话内的切换
    }
}

// —— Mock 数据层分区：仅 dev 构建装载（生产构建 __DEV__ 恒 false，动态导入被裁剪） ——
const DevMockCard = __DEV__
    ? defineAsyncComponent(() => import('./settings/DevMockCard.vue'))
    : null

// —— 适配器与工具（EnvService.Detect / SetToolPath；name = packwiz / prism-launcher）——
const toolsLoading = ref(true)
const toolsDetecting = ref(false)
const tools = ref<ToolInfo[]>([])
const packwizPath = ref('')
const packwizSaving = ref(false)

const packwizTool = computed(() => tools.value.find(x => x.name === 'packwiz'))
const prismTool = computed(() => tools.value.find(x => x.name === 'prism-launcher'))

// applyTools 收敛检测/保存两个入口的投影回填（保存返回值即最新检测结果）
function applyTools(list: ToolInfo[]): void {
    tools.value = list
    const pw = list.find(x => x.name === 'packwiz')
    if (pw && pw.path) packwizPath.value = pw.path
}

async function detectTools(): Promise<void> {
    if (toolsDetecting.value) return
    toolsDetecting.value = true
    try {
        applyTools((await EnvService.Detect()) ?? [])
        showSnackbar(t('settings.tools.detectOkToast'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        toolsDetecting.value = false
    }
}

async function savePackwizPath(): Promise<void> {
    if (packwizSaving.value || !packwizPath.value.trim()) return
    packwizSaving.value = true
    try {
        applyTools((await EnvService.SetToolPath('packwiz', packwizPath.value.trim())) ?? tools.value)
        showSnackbar(t('settings.tools.saveOkToast'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        packwizSaving.value = false
    }
}

// —— 凭据（EnvService.GetApiKey / SetApiKey）：只显示已配置状态，不回显 secret ——
const credLoading = ref(true)
const credLoadFailed = ref(false)
const credConfigured = ref(false)
const credEditing = ref(false)
const credInput = ref('')
const credSaving = ref(false)

function startReplaceCred(): void {
    credInput.value = ''
    credEditing.value = true
}

async function saveCred(): Promise<void> {
    const key = credInput.value.trim()
    if (!key || credSaving.value) return
    credSaving.value = true
    try {
        await EnvService.SetApiKey(key)
        credConfigured.value = true
        credEditing.value = false
        showSnackbar(t('settings.creds.savedToast'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        credSaving.value = false
    }
}

// —— 本地存储（SettingsService.GetStorageStats，#90 只读投影） ——
const storageLoading = ref(true)
const storageError = ref('')
const storage = ref<StorageStatsDTO | null>(null)

// formatBytes 字节 → 人类可读（整值按 GiB/MiB/KiB 归一，否则原字节数）；0 显示
// 「0」（= 不限，preserve_max_bytes 专属语义，见键 hint）。
function formatBytes(bytes: number): string {
    if (bytes === 0) return '0'
    if (bytes % (1024 ** 3) === 0) return `${bytes / 1024 ** 3} GiB`
    if (bytes % (1024 ** 2) === 0) return `${bytes / 1024 ** 2} MiB`
    if (bytes % 1024 === 0) return `${bytes / 1024} KiB`
    return `${bytes} B`
}

// —— 诊断（导出/查看日志目录待后端服务面就绪；复制版本信息走前端剪贴板） ——
async function copyVersionInfo(): Promise<void> {
    const info = [
        'PackGradle P1',
        `locale: ${locale.value}`,
        `platform: ${navigator.platform}`,
        `userAgent: ${navigator.userAgent}`,
    ].join('\n')
    try {
        await navigator.clipboard.writeText(info)
        showSnackbar(t('settings.diag.copiedToast'), 'success')
    } catch {
        showSnackbar(t('settings.diag.copyFailedToast'), 'error')
    }
}

// —— 保留策略（契约 06 §9 原型 SET-02，票 #65；行为不变仅重排） ——
const retentionLoading = ref(true)
const retentionSaving = ref(false)
const retentionGcing = ref(false)
const retentionError = ref('')

// 表单以文本承载：整数项提交时转整数；字节项支持「数字 + 单位」（GiB/MiB/KiB/B，
// 无单位=字节），与后端 int64 字节数无损互转（读回时归一化为人类可读显示）。
const retentionForm = reactive({
    keepCommits: '',
    keepDays: '',
    relationCapacity: '',
    preserveMax: '',
    trashDays: '',
})

// parseBytes 解析「数值 + 可选单位」为字节数；无法解析返回 null。
function parseBytes(text: string): number | null {
    const m = /^\s*(\d+(?:\.\d+)?)\s*(GiB|MiB|KiB|B)?\s*$/i.exec(text)
    if (!m) return null
    const n = Number(m[1])
    if (!Number.isFinite(n) || n < 0) return null
    const unit = (m[2] ?? 'B').toUpperCase()
    const mult = unit === 'GIB' ? 1024 ** 3 : unit === 'MIB' ? 1024 ** 2 : unit === 'KIB' ? 1024 : 1
    return Math.round(n * mult)
}

// fillRetention 用读投影回填表单（读默认值回填 / 保存成功后归一化重填共用）。
function fillRetention(s: RetentionSettingsDTO): void {
    retentionForm.keepCommits = String(s.keep_commits)
    retentionForm.keepDays = String(s.keep_days)
    retentionForm.relationCapacity = formatBytes(s.relation_capacity_bytes)
    retentionForm.preserveMax = formatBytes(s.preserve_max_bytes)
    retentionForm.trashDays = String(s.trash_days)
}

// buildUpdateInput 汇总表单为写输入；无法上 wire 的输入返回 null（本地拦下不发
// 请求）。范围越界不在此拦——照常提交，由后端 err.settings.retention_invalid
// 整体拒绝并回显含字段名的文案（契约 06 §3.6 唯一校验口径）。
function buildUpdateInput(): UpdateRetentionSettingsDTO | null {
    // 空串视为未填写（Number('') 恒 0，会伪装成合法 0 越界提交），转 NaN 走本地拦下
    const ints = [retentionForm.keepCommits, retentionForm.keepDays, retentionForm.trashDays].map(
        v => (v.trim() === '' ? Number.NaN : Number(v)),
    )
    const capacity = parseBytes(retentionForm.relationCapacity)
    const preserve = parseBytes(retentionForm.preserveMax)
    if (ints.some(n => !Number.isInteger(n) || n < 0) || capacity === null || preserve === null) {
        return null
    }
    return {
        keep_commits: ints[0],
        keep_days: ints[1],
        relation_capacity_bytes: capacity,
        preserve_max_bytes: preserve,
        trash_days: ints[2],
    }
}

async function saveRetention(): Promise<void> {
    if (retentionSaving.value || retentionLoading.value) return
    const input = buildUpdateInput()
    if (input === null) {
        showSnackbar(t('settings.retention.invalidInput'), 'error')
        return
    }
    retentionSaving.value = true
    try {
        // 五键整体替换；越界被后端整体拒绝（文案含字段名），既有值不受影响
        fillRetention(await SettingsService.UpdateRetentionSettings(input))
        showSnackbar(t('settings.retention.savedToast'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        retentionSaving.value = false
    }
}

async function requestGC(): Promise<void> {
    if (retentionGcing.value) return
    retentionGcing.value = true
    try {
        // 建 kind=gc 任务（全局单飞，重复点击复用既有活跃任务）；安全窗口未开
        // 时任务停排队态（msg.task.gc.waiting 文案），开窗自动续排
        await SettingsService.RequestGC()
        // 任务中心消费 syncCache 投影：主动补一轮重查让排队任务立刻可见
        triggerRequery()
        showSnackbar(t('settings.retention.gcQueuedToast'), 'success')
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        retentionGcing.value = false
    }
}

onMounted(async () => {
    // 各分区独立取数、独立降级：互不阻塞，失败在本分区行内呈现
    try {
        applyTools((await EnvService.Detect()) ?? [])
    } catch {
        // 检测失败保持未找到态，可手动填路径后保存
    } finally {
        toolsLoading.value = false
    }
    try {
        credConfigured.value = (await EnvService.GetApiKey()).trim() !== ''
    } catch {
        credLoadFailed.value = true
    } finally {
        credLoading.value = false
    }
    try {
        storage.value = await SettingsService.GetStorageStats()
    } catch (e) {
        storageError.value = errText(e)
    } finally {
        storageLoading.value = false
    }
    try {
        fillRetention(await SettingsService.GetRetentionSettings())
    } catch (e) {
        retentionError.value = errText(e)
    } finally {
        retentionLoading.value = false
    }
})
</script>

<template>
    <div class="mx-auto flex w-full max-w-[860px] flex-col p-4 text-foreground">
        <div class="mb-3.5">
            <h1 class="page-title">{{ t('settings.title') }}</h1>
            <p class="text-muted-foreground mt-1 text-sm">{{ t('settings.subtitle') }}</p>
        </div>

        <!-- 适配器与工具（SET-01 分区一）：Packwiz 路径可编辑，Prism 只读健康 -->
        <section class="border-t-0 pb-1.5 pt-1">
            <h2 class="text-[14.5px] font-bold leading-tight">{{ t('settings.tools.title') }}</h2>
            <p class="text-muted-foreground mb-3 mt-0.5 text-xs">{{ t('settings.tools.desc') }}</p>

            <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.tools.packwizPath') }}</span>
                <div class="min-w-[200px] flex-1">
                    <Input v-model="packwizPath" :placeholder="t('settings.tools.pathPlaceholder')" class="w-full" />
                </div>
                <div class="flex flex-none items-center gap-2">
                    <Badge :variant="packwizTool?.found ? 'st-ok' : 'st-warn'">
                        {{ packwizTool?.found ? t('settings.tools.found') : t('settings.tools.missing') }}
                    </Badge>
                    <Button variant="outline" size="sm" :disabled="toolsDetecting" @click="detectTools">
                        {{ t('settings.tools.checkBtn') }}
                    </Button>
                    <Button size="sm" :disabled="packwizSaving || !packwizPath.trim()" @click="savePackwizPath">
                        {{ t('settings.tools.saveBtn') }}
                    </Button>
                </div>
            </div>

            <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.tools.prism') }}</span>
                <div class="flex min-w-[200px] flex-1 items-center gap-2">
                    <Badge :variant="prismTool?.found ? 'st-ok' : 'st-warn'">
                        {{ prismTool?.found ? t('settings.tools.found') : t('settings.tools.missing') }}
                    </Badge>
                    <span v-if="prismTool?.path" class="text-muted-foreground truncate font-mono text-xs" :title="prismTool.path">
                        {{ prismTool.path }}
                    </span>
                </div>
            </div>
            <p v-if="toolsLoading" class="text-muted-foreground py-1 text-xs">{{ t('settings.tools.loading') }}</p>
        </section>

        <!-- 凭据（SET-01 分区二）：只显示已配置状态与 credential reference，不回显 secret -->
        <section class="border-t border-border pb-1.5 pt-[18px]">
            <h2 class="text-[14.5px] font-bold leading-tight">{{ t('settings.creds.title') }}</h2>
            <p class="text-muted-foreground mb-3 mt-0.5 text-xs">{{ t('settings.creds.desc') }}</p>

            <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.creds.cfKey') }}</span>
                <div v-if="credEditing" class="flex min-w-[200px] flex-1 items-center gap-2">
                    <Input
                        v-model="credInput"
                        :placeholder="t('settings.creds.inputPlaceholder')"
                        type="password"
                        class="w-full"
                        autocomplete="off"
                    />
                </div>
                <div v-else class="flex min-w-[200px] flex-1 items-center gap-2">
                    <template v-if="credLoadFailed">
                        <span class="text-destructive text-xs">{{ t('settings.creds.loadFailed') }}</span>
                    </template>
                    <template v-else-if="!credLoading">
                        <Badge :variant="credConfigured ? 'st-ok' : 'st-mut'">
                            {{ credConfigured ? t('settings.creds.configured') : t('settings.creds.notConfigured') }}
                        </Badge>
                        <span class="text-muted-foreground truncate font-mono text-xs">{{ t('settings.creds.refText') }}</span>
                    </template>
                </div>
                <div class="flex flex-none items-center gap-2">
                    <template v-if="credEditing">
                        <Button size="sm" :disabled="credSaving || !credInput.trim()" @click="saveCred">
                            {{ t('settings.creds.saveBtn') }}
                        </Button>
                        <Button variant="ghost" size="sm" :disabled="credSaving" @click="credEditing = false">
                            {{ t('common.cancel') }}
                        </Button>
                    </template>
                    <Button v-else variant="outline" size="sm" :disabled="credLoading || credLoadFailed" @click="startReplaceCred">
                        {{ t('settings.creds.replaceBtn') }}
                    </Button>
                </div>
            </div>
        </section>

        <!-- 本地存储（SET-01 分区三）：GetStorageStats 只读健康 -->
        <section class="border-t border-border pb-1.5 pt-[18px]">
            <h2 class="text-[14.5px] font-bold leading-tight">{{ t('settings.storage.title') }}</h2>
            <p class="text-muted-foreground mb-3 mt-0.5 text-xs">{{ t('settings.storage.desc') }}</p>

            <div v-if="storageLoading" class="text-muted-foreground py-1.5 text-sm">{{ t('settings.storage.loading') }}</div>
            <div v-else-if="storageError" class="text-destructive py-1.5 text-sm">
                {{ t('settings.storage.loadFailed') }}：{{ storageError }}
            </div>
            <template v-else-if="storage">
                <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.storage.casTotal') }}</span>
                    <div class="min-w-[200px] flex-1 text-sm">
                        {{ storage.cas_object_count.toLocaleString() }} {{ t('settings.storage.objectUnit') }}
                        <span class="text-muted-foreground">· {{ formatBytes(storage.cas_total_bytes) }}</span>
                    </div>
                    <div class="flex flex-none items-center gap-2">
                        <Badge variant="st-ok">{{ t('settings.storage.healthy') }}</Badge>
                    </div>
                </div>
                <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.storage.dbSize') }}</span>
                    <div class="min-w-[200px] flex-1 text-sm">{{ formatBytes(storage.db_size_bytes) }}</div>
                    <div class="flex flex-none items-center gap-2">
                        <Badge variant="st-ok">{{ t('settings.storage.healthy') }}</Badge>
                    </div>
                </div>
                <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.storage.freeDisk') }}</span>
                    <div class="min-w-[200px] flex-1 text-sm">{{ formatBytes(storage.free_disk_bytes) }}</div>
                </div>
            </template>
        </section>

        <!-- 保留策略（SET-01 分区四，原型 SET-02；行为不变仅重排） -->
        <section class="border-t border-border pb-1.5 pt-[18px]">
            <h2 class="text-[14.5px] font-bold leading-tight">{{ t('settings.retention.title') }}</h2>
            <p class="text-muted-foreground mb-3 mt-0.5 text-xs">{{ t('settings.retention.desc') }}</p>

            <div v-if="retentionLoading" class="text-muted-foreground py-1.5 text-sm">
                {{ t('settings.retention.loading') }}
            </div>
            <div v-else-if="retentionError" class="text-destructive py-1.5 text-sm">
                {{ t('settings.retention.loadFailed') }}：{{ retentionError }}
            </div>
            <template v-else>
                <!-- keep_commits：范围 5–200（K=3 硬保底后端固定，不设键） -->
                <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.retention.keepCommits') }}</span>
                    <div class="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                        <Input v-model="retentionForm.keepCommits" type="number" min="5" max="200" class="w-24" />
                        <span class="text-muted-foreground text-xs">{{ t('settings.retention.unitCommits') }}</span>
                        <span class="text-muted-foreground text-xs">{{ t('settings.retention.keepCommitsHint') }}</span>
                    </div>
                </div>

                <!-- keep_days：范围 7–365 -->
                <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.retention.keepDays') }}</span>
                    <div class="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                        <Input v-model="retentionForm.keepDays" type="number" min="7" max="365" class="w-24" />
                        <span class="text-muted-foreground text-xs">{{ t('settings.retention.unitDays') }}</span>
                        <span class="text-muted-foreground text-xs">{{ t('settings.retention.keepDaysHint') }}</span>
                    </div>
                </div>

                <!-- relation_capacity_bytes：128 MiB–20 GiB（支持「数字 + 单位」输入） -->
                <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.retention.relationCapacity') }}</span>
                    <div class="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                        <Input v-model="retentionForm.relationCapacity" placeholder="1 GiB" class="w-28" />
                        <span class="text-muted-foreground text-xs">{{ t('settings.retention.relationCapacityHint') }}</span>
                    </div>
                </div>

                <!-- preserve_max_bytes：1 MiB–512 MiB；0 = 不限 -->
                <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.retention.preserveMax') }}</span>
                    <div class="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                        <Input v-model="retentionForm.preserveMax" placeholder="32 MiB" class="w-28" />
                        <span class="text-muted-foreground text-xs">{{ t('settings.retention.preserveMaxHint') }}</span>
                    </div>
                </div>

                <!-- trash_days：范围 1–90 -->
                <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                    <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.retention.trashDays') }}</span>
                    <div class="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                        <Input v-model="retentionForm.trashDays" type="number" min="1" max="90" class="w-24" />
                        <span class="text-muted-foreground text-xs">{{ t('settings.retention.unitDays') }}</span>
                        <span class="text-muted-foreground text-xs">{{ t('settings.retention.trashDaysHint') }}</span>
                    </div>
                </div>

                <!-- 操作行：保存 + 立即回收空间（建 kind=gc 任务） -->
                <div class="flex flex-wrap items-center gap-2 py-[9px]">
                    <Button size="sm" :disabled="retentionSaving" @click="saveRetention">
                        {{ t('settings.retention.save') }}
                    </Button>
                    <Button variant="outline" size="sm" :disabled="retentionGcing" @click="requestGC">
                        <Trash2 class="size-4" />
                        {{ t('settings.retention.gcRun') }}
                    </Button>
                </div>
                <p class="text-muted-foreground mt-1 text-xs">{{ t('settings.retention.gcHint') }}</p>
            </template>
        </section>

        <!-- 诊断（SET-01 分区五）：导出/日志目录待后端服务面，复制版本信息即时可用 -->
        <section class="border-t border-border pb-1.5 pt-[18px]">
            <h2 class="text-[14.5px] font-bold leading-tight">{{ t('settings.diag.title') }}</h2>
            <p class="text-muted-foreground mb-3 mt-0.5 text-xs">{{ t('settings.diag.desc') }}</p>

            <div class="flex flex-wrap items-center gap-2 py-[9px]">
                <Button variant="outline" size="sm" @click="showSnackbar(t('settings.diag.pendingToast'), 'info')">
                    <ExternalLink class="size-4" />
                    {{ t('settings.diag.exportBtn') }}
                </Button>
                <Button variant="outline" size="sm" @click="showSnackbar(t('settings.diag.pendingToast'), 'info')">
                    <Folder class="size-4" />
                    {{ t('settings.diag.logsBtn') }}
                </Button>
                <Button variant="ghost" size="sm" @click="copyVersionInfo">
                    <Copy class="size-4" />
                    {{ t('settings.diag.copyVersionBtn') }}
                </Button>
            </div>
        </section>

        <!-- 外观：主题 / 语言（P1 既有能力，同分区语言缀于画板五分区之后） -->
        <section class="border-t border-border pb-1.5 pt-[18px]">
            <h2 class="text-[14.5px] font-bold leading-tight">{{ t('settings.appearanceTitle') }}</h2>
            <p class="text-muted-foreground mb-3 mt-0.5 text-xs">{{ t('settings.appearanceHint') }}</p>

            <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.theme') }}</span>
                <div class="min-w-[200px] flex-1 text-xs text-muted-foreground">{{ t('settings.themeHint') }}</div>
                <div class="flex flex-none items-center gap-2">
                    <Select :model-value="themePref" @update:model-value="setThemePref($event as ThemePref)">
                        <SelectTrigger class="w-36">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                            <SelectItem v-for="o in themeOptions" :key="o.value" :value="o.value">
                                {{ t(o.labelKey) }}
                            </SelectItem>
                        </SelectContent>
                    </Select>
                </div>
            </div>

            <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
                <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('settings.language') }}</span>
                <div class="flex flex-none items-center gap-2">
                    <Select :model-value="locale" @update:model-value="onLocaleChange">
                        <SelectTrigger class="w-36">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                            <SelectItem v-for="o in languageOptions" :key="o.value" :value="o.value">
                                {{ t(o.labelKey) }}
                            </SelectItem>
                        </SelectContent>
                    </Select>
                </div>
            </div>
        </section>

        <!-- Mock 数据层（仅 dev 构建渲染与生效） -->
        <DevMockCard v-if="DevMockCard" />

        <p class="text-faint mb-2 mt-3 text-xs">PackGradle · P1</p>
    </div>
</template>
