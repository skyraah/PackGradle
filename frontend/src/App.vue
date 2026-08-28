<script setup lang="ts">
// 应用壳层（PCL2 骨架）：左侧常显 icon rail + 顶栏（品牌/页面标题/任务中心/联动状态/主题）。
// rail 选中态 = 浅色底块 + 左侧亮条；顶栏 bell 打开任务中心抽屉（操作历史唯一权威）。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useTheme } from 'vuetify'
import { Events, Window } from '@wailsio/runtime'
import { isMockEnabled, setMockEnabled } from './api'
import { navRoutes } from './router'
import { useUi } from './stores/ui'
import { useApiKeyGuide } from './stores/apiKeyGuide'
import { runningCount, unseenCount } from './stores/taskCenter'
import { overview } from './stores/instances'
import TaskCenterDrawer from './components/common/TaskCenterDrawer.vue'
import OnboardingDialog from './components/common/OnboardingDialog.vue'
import ConfirmDialog from './components/common/ConfirmDialog.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const vuetifyTheme = useTheme()
const { snackbar, snackbarMsg, snackbarTone, snackbarTimeout, dismissSnackbar } = useUi()
const { apiKeyDialog, goConfigApiKey } = useApiKeyGuide()

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

// shadcn-vue 主题令牌依赖 html.dark，随 Vuetify 当前主题同步（迁移期两套主题并存）
function syncDarkClass() {
    document.documentElement.classList.toggle('dark', vuetifyTheme.current.value.dark)
}
watch(() => vuetifyTheme.current.value, syncDarkClass, { immediate: true })

function minimiseWindow() {
    wailsWindow.Minimise()
}

function toggleMaximiseWindow() {
    wailsWindow.ToggleMaximise()
}

function closeWindow() {
    wailsWindow.Close()
}

const taskDrawer = ref(false)

const noticeIcon = computed(
    () =>
        (({
            info: 'mdi-information-outline',
            success: 'mdi-check-circle-outline',
            warning: 'mdi-alert-outline',
            error: 'mdi-alert-circle-outline',
        }) as Record<string, string>)[snackbarTone.value] ?? 'mdi-information-outline',
)

let colorScheme: MediaQueryList | null = null

function applySystemTheme(event?: MediaQueryListEvent) {
    const dark = event?.matches ?? colorScheme?.matches ?? true
    vuetifyTheme.global.name.value = dark ? 'dark' : 'light'
}

onMounted(() => {
    colorScheme = window.matchMedia('(prefers-color-scheme: dark)')
    applySystemTheme()
    colorScheme.addEventListener('change', applySystemTheme)
})

onBeforeUnmount(() => {
    colorScheme?.removeEventListener('change', applySystemTheme)
    offMaximise?.()
    offUnMaximise?.()
})

function toggleTheme() {
    vuetifyTheme.global.name.value = vuetifyTheme.global.name.value === 'dark' ? 'light' : 'dark'
}

interface NavItem {
    path: string
    icon: string
    titleKey: string
}

// rail 导航项由路由表生成
const navItems: NavItem[] = []
for (const r of navRoutes) {
    const meta = r.meta as { titleKey?: unknown; icon?: unknown } | undefined
    if (meta && typeof meta.titleKey === 'string') {
        navItems.push({
            path: r.path,
            icon: typeof meta.icon === 'string' ? meta.icon : 'mdi-circle-outline',
            titleKey: meta.titleKey,
        })
    }
}

// 项目详情页归属「项目管理」高亮
const activePath = computed(() => (route.path.startsWith('/projects') ? '/projects' : route.path))

const pageTitle = computed(() => {
    const key = route.meta?.titleKey
    if (typeof key !== 'string' || !key) return ''
    const base = t(key)
    return route.name === 'project-detail' && typeof route.params.name === 'string'
        ? base + ' · ' + route.params.name
        : base
})

// 联动状态指示：Prism 定位正常 → 绿；失败/未加载 → 中性
const prismOk = computed(() => !!overview.value && !overview.value.locate_error)
const bellBadge = computed(() => unseenCount.value)

// —— Mock 数据层指示：启用时顶栏亮紫色徽标，点击一键切回真实后端 ——
const mockMode = ref(isMockEnabled())
const mockDialog = ref(false)

function confirmDisableMock() {
    mockDialog.value = false
    setMockEnabled(false)
    setTimeout(() => window.location.reload(), 300)
}
</script>

