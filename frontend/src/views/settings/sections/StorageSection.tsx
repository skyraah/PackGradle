// 设置 · 本地存储分区（自 SettingsView 拆出，SET-01 分区三）：CAS 对象数/总量、
// SQLite 库大小、剩余磁盘的只读健康投影。数据来源：useStorageStats
//（SettingsService.GetStorageStats，#90 只读数据面；staging/日志位置待后端补字段
// 后再上行）。行为边界：无任何写入口；读取失败行内降级；字节显示统一走
// lib.formatBytesHuman（最大可装单位 + 1 位小数，有损仅展示）。
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { SettingsRow, SettingsSection } from '../SettingsSection'
import { useStorageStats } from '../hooks/useStorageStats'
import { formatBytesHuman } from '../lib'

/** @description 本地存储：CAS 总量 / 库大小 / 剩余磁盘三行，前两行带健康徽章 */
export default function StorageSection() {
    const { t } = useTranslation()
    const { loading, error, stats } = useStorageStats()

    return (
        <SettingsSection titleKey="settings.storage.title" descKey="settings.storage.desc">
            {loading ? (
                <div className="text-muted-foreground py-1.5 text-sm">{t('settings.storage.loading')}</div>
            ) : error ? (
                <div className="text-destructive py-1.5 text-sm">
                    {t('settings.storage.loadFailed')}：{error}
                </div>
            ) : stats ? (
                <>
                    <SettingsRow labelKey="settings.storage.casTotal">
                        <div className="min-w-[200px] flex-1 text-sm">
                            {stats.cas_object_count.toLocaleString()} {t('settings.storage.objectUnit')}
                            <span className="text-muted-foreground">· {formatBytesHuman(stats.cas_total_bytes)}</span>
                        </div>
                        <div className="flex flex-none items-center gap-2">
                            <Badge variant="st-ok">{t('settings.storage.healthy')}</Badge>
                        </div>
                    </SettingsRow>

                    <SettingsRow labelKey="settings.storage.dbSize">
                        <div className="min-w-[200px] flex-1 text-sm">{formatBytesHuman(stats.db_size_bytes)}</div>
                        <div className="flex flex-none items-center gap-2">
                            <Badge variant="st-ok">{t('settings.storage.healthy')}</Badge>
                        </div>
                    </SettingsRow>

                    <SettingsRow labelKey="settings.storage.freeDisk">
                        <div className="min-w-[200px] flex-1 text-sm">{formatBytesHuman(stats.free_disk_bytes)}</div>
                    </SettingsRow>
                </>
            ) : null}
        </SettingsSection>
    )
}
