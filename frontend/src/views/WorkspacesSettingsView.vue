<script setup lang="ts">
// /workspaces/:id/settings：工作区详情设置区（契约 06 §9，票 #62）。
// 授权模式开关消费票 #57 的 SetWorkspaceAuthorized 与 WorkspaceDTO.authorized_apply：
// 开关状态只读缓存投影（查询 API 是唯一事实源，契约 04），切换成功后走受控重查
// 原地刷新，不做乐观回写；失败（含恢复门期间的特殊态）开关保持后端值并显原因。
// 授权模式语义（CONTEXT.md 词条 + ADR-0005 §4）：开启后非冲突操作免逐次确认
//（quick_update 编排免确认直达）；冲突与删除永不适用；恢复所需期间暂停生效、
// 开关值保留（入口由后端 err.recovery.in_progress 门禁挡）；回滚永远人工确认。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { SettingsService } from '../api'
import { bootstrapped, triggerRequery, workspaces } from '../stores/syncCache'
import { showSnackbar } from '../stores/ui'
import { errText } from '../utils/errors'
import { availabilityReasonText, canQuickUpdate } from '../utils/plans'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Switch } from '@/components/ui/switch'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const relationID = computed(() => String(route.params.id ?? ''))

// —— 工作区上下文（syncCache 投影，页面不做第二处取数）——
const wsRow = computed(() => workspaces.value.find(w => w.relation.relation_id === relationID.value))
const relationMissing = computed(() => bootstrapped.value && !wsRow.value)

// —— 授权模式开关 ——
const saving = ref(false)

// quickUpdateState 概览入口当前状态说明：点亮或后端原因码文案（唯一门控同源）
const quickUpdateState = computed(() => {
    const ws = wsRow.value
    if (!ws) return ''
    return canQuickUpdate(ws)
        ? t('workspaces.settings.entryLit')
        : t('workspaces.settings.entryGrey', [availabilityReasonText(ws, 'quick_update')])
})

async function setAuthorized(enabled: boolean): Promise<void> {
    if (saving.value || !wsRow.value) return
    saving.value = true
    try {
        await SettingsService.SetWorkspaceAuthorized(relationID.value, enabled)
        showSnackbar(t('workspaces.settings.authSavedToast'), 'success')
        // 切换经 SettingsService 写库：补一轮受控重查让缓存投影（authorized_apply
        // 与 quick_update availability）与后端同源刷新
        triggerRequery()
    } catch (e) {
        showSnackbar(errText(e), 'error')
    } finally {
        saving.value = false
    }
}
</script>

<template>
    <div class="mx-auto flex w-full max-w-4xl flex-col gap-4 p-4 text-foreground">
        <!-- 头部 -->
        <div class="flex items-start justify-between gap-4">
            <div>
                <h1 class="page-title">{{ t('workspaces.settings.title') }}</h1>
                <p class="text-muted-foreground mt-1 text-sm">
                    <template v-if="wsRow">
                        {{ wsRow.relation.project.display_name }}
                        <span class="text-muted-foreground">↔</span>
                        {{ wsRow.relation.runtime.display_name }}
                    </template>
                    <template v-else>{{ t('workspaces.settings.subtitle') }}</template>
                </p>
            </div>
            <Button variant="ghost" size="sm" @click="router.push('/workspaces')">
                {{ t('workspaces.settings.backToList') }}
            </Button>
        </div>

        <!-- 工作区不存在 -->
        <Card v-if="relationMissing">
            <CardContent class="flex flex-col items-start gap-3 py-6">
                <span class="text-destructive text-sm">{{ t('workspaces.settings.relationMissing') }}</span>
                <Button variant="outline" size="sm" @click="router.push('/workspaces')">
                    {{ t('workspaces.settings.backToList') }}
                </Button>
            </CardContent>
        </Card>

        <!-- 授权模式（工作区详情设置区，契约 06 §9；开关与投影同源，恢复期值保留） -->
        <Card v-else-if="wsRow">
            <CardHeader>
                <CardTitle>{{ t('workspaces.settings.authTitle') }}</CardTitle>
                <CardDescription>{{ t('workspaces.settings.authHint') }}</CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-3">
                <div class="flex items-center justify-between gap-4">
                    <div class="text-sm font-medium">{{ t('workspaces.settings.authLabel') }}</div>
                    <Switch :model-value="wsRow.authorized_apply" :disabled="saving" @update:model-value="setAuthorized" />
                </div>
                <div class="text-muted-foreground text-xs">
                    {{ t('workspaces.settings.entryPrefix') }}{{ quickUpdateState }}
                </div>
            </CardContent>
        </Card>
    </div>
</template>
