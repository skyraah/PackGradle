// 设置 · 外观分区（自 SettingsView 拆出；P1 既有能力，以同一分区语言缀于画板五
// 分区之后，不占 SET-01 分区位）：主题（跟随系统/浅色/深色）与语言（P1 仅 zh-CN）。
// 数据来源：useAppearance（stores/theme 持久化并落 html.dark；lib/i18n 的 setLocale
// 写 packgradle.locale）。行为边界：切换即生效即持久化，无显式保存动作。
import { useTranslation } from 'react-i18next'
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select'
import { SettingsRow, SettingsSection } from '../SettingsSection'
import { useAppearance } from '../hooks/useAppearance'
import type { ThemePref } from '@/stores/theme'

/** @description 外观：主题行（提示文案 + Select）+ 语言行（Select） */
export default function AppearanceSection() {
    const { t } = useTranslation()
    const appearance = useAppearance()

    return (
        <SettingsSection titleKey="settings.appearanceTitle" descKey="settings.appearanceHint">
            <SettingsRow labelKey="settings.theme">
                <div className="text-muted-foreground min-w-[200px] flex-1 text-xs">{t('settings.themeHint')}</div>
                <div className="flex flex-none items-center gap-2">
                    <Select value={appearance.themePref} onValueChange={v => appearance.setThemePref(v as ThemePref)}>
                        <SelectTrigger className="w-36">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                            {appearance.themeOptions.map(o => (
                                <SelectItem key={o.value} value={o.value}>
                                    {t(o.labelKey)}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                </div>
            </SettingsRow>

            <SettingsRow labelKey="settings.language">
                <div className="flex flex-none items-center gap-2">
                    <Select value={appearance.locale} onValueChange={appearance.setLocale}>
                        <SelectTrigger className="w-36">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                            {appearance.languageOptions.map(o => (
                                <SelectItem key={o.value} value={o.value}>
                                    {t(o.labelKey)}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                </div>
            </SettingsRow>
        </SettingsSection>
    )
}
