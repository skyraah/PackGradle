<script setup lang="ts">
import { ref } from 'vue'
import EnvView from './views/EnvView.vue'
import ProjectsView from './views/ProjectsView.vue'

type ViewKey = 'env' | 'projects'
const current = ref<ViewKey>('env')

const navItems = [
    { key: 'env' as ViewKey, title: '环境配置', icon: 'mdi-tune-variant' },
    { key: 'projects' as ViewKey, title: '项目管理', icon: 'mdi-package-variant-closed' },
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
                    :active="current === item.key"
                    :prepend-icon="item.icon"
                    :title="item.title"
                    @click="current = item.key"
                />
            </v-list>
        </v-navigation-drawer>

        <v-main>
            <v-container fluid class="pa-6">
                <EnvView v-if="current === 'env'" />
                <ProjectsView v-else />
            </v-container>
        </v-main>
    </v-app>
</template>
