// 设置 · 保留策略分区（自 SettingsView 拆出，SET-01 分区四，原型 SET-02 / 契约 06
// §9，票 #65）：五参数编辑 +「立即回收空间」。数据来源：useRetention
//（SettingsService.GetRetentionSettings / UpdateRetentionSettings / RequestGC）。
// 行为边界：范围校验唯一口径在后端 err.settings.retention_invalid——前端只拦
//「无法上 wire 的输入」（useRetention 内），越界值照常提交以呈现后端整体拒绝
// 文案；本组件只渲染表单行与操作行，min/max 属性仅作输入辅助不作拦截。
import { useTranslation } from 'react-i18next'
import { Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SettingsRow, SettingsSection } from '../SettingsSection'
import { useRetention } from '../hooks/useRetention'

/** @description 保留策略：五参数行 + 操作行（保存 / 立即回收空间） */
export default function RetentionSection() {
    const { t } = useTranslation()
    const retention = useRetention()

    return (
        <SettingsSection titleKey="settings.retention.title" descKey="settings.retention.desc">
            {retention.loading ? (
                <div className="text-muted-foreground py-1.5 text-sm">{t('settings.retention.loading')}</div>
            ) : retention.error ? (
                <div className="text-destructive py-1.5 text-sm">
                    {t('settings.retention.loadFailed')}：{retention.error}
                </div>
            ) : (
                <>
                    {/* keep_commits：范围 5–200（K=3 硬保底后端固定，不设键） */}
                    <SettingsRow labelKey="settings.retention.keepCommits">
                        <div className="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                            <Input
                                value={retention.form.keepCommits}
                                onChange={e => retention.setField('keepCommits', e.target.value)}
                                type="number"
                                min={5}
                                max={200}
                                className="w-24"
                            />
                            <span className="text-muted-foreground text-xs">{t('settings.retention.unitCommits')}</span>
                            <span className="text-muted-foreground text-xs">{t('settings.retention.keepCommitsHint')}</span>
                        </div>
                    </SettingsRow>

                    {/* keep_days：范围 7–365 */}
                    <SettingsRow labelKey="settings.retention.keepDays">
                        <div className="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                            <Input
                                value={retention.form.keepDays}
                                onChange={e => retention.setField('keepDays', e.target.value)}
                                type="number"
                                min={7}
                                max={365}
                                className="w-24"
                            />
                            <span className="text-muted-foreground text-xs">{t('settings.retention.unitDays')}</span>
                            <span className="text-muted-foreground text-xs">{t('settings.retention.keepDaysHint')}</span>
                        </div>
                    </SettingsRow>

                    {/* relation_capacity_bytes：128 MiB–20 GiB（支持「数字 + 单位」输入） */}
                    <SettingsRow labelKey="settings.retention.relationCapacity">
                        <div className="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                            <Input
                                value={retention.form.relationCapacity}
                                onChange={e => retention.setField('relationCapacity', e.target.value)}
                                placeholder="1 GiB"
                                className="w-28"
                            />
                            <span className="text-muted-foreground text-xs">
                                {t('settings.retention.relationCapacityHint')}
                            </span>
                        </div>
                    </SettingsRow>

                    {/* preserve_max_bytes：1 MiB–512 MiB；0 = 不限 */}
                    <SettingsRow labelKey="settings.retention.preserveMax">
                        <div className="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                            <Input
                                value={retention.form.preserveMax}
                                onChange={e => retention.setField('preserveMax', e.target.value)}
                                placeholder="32 MiB"
                                className="w-28"
                            />
                            <span className="text-muted-foreground text-xs">{t('settings.retention.preserveMaxHint')}</span>
                        </div>
                    </SettingsRow>

                    {/* trash_days：范围 1–90 */}
                    <SettingsRow labelKey="settings.retention.trashDays">
                        <div className="flex min-w-[200px] flex-1 flex-wrap items-center gap-x-2.5 gap-y-1">
                            <Input
                                value={retention.form.trashDays}
                                onChange={e => retention.setField('trashDays', e.target.value)}
                                type="number"
                                min={1}
                                max={90}
                                className="w-24"
                            />
                            <span className="text-muted-foreground text-xs">{t('settings.retention.unitDays')}</span>
                            <span className="text-muted-foreground text-xs">{t('settings.retention.trashDaysHint')}</span>
                        </div>
                    </SettingsRow>

                    {/* 操作行：保存 + 立即回收空间（建 kind=gc 任务） */}
                    <div className="flex flex-wrap items-center gap-2 py-[9px]">
                        <Button size="sm" disabled={retention.saving} onClick={() => void retention.save()}>
                            {t('settings.retention.save')}
                        </Button>
                        <Button variant="outline" size="sm" disabled={retention.gcing} onClick={() => void retention.requestGC()}>
                            <Trash2 className="size-4" />
                            {t('settings.retention.gcRun')}
                        </Button>
                    </div>
                    <p className="text-muted-foreground mt-1 text-xs">{t('settings.retention.gcHint')}</p>
                </>
            )}
        </SettingsSection>
    )
}
