// 设置 · 凭据分区（自 SettingsView 拆出，SET-01 分区二）：CF API Key 只显示已配置
// 状态与 credential reference，不回显完整 secret（原型 §7.11）。数据来源：
// useCredentials（EnvService.GetApiKey / SetApiKey）。行为边界：更换进入编辑态
//（password 输入每次清空，不回填旧值），保存成功仅置已配置态；读取失败行内降级
// 并禁用更换入口。
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SettingsRow, SettingsSection } from '../SettingsSection'
import { useCredentials } from '../hooks/useCredentials'

/** @description 凭据：状态行（徽章 + credential reference）↔ 编辑行（password 输入 + 保存/取消） */
export default function CredentialsSection() {
    const { t } = useTranslation()
    const cred = useCredentials()

    return (
        <SettingsSection titleKey="settings.creds.title" descKey="settings.creds.desc">
            <SettingsRow labelKey="settings.creds.cfKey">
                {cred.editing ? (
                    <div className="flex min-w-50 flex-1 items-center gap-2">
                        <Input
                            value={cred.input}
                            onChange={e => cred.setInput(e.target.value)}
                            placeholder={t('settings.creds.inputPlaceholder')}
                            type="password"
                            className="w-full"
                            autoComplete="off"
                        />
                    </div>
                ) : (
                    <div className="flex min-w-50 flex-1 items-center gap-2">
                        {cred.loadFailed ? (
                            <span className="text-destructive text-xs">{t('settings.creds.loadFailed')}</span>
                        ) : (
                            !cred.loading && (
                                <>
                                    <Badge variant={cred.configured ? 'st-ok' : 'st-mut'}>
                                        {cred.configured ? t('settings.creds.configured') : t('settings.creds.notConfigured')}
                                    </Badge>
                                    <span className="text-muted-foreground truncate font-mono text-xs">
                                        {t('settings.creds.refText')}
                                    </span>
                                </>
                            )
                        )}
                    </div>
                )}
                <div className="flex flex-none items-center gap-2">
                    {cred.editing ? (
                        <>
                            <Button size="sm" disabled={cred.saving || !cred.input.trim()} onClick={() => void cred.save()}>
                                {t('settings.creds.saveBtn')}
                            </Button>
                            <Button variant="ghost" size="sm" disabled={cred.saving} onClick={cred.cancelEdit}>
                                {t('common.cancel')}
                            </Button>
                        </>
                    ) : (
                        <Button
                            variant="outline"
                            size="sm"
                            disabled={cred.loading || cred.loadFailed}
                            onClick={cred.startReplace}
                        >
                            {t('settings.creds.replaceBtn')}
                        </Button>
                    )}
                </div>
            </SettingsRow>
        </SettingsSection>
    )
}
