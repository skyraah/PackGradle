<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import EnvView from './views/EnvView.vue'
import ProjectsView from './views/ProjectsView.vue'
import { currentView, type ViewKey } from './nav'

const { t } = useI18n()

const navItems = [
    { key: 'env' as ViewKey, titleKey: 'nav.env', icon: 'mdi-tune-variant' },
    { key: 'projects' as ViewKey, titleKey: 'nav.projects', icon: 'mdi-package-variant-closed' },
]
</script>

<template>
    <v-app>
        <v-app-bar flat density="compact">
            <v-app-bar-title>
                <v-icon icon="mdi-hammer-wrench" class="mr-2" color="primary" />
                PackGradle
            </v-app-bar-title>
            <v-spacer />
            <v-chip size="small" variant="outlined" prepend-icon="mdi-launch">
                packwiz × Prism Launcher
            </v-chip>
        </v-app-bar>

        <v-navigation-drawer permanent>
            <v-list nav>
                <v-list-item
                    v-for="item in navItems"
                    :key="item.key"
                    :active="currentView === item.key"
                    :prepend-icon="item.icon"
                    :title="t(item.titleKey)"
                    @click="currentView = item.key"
                />
            </v-list>
        </v-navigation-drawer>

        <v-main>
            <v-container fluid class="pa-6">
                <EnvView v-if="currentView === 'env'" />
                <ProjectsView v-else />
            </v-container>
        </v-main>
    </v-app>
</template>
