// 页面级查询相位状态机与展示辅助的共享件（Phase 2 评审修复，PR #47）：
// loading/error/gate/empty/ready 互斥主状态沿 changes 页先例；徽标色调常量、
// 时间格式化与单页行数原为 history/commit/recovery/changes 四视图各自复制的
// 同形代码，收敛于此（纯函数/常量，无副作用、无依赖）。
// 票 #102：徽标色调全量切换为原型 st-* 形态，视图内本地复制的状态徽章映射
// （工作区四态组/任务/计划/变更分类/预检/端点健康）继续收敛为这里的单一
// 共享来源；资源判断纯文字着色（v-p/v-r/v-c/v-d/v-s 家族）同票落此。

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

// —— 徽标色调（shadcn Badge variant + st-* 语义变体 + 语义色文字 class）——
// 票 #102：全应用状态徽章切换为 UX 原型 st 形态（高 22px/圆角 4px/tint 底/
// 前置圆点，见 components/ui/badge）；st-run 的呼吸动画由调用方以 <Badge pulse>
// 承接（原型仅任务 running 加 .pulse），资源表判断列等 plain 场景以 <Badge plain>
// 去点。shadcn 四变体保留给计数 chip 等非状态场景。
export type BadgeVariant =
    | 'default'
    | 'secondary'
    | 'destructive'
    | 'outline'
    | 'st-ok'
    | 'st-run'
    | 'st-warn'
    | 'st-err'
    | 'st-mut'
    | 'st-info'

export interface BadgeTone {
    variant: BadgeVariant
    class?: string
}

export const OK: BadgeTone = { variant: 'st-ok' }
export const RUN: BadgeTone = { variant: 'st-run' }
export const WARN: BadgeTone = { variant: 'st-warn' }
export const NEUTRAL: BadgeTone = { variant: 'st-mut' }
export const BAD: BadgeTone = { variant: 'st-err' }
export const INFO: BadgeTone = { variant: 'st-info' }

// toneOf 按取值查色调表，未知值回落中性。
export function toneOf(map: Record<string, BadgeTone>, value: string): BadgeTone {
    return map[value] ?? NEUTRAL
}

// —— 状态徽标映射（单一共享来源；分组沿原型 ST 表与后端枚举，禁止自造同义词）——

// 工作区四正交态（原型 ST.scan/base/diff/health；键为后端枚举值）
export const SCAN_TONES: Record<string, BadgeTone> = {
    never_scanned: NEUTRAL,
    queued: RUN,
    scanning: RUN,
    ready: OK,
    failed: BAD,
}
export const BASELINE_TONES: Record<string, BadgeTone> = { none: NEUTRAL, ready: OK, stale: WARN }
export const DIFF_TONES: Record<string, BadgeTone> = {
    unknown: NEUTRAL,
    initialization_required: INFO,
    clean: OK,
    dirty: RUN,
    conflicted: WARN,
}
export const HEALTH_TONES: Record<string, BadgeTone> = {
    healthy: OK,
    endpoint_missing: BAD,
    rebind_required: WARN,
    recovery_required: BAD,
}

// 任务状态（原型 ST.task；键为 TaskDTO.status）
export const TASK_TONES: Record<string, BadgeTone> = {
    queued: NEUTRAL,
    running: RUN,
    succeeded: OK,
    failed: BAD,
    cancelled: NEUTRAL,
    recovery_required: BAD,
}

// 计划状态：P2 同步计划与回滚计划共用（confirmed 仅回滚计划出现）
export const PLAN_TONES: Record<string, BadgeTone> = {
    draft: NEUTRAL,
    resolved: OK,
    confirmed: INFO,
    applied: OK,
    stale: WARN,
    expired: WARN,
}

// 变更分类徽标（changes 页判断列；形态沿原型资源表 st plain 映射：
// 项目/Runtime 侧修改→run、双端冲突→warn、删除冲突→err、其余 mut）
export const CLASS_TONES: Record<string, BadgeTone> = {
    noop: NEUTRAL,
    converged: OK,
    adopt_equal: OK,
    init_choice: WARN,
    project_to_runtime: RUN,
    runtime_to_project: RUN,
    remove_runtime_candidate: WARN,
    remove_project_candidate: WARN,
    merged_clean: OK,
    conflict_modify: WARN,
    conflict_delete_modify: BAD,
}

