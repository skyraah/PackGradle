// 系统文件/目录选择：封装 @wailsio/runtime 的 Dialogs.OpenFile。
// 注意：Wails 构建版 Dialogs.Question 等消息类对话框存在 Promise 挂起问题，
// 确认/询问一律走应用内对话框（shadcn AlertDialog）；OpenFile 不受影响，可放心使用。
import { Dialogs } from '@wailsio/runtime'
import { isMockEnabled } from '../api'

// 选择目录；用户取消返回 null
// dev 构建的 mock 模式下不弹系统对话框，直接返回模拟路径
// （__DEV__ 静态门：生产构建此分支连同 mock 路径字面量一并裁剪）
export async function pickDirectory(title?: string): Promise<string | null> {
    if (__DEV__ && isMockEnabled()) return 'C:\\mock\\downloads'
    const picked = await Dialogs.OpenFile({
        Title: title,
        CanChooseFiles: false,
        CanChooseDirectories: true,
        CanCreateDirectories: false,
    })
    return picked || null
}

// 选择文件；用户取消返回 null（回滚补全「提供文件」用，契约 06 §3.5，票 #61）。
// mock 语义与 pickDirectory 同款：dev mock 模式返回模拟路径，不弹系统对话框。
export async function pickFile(title?: string): Promise<string | null> {
    if (__DEV__ && isMockEnabled()) return 'C:\\mock\\provided-mod.jar'
    const picked = await Dialogs.OpenFile({
        Title: title,
        CanChooseFiles: true,
        CanChooseDirectories: false,
        CanCreateDirectories: false,
    })
    return picked || null
}
