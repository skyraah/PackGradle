<script setup lang="ts">
// 设置页 · Mock 数据层分区：仅开发构建装载（SettingsView 以 __DEV__ 门控异步导入，
// 生产构建此组件连同 mock.* 文案引用整体裁剪）。切换需确认——确认后整页刷新重载缓存。
// 票 #107：随设置页去卡片化，改为与画板 SET-01 同语言的全宽分区行（150px 标签）。
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { isMockEnabled, setMockEnabled } from '../../api'
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
    <section class="border-t border-border pb-1.5 pt-[18px]">
        <h2 class="text-[14.5px] font-bold leading-tight">{{ t('settings.devTitle') }}</h2>
        <p class="text-muted-foreground mb-3 mt-0.5 text-xs">{{ t('mock.hint') }}</p>
        <div class="flex flex-wrap items-center gap-3.5 py-[9px]">
            <span class="w-[150px] flex-none text-[12.5px] text-muted-foreground">{{ t('mock.switchLabel') }}</span>
            <div class="flex flex-none items-center gap-2">
                <Switch :model-value="mockOn" @update:model-value="askMock" />
            </div>
        </div>
    </section>

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
