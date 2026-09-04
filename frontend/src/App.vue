<script setup lang="ts">
// 应用壳层：左侧 68px icon rail + 顶栏（品牌/页面标题/mock 徽标(仅 dev)/任务中心/主题/窗口控制）。
// ADR-0001 切换发布后全部为 shadcn-vue + Tailwind v4，导航按 UX 原型 §4.1 四项一级入口。
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Events, Window } from '@wailsio/runtime'
import {
    Bell,
    BellRing,
    CircleAlert,
    CircleCheck,
    CircleX,
    Gamepad2,
    Hammer,
    Info,
    LayoutGrid,
    Minus,
    Moon,
    Package,
    Settings as SettingsIcon,
    Square,
    Sun,
    X,
} from '@lucide/vue'
import { navRoutes } from './router'
import { useUi } from './stores/ui'
import { tasks } from './stores/syncCache'
import { isDark, toggleTheme } from './stores/theme'
import TaskCenterDrawer from './components/common/TaskCenterDrawer.vue'
import { Button } from '@/components/ui/button'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { snackbar, snackbarMsg, snackbarTone, snackbarTimeout, dismissSnackbar } = useUi()

// 无边框窗口：标题栏控件全部自绘，需跟踪最大化状态以切换图标并去除圆角
const wailsWindow = Window
const isMaximised = ref(false)
wailsWindow.IsMaximised().then((v: boolean) => (isMaximised.value = v)).catch(() => {})
const offMaximise = Events.On(Events.Types.Windows.WindowMaximise, () => (isMaximised.value = true))
const offUnMaximise = Events.On(Events.Types.Windows.WindowUnMaximise, () => (isMaximised.value = false))

function syncMaximisedClass() {
    document.documentElement.classList.toggle('window-maximised', isMaximised.value)
}
watch(isMaximised, syncMaximisedClass)
onMounted(syncMaximisedClass)

function minimiseWindow() {
    wailsWindow.Minimise()
}

function toggleMaximiseWindow() {
    wailsWindow.ToggleMaximise()
}

function closeWindow() {
    wailsWindow.Close()
}

// rail 导航项由路由表生成，meta.icon 为 lucide 图标名
const navIcons: Record<string, Component> = {
    LayoutGrid,
    Package,
    Gamepad2,
    Settings: SettingsIcon,
}

const navItems = navRoutes
    .map(r => {
        const meta = r.meta as { titleKey?: unknown; icon?: unknown } | undefined
        if (!meta || typeof meta.titleKey !== 'string') return null
        return {
            path: r.path,
            icon: typeof meta.icon === 'string' && navIcons[meta.icon] ? navIcons[meta.icon] : LayoutGrid,
            titleKey: meta.titleKey,
        }
    })
    .filter((v): v is NonNullable<typeof v> => v !== null)

// rail 高亮按一级路径归属（/workspaces/:id/... 高亮「工作区」）
const activePath = computed(() => '/' + (route.path.split('/')[1] ?? ''))

const pageTitle = computed(() => {
    const key = route.meta?.titleKey
    return typeof key === 'string' && key ? t(key) : ''
})

const taskDrawer = ref(false)
const activeTaskCount = computed(() => tasks.value.size)

// —— Mock 模式徽标：仅开发构建装载（生产构建 __DEV__ 恒 false，动态导入被裁剪，
// dist 中无 MOCK 徽标与 mock 痕迹，ADR-0001 §5） ——
const MockBadge = __DEV__
    ? defineAsyncComponent(() => import('./components/common/MockBadge.vue'))
    : null

// —— 全局通知队列：ui store 排队，这里负责计时自动关闭 ——
let snackbarTimer: ReturnType<typeof setTimeout> | undefined
watch(
    [snackbar, snackbarTimeout],
    ([open, timeout]) => {
        if (snackbarTimer) clearTimeout(snackbarTimer)
        if (open) snackbarTimer = setTimeout(dismissSnackbar, timeout)
    },
    { immediate: true },
)

const toneIcon: Record<string, Component> = {
    info: Info,
    success: CircleCheck,
    warning: CircleAlert,
    error: CircleX,
}
const toneIconClass: Record<string, string> = {
    info: 'text-primary',
    success: 'text-emerald-500',
    warning: 'text-amber-500',
    error: 'text-destructive',
}
// 原型 toast（.toast.ok/.err）：中性面板 + 3px 语义色左边框；info/warning 顺延主色 / 琥珀
const toneAccentClass: Record<string, string> = {
    info: 'border-l-primary',
    success: 'border-l-emerald-500',
    warning: 'border-l-amber-500',
    error: 'border-l-destructive',
}

onBeforeUnmount(() => {
    offMaximise?.()
    offUnMaximise?.()
    if (snackbarTimer) clearTimeout(snackbarTimer)
})
</script>

