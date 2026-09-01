// 计划用例的页面侧共享逻辑（T11，票 #21；P2 apply_sync 门控，票 #43）：
// prepare_sync 入口的三份同形实现（列表行/changes 头部/计划页重新生成）收敛于此。
// 可用性门控只用后端推导的 availability（契约 03 §2.1「前端不得自行推断」），
// scan_state=ready 由后端推导表保证，快照摘要随之齐备。
// P1 固定请求 exact：请求确切度随计划记录（Phase 2 Apply 才消费），P1 用户预期完整应用。
import { SyncService } from '../api'
import type { ActionAvailabilityDTO, SyncPlanDTO, WorkspaceDTO } from '../api'
import { t } from '../i18n'

// canPrepareSync 判断工作区是否渲染「同步计划」入口：features + availability（唯一门控）。
export function canPrepareSync(ws: WorkspaceDTO | null | undefined): boolean {
    return (
        ws?.features.sync_preview === true &&
        ws.availability?.some(a => a.action === 'prepare_sync' && a.available) === true
    )
}

// availabilityOf 返回动作的 availability 条目（契约 03 §2.1：后端按当前状态推导，
// 前端不得自行推断）；未注册（feature 未实现）时为 undefined。
export function availabilityOf(ws: WorkspaceDTO | null | undefined, action: string): ActionAvailabilityDTO | undefined {
    return ws?.availability?.find(a => a.action === action)
}

// canApplySync 判断是否渲染「应用同步」主操作（契约 05 §1）：features.sync_apply
// 且 apply_sync availability 可用（唯一门控；计划面有无可应用计划由后端推导）。
export function canApplySync(ws: WorkspaceDTO | null | undefined): boolean {
    return ws?.features.sync_apply === true && availabilityOf(ws, 'apply_sync')?.available === true
}

// canQuickUpdate 判断「快速更新」入口是否点亮（契约 06 §1/§9，票 #62）：
// quick_update availability 唯一门控（授权开关 + 活跃任务/恢复门/扫描就绪
// 三门禁全部由后端推导，前端不自行推断；无独立 feature 开关）。
export function canQuickUpdate(ws: WorkspaceDTO | null | undefined): boolean {
    return availabilityOf(ws, 'quick_update')?.available === true
}

// availabilityReasonText 渲染动作当前不可用的后端原因码文案（契约 03 §2.1：不可用
// 动作必须带原因码供 locale 渲染）。availability 推导不携带参量，vue-i18n 对缺失
// 参量输出空串、残留分隔符（如 err.plan.expired 的 {0}）在此收敛；
// 动作可用或未注册时返回空串。
export function availabilityReasonText(ws: WorkspaceDTO | null | undefined, action: string): string {
    const a = availabilityOf(ws, action)
    if (!a || a.available || !a.reason_code) return ''
    return t(a.reason_code, a.reason_args ?? [])
        .replace(/\s*[：:]\s*$/, '')
        .trim()
}

// canRescan 判断是否可发起重新扫描（stale 计划的「重新扫描并生成新计划」主操作）。
export function canRescan(ws: WorkspaceDTO | null | undefined): boolean {
    return (
        ws?.features.scan === true &&
        ws.availability?.some(a => a.action === 'scan' && a.available) === true
    )
}

// canRebind 判断工作区列表是否渲染「重新绑定」入口：availability 唯一门控
// （rebind 无 feature 开关，契约 03 §2.1 推导表：无活跃任务且非恢复占用即可
// 主动重绑，路径迁移是合法操作，rebind_required 等健康态不阻止）。
export function canRebind(ws: WorkspaceDTO | null | undefined): boolean {
    return ws?.availability?.some(a => a.action === 'rebind' && a.available) === true
}

// prepareSync 用工作区缓存的当前修订与最新双端快照发起 PrepareSync，返回新计划。
// 抛错时返回 rejected promise，由调用方决定 snackbar 呈现。
export function prepareSync(ws: WorkspaceDTO): Promise<SyncPlanDTO> {
    const project = ws.latest_project_snapshot
    const runtime = ws.latest_runtime_snapshot
    if (!project || !runtime) {
        return Promise.reject(new Error('missing latest snapshots'))
    }
    return SyncService.PrepareSync({
        relation_id: ws.relation.relation_id,
        relation_revision: ws.relation.revision,
        input_project_snapshot_id: project.snapshot_id,
        input_runtime_snapshot_id: runtime.snapshot_id,
        requested_exactness: 'exact',
    })
}
