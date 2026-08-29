// 主题外观：html.dark 单开关（Tailwind v4 自定义变体 + shadcn 令牌均挂在 .dark 上）。
// 三态偏好（跟随系统/浅色/深色）持久化在 localStorage，跟随系统时监听
// prefers-color-scheme 变化实时切换；设置页与顶栏主题按钮共用本模块。
import { ref, watch } from 'vue'

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

export const themePref = ref<ThemePref>(loadPref())

const systemPref = ref(true)
let media: MediaQueryList | null = null

export const isDark = ref(true)

function apply() {
    isDark.value = themePref.value === 'system' ? systemPref.value : themePref.value === 'dark'
    document.documentElement.classList.toggle('dark', isDark.value)
}

// initTheme 在 App mount 前调用：读系统偏好、装监听、落初始 dark 类（避免首帧闪白）
export function initTheme(): void {
    media = window.matchMedia('(prefers-color-scheme: dark)')
    systemPref.value = media.matches
    media.addEventListener('change', e => {
        systemPref.value = e.matches
        apply()
    })
    apply()
}

export function setThemePref(p: ThemePref): void {
    themePref.value = p
    try {
        if (p === 'system') localStorage.removeItem(PREF_KEY)
        else localStorage.setItem(PREF_KEY, p)
    } catch {
        // 持久化失败不阻断本次会话内的切换
    }
    // apply 由 watch(themePref) 统一触发
}

// toggleTheme 顶栏按钮：在浅色/深色间显式切换（脱离「跟随系统」）
export function toggleTheme(): void {
    setThemePref(isDark.value ? 'light' : 'dark')
}

watch(themePref, apply)