<template>
    <v-app>
        <v-app-bar flat height="52" color="surface" class="shell-bar">
            <div class="brand-lockup app-no-drag">
                <span class="brand-mark"><v-icon icon="mdi-hammer-wrench" size="18" /></span>
                <span class="brand-name">PackGradle</span>
            </div>
            <div class="title-divider" />
            <div v-if="pageTitle" class="text-body-2 text-medium-emphasis page-title">{{ pageTitle }}</div>
            <!-- Wails 拖拽区：占据顶栏中部空白，内部不放交互控件 -->
            <div class="app-drag align-self-stretch" style="flex: 1 1 auto; min-width: 24px"></div>

            <!-- Mock 模式指示（点击一键切回真实后端） -->
            <v-chip
                v-if="mockMode"
                size="small"
                variant="flat"
                color="secondary"
                prepend-icon="mdi-flask"
                class="mr-1 app-no-drag"
                :title="t('app.mockBadgeTip')"
                @click="mockDialog = true"
            >
                MOCK
            </v-chip>

            <!-- 联动状态指示（可点击跳联动页） -->
            <v-chip
                size="small"
                variant="tonal"
                :color="prismOk ? 'success' : 'grey'"
                prepend-icon="mdi-link-variant"
                class="mr-1 app-no-drag integration-chip"
                :title="t('app.integrationTip')"
                @click="router.push('/instances')"
            >
                {{ prismOk ? t('app.integrationOk') : t('app.integrationIdle') }}
            </v-chip>

            <!-- 任务中心入口 -->
            <v-badge :content="bellBadge" :model-value="bellBadge > 0" color="primary" offset-x="4" offset-y="4">
                <v-btn icon variant="text" class="app-no-drag" :title="t('tasks.title')" @click="taskDrawer = true">
                    <v-icon :icon="runningCount > 0 ? 'mdi-bell-ring-outline' : 'mdi-bell-outline'" />
                </v-btn>
            </v-badge>

            <v-btn
                icon
                variant="text"
                class="app-no-drag"
                :title="t('common.toggleTheme')"
                @click="toggleTheme"
            >
                <v-icon :icon="vuetifyTheme.global.name.value === 'dark' ? 'mdi-white-balance-sunny' : 'mdi-weather-night'" />
            </v-btn>

            <!-- 无边框窗口控制按钮 -->
            <div class="window-controls app-no-drag">
                <v-btn icon variant="text" size="small" :title="t('app.minimise')" @click="minimiseWindow">
                    <v-icon icon="mdi-minus" size="18" />
                </v-btn>
                <v-btn icon variant="text" size="small" :title="isMaximised ? t('app.restore') : t('app.maximise')" @click="toggleMaximiseWindow">
                    <v-icon :icon="isMaximised ? 'mdi-window-restore' : 'mdi-window-maximize'" size="18" />
                </v-btn>
                <v-btn icon variant="text" size="small" class="window-close-btn" :title="t('app.close')" @click="closeWindow">
                    <v-icon icon="mdi-close" size="18" />
                </v-btn>
            </div>
        </v-app-bar>

        <!-- 左侧 icon rail（PCL2 骨架，常显不可收起） -->
        <v-navigation-drawer width="68" permanent class="shell-rail">
            <div class="pt-3">
                <div
                    v-for="item in navItems"
                    :key="item.path"
                    class="rail-item"
                    :class="{ 'rail-active': activePath === item.path }"
                    role="button"
                    tabindex="0"
                    @click="router.push(item.path)"
                    @keyup.enter="router.push(item.path)"
                >
                    <v-icon :icon="item.icon" size="24" />
                    <span class="rail-label">{{ t(item.titleKey) }}</span>
                </div>
            </div>
        </v-navigation-drawer>

        <v-main class="shell-main">
            <!-- 项目列表保持存活：从详情页返回时保留搜索/滚动状态 -->
            <router-view v-slot="{ Component, route: r }">
                <keep-alive include="ProjectsView">
                    <component :is="Component" :key="r.fullPath" />
                </keep-alive>
            </router-view>
        </v-main>

        <!-- 任务中心抽屉 -->
        <TaskCenterDrawer v-model="taskDrawer" />

        <!-- 首次引导（仅未检测到 config.toml 时弹出一次） -->
        <OnboardingDialog />

        <!-- Mock 切回确认 -->
        <ConfirmDialog
            v-model="mockDialog"
            :title="t('mock.confirmTitle')"
            :text="t('mock.confirmText')"
            :consequences="[t('mock.cOff1')]"
            :confirm-text="t('mock.disable')"
            icon="mdi-flask"
            icon-color="secondary"
            @confirm="confirmDisableMock"
        />

        <!-- API Key 引导（全局）：项目相关操作遇到 Key 错误时在此弹出 -->
        <v-dialog v-model="apiKeyDialog" max-width="480">
            <v-card class="dialog-card" elevation="8">
                <v-card-title class="d-flex align-center pt-5">
                    <v-icon icon="mdi-key-alert-outline" color="warning" class="mr-2" />
                    {{ t('projects.apiKeyDialogTitle') }}
                </v-card-title>
                <v-card-text>{{ t('projects.apiKeyDialogText') }}</v-card-text>
                <v-card-actions class="px-5 pb-4">
                    <v-spacer />
                    <v-btn variant="text" @click="apiKeyDialog = false">{{ t('common.cancel') }}</v-btn>
                    <v-btn color="primary" variant="flat" @click="goConfigApiKey">
                        {{ t('projects.goConfigureApiKey') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-snackbar
            v-model="snackbar"
            :timeout="snackbarTimeout"
            location="top right"
            color="surface-bright"
            class="app-snackbar"
            elevation="12"
        >
            <div class="notice-content">
                <v-icon :icon="noticeIcon" :color="snackbarTone" size="20" />
                <span>{{ snackbarMsg }}</span>
            </div>
            <template #actions>
                <v-btn
                    icon="mdi-close"
                    size="small"
                    variant="text"
                    :title="t('common.dismiss')"
                    @click="dismissSnackbar"
                />
            </template>
        </v-snackbar>
    </v-app>
</template>

<style scoped>
.page-title {
    max-width: 320px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
</style>
