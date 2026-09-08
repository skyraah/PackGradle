// [接线缝] 壳层占位数据（ADR-0014 §2 分工：stores 是用户车道，壳层只留挂点）。
// 单独成件的原因：App.tsx（顶条面包屑）与 AppSidebar/WorkspaceSwitcher（切换器）双端
// 消费，若挂在 AppSidebar 导出上，数据流会穿视图件（App ← 侧栏组件 ← 数据）。
import type { SwitcherWorkspace } from './sidebar/WorkspaceSwitcher'

// [接线缝] 占位工作区清单——store 移植时换成真实清单与 activeId（如 useWorkspaceStore）；
// onSelect 跳的子页（现为 changes）与 onCreate 目标（/workspaces/new）届时一并校准。
// slug 供 App.tsx 顶条面包屑与切换器第二行展示。
// TODO(store): 工作区清单/选中项待接用户 store
export const PLACEHOLDER_WORKSPACES: SwitcherWorkspace[] = [
    { id: 'demo-a', name: '示例工作区 A', slug: 'packgradle' },
    { id: 'demo-b', name: '示例工作区 B', slug: 'demo-b' },
]

// [接线缝] 侧栏「运行实例」项的药丸徽标数值（原型 nav-badge 同位）。
// TODO(store): 运行实例数待接用户 store
export const PLACEHOLDER_RUNNING_RUNTIMES = 2
