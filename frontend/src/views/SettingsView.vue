<script setup lang="ts">
// 设置页（shadcn-vue）：只迁新栈消费的项（ADR-0001 §6）——主题/语言；
// dev 构建另有 mock 数据层卡片（settings/DevMockCard，__DEV__ 门控异步装载，
// 生产构建该组件连同 mock.* 文案引用整体裁剪）。
// packwiz CLI 路径、实例目录路径、CF API Key 不迁——待 Phase 3 重新消费时再入。
// 保留策略节（契约 06 §9 原型 SET-02，票 #65）：五参数编辑（读默认值回填 +
// 每键合法区间提示）+「立即回收空间」（建 kind=gc 任务，消费票 #64 GC 任务面）。
// 范围校验唯一口径在后端（err.settings.retention_invalid {0}=字段名整体拒绝），
// 前端只拦「无法上 wire 的输入」（非整数/字节数无法解析），越界值照常提交以
// 呈现后端整体拒绝文案。
import { defineAsyncComponent, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Trash2 } from '@lucide/vue'
import { SettingsService } from '../api'
import type { RetentionSettingsDTO, UpdateRetentionSettingsDTO } from '../api'
import { triggerRequery } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { setThemePref, themePref } from '../stores/theme'
import type { ThemePref } from '../stores/theme'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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

// —— Mock 数据层卡片：仅 dev 构建装载（生产构建 __DEV__ 恒 false，动态导入被裁剪） ——
const DevMockCard = __DEV__
    ? defineAsyncComponent(() => import('./settings/DevMockCard.vue'))
    : null

// —— 保留策略（契约 06 §9 原型 SET-02，票 #65）——
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

// formatBytes 字节 → 人类可读（整值按 GiB/MiB/KiB 归一，否则原字节数）；0 显示
// 「0」（= 不限，preserve_max_bytes 专属语义，见键 hint）。
function formatBytes(bytes: number): string {
    if (bytes === 0) return '0'
    if (bytes % (1024 ** 3) === 0) return `${bytes / 1024 ** 3} GiB`
    if (bytes % (1024 ** 2) === 0) return `${bytes / 1024 ** 2} MiB`
    if (bytes % 1024 === 0) return `${bytes / 1024} KiB`
    return `${bytes} B`
}

// fillRetention 用读投影回填表单（读默认值回填 / 保存成功后归一化重填共用）。
function fillRetention(s: RetentionSettingsDTO): void {
    retentionForm.keepCommits = String(s.keep_commits)
    retentionForm.keepDays = String(s.keep_days)
    retentionForm.relationCapacity = formatBytes(s.relation_capacity_bytes)
    retentionForm.preserveMax = formatBytes(s.preserve_max_bytes)
    retentionForm.trashDays = String(s.trash_days)
}

onMounted(async () => {
    try {
        fillRetention(await SettingsService.GetRetentionSettings())
    } catch (e) {
        retentionError.value = errText(e)
    } finally {
        retentionLoading.value = false
    }
})

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
</script>

