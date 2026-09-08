// 错误文本渲染（自 main:frontend/src/utils/errors.ts 移植）：Go 端只返回错误码
// （errs.AppError，JSON 序列化），经 Wails MarshalError 传至前端 err.cause，或
// 作为文本落入数据字段（如 RefreshResult.Output / PackProject.Error）。
// 两种路径统一解析并翻译；非结构化文本（packwiz CLI 输出等）原样返回。
//
// 插值适配（lib/i18n.ts 头注释所记）：zh-CN.json 全量沿用 vue-i18n 位置插值写法
// {0}，而 i18next 默认期望 {{0}}——renderAppErr 取键值时跳过 i18next 插值
// （skipInterpolation），再手工替换 {n} 占位，单点适配不影响其余键的常规 t()。
import { t } from '../lib/i18n'

// Go 端 errs.AppError 的结构化形态
export interface AppErrorCause {
    code?: string
    args?: string[]
    detail?: string
}

// 从 Wails 调用错误中提取错误码；非结构化错误返回 undefined
export function errorCode(e: unknown): string | undefined {
    const cause = causeOf(e)
    if (cause) return cause.code
    return undefined
}

// 渲染错误对象为用户可读文本
export function errText(e: unknown): string {
    const cause = causeOf(e)
    if (cause) return renderAppErr(cause)
    return displayText(rawMessage(e))
}

// 渲染数据字段中的文本：错误码 JSON → 翻译；其余（packwiz 输出等）原样返回
export function displayText(s: string | undefined | null): string {
    if (!s) return s ?? ''
    const appErr = tryParseAppErr(s)
    return appErr ? renderAppErr(appErr) : s
}

// 解析 e.cause（Wails RuntimeError 携带的结构化错误）
function causeOf(e: unknown): AppErrorCause | undefined {
    if (e instanceof Error) return tryParseAppErr((e as Error & { cause?: unknown }).cause)
    if (e && typeof e === 'object' && 'cause' in e) return tryParseAppErr((e as { cause?: unknown }).cause)
    return undefined
}

// parseAppErr 解析数据字段中的错误码 JSON 文本（如 PrismOverview.locate_error）；
// 非错误码 JSON 返回 undefined。
export function parseAppErr(v: unknown): AppErrorCause | undefined {
    return tryParseAppErr(v)
}

// cause 是 Go 端 MarshalError 反序列化后的对象；文本形式（数据字段）是 JSON 字符串
function tryParseAppErr(v: unknown): AppErrorCause | undefined {
    let obj: unknown = v
    if (typeof v === 'string') {
        try {
            obj = JSON.parse(v)
        } catch {
            return undefined
        }
    }
    if (obj && typeof obj === 'object' && 'code' in obj) {
        const c = obj as AppErrorCause
        if (typeof c.code === 'string' && c.code.startsWith('err.')) return c
    }
    return undefined
}

function renderAppErr(cause: AppErrorCause): string {
    if (!cause.code) return cause.detail ?? ''
    // 缺失翻译键时返回键名本身（defaultValue），便于发现遗漏；
    // skipInterpolation 取原始键值（含 {n} 字面量），下方手工回填位置参数
    let base = t(cause.code, { defaultValue: cause.code, interpolation: { skipInterpolation: true } })
    cause.args?.forEach((arg, i) => {
        base = base.replaceAll(`{${i}}`, arg)
    })
    return cause.detail ? `${base}: ${cause.detail}` : base
}

function rawMessage(e: unknown): string {
    if (e instanceof Error) return e.message
    if (typeof e === 'string') return e
    if (e && typeof e === 'object' && 'message' in e) return String((e as { message: unknown }).message)
    return String(e)
}
