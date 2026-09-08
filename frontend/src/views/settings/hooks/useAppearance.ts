// 外观分区取数钩子（自 SettingsView 拆出）：主题与语言偏好的读/写/选项清单，
// AppearanceSection 只管渲染。
// 数据来源：stores/theme（useThemeStore 读 pref、setThemePref 写持久化并落
// html.dark）、lib/i18n（i18n.language 读当前语言、setLocale 切换并写
// packgradle.locale）。
// 行为边界：切换即生效即持久化，无显式保存动作；选项清单是前端静态枚举——
// 主题三值（system/light/dark），语言 P1 仅 zh-CN（后续语言包就位后自然扩展）。
import { useTranslation } from 'react-i18next'
import { setLocale } from '@/lib/i18n'
import { setThemePref, useThemeStore, type ThemePref } from '@/stores/theme'

// —— 主题：跟随系统 / 浅色 / 深色（stores/theme 持久化 + 落 html.dark） ——
const themeOptions: { value: ThemePref; labelKey: string }[] = [
    { value: 'system', labelKey: 'settings.themeSystem' },
    { value: 'light', labelKey: 'settings.themeLight' },
    { value: 'dark', labelKey: 'settings.themeDark' },
]

// —— 语言：P1 仅 zh-CN 一套文案，选择即应用（后续语言包就位后此列表自然扩展） ——
const languageOptions = [{ value: 'zh-CN', labelKey: 'settings.languageZh' }]

/** @description 外观分区：主题/语言当前值、写入动作与选项清单 */
export function useAppearance() {
    const { i18n } = useTranslation()
    const themePref = useThemeStore(s => s.pref)

    return { themePref, setThemePref, locale: i18n.language, setLocale, themeOptions, languageOptions }
}
