// 设置 · 适配器与工具分区(自 SettingsView 拆出,SET-01 分区一):packwiz 与
// prism-launcher 两行对称的 ToolPathRow——锁定态(检测 found 且带回 path)只读
// 展示路径 +「更改」退路,未找到或点「更改」后回输入框手填保存。数据来源:
// useTools(EnvService.Detect / SetToolPath)。行为边界:前端不判路径对错——
// Detect 的 found 是唯一健康口径兼锁定条件,检测失败保持未找到态(可手动填
// 路径后保存),保存回执即最新检测投影直接回填输入框,保存成功才退出编辑态。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { ToolInfo } from '../../../../bindings/packgradle/internal/service'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SettingsRow, SettingsSection } from '../SettingsSection'
import { useTools } from '../hooks/useTools'

/**
 * @description 工具路径行(双工具对称):锁定态只读路径(st-ok 徽章 + truncate
 *              mono 文本,悬浮 title 见全文)+「更改」;编辑/未找到态为 Input +
 *              徽章 + 检测/保存/取消。editing 纯 UI 局部量:保存成功或取消退出。
 */
function ToolPathRow({
    labelKey,
    placeholderKey,
    tool,
    path,
    saving,
    detecting,
    onPathChange,
    onDetect,
    onSave,
}: {
    labelKey: string
    placeholderKey: string
    tool: ToolInfo | undefined
    path: string
    saving: boolean
    detecting: boolean
    onPathChange: (v: string) => void
    onDetect: () => void
    onSave: () => Promise<boolean>
}) {
    const { t } = useTranslation()
    const [editing, setEditing] = useState(false)

    const locked = !editing && !!tool?.found && !!tool?.path

    return (
        <SettingsRow labelKey={labelKey}>
            {locked ? (
                <div className="flex min-w-50 flex-1 items-center gap-2">
                    <Badge variant="st-ok">{t('settings.tools.found')}</Badge>
                    <span
                        className="text-muted-foreground truncate font-mono text-xs"
                        title={tool?.path}
                    >
                        {tool?.path}
                    </span>
                </div>
            ) : (
                <div className="min-w-50 flex-1">
                    <Input
                        value={path}
                        onChange={e => onPathChange(e.target.value)}
                        placeholder={t(placeholderKey)}
                        className="w-full"
                    />
                </div>
            )}
            <div className="flex flex-none items-center gap-2">
                {locked ? (
                    <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
                        {t('settings.tools.changeBtn')}
                    </Button>
                ) : (
                    <>
                        <Badge variant={tool?.found ? 'st-ok' : 'st-warn'}>
                            {tool?.found ? t('settings.tools.found') : t('settings.tools.missing')}
                        </Badge>
                        <Button variant="outline" size="sm" disabled={detecting} onClick={onDetect}>
                            {t('settings.tools.checkBtn')}
                        </Button>
                        <Button
                            size="sm"
                            disabled={saving || !path.trim()}
                            onClick={async () => {
                                if (await onSave()) setEditing(false)
                            }}
                        >
                            {t('settings.tools.saveBtn')}
                        </Button>
                        {editing && (
                            <Button
                                variant="ghost"
                                size="sm"
                                onClick={() => {
                                    onPathChange(tool?.path ?? '')
                                    setEditing(false)
                                }}
                            >
                                {t('settings.tools.cancelBtn')}
                            </Button>
                        )}
                    </>
                )}
            </div>
        </SettingsRow>
    )
}

/** @description 适配器与工具:packwiz / prism 双 ToolPathRow(锁定只读/编辑双态) */
export default function ToolsSection() {
    const { t } = useTranslation()
    const tools = useTools()

    return (
        <SettingsSection first titleKey="settings.tools.title" descKey="settings.tools.desc">
            <ToolPathRow
                labelKey="settings.tools.packwizPath"
                placeholderKey="settings.tools.pathPlaceholder"
                tool={tools.packwizTool}
                path={tools.path('packwiz')}
                saving={tools.saving('packwiz')}
                detecting={tools.detecting}
                onPathChange={v => tools.setPath('packwiz', v)}
                onDetect={() => void tools.detectTools()}
                onSave={() => tools.saveToolPath('packwiz')}
            />

            <ToolPathRow
                labelKey="settings.tools.prism"
                placeholderKey="settings.tools.prismPathPlaceholder"
                tool={tools.prismTool}
                path={tools.path('prism-launcher')}
                saving={tools.saving('prism-launcher')}
                detecting={tools.detecting}
                onPathChange={v => tools.setPath('prism-launcher', v)}
                onDetect={() => void tools.detectTools()}
                onSave={() => tools.saveToolPath('prism-launcher')}
            />

            {tools.loading && (
                <p className="text-muted-foreground py-1 text-xs">{t('settings.tools.loading')}</p>
            )}
        </SettingsSection>
    )
}
