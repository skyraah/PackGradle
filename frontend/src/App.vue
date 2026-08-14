<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { navRoutes } from './router'
import { useUi } from './stores/ui'
import { useApiKeyGuide } from './stores/apiKeyGuide'

const { t } = useI18n()
const route = useRoute()
const { snackbar, snackbarMsg } = useUi()
const { apiKeyDialog, goConfigApiKey } = useApiKeyGuide()
const rail = ref(false)

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
        <v-app-bar flat height="56" color="surface" class="shell-bar">
            <v-app-bar-nav-icon @click="rail = !rail" />
            <v-app-bar-title class="d-flex align-center" style="min-width: 0">
                <v-icon icon="mdi-hammer-wrench" color="primary" class="mr-2" />
                <span class="text-subtitle-1 font-weight-bold">PackGradle</span>
            </v-app-bar-title>
            <v-divider vertical class="mx-2" />
            <div v-if="pageTitle" class="text-body-2 text-medium-emphasis page-title">{{ pageTitle }}</div>
            <!-- Wails 拖拽区：占据顶栏中部空白，内部不放交互控件 -->
            <div class="app-drag align-self-stretch" style="flex: 1 1 auto; min-width: 24px"></div>
            <v-chip size="small" variant="outlined" prepend-icon="mdi-launch" class="mr-3 app-no-drag">
                packwiz × Prism Launcher
            </v-chip>
        </v-app-bar>

        <v-navigation-drawer v-model:rail="rail" rail-width="72" width="224" permanent class="shell-drawer">
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
                            rounded="lg"
                            color="primary"
                        />
                    </template>
                </v-tooltip>
            </v-list>
            <template #append>
                <div v-if="!rail" class="px-4 pb-4">
                    <div class="text-caption text-medium-emphasis">
                        packwiz × Prism Launcher<br />
                        整合包开发工作台
                    </div>
                </div>
            </template>
        </v-navigation-drawer>

        <v-main>
            <!-- 项目列表保持存活：从详情页返回时保留搜索/滚动状态 -->
            <router-view v-slot="{ Component, route: r }">
                <keep-alive include="ProjectsView">
                    <component :is="Component" :key="r.fullPath" />
                </keep-alive>
            </router-view>
        </v-main>

        <!-- API Key 引导（全局）：项目相关操作遇到 Key 错误时在此弹出 -->
        <v-dialog v-model="apiKeyDialog" max-width="480">
            <v-card elevation="8">
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

        <v-snackbar v-model="snackbar" timeout="4000" location="bottom right">
            {{ snackbarMsg }}
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
