<script lang="ts">
// 复用方（#105 变化页 / #108 计划页 / #110 受管范围页）可直接 import 的视图模型类型
import type { BadgeTone } from '../../utils/pageState'

// 副行状态徽章：复用 utils/pageState 的 BadgeTone（st-* 变体 + 语义色文字）
export interface HeadBadge {
    label: string
    tone: BadgeTone
}

// 「更多」菜单项：id 由调用方消费；disabled + title 承载可用性原因码文案
export interface HeadMenuItem {
    id: string
    label: string
    disabled?: boolean
    title?: string
}

// 页签：to 存在 = 跨页导航（点击即路由跳转，活动态由目标页自持）；
// 无 to = 页内切换（emit('tab') + v-model:active-tab）
export interface HeadTab {
    value: string
    label: string
    to?: string
}

export default {}
</script>

<script setup lang="ts">
// 工作区对象头（UX 原型 workspace-ux-prototype.html §7.1 wsObjHead，票 #105）：
// h1「Project名 ↔ Runtime名」（swap 图标）+ 副行（关系健康徽章 + 变化状态徽章 +
// 双适配器 + 最近扫描时间）+ 右侧唯一主操作与「更多」菜单（规范五项：快速更新/
// 重新绑定/工作区设置/打开端点位置/复制诊断信息）+ 「变化 | 受管范围」真 Tabs
// （主色文字 + 底部 2px 下划线，§3 .tab）。
// 变化页（#105）/计划页（#108）/受管范围页（#110）共用：标题、副行、主操作、
// 菜单项与页签全部 prop 驱动，不内嵌任何页面特有逻辑；菜单/主操作行为经事件
// 交调用方接线，带 to 的页签由组件内导航（跨页页签，活动态由各页面自持）。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ArrowLeftRight, Ellipsis } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

const props = withDefaults(
    defineProps<{
        project: string
        runtime: string
        healthBadge?: HeadBadge | null
        diffBadge?: HeadBadge | null
        /** 双适配器一行文案，如「Packwiz 1.39 · Prism Launcher」 */
        adapters?: string
        /** 最近扫描时间（已格式化；空串不渲染该段） */
        lastScan?: string
        /** 右侧唯一主操作；null 不渲染。tonal = 次级强调（原型 btn-tonal） */
        primaryAction?: { label: string; tonal?: boolean; disabled?: boolean; title?: string } | null
        /** 缺省即规范五项（票 #105 拍板），调用方可传替换/带门控的副本 */
        menuItems?: HeadMenuItem[]
        tabs?: HeadTab[]
    }>(),
    {
        healthBadge: null,
        diffBadge: null,
        adapters: '',
        lastScan: '',
        primaryAction: null,
        menuItems: undefined,
        tabs: () => [],
    },
)

const emit = defineEmits<{
    primary: []
    menu: [id: string]
    tab: [value: string]
}>()

const activeTab = defineModel<string>('activeTab', { default: '' })

const { t } = useI18n()
const router = useRouter()

// 规范五项（次序即票面次序）；仅当调用方未传 menuItems 时使用
const defaultMenu = computed<HeadMenuItem[]>(() => [
    { id: 'quick-update', label: t('objHead.menu.quickUpdate') },
    { id: 'rebind', label: t('objHead.menu.rebind') },
    { id: 'settings', label: t('objHead.menu.settings') },
    { id: 'open-endpoint', label: t('objHead.menu.openEndpoint') },
    { id: 'copy-diagnostics', label: t('objHead.menu.copyDiagnostics') },
])
const menu = computed(() => props.menuItems ?? defaultMenu.value)

function onTab(tab: HeadTab): void {
    if (tab.to) {
        if (router.currentRoute.value.fullPath !== tab.to) void router.push(tab.to)
        return
    }
    activeTab.value = tab.value
    emit('tab', tab.value)
}

// 原型 .tab：12.5px/600，活动态主色文字 + 底部 2px 主色下划线（inset 8px），
// 去掉 shadcn 活动态底色/边框/阴影（深色主题同步覆盖）。
const tabTriggerClass =
    'h-auto flex-none rounded-[6px_6px_0_0] border-none bg-transparent px-3.5 py-2 text-[12.5px] font-semibold text-muted-foreground shadow-none data-[state=active]:bg-transparent data-[state=active]:text-primary data-[state=active]:shadow-none dark:data-[state=active]:bg-transparent dark:data-[state=active]:border-transparent dark:data-[state=active]:text-primary relative after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full after:bg-primary data-[state=active]:after:content-[""]'
</script>

<template>
    <div>
        <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
                <h1 class="page-title flex flex-wrap items-center gap-2">
                    <span class="truncate">{{ project }}</span>
                    <ArrowLeftRight class="text-muted-foreground size-4 flex-none" aria-hidden="true" />
                    <span class="truncate">{{ runtime }}</span>
                </h1>
                <!-- 副行（原型 .page-sub）：健康/变化徽章 + 适配器 + 最近扫描 -->
                <div class="mt-1.5 flex flex-wrap items-center gap-2.5 text-xs text-muted-foreground">
                    <Badge v-if="healthBadge" :variant="healthBadge.tone.variant" :class="healthBadge.tone.class">
                        {{ healthBadge.label }}
                    </Badge>
                    <Badge v-if="diffBadge" :variant="diffBadge.tone.variant" :class="diffBadge.tone.class">
                        {{ diffBadge.label }}
                    </Badge>
                    <span v-if="adapters">{{ adapters }}</span>
                    <span v-if="lastScan">{{ t('objHead.lastScan', [lastScan]) }}</span>
                </div>
            </div>
            <div class="flex flex-none items-center gap-2">
                <Button
                    v-if="primaryAction"
                    :variant="primaryAction.tonal ? 'secondary' : 'default'"
                    :disabled="primaryAction.disabled"
                    :title="primaryAction.title"
                    @click="emit('primary')"
                >
                    {{ primaryAction.label }}
                </Button>
                <DropdownMenu>
                    <DropdownMenuTrigger as-child>
                        <Button variant="ghost" size="icon" :title="t('objHead.more')" :aria-label="t('objHead.more')">
                            <Ellipsis class="size-4" />
                        </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" class="w-44">
                        <DropdownMenuItem
                            v-for="item in menu"
                            :key="item.id"
                            :disabled="item.disabled"
                            :title="item.title"
                            @select="emit('menu', item.id)"
                        >
                            {{ item.label }}
                        </DropdownMenuItem>
                    </DropdownMenuContent>
                </DropdownMenu>
            </div>
        </div>

        <!-- 页签（原型 .tabs：底部 1px 分隔线，活动项 2px 主色下划线）。
             单向绑定：跨页页签由 onTab 路由跳转，活动态始终来自调用方，防止
             跳转前活动下划线先被 TabsRoot 内部更新污染 -->
        <Tabs v-if="tabs.length" :model-value="activeTab" class="mt-2">
            <TabsList class="h-auto w-full justify-start gap-0.5 rounded-none border-b bg-transparent p-0">
                <TabsTrigger
                    v-for="tab in tabs"
                    :key="tab.value"
                    :value="tab.value"
                    :class="tabTriggerClass"
                    @click="onTab(tab)"
                >
                    {{ tab.label }}
                </TabsTrigger>
            </TabsList>
        </Tabs>
    </div>
</template>
