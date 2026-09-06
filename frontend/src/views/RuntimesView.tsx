// 移植来源：main:frontend/src/views/RuntimesView.vue（412 行）
// 页面职责：运行实例端点管理页（Prism 侧；契约 03 Runtime 服务）。
// 接线要点：与 Sources 页共用 useEndpointPage 逻辑，两页一起移植最省。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function RuntimesView() {
    return <PlaceholderPage titleKey="nav.runtimes" />
}
