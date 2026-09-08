// 无边框窗口控制（壳层）：嵌入主区顶条行尾——主题切换钮 + 最小化/最大化还原/关闭三键
// 同排平铺，全部走 shadcn Button（ghost + 调用处覆写）。对齐原型 .win-controls/.wc：44px
// 宽命中区撑满 44px 顶条、方角无底，hover 中性灰叠加、关闭键 hover 红底白字。整组压
// app-no-drag + relative z-30：前者防点击被顶条 app-drag 拖拽面吞掉，后者压过顶部 8px
// 隐形拖拽带（z-20），保证按钮上半段也能点中。折叠侧栏钮与主题钮对调（2026-09-08）：
// 折叠钮在侧栏 footer（AppSidebar），主题行原位迁入此处。主题翻转逻辑（View Transitions
// 换肤）随 stores/theme 落地整体收编（原 useLocalDarkTheme 壳件已按其 TODO 注释删除）。
import { LayoutGrid, Minus, Moon, Square, Sun, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Window } from '@wailsio/runtime'
import { useWindowMaximised } from './useWindowMaximised'
import { toggleTheme, useThemeStore } from '@/stores/theme'
import { Button } from '@/components/ui/button'

// 嵌入态公共类：h-auto 让 flex items-stretch 撑满顶条高（压过按钮基类尺寸），44px 宽命中
// 区、方角无内边距；常态图标色对齐原型 .wc（--muted = 侧栏 muted #8a8f98，与面包屑一致），
// hover 中性 7% + 图标回 foreground；图标 16px / stroke 1.5（与侧栏导航图标同规格）
const WC_BTN =
    'h-auto w-11 rounded-none px-0 text-sidebar-muted-foreground hover:bg-foreground/7 hover:text-foreground'

/**
 * @description 顶条行尾窗口控制组：主题切换钮 + 窗口三键；最大化图标随窗口状态切换，
 *              关闭键 hover 红底白字。弹层（z-50）开启时压住本组属预期（互斥使用）。
 */
export default function WindowControls() {
    const { t } = useTranslation()
    const maximised = useWindowMaximised()
    const dark = useThemeStore(s => s.isDark)

    return (
        <div className="app-no-drag relative z-30 ml-auto flex h-full flex-none items-stretch">
            <Button
                variant="ghost"
                className={WC_BTN}
                title={t('common.toggleTheme')}
                aria-label={t('common.toggleTheme')}
                aria-pressed={dark}
                onClick={toggleTheme}
            >
                {dark ? (
                    <Moon className="size-4" strokeWidth={1.5} />
                ) : (
                    <Sun className="size-4" strokeWidth={1.5} />
                )}
            </Button>
            <Button
                variant="ghost"
                className={WC_BTN}
                title={t('app.minimise')}
                onClick={() => Window.Minimise()}
            >
                <Minus className="size-4" strokeWidth={1.5} />
            </Button>
            <Button
                variant="ghost"
                className={WC_BTN}
                title={maximised ? t('app.restore') : t('app.maximise')}
                onClick={() => Window.ToggleMaximise()}
            >
                {maximised ? (
                    <LayoutGrid className="size-4" strokeWidth={1.5} />
                ) : (
                    <Square className="size-4" strokeWidth={1.5} />
                )}
            </Button>
            <Button
                variant="ghost"
                className="h-auto w-11 rounded-none px-0 text-sidebar-muted-foreground hover:bg-[#dc2626] hover:text-white"
                title={t('app.close')}
                onClick={() => Window.Close()}
            >
                <X className="size-4" strokeWidth={1.5} />
            </Button>
        </div>
    )
}
