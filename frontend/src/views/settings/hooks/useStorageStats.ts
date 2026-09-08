// 本地存储分区取数钩子（自 SettingsView 拆出）：存储统计只读投影，StorageSection
// 只管渲染。
// 数据来源：SettingsService.GetStorageStats（#90 只读数据面：CAS 对象数/总量、
// SQLite 库大小、剩余磁盘；staging/日志位置待后端补字段后再上行）。
// 行为边界：无任何写入口；读取失败置 error 行内降级，不弹全局错误、不阻塞其他分区。
import { useEffect, useState } from 'react'
import { SettingsService } from '@/api'
import type { StorageStatsDTO } from '../../../../bindings/packgradle/internal/transport/models'
import { errText } from '@/utils/errors'

/** @description 本地存储分区：统计投影的读取与降级，首装读取一次 */
export function useStorageStats() {
    const [loading, setLoading] = useState(true)
    const [error, setError] = useState('')
    const [stats, setStats] = useState<StorageStatsDTO | null>(null)

    useEffect(() => {
        void (async () => {
            try {
                setStats(await SettingsService.GetStorageStats())
            } catch (e) {
                setError(errText(e))
            } finally {
                setLoading(false)
            }
        })()
        // eslint-disable-next-line react-hooks/exhaustive-deps -- 首装取数一次，与 Vue onMounted 同口径
    }, [])

    return { loading, error, stats }
}
