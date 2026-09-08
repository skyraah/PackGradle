// 设置 · 适配器与工具分区（自 SettingsView 拆出，SET-01 分区一）：Packwiz 路径可
// 编辑，Prism 只读健康。数据来源：useTools（EnvService.Detect / SetToolPath）。
// 行为边界：前端不判路径对错——Detect 的 found 是唯一健康口径，检测失败保持
// 未找到态（可手动填路径后保存），保存回执即最新检测投影直接回填输入框。
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SettingsRow, SettingsSection } from '../SettingsSection'
import { useTools } from '../hooks/useTools'

/** @description 适配器与工具：Packwiz 路径行（输入 + 健康 + 检测/保存）+ Prism 只读行 */
export default function ToolsSection() {
    const { t } = useTranslation()
    const tools = useTools()

    return (
        <SettingsSection first titleKey="settings.tools.title" descKey="settings.tools.desc">
            <SettingsRow labelKey="settings.tools.packwizPath">
                <div className="min-w-[200px] flex-1">
                    <Input
                        value={tools.packwizPath}
                        onChange={e => tools.setPackwizPath(e.target.value)}
                        placeholder={t('settings.tools.pathPlaceholder')}
                        className="w-full"
                    />
                </div>
                <div className="flex flex-none items-center gap-2">
                    <Badge variant={tools.packwizTool?.found ? 'st-ok' : 'st-warn'}>
                        {tools.packwizTool?.found ? t('settings.tools.found') : t('settings.tools.missing')}
                    </Badge>
                    <Button variant="outline" size="sm" disabled={tools.detecting} onClick={() => void tools.detectTools()}>
                        {t('settings.tools.checkBtn')}
                    </Button>
                    <Button
                        size="sm"
                        disabled={tools.saving || !tools.packwizPath.trim()}
                        onClick={() => void tools.savePackwizPath()}
                    >
                        {t('settings.tools.saveBtn')}
                    </Button>
                </div>
            </SettingsRow>

            <SettingsRow labelKey="settings.tools.prism">
                <div className="flex min-w-[200px] flex-1 items-center gap-2">
                    <Badge variant={tools.prismTool?.found ? 'st-ok' : 'st-warn'}>
                        {tools.prismTool?.found ? t('settings.tools.found') : t('settings.tools.missing')}
                    </Badge>
                    {tools.prismTool?.path && (
                        <span
                            className="text-muted-foreground truncate font-mono text-xs"
                            title={tools.prismTool.path}
                        >
                            {tools.prismTool.path}
                        </span>
                    )}
                </div>
            </SettingsRow>

            {tools.loading && (
                <p className="text-muted-foreground py-1 text-xs">{t('settings.tools.loading')}</p>
            )}
        </SettingsSection>
    )
}
