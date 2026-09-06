// 移植来源：main:frontend/src/views/WorkspacesHistoryView.vue（501 行）
// 页面职责：同步历史页——ListCommits 分页（UX 原型 §7.7；history_view 门控，契约 05 §5）。
// 接线要点：Browser/Clipboard 直连同 Changes 页；行入口进记录详情页与回滚计划页。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function WorkspacesHistoryView() {
    return <PlaceholderPage titleKey="nav.workspacesHistory" />
}
