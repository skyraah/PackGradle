<script setup lang="ts">
// 设置页（shadcn-vue）：只迁新栈消费的项（ADR-0001 §6）——主题/语言；
// dev 构建另有 mock 数据层卡片（settings/DevMockCard，__DEV__ 门控异步装载，
// 生产构建该组件连同 mock.* 文案引用整体裁剪）。
// packwiz CLI 路径、实例目录路径、CF API Key 不迁——待 Phase 3 重新消费时再入。
import { defineAsyncComponent } from 'vue'
import { useI18n } from 'vue-i18n'
import { setThemePref, themePref } from '../stores/theme'
import type { ThemePref } from '../stores/theme'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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
</script>

<template>
    <div class="mx-auto flex w-full max-w-3xl flex-col gap-4 p-4">
        <div>
            <h1 class="text-xl font-semibold">{{ t('settings.title') }}</h1>
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

        <!-- Mock 数据层（仅 dev 构建渲染与生效） -->
        <DevMockCard v-if="DevMockCard" />

        <p class="text-faint mt-2 text-xs">PackGradle · P1</p>
    </div>
</template>
