// 系统文件/目录选择：封装 @wailsio/runtime 的 Dialogs.OpenFile。
// 注意：Wails 构建版 Dialogs.Question 等消息类对话框存在 Promise 挂起问题，
// 确认/询问一律走 Vuetify 对话框（ConfirmDialog）；OpenFile 不受影响，可放心使用。
import { Dialogs } from '@wailsio/runtime'
import { t } from '../i18n'
import { isMockEnabled } from '../api'

// Mock 模式下不弹系统对话框，直接返回模拟路径（配合 mock ImportProject 等写操作）

// 选择整合包的 pack.toml；用户取消返回 null
export async function pickPackToml(): Promise<string | null> {
    if (isMockEnabled()) return 'C:\\mock\\packs\\MockPack\\pack.toml'
    const picked = await Dialogs.OpenFile({
        Title: t('dialogs.pickPackToml'),
        CanChooseFiles: true,
        CanChooseDirectories: false,
        Filters: [{ DisplayName: 'pack.toml', Pattern: 'pack.toml' }],
    })
    return picked || null
}

// 选择工具路径（可执行文件或其所在目录）；用户取消返回 null
export async function pickToolPath(): Promise<string | null> {
    if (isMockEnabled()) return 'C:\\mock\\bin\\packwiz.exe'
    const picked = await Dialogs.OpenFile({
        Title: t('dialogs.pickToolPath'),
        CanChooseFiles: true,
        CanChooseDirectories: true,
    })
    return picked || null
}

// 选择目录；用户取消返回 null
export async function pickDirectory(title?: string): Promise<string | null> {
    if (isMockEnabled()) return 'C:\\mock\\downloads'
    const picked = await Dialogs.OpenFile({
        Title: title,
        CanChooseFiles: false,
        CanChooseDirectories: true,
        CanCreateDirectories: false,
    })
    return picked || null
}
