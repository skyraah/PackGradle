// 错误文本渲染：Go 端只返回错误码（errs.AppError，JSON 序列化），
// 经 Wails MarshalError 传至前端 err.cause，或作为文本落入数据字段
// （如 RefreshResult.Output / PackProject.Error / Errors[].Error）。
// 两种路径统一解析并翻译；非结构化文本（packwiz CLI 输出等）原样返回。
import { t } from '../i18n'

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
    // 缺失翻译键时 vue-i18n 返回键名本身，便于发现遗漏
    const base = t(cause.code, cause.args ?? [])
    return cause.detail ? `${base}: ${cause.detail}` : base
}

function rawMessage(e: unknown): string {
    if (e instanceof Error) return e.message
    if (typeof e === 'string') return e
    if (e && typeof e === 'object' && 'message' in e) return String((e as { message: unknown }).message)
    return String(e)
}
