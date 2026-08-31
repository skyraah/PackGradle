// 新栈同步缓存：受控重查管线 + 工作区/任务缓存（契约 04 §2.3/§2.4）。
// 硬约束：事件只做通知，查询 API 是唯一事实源；缓存唯一写入方是查询 API 的返回值，
// 事件 payload 一概不进缓存；不得用页面 loading 状态推测 Task 完成。
import { ref } from 'vue'
import type { TaskDTO, WorkspaceDTO } from '../../bindings/packgradle/internal/transport/models'
import { SyncService } from '../api'
import { t } from '../i18n'
import { errText } from '../utils/errors'
import { showSnackbar } from './ui'

// 单轮分页上限：与后端 MaxPageLimit 对齐，分页循环拉满
const PAGE_LIMIT = 200
// 周期对账兜底（契约 04 §2.4 Q7）：仅窗口可见时执行，为缓存陈旧时间提供上界
const RECONCILE_MS = 30_000

const TASK_TERMINAL_STATUSES = new Set(['succeeded', 'failed', 'cancelled', 'recovery_required'])

// —— 缓存（查询 API 的投影；bootstrap 首次填充前页面渲骨架）——
export const workspaces = ref<WorkspaceDTO[]>([])
export const tasks = ref<Map<string, TaskDTO>>(new Map())
export const bootstrapped = ref(false)
export const bootstrapError = ref('')

// —— 管线状态（模块内私有；inflight 单飞 + dirty 二段刷新，零人为延迟）——
let inflight = false
let dirty = false
const dirtyTasks = new Set<string>()
let reconcileTimer: ReturnType<typeof setInterval> | undefined
let watchFailedNoticed = false

// triggerRequery 发起一轮受控重查；进行中再触发只标 dirty，本轮结束后立刻再刷一轮直到干净。
// 事件、漏包、30s 对账、页面动作后的刷新共用同一条管线（bootstrap 复用它，无第二套初始化逻辑）。
export function triggerRequery(): void {
    if (inflight) {
        dirty = true
        return
    }
    void runRound()
}

// markTaskDirty 对 task_updated 事件标记的 task_id 做 GetTask 重读（契约 04 §2.3）
export function markTaskDirty(taskID: string): void {
    dirtyTasks.add(taskID)
    triggerRequery()
}

// notifyWatchFailed 预留语义：按 invalidation 处理（触发方已调 triggerRequery）+ 一次性提示
export function notifyWatchFailed(): void {
    if (watchFailedNoticed) return
    watchFailedNoticed = true
    showSnackbar(t('workspaces.watchFailed'), 'warning')
}

// bootstrapSyncCache 启动引导：与 App mount 并行发起首轮查询，不阻塞首帧（契约 04 §2.1）。
// 窗口 reload / 应用重启后重新走同一条管线，状态从查询 API 恢复（§2.6）。
export function bootstrapSyncCache(): void {
    ensureReconcileTimer()
    triggerRequery()
}

// retryBootstrap 首轮查询失败后的重试入口（统一错误态可重试，走同一管线）
export function retryBootstrap(): void {
    bootstrapError.value = ''
    triggerRequery()
}

async function runRound(): Promise<void> {
    if (inflight) return
    inflight = true
    try {
        do {
            dirty = false
            const ids = [...dirtyTasks]
            dirtyTasks.clear()
            try {
                await queryAndCommit(ids)
            } catch (e) {
                // bootstrap 失败 → 统一错误态可重试；已有缓存时静默保留旧数据，
                // 等下一次事件/对账再试（查询期间 UI 原地更新，不显示全屏 loading）
                if (!bootstrapped.value) bootstrapError.value = errText(e)
                console.warn('[syncCache] 重查失败', e)
            }
        } while (dirty)
    } finally {
        inflight = false
    }
    // 本轮查询期间又有触发进入：立刻再刷一轮（二段刷新直到干净；风暴场景最多两轮）
    if (dirty) void runRound()
}

