// 移植来源：main:frontend/src/views/WorkspacesChangesView.vue（913 行）
// 页面职责：工作区资源变更页——GetChanges 分页 diff + 筛选（UX 原型 §7.3）。
// 接线要点：此页直接用 @wailsio/runtime 的 Browser（打开所在目录）与 Clipboard（复制），
//   属少数绕过 api 门面直连 runtime 的视图；页头三常驻页签（useWorkspaceHeadTabs，
//   main:frontend/src/composables/）需随本页移植成 hook。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function WorkspacesChangesView() {
    return <PlaceholderPage titleKey="nav.workspacesChanges" />
}
