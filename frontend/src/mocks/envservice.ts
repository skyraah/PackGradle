// EnvService 的 Mock 实现：工具检测 / PATH 配置 / API Key / 首次运行标记。
// 方法名与参数严格对齐 bindings/packgradle/internal/service/envservice.ts。
import { clone, delay, db } from './fixtures'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'

// ConfigExists 判断全局 config.toml 是否已在磁盘上（首次引导依据）
export async function ConfigExists(): Promise<boolean> {
    await delay(120)
    return db.configExists
}

// Configure 模拟写入 PATH：无新增目录
export async function Configure(): Promise<[ToolInfo[] | null, string[] | null]> {
    await delay(500)
    return [clone(db.tools), []]
}

export async function Detect(): Promise<ToolInfo[] | null> {
    await delay(300)
    return clone(db.tools)
}

export async function GetApiKey(): Promise<string> {
    await delay(100)
    return db.apiKey
}

export async function MarkConfigCreated(): Promise<void> {
    db.configExists = true
}

export async function SetApiKey(key: string): Promise<void> {
    await delay(200)
    db.apiKey = key
}

// SetToolPath 保存手动路径：标记为已找到（来源 config），返回最新检测结果
export async function SetToolPath(name: string, path: string): Promise<ToolInfo[] | null> {
    await delay(400)
    const tool = db.tools.find(t => t.name === name)
    if (!tool) throw new Error(`[mock] 未知工具: ${name}`)
    if (path) {
        tool.found = true
        tool.path = path
        tool.source = 'config'
    }
    return clone(db.tools)
}
