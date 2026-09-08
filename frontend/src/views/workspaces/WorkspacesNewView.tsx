// 移植来源：main:frontend/src/views/WorkspacesNewView.vue（610 行）
// 页面职责：新建工作区向导（Prepare → Apply 建工作区，UX 原型 §7.2）。
// 接线要点：目录选择走 utils/dialogs 的 Wails 文件对话框包装；Prepare/Apply 两段式
//   服务调用与任务态提示经 syncCache/ui store。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function WorkspacesNewView() {
    return <PlaceholderPage titleKey="nav.workspacesNew" />
}