// queryAndCommit 单轮重查内容（P1 全量）：ListWorkspaces + 各关系 ListTasks(active=true)
// + 本轮标 dirty 的 GetTask；全部查询完成后再一次性提交缓存，页面原地换新不闪烁。
async function queryAndCommit(dirtyIDs: string[]): Promise<void> {
    const wss = await listAllWorkspaces()
    const relations = wss.map(w => w.relation.relation_id)
    const [taskLists, rereads] = await Promise.all([
        Promise.all(relations.map(id => listActiveTasks(id))),
        Promise.all(
            dirtyIDs.map(id =>
                SyncService.GetTask(id).catch(e => {
                    console.warn('[syncCache] GetTask 重读失败', id, e)
                    return null
                }),
            ),
        ),
    ])

    const nextTasks = new Map<string, TaskDTO>()
    for (const list of taskLists) {
        for (const task of list) nextTasks.set(task.task_id, task)
    }
    // GetTask 重读结果只在任务仍活跃时入缓存（终态由下一轮 ListTasks(active) 自然收敛）；
    // 唯一例外 recovery_required：终态但仍是任务中心的注意面（「处理恢复」动作挂载点，
    // 契约 05 §5），重读放行入缓存；重读失败不阻塞本轮，记录诊断后等下一次事件/对账重试
    for (const task of rereads) {
        if (task && (task.status === 'recovery_required' || !TASK_TERMINAL_STATUSES.has(task.status))) {
            nextTasks.set(task.task_id, task)
        }
    }
    // recovery_required 任务已离开活跃列表，按轮重建会把它丢掉：关系处于恢复门期间
    // 从上轮缓存保留（任务终态后状态不再变化，关系投影是唯一收敛信号——acknowledge
    // 或 probe 收口发布 relation_invalidated，health 离开 recovery_required 后下一轮自然剔除）
    const recoveryRelations = new Set(
        wss.filter(w => w.relation.health === 'recovery_required').map(w => w.relation.relation_id),
    )
    for (const prev of tasks.value.values()) {
        if (prev.status === 'recovery_required' && prev.relation_id && recoveryRelations.has(prev.relation_id)) {
            nextTasks.set(prev.task_id, prev)
        }
    }
    // 冷启动发现（T13 B 口径走查发现补，票 #45）：应用重启后上轮缓存不存在，
    // 恢复门内的恢复任务既不在活跃列表也无从保留——任务中心「处理恢复」入口断链。
    // 以查询 API 为事实源（契约 05 §5）：GetApplyRun 最近运行 → GetTask 重读，
    // 经上方 recovery_required 例外放行入缓存；失败不阻塞本轮，等对账重试。
    for (const w of wss) {
        if (w.relation.health !== 'recovery_required') continue
        const known = [...nextTasks.values()].some(
            t => t.relation_id === w.relation.relation_id && t.status === 'recovery_required',
        )
        if (known) continue
        try {
            const run = await SyncService.GetApplyRun(w.relation.relation_id)
            if (!run?.task_id) continue
            const t = await SyncService.GetTask(run.task_id)
            if (t && t.status === 'recovery_required') nextTasks.set(t.task_id, t)
        } catch (e) {
            console.warn('[syncCache] 恢复任务发现失败', w.relation.relation_id, e)
        }
    }

    workspaces.value = wss
    tasks.value = nextTasks
    bootstrapped.value = true
    bootstrapError.value = ''
}

// listPage 拉满一页游标序列（与后端分页协议对齐：next_cursor 为空即止）
async function listPage<T>(fetcher: (cursor: string, limit: number) => Promise<{ items?: T[] | null; next_cursor?: string }>): Promise<T[]> {
    const out: T[] = []
    let cursor = ''
    do {
        const page = await fetcher(cursor, PAGE_LIMIT)
        out.push(...(page.items ?? []))
        cursor = page.next_cursor ?? ''
    } while (cursor)
    return out
}

const listAllWorkspaces = () => listPage((cursor, limit) => SyncService.ListWorkspaces(cursor, limit))
const listActiveTasks = (relationID: string) =>
    listPage((cursor, limit) => SyncService.ListTasks(relationID, true, cursor, limit))

function ensureReconcileTimer(): void {
    if (reconcileTimer) return
    reconcileTimer = setInterval(() => {
        // 仅窗口可见时对账：不可见时无查询流量（契约 04 施工检查单 7）
        if (document.visibilityState === 'visible') triggerRequery()
    }, RECONCILE_MS)
}
