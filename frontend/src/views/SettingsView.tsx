// 移植来源：main:frontend/src/views/SettingsView.vue（563 行）+ settings/DevMockCard.vue
// 页面职责：设置页——保留窗口/GC（契约 06 §3.6）、诊断区（票 #107 入口已留，
//   #113 的导出脱敏诊断包/打开日志目录按钮接好服务后启用）。
// 接线要点：DevMock 卡仅 dev 构建渲染（__DEV__ 门控 + 动态导入裁剪，ADR-0001 §5），
//   生产构建 dist 无 mock 痕迹是构建验收项；设置页只迁入新栈消费的项（ADR-0001 §6）。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function SettingsView() {
    return <PlaceholderPage titleKey="nav.settings" />
}
