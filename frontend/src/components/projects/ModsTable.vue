<script setup lang="ts">
// mod 表格（v-data-table）：side 标识 / 文件 / 版本（本地优先，CF 回填）/ 单 mod 版本获取。
// 内置排序（localStorage 持久化）、分页、搜索、side 过滤、「仅看未获取版本」开关。
// 行内获取成功后该行短暂高亮（row-flash）。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ModInfo } from '../../../bindings/packgradle/internal/packwiz'
import { isCfMod, cfReleaseKey, cfDateText, sideColors } from '../../utils/cf'

const { t } = useI18n()

const props = defineProps<{
    mods: ModInfo[]
    /** 正在获取版本的 mod id（行内 loading） */
    fetching?: string | null
    /** 批量获取进行中时禁用单行按钮 */
    fetchDisabled?: boolean
    /** 刚获取成功的 mod id（行高亮用，父级负责清除） */
    flashed?: string | null
}>()

const emit = defineEmits<{
    (e: 'fetch', mod: ModInfo): void
    (e: 'fetchAll'): void
}>()

const search = ref('')
const sideFilter = ref('')
const onlyMissing = ref(false)

const sideOptions = computed(() => [
    { value: '', label: t('projects.filterAll') },
    { value: 'client', label: t('side.client') },
    { value: 'server', label: t('side.server') },
    { value: 'both', label: t('side.both') },
])

const headers = computed(() => [
    { title: t('projects.colMod'), key: 'name', sortable: true },
    { title: t('projects.colSide'), key: 'side', sortable: true, width: 120 },
    { title: t('projects.colFile'), key: 'file', sortable: true, width: 260 },
    { title: t('projects.colVersion'), key: 'version', sortable: true, width: 240 },
    { title: t('projects.colAction'), key: 'actions', sortable: false, align: 'end' as const, width: 88 },
])

const filtered = computed(() => {
    const q = search.value.trim().toLowerCase()
    return props.mods.filter(m => {
        if (sideFilter.value && m.side !== sideFilter.value) return false
        if (onlyMissing.value && (m.version || m.cf_version)) return false
        if (!q) return true
        return (m.name || m.id).toLowerCase().includes(q) || m.id.toLowerCase().includes(q)
    })
})

// 排序持久化
const sortBy = ref<{ key: string; order: 'asc' | 'desc' }[]>([])
try {
    const saved = JSON.parse(localStorage.getItem('packgradle.modsTable.sortBy') ?? '[]')
    if (Array.isArray(saved)) sortBy.value = saved
} catch {
    // 非法持久化内容忽略
}
watch(
    sortBy,
    v => {
        try {
            localStorage.setItem('packgradle.modsTable.sortBy', JSON.stringify(v))
        } catch {
            // WebView 禁用持久化时仍保留当前会话排序
        }
    },
    { deep: true },
)

function sideText(mod: ModInfo): string {
    return mod.side ? t('side.' + mod.side) : t('side.unknown')
}

function releaseText(tp: number): string {
    const key = cfReleaseKey(tp)
    return key ? t(key) : ''
}

function versionSortValue(mod: ModInfo): string {
    return mod.version || mod.cf_version || ''
}
</script>

<template>
    <div>
        <div class="d-flex align-center flex-wrap ga-3 mb-3">
            <v-text-field
                v-model="search"
                :placeholder="t('projects.searchMods')"
                prepend-inner-icon="mdi-magnify"
                density="compact"
                hide-details
                clearable
                class="mod-search"
            />
            <v-btn-toggle v-model="sideFilter" density="comfortable" mandatory color="primary" variant="outlined" divided>
                <v-btn v-for="opt in sideOptions" :key="opt.value" :value="opt.value" size="small">
                    {{ opt.label }}
                </v-btn>
            </v-btn-toggle>
            <v-chip
                :variant="onlyMissing ? 'flat' : 'tonal'"
                :color="onlyMissing ? 'primary' : undefined"
                filter
                @click="onlyMissing = !onlyMissing"
            >
                {{ t('projects.onlyMissingVersion') }}
            </v-chip>
            <v-spacer />
            <v-btn
                size="small"
                variant="tonal"
                prepend-icon="mdi-cloud-download-outline"
                :loading="fetchDisabled"
                @click="emit('fetchAll')"
            >
                {{ t('projects.tooltipFetchAll') }}
            </v-btn>
        </div>

        <v-data-table
            v-model:sort-by="sortBy"
            :headers="headers"
            :items="filtered"
            item-value="id"
            density="comfortable"
            hover
            :items-per-page="50"
            :items-per-page-options="[25, 50, 100, { value: -1, title: t('common.all') }]"
            class="mods-table"
        >
            <template #[`item.name`]="{ item: mod }">
                <div>{{ mod.name || mod.id }}</div>
                <div class="text-caption text-medium-emphasis">{{ mod.id }}</div>
            </template>

            <template #[`item.side`]="{ item: mod }">
                <v-chip size="x-small" :color="sideColors[mod.side] ?? 'grey'" variant="flat">
                    {{ sideText(mod) }}
                </v-chip>
            </template>

            <template #[`item.file`]="{ item: mod }">
                <span class="text-caption">{{ mod.file || '—' }}</span>
            </template>

            <template #[`item.version`]="{ item: mod }">
                <!-- 本地版本优先；CF displayName 与文件名一致时改显示发布日期 -->
                <span v-if="mod.version" class="text-caption" :title="mod.cf_version || ''">{{ mod.version }}</span>
                <span v-else-if="mod.cf_version && mod.cf_version !== mod.file" class="text-caption">{{ mod.cf_version }}</span>
                <span v-else-if="mod.cf_version" class="text-caption">{{ t('projects.published') }} {{ cfDateText(mod.cf_file_date) || '—' }}</span>
                <span v-else class="text-caption text-medium-emphasis">—</span>
                <div v-if="mod.cf_version && mod.cf_version !== mod.file" class="text-caption text-medium-emphasis">
                    {{ releaseText(mod.cf_release_type) }}
                    <template v-if="mod.cf_release_type && mod.cf_file_date"> · </template>{{ cfDateText(mod.cf_file_date) }}
                </div>
                <div v-else-if="mod.cf_version && releaseText(mod.cf_release_type)" class="text-caption text-medium-emphasis">
                    {{ releaseText(mod.cf_release_type) }}
                </div>
            </template>

            <template #[`item.actions`]="{ item: mod }">
                <v-btn
                    v-if="isCfMod(mod)"
                    icon="mdi-cloud-download-outline"
                    size="small"
                    variant="text"
                    :loading="fetching === mod.id"
                    :disabled="fetchDisabled || fetching !== null"
                    :title="mod.cf_version ? t('projects.tooltipRefetch') : t('projects.tooltipFetch')"
                    @click="emit('fetch', mod)"
                />
            </template>

        </v-data-table>

        <div class="d-flex align-center justify-space-between mt-2">
            <div class="text-caption text-medium-emphasis">{{ t('projects.modsFiltered', [filtered.length, props.mods.length]) }}</div>
        </div>

        <div v-if="filtered.length === 0" class="text-center text-medium-emphasis py-8 text-body-2">
            {{ props.mods.length === 0 ? t('projects.noMods') : t('projects.noModMatch', [search]) }}
        </div>
    </div>
</template>

<style scoped>
.mod-search {
    max-width: 280px;
}
.mods-table :deep(tr.row-flash) {
    animation: row-flash 1.8s ease-out forwards;
}
</style>
