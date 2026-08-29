<script setup lang="ts">
// 顶栏 Mock 模式徽标：仅开发构建装载（App 以 __DEV__ 门控异步导入，生产构建整体裁剪）。
// 点击进设置页关闭 mock（设置页 dev 卡片）。
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { isMockEnabled } from '../../api'
import { Badge } from '@/components/ui/badge'

const { t } = useI18n()
const router = useRouter()
</script>

<template>
    <Badge
        v-if="isMockEnabled()"
        variant="secondary"
        class="app-no-drag mr-1 cursor-pointer"
        :title="t('app.mockBadgeTip')"
        @click="router.push('/settings')"
    >
        MOCK
    </Badge>
</template>
