// 移植来源：main:frontend/src/views/SourcesView.vue（422 行）
// 页面职责：项目源端点管理页（packwiz 侧；契约 03 Project 服务 + UX 原型 §7.x）。
// 接线要点：与 Runtimes 页共用端点页逻辑（useEndpointPage → hook 化移植）；
//   会话级健康缓存经 stores/endpoints。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function SourcesView() {
    return <PlaceholderPage titleKey="nav.sources" />
}
