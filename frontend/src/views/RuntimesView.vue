<script setup lang="ts">
// /runtimes：Prism 运行实例端点管理（发现·登记·健康；契约 03 §2.5，IA 见 05 原型 §7.10）。
// 只管理端点登记与健康，不含任何跨端写入动作。
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RuntimeService } from '../api'
import type { EndpointDTO, EndpointHealthDTO, RuntimeCandidateDTO } from '../api'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const { t } = useI18n()

const registered = ref<EndpointDTO[]>([])
const candidates = ref<RuntimeCandidateDTO[]>([])
const manualPath = ref('')
const loadingList = ref(false)
const discovering = ref(false)
const registering = ref(false)
// endpoint_id -> 健康结果；'checking' 表示检查进行中
const health = reactive(new Map<string, EndpointHealthDTO | 'checking'>())

onMounted(() => {
    loadRegistered()
    discover()
})

async function loadRegistered() {
    loadingList.value = true
    try {
        registered.value = (await RuntimeService.ListRuntimes()) ?? []
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        loadingList.value = false
    }
}

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

async function register(rootPath: string) {
    registering.value = true
    try {
        const ep = await RuntimeService.RegisterRuntime({ root_path: rootPath })
        showSnackbar(t('endpoints.registerOk', [ep.display_name]), 'success')
        manualPath.value = ''
        await Promise.all([loadRegistered(), discover()])
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        registering.value = false
    }
}

async function checkHealth(ep: EndpointDTO) {
    health.set(ep.id, 'checking')
    try {
        health.set(ep.id, await RuntimeService.GetRuntimeHealth(ep.id))
    } catch (e) {
        health.delete(ep.id)
        showSnackbar(errText(e), 'error')
    }
}

function healthBadgeVariant(status: string) {
    if (status === 'ok') return 'default' as const
    if (status === 'missing') return 'destructive' as const
    return 'secondary' as const
}

// healthOf 取健康结果；'checking' 哨兵视为无结果（供模板收窄）
function healthOf(id: string): EndpointHealthDTO | undefined {
    const h = health.get(id)
    return h && h !== 'checking' ? h : undefined
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4 p-4 text-foreground">
        <div>
            <h1 class="text-xl font-semibold">{{ t('runtimes.title') }}</h1>
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
                                    <Badge :variant="healthBadgeVariant(healthOf(ep.id)!.status)">
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
                    {{ loadingList ? t('endpoints.health.checking') : t('runtimes.registeredEmpty') }}
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
                            @keydown.enter="register(manualPath.trim())"
                        />
                    </div>
                    <Button
                        variant="secondary"
                        :disabled="registering || !manualPath.trim()"
                        class="mt-5"
                        @click="register(manualPath.trim())"
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
                                <Badge v-if="c.registered" variant="secondary">{{ t('runtimes.registeredTitle') }}</Badge>
                                <Button v-else variant="outline" size="sm" :disabled="registering" @click="register(c.instance_dir)">
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
