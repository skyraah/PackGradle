<script setup lang="ts">
// 设置页 · Mock 数据层卡片：仅开发构建装载（SettingsView 以 __DEV__ 门控异步导入，
// 生产构建此组件连同 mock.* 文案引用整体裁剪）。切换需确认——确认后整页刷新重载缓存。
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { isMockEnabled, setMockEnabled } from '../../api'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Switch } from '@/components/ui/switch'
import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
} from '@/components/ui/alert-dialog'

const { t } = useI18n()

const mockOn = ref(isMockEnabled())
const mockDialog = ref(false)
const mockPending = ref(false)

function askMock(v: boolean) {
    mockPending.value = v
    mockDialog.value = true
}

function confirmMock() {
    mockDialog.value = false
    mockOn.value = mockPending.value
    setMockEnabled(mockPending.value)
    setTimeout(() => window.location.reload(), 300)
}
</script>

<template>
    <Card>
        <CardHeader>
            <CardTitle>{{ t('settings.devTitle') }}</CardTitle>
            <CardDescription>{{ t('mock.hint') }}</CardDescription>
        </CardHeader>
        <CardContent class="flex items-center justify-between gap-4">
            <div class="text-sm font-medium">{{ t('mock.switchLabel') }}</div>
            <Switch :model-value="mockOn" @update:model-value="askMock" />
        </CardContent>
    </Card>

    <!-- Mock 切换确认（切换后整页刷新） -->
    <AlertDialog v-model:open="mockDialog">
        <AlertDialogContent>
            <AlertDialogHeader>
                <AlertDialogTitle>{{ t('mock.confirmTitle') }}</AlertDialogTitle>
                <AlertDialogDescription>{{ t('mock.confirmText') }}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
                <AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel>
                <AlertDialogAction @click="confirmMock">
                    {{ mockPending ? t('mock.enable') : t('mock.disable') }}
                </AlertDialogAction>
            </AlertDialogFooter>
        </AlertDialogContent>
    </AlertDialog>
</template>
