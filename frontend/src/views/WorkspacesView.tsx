// 移植来源：main:frontend/src/views/WorkspacesView.vue（432 行）
// 页面职责：工作区列表（首页）——列表、健康/状态徽章、行操作入口
//   （变更/受管范围/设置/计划/重绑/历史）与「新建工作区」入口。
// 接线要点：EnvService/SyncService 经 api 门面（api 层待移植，main:frontend/src/api/index.ts）；
//   健康缓存经 stores/endpoints；列表与任务态经 stores/syncCache（受控重查管线，全应用状态核心）。
//   本页复杂度中等，建议作为第一页移植，把「调服务/订阅/读写 store/文案」的样板立起来。
import PlaceholderPage from '@/components/common/PlaceholderPage'

export default function WorkspacesView() {
    return <PlaceholderPage titleKey="nav.workspaces" />
}
