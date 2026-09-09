// 设置页拼装层（二阶段拆分，仿壳层 components/shell/ 拆装式；画板 SET-01 对齐，
// 票 #107）：页头（page-title h1 + 副标题）+ 六分区纵向排列 + DevMockCard 懒挂槽，
// 本件只做拼装与注释分流，不藏逻辑——分区骨架（section / border-t / 150px 标签行 /
// 入场动画）在 SettingsSection.tsx，行结构纯函数在 lib.ts，取数/保存/降级全在
// hooks/*，各分区渲染在 sections/*（每件契约见各自文件头）。
// 分区清单：Tools（EnvService.Detect/SetToolPath）→ Credentials（GetApiKey/
// SetApiKey）→ Storage（GetStorageStats）→ Retention（Get/UpdateRetentionSettings
// + RequestGC）→ Diagnostics（前端剪贴板）→ Appearance（stores/theme + i18n）。
// 入场：本容器持 staggerContainer 编排六分区（SettingsSection 内 fadeUp 随 delay
// 递增上浮淡入，总时长 ~0.33s < 400ms；减动效跳过见 SettingsSection 头注释）。
// dev 构建另有 mock 数据层分区（DevMockCard，import.meta.env.DEV 门控 React.lazy
// 动态装载，生产构建该组件连同 mock.* 文案引用整体裁剪）。
import { Suspense, lazy } from 'react'
import { useTranslation } from 'react-i18next'
import { motion } from 'motion/react'
import { staggerContainer } from '@/lib/motion'
import ToolsSection from './sections/ToolsSection'
import CredentialsSection from './sections/CredentialsSection'
import StorageSection from './sections/StorageSection'
import RetentionSection from './sections/RetentionSection'
import DiagnosticsSection from './sections/DiagnosticsSection'
import AppearanceSection from './sections/AppearanceSection'

// —— Mock 数据层分区：仅 dev 构建装载（生产构建 import.meta.env.DEV 恒 false，
// 分支经常量折叠消除，动态导入连同组件整体裁剪出产物） ——
const DevMockCard = import.meta.env.DEV ? lazy(() => import('./DevMockCard')) : null

export default function SettingsView() {
    const { t } = useTranslation()

    return (
        <motion.div
            variants={staggerContainer}
            initial="initial"
            animate="animate"
            className="text-foreground mx-auto flex w-full max-w-215 flex-col p-4"
        >
            <div className="mb-3.5">
                <h1 className="page-title">{t('settings.title')}</h1>
                <p className="text-muted-foreground mt-1 text-sm">{t('settings.subtitle')}</p>
            </div>

            <ToolsSection />
            <CredentialsSection />
            <StorageSection />
            <RetentionSection />
            <DiagnosticsSection />
            <AppearanceSection />

            {/* Mock 数据层（仅 dev 构建渲染与生效；无标签操作行不入 stagger 编排） */}
            {DevMockCard && (
                <Suspense fallback={null}>
                    <DevMockCard />
                </Suspense>
            )}

            <p className="text-faint mb-2 mt-3 text-xs">PackGradle · P1</p>
        </motion.div>
    )
}
