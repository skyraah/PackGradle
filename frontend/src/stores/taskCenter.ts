// 任务中心：全局操作执行与历史追溯。
// 所有写操作统一经 runTask 走「确认 → 执行（进度可见）→ 结果驻留」四段式；
// snackbar 仅保留给轻量提示，操作历史以任务中心为权威。
import { computed, ref } from 'vue'
import { errText } from '../utils/errors'

export type TaskStatus = 'running' | 'success' | 'warning' | 'error'
export type TaskKind = 'refresh' | 'fetch' | 'update' | 'meta' | 'link' | 'import' | 'remove' | 'config' | 'other'

export interface TaskItem {
    id: number
    title: string
    kind: TaskKind
    status: TaskStatus
    /** 0~1；执行中实时推进，完成后为 1 */
    progress: number
    /** 当前步骤文本（执行方通过 report 上报；单次调用无中间上报时为空） */
    stepText: string
    /** 完成后的结果摘要 */
    resultText: string
    /** 可展开的详细输出（CLI 输出/错误详情） */
    output: string
    startedAt: Date
    finishedAt: Date | null
    /** 用户是否已在任务中心里看过该结果 */
    seen: boolean
}

const tasks = ref<TaskItem[]>([])
let nextID = 1

export const taskList = computed(() => tasks.value)
export const runningCount = computed(() => tasks.value.filter(t => t.status === 'running').length)
export const unseenCount = computed(() => tasks.value.filter(t => t.status !== 'running' && !t.seen).length)

export interface RunTaskOptions {
    title: string
    kind: TaskKind
    /** 任务体；通过 report 上报进度，返回值作为结果摘要 */
    run: (report: (fraction: number, stepText: string) => void) => Promise<string>
    /** 完成后追加的详细输出（可选） */
    output?: () => string
    /** 完成但需用户注意时用 warning 态（默认 success） */
    warn?: (resultText: string) => boolean
    /** 需要在原操作位置保留错误时使用（例如确认弹窗内联错误） */
    onError?: (message: string) => void
}

export async function runTask(opts: RunTaskOptions): Promise<string | null> {
    const item: TaskItem = {
        id: nextID++,
        title: opts.title,
        kind: opts.kind,
        status: 'running',
        progress: 0,
        stepText: '',
        resultText: '',
        output: '',
        startedAt: new Date(),
        finishedAt: null,
        seen: false,
    }
    tasks.value = [item, ...tasks.value]
    try {
        const resultText = await opts.run((fraction, stepText) => {
            item.progress = Math.min(1, Math.max(0, fraction))
            item.stepText = stepText
        })
        item.progress = 1
        item.resultText = resultText
        item.output = opts.output?.() ?? ''
        item.status = opts.warn?.(resultText) ? 'warning' : 'success'
        item.seen = false
        return resultText
    } catch (e) {
        const message = errText(e)
        item.progress = 1
        item.status = 'error'
        item.resultText = message
        item.output = opts.output?.() ?? ''
        item.seen = false
        opts.onError?.(message)
        return null
    } finally {
        item.finishedAt = new Date()
    }
}

export function markAllSeen() {
    for (const t of tasks.value) {
        if (t.status !== 'running') t.seen = true
    }
}

export function clearFinished() {
    tasks.value = tasks.value.filter(t => t.status === 'running')
}

export function useTaskCenter() {
    return { taskList, runningCount, unseenCount, runTask, markAllSeen, clearFinished }
}
