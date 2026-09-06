// 移植来源：main:frontend/src/views/WorkspacesSettingsView.vue（106 行）
// 页面职责：工作区设置页——授权模式开关（SetWorkspaceAuthorized，契约 06 §9，票 #62）。
// 接线要点：单开关页，最轻；可与Mappings 一起当 zustand 表单样板。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function WorkspacesSettingsView() {
    return <PlaceholderPage titleKey="nav.workspacesSettings" />
}