<template>
    <div class="mx-auto flex w-full max-w-3xl flex-col gap-4 p-4">
        <div>
            <h1 class="page-title">{{ t('settings.title') }}</h1>
            <p class="text-muted-foreground mt-1 text-sm">{{ t('settings.subtitle') }}</p>
        </div>

        <!-- 外观：主题 / 语言 -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('settings.appearanceTitle') }}</CardTitle>
                <CardDescription>{{ t('settings.appearanceHint') }}</CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
                <div class="flex items-center justify-between gap-4">
                    <div>
                        <div class="text-sm font-medium">{{ t('settings.theme') }}</div>
                        <div class="text-muted-foreground mt-0.5 text-xs">{{ t('settings.themeHint') }}</div>
                    </div>
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

                <div class="flex items-center justify-between gap-4">
                    <div class="text-sm font-medium">{{ t('settings.language') }}</div>
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
            </CardContent>
        </Card>

        <!-- 保留策略（契约 06 §9 原型 SET-02，票 #65）：五参数编辑 + 立即回收空间 -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('settings.retention.title') }}</CardTitle>
                <CardDescription>{{ t('settings.retention.desc') }}</CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
                <div v-if="retentionLoading" class="text-muted-foreground py-2 text-sm">
                    {{ t('settings.retention.loading') }}
                </div>
                <div v-else-if="retentionError" class="text-destructive text-sm">
                    {{ t('settings.retention.loadFailed') }}：{{ retentionError }}
                </div>
                <template v-else>
                    <!-- keep_commits：范围 5–200（K=3 硬保底后端固定，不设键） -->
                    <div class="flex items-center justify-between gap-4">
                        <div class="min-w-0">
                            <div class="text-sm font-medium">{{ t('settings.retention.keepCommits') }}</div>
                            <div class="text-muted-foreground mt-0.5 text-xs">{{ t('settings.retention.keepCommitsHint') }}</div>
                        </div>
                        <div class="flex w-44 shrink-0 items-center gap-2">
                            <Input v-model="retentionForm.keepCommits" type="number" min="5" max="200" class="w-full" />
                            <span class="text-muted-foreground shrink-0 text-xs">{{ t('settings.retention.unitCommits') }}</span>
                        </div>
                    </div>

                    <!-- keep_days：范围 7–365 -->
                    <div class="flex items-center justify-between gap-4">
                        <div class="min-w-0">
                            <div class="text-sm font-medium">{{ t('settings.retention.keepDays') }}</div>
                            <div class="text-muted-foreground mt-0.5 text-xs">{{ t('settings.retention.keepDaysHint') }}</div>
                        </div>
                        <div class="flex w-44 shrink-0 items-center gap-2">
                            <Input v-model="retentionForm.keepDays" type="number" min="7" max="365" class="w-full" />
                            <span class="text-muted-foreground shrink-0 text-xs">{{ t('settings.retention.unitDays') }}</span>
                        </div>
                    </div>

                    <!-- relation_capacity_bytes：128 MiB–20 GiB（支持「数字 + 单位」输入） -->
                    <div class="flex items-center justify-between gap-4">
                        <div class="min-w-0">
                            <div class="text-sm font-medium">{{ t('settings.retention.relationCapacity') }}</div>
                            <div class="text-muted-foreground mt-0.5 text-xs">{{ t('settings.retention.relationCapacityHint') }}</div>
                        </div>
                        <div class="flex w-44 shrink-0 items-center gap-2">
                            <Input v-model="retentionForm.relationCapacity" class="w-full" placeholder="1 GiB" />
                        </div>
                    </div>

                    <!-- preserve_max_bytes：1 MiB–512 MiB；0 = 不限 -->
                    <div class="flex items-center justify-between gap-4">
                        <div class="min-w-0">
                            <div class="text-sm font-medium">{{ t('settings.retention.preserveMax') }}</div>
                            <div class="text-muted-foreground mt-0.5 text-xs">{{ t('settings.retention.preserveMaxHint') }}</div>
                        </div>
                        <div class="flex w-44 shrink-0 items-center gap-2">
                            <Input v-model="retentionForm.preserveMax" class="w-full" placeholder="32 MiB" />
                        </div>
                    </div>

                    <!-- trash_days：范围 1–90 -->
                    <div class="flex items-center justify-between gap-4">
                        <div class="min-w-0">
                            <div class="text-sm font-medium">{{ t('settings.retention.trashDays') }}</div>
                            <div class="text-muted-foreground mt-0.5 text-xs">{{ t('settings.retention.trashDaysHint') }}</div>
                        </div>
                        <div class="flex w-44 shrink-0 items-center gap-2">
                            <Input v-model="retentionForm.trashDays" type="number" min="1" max="90" class="w-full" />
                            <span class="text-muted-foreground shrink-0 text-xs">{{ t('settings.retention.unitDays') }}</span>
                        </div>
                    </div>

                    <!-- 操作行：保存 + 立即回收空间（建 kind=gc 任务） -->
                    <div class="flex flex-wrap items-center gap-2 pt-1">
                        <Button size="sm" :disabled="retentionSaving" @click="saveRetention">
                            {{ t('settings.retention.save') }}
                        </Button>
                        <Button variant="outline" size="sm" :disabled="retentionGcing" @click="requestGC">
                            <Trash2 class="size-4" />
                            {{ t('settings.retention.gcRun') }}
                        </Button>
                    </div>
                    <div class="text-muted-foreground text-xs">{{ t('settings.retention.gcHint') }}</div>
                </template>
            </CardContent>
        </Card>

        <!-- Mock 数据层（仅 dev 构建渲染与生效） -->
        <DevMockCard v-if="DevMockCard" />

        <p class="text-faint mt-2 text-xs">PackGradle · P1</p>
    </div>
</template>
