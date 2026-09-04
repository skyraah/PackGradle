<script setup lang="ts">
// /sources：Packwiz 项目源端点管理（发现·登记·健康；契约 03 §2.5，IA 见 05 原型 §7.9）。
// 只管理端点登记与健康，不含任何跨端同步操作。
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ProjectService } from '../api'
import type { ProjectCandidateDTO } from '../api'
import { useEndpointPage } from '../composables/useEndpointPage'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()

const page = useEndpointPage({
    list: () => ProjectService.ListProjects(),
    register: (rootPath) => ProjectService.RegisterProject({ root_path: rootPath }),
    health: (endpointID) => ProjectService.GetProjectHealth(endpointID),
})
const { registered, loadingList, registering, health, loadRegistered, register, checkHealth, healthOf, healthBadgeTone } = page

const candidates = ref<ProjectCandidateDTO[]>([])
const parentDir = ref('')
const manualPath = ref('')
const discovering = ref(false)

onMounted(loadRegistered)

async function discover() {
    if (!parentDir.value.trim()) return
    discovering.value = true
    try {
        candidates.value = (await ProjectService.DiscoverProjects(parentDir.value.trim())) ?? []
    } catch (e) {
        candidates.value = []
        showSnackbar(errText(e), 'error')
    } finally {
        discovering.value = false
    }
}

// registerFromPath 登记后联动重发现（刷新候选的 registered 状态）
async function registerFromPath(rootPath: string) {
    const ep = await register(rootPath)
    if (ep) {
        manualPath.value = ''
        discover()
    }
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4 p-4 text-foreground">
        <div>
            <h1 class="text-xl font-semibold">{{ t('sources.title') }}</h1>
            <p class="text-muted-foreground mt-1 text-sm">{{ t('sources.subtitle') }}</p>
        </div>

        <!-- 已登记端点 -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('sources.registeredTitle') }}</CardTitle>
            </CardHeader>
            <CardContent>
                <Table v-if="registered.length">
                    <TableHeader>
                        <TableRow>
                            <TableHead>{{ t('endpoints.colName') }}</TableHead>
                            <TableHead>{{ t('endpoints.colPath') }}</TableHead>
                            <TableHead>{{ t('endpoints.colAdapter') }}</TableHead>
                            <TableHead>{{ t('endpoints.colHealth') }}</TableHead>
                            <TableHead>{{ t('endpoints.colAction') }}</TableHead>
                        </TableRow>
                    </TableHeader>
                    <TableBody>
                        <TableRow v-for="ep in registered" :key="ep.id">
                            <TableCell class="font-medium">{{ ep.display_name }}</TableCell>
                            <TableCell class="text-muted-foreground max-w-80 truncate text-xs" :title="ep.root_path">
                                {{ ep.root_path }}
                            </TableCell>
                            <TableCell>
                                <Badge variant="outline">{{ ep.adapter }}</Badge>
                            </TableCell>
                            <TableCell>
                                <template v-if="health.get(ep.id) === 'checking'">
                                    <span class="text-muted-foreground text-xs">{{ t('endpoints.health.checking') }}</span>
                                </template>
                                <template v-else-if="healthOf(ep.id)">
                                    <Badge :variant="healthBadgeTone(healthOf(ep.id)!.status).variant">
                                        {{ t('endpoints.health.' + healthOf(ep.id)!.status) }}
                                    </Badge>
                                </template>
                                <span v-else class="text-muted-foreground text-xs">{{ t('endpoints.health.unchecked') }}</span>
                            </TableCell>
                            <TableCell>
                                <Button variant="outline" size="sm" @click="checkHealth(ep)">
                                    {{ t('endpoints.checkBtn') }}
                                </Button>
                            </TableCell>
                        </TableRow>
                    </TableBody>
                </Table>
                <p v-else class="text-muted-foreground text-sm">
                    {{ loadingList ? t('endpoints.loading') : t('sources.registeredEmpty') }}
                </p>
            </CardContent>
        </Card>

        <!-- 发现与登记 -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('sources.candidatesTitle') }}</CardTitle>
                <CardDescription>{{ t('sources.subtitle') }}</CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
                <div class="flex items-center gap-2">
                    <div class="flex w-96 max-w-full flex-col gap-1">
                        <label class="text-xs font-medium" for="sources-parent-dir">{{ t('sources.parentLabel') }}</label>
                        <Input
                            id="sources-parent-dir"
                            v-model="parentDir"
                            :placeholder="t('sources.parentPlaceholder')"
                            @keydown.enter="discover"
                        />
                    </div>
                    <Button :disabled="discovering || !parentDir.trim()" class="mt-5" @click="discover">
                        {{ t('sources.discoverBtn') }}
                    </Button>
                </div>
                <div class="flex items-center gap-2">
                    <div class="flex w-96 max-w-full flex-col gap-1">
                        <label class="text-xs font-medium" for="sources-manual-path">{{ t('endpoints.pathLabel') }}</label>
                        <Input
                            id="sources-manual-path"
                            v-model="manualPath"
                            :placeholder="t('endpoints.pathPlaceholder')"
                            @keydown.enter="registerFromPath(manualPath.trim())"
                        />
                    </div>
                    <Button
                        variant="secondary"
                        :disabled="registering || !manualPath.trim()"
                        class="mt-5"
                        @click="registerFromPath(manualPath.trim())"
                    >
                        {{ t('sources.registerBtn') }}
                    </Button>
                </div>

                <Table v-if="candidates.length">
                    <TableHeader>
                        <TableRow>
                            <TableHead>{{ t('endpoints.colName') }}</TableHead>
                            <TableHead>{{ t('endpoints.colPath') }}</TableHead>
                            <TableHead>{{ t('endpoints.colMC') }}</TableHead>
                            <TableHead>{{ t('endpoints.colLoader') }}</TableHead>
                            <TableHead>{{ t('endpoints.colAction') }}</TableHead>
                        </TableRow>
                    </TableHeader>
                    <TableBody>
                        <TableRow v-for="c in candidates" :key="c.root_path">
                            <TableCell class="font-medium">{{ c.display_name }}</TableCell>
                            <TableCell class="text-muted-foreground max-w-72 truncate text-xs" :title="c.root_path">
                                {{ c.root_path }}
                            </TableCell>
                            <TableCell>{{ c.minecraft || '—' }}</TableCell>
                            <TableCell>{{ c.modloader || '—' }}</TableCell>
                            <TableCell>
                                <Badge v-if="c.registered" variant="secondary">{{ t('endpoints.registeredBadge') }}</Badge>
                                <Button v-else variant="outline" size="sm" :disabled="registering" @click="registerFromPath(c.root_path)">
                                    {{ t('sources.registerBtn') }}
                                </Button>
                            </TableCell>
                        </TableRow>
                    </TableBody>
                </Table>
                <p v-else-if="parentDir.trim()" class="text-muted-foreground text-sm">{{ t('sources.candidatesEmpty') }}</p>
            </CardContent>
        </Card>
    </div>
</template>
