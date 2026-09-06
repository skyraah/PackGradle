// 应用壳层（ADR-0014 脚手架）：左侧 68px icon rail + 顶栏（品牌/页面标题/窗口控制）。
// 视觉与结构对齐 Vue 版（main:frontend/src/App.vue）。以下壳层件随 store 移植接回，
// 挂点即本文件对应注释处：
// - 主题切换按钮 + html.dark 先于首帧落位避免闪烁（stores/theme）
// - 任务中心铃铛（活跃任务数徽标）与抽屉（stores/ui 的 taskDrawerOpen + stores/syncCache 的 tasks）
// - 全局通知 snackbar 队列（stores/ui 排队，右下角 toast，语义色左边框）
// - 顶栏 MOCK 徽标（仅 dev 构建 __DEV__ 门控 + 动态导入裁剪，ADR-0001 §5）
import { useEffect, useState } from 'react'
import { Events, Window } from '@wailsio/runtime'
import { Hammer, LayoutGrid, Minus, Package, Gamepad2, Settings as SettingsIcon, Square, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Outlet, useLocation, useMatches, useNavigate } from 'react-router'
import { navItems } from './router'

const wailsWindow = Window

const railIconClass = 'size-6'

export default function App() {
    const { t } = useTranslation()
    const navigate = useNavigate()
    const location = useLocation()
    const matches = useMatches()

    // 无边框窗口：标题栏控件全部自绘，跟踪最大化状态以切换图标并去除圆角
    // （html.window-maximised，main.css 里据此切 body 圆角）
    const [maximised, setMaximised] = useState(false)
    useEffect(() => {
        let alive = true
        wailsWindow.IsMaximised().then((v: boolean) => { if (alive) setMaximised(v) }).catch(() => {})
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

    // rail 高亮按一级路径归属（/workspaces/:id/... 高亮「工作区」）
    const activeRoot = '/' + (location.pathname.split('/')[1] ?? '')

    // 顶栏页面标题取当前匹配路由的 handle.titleKey（见 router.tsx 各路由定义）
    const titleKey = [...matches]
        .reverse()
        .map((m) => (m.handle as { titleKey?: string } | undefined)?.titleKey)
        .find(Boolean)
    const pageTitle = titleKey ? t(titleKey) : ''

    return (
        <div className="bg-background text-foreground flex h-full w-full flex-col overflow-hidden">
            <header className="bg-card/90 flex h-[46px] flex-none items-center border-b border-border px-2.5 backdrop-blur">
                <div className="app-no-drag flex flex-none items-center gap-2">
                    <span className="bg-primary text-primary-foreground grid size-[30px] place-items-center rounded-lg">
                        <Hammer className="size-4" />
                    </span>
                    <span className="text-sm font-semibold">PackGradle</span>
                </div>
                <div className="bg-border mx-3.5 h-5 w-px flex-none" />
                <div className="text-muted-foreground max-w-80 truncate text-sm">{pageTitle}</div>
                {/* Wails 拖拽区：占据顶栏中部空白，内部不放交互控件 */}
                <div className="app-drag min-w-6 flex-1 self-stretch" />
                {/* [待接回] MOCK 徽标（dev）/任务中心铃铛/主题切换按钮——见文件头注释 */}

                {/* 无边框窗口控制按钮 */}
                <div className="app-no-drag ml-1 flex items-center">
                    <button
                        className="hover:bg-accent grid size-8 place-items-center rounded-md"
                        title={t('app.minimise')}
                        onClick={() => wailsWindow.Minimise()}
                    >
                        <Minus className="size-4" />
                    </button>
                    <button
                        className="hover:bg-accent grid size-8 place-items-center rounded-md"
                        title={maximised ? t('app.restore') : t('app.maximise')}
                        onClick={() => wailsWindow.ToggleMaximise()}
                    >
                        {maximised ? <LayoutGrid className="size-3.5" /> : <Square className="size-3.5" />}
                    </button>
                    <button
                        className="grid size-8 place-items-center rounded-md hover:bg-[#e81123] hover:text-white"
                        title={t('app.close')}
                        onClick={() => wailsWindow.Close()}
                    >
                        <X className="size-4" />
                    </button>
                </div>
            </header>

            <div className="flex min-h-0 flex-1">
                {/* 左侧 icon rail（常显不可收起，UX 原型 §5.1） */}
                <nav className="bg-rail border-border flex w-[68px] flex-none flex-col border-r pt-3">
                    {navItems.map((item) => (
                        <button
                            key={item.path}
                            className={
                                'mx-auto my-1 flex h-14 w-14 flex-col items-center justify-center gap-1 rounded-[10px] ' +
                                (activeRoot === item.path
                                    ? 'bg-tint-primary text-primary'
                                    : 'text-muted-foreground hover:bg-accent hover:text-foreground')
                            }
                            onClick={() => navigate(item.path)}
                        >
                            <item.icon className={railIconClass} />
                            <span className="text-[10px] leading-none">{t(item.titleKey)}</span>
                        </button>
                    ))}
                </nav>

                {/* 主内容区：唯一页面纵向滚动容器 */}
                <main className="min-w-0 flex-1 overflow-y-auto">
                    <Outlet />
                </main>
            </div>
        </div>
    )
}
