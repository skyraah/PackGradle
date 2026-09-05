// 工作区对象头三常驻页签（拍板 Q8-a）的共享构建器：变化/受管范围/历史三页各以
// 自身页签为活动态。原为三视图各自复制的同形数组（历史页签曾在变化/受管范围两页
// 遗漏，票 #111 评审修复），收敛于此防再漂移：活动页签不带 to（页内激活，下划线
// 由 active-tab 驱动），其余带跨页路由由 WorkspaceObjectHead 内导航。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ComputedRef } from 'vue'
import type { HeadTab } from '../components/common/WorkspaceObjectHead.vue'

export type WorkspaceHeadTabValue = 'changes' | 'mappings' | 'history'

// relationID 以 getter 传入保持响应性；active 为调用页自己的页签值
export function useWorkspaceHeadTabs(
    relationID: () => string,
    active: WorkspaceHeadTabValue,
): ComputedRef<HeadTab[]> {
    const { t } = useI18n()
    return computed<HeadTab[]>(() => {
        const base = '/workspaces/' + relationID() + '/'
        const tab = (value: WorkspaceHeadTabValue, labelKey: string): HeadTab =>
            value === active
                ? { value, label: t(labelKey) }
                : { value, label: t(labelKey), to: base + value }
        return [
            tab('changes', 'objHead.tab.changes'),
            tab('mappings', 'objHead.tab.mappings'),
            tab('history', 'objHead.tab.history'),
        ]
    })
}