<template>
    <div class="flex h-full w-full flex-col overflow-hidden bg-background text-foreground">
        <header class="bg-card/90 flex h-[46px] flex-none items-center border-b border-border px-2.5 backdrop-blur">
            <div class="app-no-drag flex flex-none items-center gap-2">
                <span class="grid size-[30px] place-items-center rounded-lg bg-primary text-primary-foreground">
                    <Hammer class="size-4" />
                </span>
                <span class="text-sm font-semibold">PackGradle</span>
            </div>
            <div class="mx-3.5 h-5 w-px flex-none bg-border" />
            <div v-if="pageTitle" class="max-w-80 truncate text-sm text-muted-foreground">{{ pageTitle }}</div>
            <!-- Wails 拖拽区：占据顶栏中部空白，内部不放交互控件 -->
            <div class="app-drag min-w-6 flex-1 self-stretch"></div>

            <!-- Mock 模式徽标（仅 dev 构建；点击进设置页关闭） -->
            <MockBadge v-if="MockBadge" />

            <!-- 任务中心入口（徽标 = 活跃任务数） -->
            <Button
                variant="ghost"
                size="icon-sm"
                class="app-no-drag relative"
                :title="t('tasks.title')"
                @click="taskDrawer = true"
            >
                <BellRing v-if="activeTaskCount > 0" class="size-4.5" />
                <Bell v-else class="size-4.5" />
                <span
                    v-if="activeTaskCount > 0"
                    class="bg-primary text-primary-foreground absolute -top-0.5 -right-0.5 grid size-4 place-items-center rounded-full text-[10px] font-semibold"
                >
                    {{ activeTaskCount }}
                </span>
            </Button>

            <Button
                variant="ghost"
                size="icon-sm"
                class="app-no-drag"
                :title="t('common.toggleTheme')"
                @click="toggleTheme"
            >
                <Sun v-if="isDark" class="size-4.5" />
                <Moon v-else class="size-4.5" />
            </Button>

            <!-- 无边框窗口控制按钮 -->
            <div class="app-no-drag ml-1 flex items-center">
                <button
                    class="hover:bg-accent grid size-8 place-items-center rounded-md"
                    :title="t('app.minimise')"
                    @click="minimiseWindow"
                >
                    <Minus class="size-4" />
                </button>
                <button
                    class="hover:bg-accent grid size-8 place-items-center rounded-md"
                    :title="isMaximised ? t('app.restore') : t('app.maximise')"
                    @click="toggleMaximiseWindow"
                >
                    <LayoutGrid v-if="isMaximised" class="size-3.5" />
                    <Square v-else class="size-3.5" />
                </button>
                <button
                    class="grid size-8 place-items-center rounded-md hover:bg-[#e81123] hover:text-white"
                    :title="t('app.close')"
                    @click="closeWindow"
                >
                    <X class="size-4" />
                </button>
            </div>
        </header>

        <div class="flex min-h-0 flex-1">
            <!-- 左侧 icon rail（常显不可收起，UX 原型 §5.1） -->
            <nav class="bg-rail flex w-[68px] flex-none flex-col border-r border-border pt-3">
                <button
                    v-for="item in navItems"
                    :key="item.path"
                    class="mx-auto my-1 flex h-14 w-14 flex-col items-center justify-center gap-1 rounded-[10px]"
                    :class="
                        activePath === item.path
                            ? 'bg-tint-primary text-primary'
                            : 'text-muted-foreground hover:bg-accent hover:text-foreground'
                    "
                    @click="router.push(item.path)"
                >
                    <component :is="item.icon" class="size-6" />
                    <span class="text-[10px] leading-none">{{ t(item.titleKey) }}</span>
                </button>
            </nav>

            <!-- 主内容区：唯一页面纵向滚动容器 -->
            <main class="min-w-0 flex-1 overflow-y-auto">
                <router-view />
            </main>
        </div>
    </div>

    <!-- 任务中心抽屉 -->
    <TaskCenterDrawer v-model="taskDrawer" />

    <!-- 全局通知（ui store 排队，右下角 toast：语义色左边框 + 约 3200ms 自动消失；长任务结果以任务中心为权威） -->
    <Transition name="toast">
        <div
            v-if="snackbar"
            class="app-no-drag bg-surface-3 fixed right-[18px] bottom-[18px] z-[2400] flex max-w-[380px] items-start gap-2.5 rounded-lg border border-border border-l-[3px] px-3.5 py-[9px] text-[12.5px] leading-relaxed shadow-lg"
            :class="toneAccentClass[snackbarTone] ?? toneAccentClass.info"
        >
            <component :is="toneIcon[snackbarTone] ?? Info" class="mt-0.5 size-4 flex-none" :class="toneIconClass[snackbarTone]" />
            <span class="min-w-0 break-all">{{ snackbarMsg }}</span>
            <button
                class="text-muted-foreground hover:text-foreground ml-1 flex-none"
                :title="t('common.dismiss')"
                @click="dismissSnackbar"
            >
                <X class="size-4" />
            </button>
        </div>
    </Transition>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
    transition:
        opacity 160ms ease,
        transform 160ms ease;
}
.toast-enter-from,
.toast-leave-to {
    opacity: 0;
    transform: translateY(8px);
}
</style>
