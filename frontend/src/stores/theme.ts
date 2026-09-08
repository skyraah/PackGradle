// 主题外观（自 main:frontend/src/stores/theme.ts 移植，Vue ref/watch → zustand）：
// html.dark 单开关（Tailwind v4 自定义变体 + shadcn 令牌均挂在 .dark 上）。
// 三态偏好（跟随系统/浅色/深色）持久化在 localStorage（packgradle.theme），跟随
// 系统时监听 prefers-color-scheme 变化实时切换；设置页与顶条主题按钮共用本模块。
//
// View Transitions 换肤（壳件 useLocalDarkTheme 的既有行为，随本 store 落地整体
// 收编，该壳件已删）：用户显式切换走 startViewTransition 快照式换肤（main.css 定
// 300ms 交叉淡入淡出），系统偏好变化与初始化瞬时应用不入场；引擎过旧或
// prefers-reduced-motion 时瞬时翻转兜底。
import { create } from 'zustand'
import { flushSync } from 'react-dom'

export type ThemePref = 'system' | 'light' | 'dark'

const PREF_KEY = 'packgradle.theme'

function loadPref(): ThemePref {
    try {
        const v = localStorage.getItem(PREF_KEY)
        if (v === 'light' || v === 'dark') return v
    } catch {
        // localStorage 不可用时降级为跟随系统
    }
    return 'system'
}

interface ThemeState {
    pref: ThemePref
    isDark: boolean
}

// 初始 isDark 先占位 true：模块加载即执行 initTheme()（见文件尾）同步校正，
// 消费组件首帧拿到的必是已解析值
export const useThemeStore = create<ThemeState>(() => ({
    pref: loadPref(),
    isDark: true,
}))

// 系统偏好为模块私有：仅 pref==='system' 时参与解析，不单独广播
let systemPref = true

function resolveDark(pref: ThemePref): boolean {
    return pref === 'system' ? systemPref : pref === 'dark'
}

// applyInstant 瞬时落 html.dark 并提交状态（initTheme / 系统偏好变化路径）
function applyInstant(pref: ThemePref): void {
    const dark = resolveDark(pref)
    document.documentElement.classList.toggle('dark', dark)
    useThemeStore.setState({ pref, isDark: dark })
}

// withViewTransition 快照式换肤包装（收编自壳件 useLocalDarkTheme）：
// React 状态更新必须在快照捕获前同步落地——并发调度不保证时序，若渲染晚于捕获，
// 新快照残留旧 UI（Moon/Sun 图标互换等）；flushSync 是 React + View Transitions
// 的官方配方。
function withViewTransition(flip: () => void): void {
    const doc = document as Document & {
        startViewTransition?: (cb: () => void) => { ready: Promise<void> }
    }
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (typeof doc.startViewTransition !== 'function' || reduced) {
        flip()
        return
    }
    doc.startViewTransition(flip)
}

export function setThemePref(p: ThemePref): void {
    try {
        if (p === 'system') localStorage.removeItem(PREF_KEY)
        else localStorage.setItem(PREF_KEY, p)
    } catch {
        // 持久化失败不阻断本次会话内的切换
    }
    withViewTransition(() => applyInstant(p))
}

// toggleTheme 顶条按钮：在浅色/深色间显式切换（脱离「跟随系统」）
export function toggleTheme(): void {
    setThemePref(useThemeStore.getState().isDark ? 'light' : 'dark')
}

// initTheme 读系统偏好、装监听、落初始 dark 类。Vue 版在 main.ts mount 前调用
// （避免首帧闪白）；React 版在模块加载时自执行——main.tsx 的 import 链
// （router → views → 本模块）先于 createRoot 渲染求值，首帧前同样完成落类，
// 效果等价且不依赖入口文件改动。幂等：重复调用直接返回。
let inited = false

export function initTheme(): void {
    if (inited) return
    inited = true
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    systemPref = media.matches
    media.addEventListener('change', e => {
        systemPref = e.matches
        applyInstant(useThemeStore.getState().pref)
    })
    applyInstant(useThemeStore.getState().pref)
}

initTheme()
