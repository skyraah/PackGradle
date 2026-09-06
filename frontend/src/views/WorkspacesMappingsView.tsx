// 移植来源：main:frontend/src/views/WorkspacesMappingsView.vue（709 行）
// 页面职责：受管范围页——GetMappingPolicy/UpdateMappingPolicy 读写 + 乐观锁编辑（UX 原型 §7.4）。
// 接线要点：乐观锁（策略集版本修订号）冲突提示是本页核心行为；Browser/Clipboard 直连同 Changes 页。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function WorkspacesMappingsView() {
    return <PlaceholderPage titleKey="nav.workspacesMappings" />
}
