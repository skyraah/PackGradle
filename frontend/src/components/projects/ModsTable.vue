<script setup lang="ts">
// mod 表格：side 标识 / 文件 / 版本（本地优先，CF 缓存回填）/ 单 mod 版本获取。
// 内置搜索与 side 过滤，供项目详情页使用。
import { computed, ref } from 'vue'
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
}>()

const emit = defineEmits<{
    (e: 'fetch', mod: ModInfo): void
}>()

const search = ref('')
const sideFilter = ref('')

const sideOptions = computed(() => [
    { value: '', label: t('projects.filterAll') },
    { value: 'client', label: t('side.client') },
    { value: 'server', label: t('side.server') },
    { value: 'both', label: t('side.both') },
])

const filtered = computed(() => {
    const q = search.value.trim().toLowerCase()
    return props.mods.filter(m => {
        if (sideFilter.value && m.side !== sideFilter.value) return false
        if (!q) return true
        return (m.name || m.id).toLowerCase().includes(q) || m.id.toLowerCase().includes(q)
    })
})

// side 中文标签（side.* 翻译键，缺失时显示未知）
function sideText(mod: ModInfo): string {
    return mod.side ? t('side.' + mod.side) : t('side.unknown')
}

// releaseType 中文标签（cf.release.* 翻译键）
function releaseText(tp: number): string {
    const key = cfReleaseKey(tp)
    return key ? t(key) : ''
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
            <div class="text-caption text-medium-emphasis">{{ t('projects.modsFiltered', [filtered.length, props.mods.length]) }}</div>
        </div>

        <v-table density="comfortable" hover>
            <thead>
                <tr>
                    <th>{{ t('projects.colMod') }}</th>
                    <th style="width: 108px">{{ t('projects.colSide') }}</th>
                    <th style="width: 260px">{{ t('projects.colFile') }}</th>
                    <th style="width: 250px">{{ t('projects.colVersion') }}</th>
                    <th class="text-right" style="width: 76px">{{ t('projects.colAction') }}</th>
                </tr>
            </thead>
            <tbody>
                <tr v-for="mod in filtered" :key="mod.id">
                    <td>
                        {{ mod.name || mod.id }}
                        <div class="text-caption text-medium-emphasis">{{ mod.id }}</div>
                    </td>
                    <td>
                        <v-chip size="x-small" :color="sideColors[mod.side] ?? 'grey'" variant="tonal">
                            {{ sideText(mod) }}
                        </v-chip>
                    </td>
                    <td class="text-caption">{{ mod.file || '—' }}</td>
                    <td class="text-caption">
                        <!-- 本地版本优先；CurseForge displayName 与文件名一致时不再重复显示，改为发布日期 -->
                        <span v-if="mod.version" :title="mod.cf_version || ''">{{ mod.version }}</span>
                        <span v-else-if="mod.cf_version && mod.cf_version !== mod.file">{{ mod.cf_version }}</span>
                        <span v-else-if="mod.cf_version">{{ t('projects.published') }} {{ cfDateText(mod.cf_file_date) || '—' }}</span>
                        <span v-else>—</span>
                        <div v-if="mod.cf_version && mod.cf_version !== mod.file" class="text-medium-emphasis">
                            {{ releaseText(mod.cf_release_type) }}
                            <template v-if="mod.cf_release_type && mod.cf_file_date"> · </template>{{ cfDateText(mod.cf_file_date) }}
                        </div>
                        <div v-else-if="mod.cf_version && releaseText(mod.cf_release_type)" class="text-medium-emphasis">
                            {{ releaseText(mod.cf_release_type) }}
                        </div>
                    </td>
                    <td class="text-right">
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
                    </td>
                </tr>
                <tr v-if="filtered.length === 0">
                    <td colspan="5" class="text-center text-medium-emphasis py-6">
                        {{ props.mods.length === 0 ? t('projects.noMods') : t('projects.noMatch', [search]) }}
                    </td>
                </tr>
            </tbody>
        </v-table>
    </div>
</template>

<style scoped>
.mod-search {
    max-width: 280px;
}
</style>
