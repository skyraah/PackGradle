import { useEffect, useState } from 'react'
import { Events, Window } from '@wailsio/runtime'

/**
 * @description 无边框窗口最大化状态跟踪：挂载时查一次当前值，之后订阅 Wails 的
 *              WindowMaximise/WindowUnMaximise 事件增量维护；状态变化同步到
 *              html.window-maximised（assets/main.css 据此切 body 圆角）。
 * @description 卸载时反订阅事件；alive 标志挡掉初次查询竞态里的迟到回写。
 */
export function useWindowMaximised(): boolean {
    const [maximised, setMaximised] = useState(false)

    useEffect(() => {
        let alive = true
        Window.IsMaximised().then((v: boolean) => { if (alive) setMaximised(v) }).catch(() => {})
        const offMaximise = Events.On(Events.Types.Windows.WindowMaximise, () => setMaximised(true))
        const offUnMaximise = Events.On(Events.Types.Windows.WindowUnMaximise, () => setMaximised(false))
        return () => {
            alive = false
            offMaximise?.()
            offUnMaximise?.()
        }
    }, [])

    useEffect(() => {
        document.documentElement.classList.toggle('window-maximised', maximised)
    }, [maximised])

    return maximised
}