// checkTone 预检查结果色调（新建工作区/重绑两页共用）：通过即 ok，
// 未过按严重度分阻断（err）与提示（mut）。
export function checkTone(passed: boolean, severity: string): BadgeTone {
    if (passed) return OK
    return severity === 'blocking' ? BAD : NEUTRAL
}

// endpointHealthTone 端点健康色调（项目源/运行实例两页共用）：
// ok 即正常，missing 缺失，检测中/未知回落中性。
export function endpointHealthTone(status: string): BadgeTone {
    if (status === 'ok') return OK
    if (status === 'missing') return BAD
    return NEUTRAL
}

// completenessTone 历史完整性色调：exact 绿色、partial 警示。
export function completenessTone(c: string): BadgeTone {
    return c === 'exact' ? OK : WARN
}

// —— 资源判断纯文字着色（原型 verdictLabel 的 v-p/v-r/v-c/v-d/v-s/v-l 家族）——
// 判断值与变更分类对应：p/r(rd)=单侧修改、c=双端冲突、dc=删除冲突、s=一致
// （noop）、l=身份需人工确认（init_choice）、diag=仅诊断；未知值回落 muted，
// 同原型 verdictLabel 的 ['—','v-s'] 兜底。
const VERDICT_CLASSES: Record<string, string> = {
    p: 'text-primary',
    r: 'text-info',
    rd: 'text-info',
    c: 'text-warning',
    dc: 'text-error',
    s: 'text-muted-foreground',
    l: 'text-warning',
    diag: 'text-muted-foreground',
}

// verdictClass 返回判断值的纯文字着色 class（表格行内直接给 span/td 用，
// 不带徽章底色——原型资源判断证据文案即此形态）。
export function verdictClass(v: string): string {
    return VERDICT_CLASSES[v] ?? 'text-muted-foreground'
}

// CHANGE_VERDICTS 把后端变更分类映射到原型资源表判断键（票 #105：判断列纯文字
// 着色走 verdictClass 家族）。映射沿原型 verdictLabel 语义：收敛/等值/自动合并
// 属「一致」族(s)，初始化待选择为身份确认(l)，项目源侧改/删为 p，运行实例侧改/删
// 为 r/rd，双端冲突 c，删除冲突 dc。
export const CHANGE_VERDICTS: Record<string, string> = {
    noop: 's',
    converged: 's',
    adopt_equal: 's',
    merged_clean: 's',
    init_choice: 'l',
    project_to_runtime: 'p',
    remove_project_candidate: 'p',
    runtime_to_project: 'r',
    remove_runtime_candidate: 'rd',
    conflict_modify: 'c',
    conflict_delete_modify: 'dc',
}

// verdictKeyOf 返回变更分类对应的原型判断键（未知分类返回空串 → verdictClass 兜底 muted）。
export function verdictKeyOf(classification: string): string {
    return CHANGE_VERDICTS[classification] ?? ''
}

// formatTime 把 RFC3339 时间戳渲染为本地时间；空值显「—」，无法解析原样返回
// （后端时间恒为 RFC3339，此分支兜底）。
export function formatTime(s?: string): string {
    if (!s) return '—'
    const at = Date.parse(s)
    return Number.isNaN(at) ? s : new Date(at).toLocaleString()
}

// latestScanText 最近扫描时间文案：取双端最新快照 captured_at 的最大值渲染为本地
// 时间，无有效时间戳显「—」（原为 workspaces 列表/变化/历史/受管范围四视图各自
// 复制的同形代码，票 #111 评审收敛于此）。
export function latestScanText(projectAt?: string, runtimeAt?: string): string {
    const stamps = [projectAt, runtimeAt]
        .filter((s): s is string => !!s)
        .map(s => Date.parse(s))
        .filter(v => !Number.isNaN(v))
    if (!stamps.length) return '—'
    return new Date(Math.max(...stamps)).toLocaleString()
}
