// 页面级查询相位状态机与展示辅助的共享件（Phase 2 评审修复，PR #47）：
// loading/error/gate/empty/ready 互斥主状态沿 changes 页先例；徽标色调常量、
// 时间格式化与单页行数原为 history/commit/recovery/changes 四视图各自复制的
// 同形代码，收敛于此（纯函数/常量，无副作用、无依赖）。

// QueryPhase 是单次查询的生命周期相位（互斥主状态；inflight 只在已有快照时
// 投影为 refreshing，属视图本地状态，不在此折叠）。
export type QueryPhase = 'loading' | 'error' | 'ready'

// PageState 是折叠 feature 门控与空态后的页面主状态（gate：features.* 未点亮
// 时页面不渲染内容，契约 03 §2.1）。
export type PageState = 'loading' | 'error' | 'gate' | 'empty' | 'ready'

// resolvePageState 把查询相位折叠为页面主状态：loading/error 优先（首查未就绪
// 或失败不可读），其次 feature 门控，最后空/就绪。
export function resolvePageState(phase: QueryPhase, gated: boolean, hasRows: boolean): PageState {
    if (phase === 'loading') return 'loading'
    if (phase === 'error') return 'error'
    if (gated) return 'gate'
    if (!hasRows) return 'empty'
    return 'ready'
}

// PAGE_LIMIT 单页行数：与后端 ports.MaxPageLimit 对齐——页面单页拉满，
// syncCache 分页循环拉满。
export const PAGE_LIMIT = 200

// —— 徽标色调（shadcn Badge variant + 语义色文字 class）——
export interface BadgeTone {
    variant: 'default' | 'secondary' | 'destructive' | 'outline'
    class?: string
}

export const OK: BadgeTone = { variant: 'outline', class: 'text-emerald-600 dark:text-emerald-400' }
export const WARN: BadgeTone = { variant: 'outline', class: 'text-amber-600 dark:text-amber-400' }
export const NEUTRAL: BadgeTone = { variant: 'outline' }
export const BUSY: BadgeTone = { variant: 'secondary' }
export const BAD: BadgeTone = { variant: 'destructive' }

// toneOf 按取值查色调表，未知值回落中性。
export function toneOf(map: Record<string, BadgeTone>, value: string): BadgeTone {
    return map[value] ?? NEUTRAL
}

// completenessTone 历史完整性色调：exact 绿色、partial 警示。
export function completenessTone(c: string): BadgeTone {
    return c === 'exact' ? OK : WARN
}

// formatTime 把 RFC3339 时间戳渲染为本地时间；空值显「—」，无法解析原样返回
// （后端时间恒为 RFC3339，此分支兜底）。
export function formatTime(s?: string): string {
    if (!s) return '—'
    const at = Date.parse(s)
    return Number.isNaN(at) ? s : new Date(at).toLocaleString()
}
