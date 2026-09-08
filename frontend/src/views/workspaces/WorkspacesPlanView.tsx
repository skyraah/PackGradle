// 移植来源：main:frontend/src/views/WorkspacesPlanView.vue（823 行）
// 页面职责：同步计划页——PrepareSync/ResolvePlan/GetPlan 只读计划 + choose_side 冲突决议
//   （UX 原型 §7.5；忽略/手动处理链见 ADR-0013，票 #100/#101）。
// 接线要点：系统通知点击直达本页（packgradle://notify → notificationNav，api 层待移植）；
//   危险确认弹窗一律用 shadcn AlertDialog（Wails 原生 Question 在构建版挂起，devdocs 惯例页）。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function WorkspacesPlanView() {
    return <PlaceholderPage titleKey="nav.workspacesPlan" />
}
