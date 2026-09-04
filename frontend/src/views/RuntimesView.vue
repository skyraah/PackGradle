<script setup lang="ts">
// /runtimes：Prism 运行实例端点管理（发现·登记·健康；契约 03 §2.5，IA 见 05 原型 §7.10）。
// 只管理端点登记与健康，不含任何跨端写入动作。
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RuntimeService } from '../api'
import type { RuntimeCandidateDTO } from '../api'
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
    list: () => RuntimeService.ListRuntimes(),
    register: (rootPath) => RuntimeService.RegisterRuntime({ root_path: rootPath }),
    health: (endpointID) => RuntimeService.GetRuntimeHealth(endpointID),
})
const { registered, loadingList, registering, health, loadRegistered, register, checkHealth, healthOf, healthBadgeTone } = page

const candidates = ref<RuntimeCandidateDTO[]>([])
const manualPath = ref('')
const discovering = ref(false)

onMounted(() => {
    loadRegistered()
    discover()
})

async function discover() {
    discovering.value = true
    try {
        candidates.value = (await RuntimeService.DiscoverRuntimes()) ?? []
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
            <h1 class="page-title">{{ t('runtimes.title') }}</h1>
            <p class="text-muted-foreground mt-1 text-sm">{{ t('runtimes.subtitle') }}</p>
        </div>

        <!-- 已登记端点 -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('runtimes.registeredTitle') }}</CardTitle>
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
                    {{ loadingList ? t('endpoints.loading') : t('runtimes.registeredEmpty') }}
                </p>
            </CardContent>
        </Card>

        <!-- 发现与登记（Prism 实例自动发现 + 手动登记路径） -->
        <Card>
            <CardHeader>
                <CardTitle>{{ t('runtimes.candidatesTitle') }}</CardTitle>
                <CardDescription>{{ t('runtimes.subtitle') }}</CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
                <div class="flex items-center gap-2">
                    <Button :disabled="discovering" @click="discover">{{ t('runtimes.discoverBtn') }}</Button>
                </div>
                <div class="flex items-center gap-2">
                    <div class="flex w-96 max-w-full flex-col gap-1">
                        <label class="text-xs font-medium" for="runtimes-manual-path">{{ t('endpoints.pathLabel') }}</label>
                        <Input
                            id="runtimes-manual-path"
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
                        {{ t('runtimes.registerBtn') }}
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
                        <TableRow v-for="c in candidates" :key="c.instance_id">
                            <TableCell class="font-medium">
                                {{ c.display_name }}
                                <span v-if="c.minecraft" class="text-muted-foreground ml-1 text-xs">{{ c.minecraft }}</span>
                            </TableCell>
                            <TableCell class="text-muted-foreground max-w-72 truncate text-xs" :title="c.game_dir">
                                {{ c.game_dir }}
                            </TableCell>
                            <TableCell>{{ c.minecraft || '—' }}</TableCell>
                            <TableCell>{{ c.modloader || '—' }}</TableCell>
                            <TableCell>
                                <Badge v-if="c.registered" variant="secondary">{{ t('endpoints.registeredBadge') }}</Badge>
                                <Button v-else variant="outline" size="sm" :disabled="registering" @click="registerFromPath(c.instance_dir)">
                                    {{ t('runtimes.registerBtn') }}
                                </Button>
                            </TableCell>
                        </TableRow>
                    </TableBody>
                </Table>
                <p v-else class="text-muted-foreground text-sm">{{ t('runtimes.candidatesEmpty') }}</p>
            </CardContent>
        </Card>
    </div>
</template>
