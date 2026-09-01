// 快速更新编排（契约 06 §4，票 #62）：「一次点击跟上上游」的纯前端编排，
// 零新方法零新 DTO——只串联既有 StartScan / PrepareSync / ResolvePlan / ConfirmPlan。
// 免确认判定唯一口径（Q7，全入口一致）：计划 confirmation_requirements 为空 ∧
// 工作区 authorized_apply ⇒ 跳过确认页 ConfirmPlan 直达；否则转待确认计划页走
// P2 既有确认流。不新增任何按入口特判的分支：这里的判定条件就是唯一口径本身，
// 计划页（P2 既有流）不感知本编排的存在。
// 冲突与删除永不自动（ADR-0005 §4）：含冲突的 draft 没有决议输入，直接转计划页
// 由用户决议（用户不处理则下轮扫描自然重现）；requirements 非空（删除/不可恢复
// 等损失面）同样转计划页人工确认。不自动触发：本编排只由用户单次点击发起。
import { SyncService } from '../api'
import type { TaskDTO, WorkspaceDTO } from '../api'
import { t } from '../i18n'

// 编排阶段（概览入口按钮的进行中文案键：workspaces.quickUpdate.phase.<phase>）
export type QuickUpdatePhase = 'scan' | 'plan' | 'apply'

// 编排结果：
// - committed：ConfirmPlan 已受理，apply 任务移交任务中心（可离开页面，UX §7.9）
// - manual：计划需要人工介入（冲突决议 / confirmation_requirements 非空），
//   调用方导航到待确认计划页 /workspaces/:id/plans/:plan_id
export type QuickUpdateOutcome = { kind: 'committed' | 'manual'; planID: string }

// 任务终态集合（与 stores/syncCache 口径一致）
const TASK_TERMINAL = new Set(['succeeded', 'failed', 'cancelled', 'recovery_required'])

// SCAN_POLL_MS 是编排内等待扫描终态的轮询间隔（编排推进等待，不是 UI 状态面：
// 界面状态仍由事件 + 受控重查管线刷新，契约 04「不得用页面 loading 推测 Task 完成」）。
const SCAN_POLL_MS = 500

// waitForTask 轮询 GetTask 直至终态（扫描是长任务，编排必须等它收口才能取新快照）。
async function waitForTask(taskID: string): Promise<TaskDTO> {
    for (;;) {
        const task = await SyncService.GetTask(taskID)
        if (TASK_TERMINAL.has(task.status)) return task
        await new Promise(resolve => setTimeout(resolve, SCAN_POLL_MS))
    }
}

// runQuickUpdate 执行一次快速更新编排，返回结果由调用方决定导航与提示。
// 抛错（扫描未成功 / prepare/resolve/confirm 被拒）由调用方 snackbar 呈现。
export async function runQuickUpdate(
    ws: WorkspaceDTO,
    onPhase: (phase: QuickUpdatePhase) => void,
): Promise<QuickUpdateOutcome> {
    const relationID = ws.relation.relation_id

    // ① 扫描：察觉上游变更（quick_update availability 已由后端保证 scan ready）
    onPhase('scan')
    const scan = await SyncService.StartScan(relationID)
    const done = await waitForTask(scan.task_id)
    if (done.status !== 'succeeded') {
        throw new Error(t('workspaces.quickUpdate.scanStopped'))
    }

    // ② 扫描收口后重读工作区投影（修订与双端快照已更新），再 PrepareSync；
    //    输入形状与 utils/plans.prepareSync 同款（当前修订 + 最新双端快照 + exact）
    onPhase('plan')
    const fresh = await SyncService.GetWorkspace(relationID)
    const project = fresh.latest_project_snapshot
    const runtime = fresh.latest_runtime_snapshot
    if (!project || !runtime) {
        throw new Error(t('workspaces.quickUpdate.scanStopped'))
    }
    const draft = await SyncService.PrepareSync({
        relation_id: relationID,
        relation_revision: fresh.relation.revision,
        input_project_snapshot_id: project.snapshot_id,
        input_runtime_snapshot_id: runtime.snapshot_id,
        requested_exactness: 'exact',
    })

    // 冲突永不自动：含冲突 draft 无决议输入，转计划页由用户 choose_side
    if ((draft.conflicts?.length ?? 0) > 0) {
        return { kind: 'manual', planID: draft.plan_id }
    }

    // 无冲突 draft → 空决议推进 resolved（与计划页既有自动推进同款，零特判）
    const resolved = await SyncService.ResolvePlan({ plan_id: draft.plan_id, resolutions: [] })

    // ③ 免确认唯一口径（Q7）：requirements 空 ∧ authorized → ConfirmPlan 直达；
    //    否则（删除/不可恢复等损失面要求人工确认）转待确认计划页
    const authorized = fresh.authorized_apply === true
    const needsManual = (resolved.confirmation_requirements?.length ?? 0) > 0
    if (authorized && !needsManual) {
        onPhase('apply')
        await SyncService.ConfirmPlan({ plan_id: resolved.plan_id })
        return { kind: 'committed', planID: resolved.plan_id }
    }
    return { kind: 'manual', planID: resolved.plan_id }
}
