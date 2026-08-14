<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { useTheme } from 'vuetify'
import { navRoutes } from './router'
import { useUi } from './stores/ui'
import { useApiKeyGuide } from './stores/apiKeyGuide'

const { t } = useI18n()
const route = useRoute()
const vuetifyTheme = useTheme()
const { snackbar, snackbarMsg, snackbarTone, snackbarTimeout, dismissSnackbar } = useUi()
const { apiKeyDialog, goConfigApiKey } = useApiKeyGuide()
const rail = ref(localStorage.getItem('packgradle.navigation.rail') === 'true')

watch(rail, value => {
    localStorage.setItem('packgradle.navigation.rail', String(value))
})

const noticeIcon = computed(() => ({
    info: 'mdi-information-outline',
    success: 'mdi-check-circle-outline',
    warning: 'mdi-alert-outline',
    error: 'mdi-alert-circle-outline',
})[snackbarTone.value])

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
})

interface NavItem {
    path: string
    icon: string
    titleKey: string
}

// 侧栏导航项直接由路由表生成：新增页面注册路由即自动出现
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
</script>

<template>
    <v-app>
        <v-app-bar flat height="52" color="surface" class="shell-bar">
            <v-btn
                icon="mdi-menu"
                variant="text"
                class="app-no-drag shell-menu"
                :title="t('common.toggleNavigation')"
                @click="rail = !rail"
            />
            <div class="brand-lockup">
                <span class="brand-mark"><v-icon icon="mdi-hammer-wrench" size="18" /></span>
                <span class="brand-name">PackGradle</span>
            </div>
            <div class="title-divider" />
            <div v-if="pageTitle" class="text-body-2 text-medium-emphasis page-title">{{ pageTitle }}</div>
            <!-- Wails 拖拽区：占据顶栏中部空白，内部不放交互控件 -->
            <div class="app-drag align-self-stretch" style="flex: 1 1 auto; min-width: 24px"></div>
            <v-chip size="small" variant="tonal" prepend-icon="mdi-connection" class="mr-2 app-no-drag integration-chip">
                {{ t('app.integration') }}
            </v-chip>
        </v-app-bar>

        <v-navigation-drawer v-model:rail="rail" rail-width="60" width="220" permanent class="shell-drawer">
            <v-list nav density="comfortable" class="py-3 px-2">
                <v-tooltip
                    v-for="item in navItems"
                    :key="item.path"
                    :text="t(item.titleKey)"
                    location="right"
                    :disabled="!rail"
                >
                    <template v-slot:activator="{ props }">
                        <v-list-item
                            v-bind="props"
                            :to="item.path"
                            :active="activePath === item.path"
                            :prepend-icon="item.icon"
                            :title="t(item.titleKey)"
                            class="nav-item"
                            color="primary"
                        />
                    </template>
                </v-tooltip>
            </v-list>
            <template #append>
                <div v-if="!rail" class="px-4 pb-4">
                    <div class="shell-caption text-caption text-medium-emphasis">{{ t('app.tagline') }}</div>
                </div>
            </template>
        </v-navigation-drawer>

        <v-main class="shell-main">
            <!-- 项目列表保持存活：从详情页返回时保留搜索/滚动状态 -->
            <router-view v-slot="{ Component, route: r }">
                <keep-alive include="ProjectsView">
                    <component :is="Component" :key="r.fullPath" />
                </keep-alive>
            </router-view>
        </v-main>

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
