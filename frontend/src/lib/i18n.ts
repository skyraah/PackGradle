// 全局 i18n（ADR-0014 脚手架）：唯一文案来源 src/locales/zh-CN.json（约 1080 键）。
// 服务端只返回错误码（err.*），由前端渲染为用户可读文本。
//
// ———————— 受保护键（改文案可以，删键/改名不行） ————————
// zh-CN.json 被 main.go 在编译期嵌入（go:embed frontend/src/locales/zh-CN.json）：
// - notify.pendingPlan.title / notify.pendingPlan.body —— 系统通知文案（loadNotifyCopy）
// - err.* / msg.* —— internal/errs/locale_conformance_test.go 双向扫描：
//   Go 发射的每个错误码必须有键；locale 里每个 err.* 键也必须有 Go 发射点
// - workspaces.taskKind.* —— 同一测试按 TaskKind 矩阵校验
// 文件路径本身也是契约（go:embed 与该测试都硬编码此路径），勿移动本文件。
//
// ———————— 移植注意（给 utils/errors.ts 的搬迁，Vue 版在 main 分支） ————————
// vue-i18n 位置插值写 {0}，i18next 默认插值写 {{0}}——zh-CN.json 有 143 行含 {0}。
// 已定方案（utils/errors.ts renderAppErr 落地）：t 时 skipInterpolation 取原始键值，
// 再手工替换 {n} 占位——单点适配，其余键的常规 t() 不受影响，无需 postprocessor。
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import zhCN from '../locales/zh-CN.json'

// 语言偏好持久化（packgradle.locale，自 Vue 版 SettingsView 移植到初始化处）：
// P1 仅 zh-CN 一套文案；启动时读存储（缺省 zh-CN），设置页语言 Select 经
// i18n.changeLanguage + 写回该键生效（localStorage 不可用时保持默认）。
const LOCALE_KEY = 'packgradle.locale'

function loadLocale(): string {
    try {
        const saved = localStorage.getItem(LOCALE_KEY)
        if (saved) return saved
    } catch {
        // localStorage 不可用时保持默认 zh-CN
    }
    return 'zh-CN'
}

void i18n.use(initReactI18next).init({
    lng: loadLocale(),
    fallbackLng: 'zh-CN',
    resources: { 'zh-CN': { translation: zhCN } },
    // React 自带 XSS 防护，i18next 侧无需再转义
    interpolation: { escapeValue: false },
})

// setLocale 切换语言并持久化（设置页语言 Select 的落点；持久化失败不阻断本次
// 会话内的切换）——新语言包就位后即插即用。
export function setLocale(locale: string): void {
    void i18n.changeLanguage(locale)
    try {
        localStorage.setItem(LOCALE_KEY, locale)
    } catch {
        // 忽略：会话内切换仍生效
    }
}

export default i18n

// 供非组件模块（如未来的 utils/errors.ts）在 hook 之外使用（对应 vue-i18n 版的 i18n.global.t）
export const t = i18n.t
