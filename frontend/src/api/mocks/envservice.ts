// EnvService 的 Mock 实现（自 main:frontend/src/mocks/envservice.ts 原样移植）：
// 工具检测 / PATH 配置 / API Key / 首次运行标记。
// 方法名与参数严格对齐 bindings/packgradle/internal/service/envservice.ts。
import { clone, delay, db } from './fixtures'

// ConfigExists 判断全局 config.toml 是否已在磁盘上（首次引导依据）
export async function ConfigExists(): Promise<boolean> {
    await delay(120)
    return db.configExists
}

// Configure 模拟写入 PATH：无新增目录
export async function Configure(): Promise<[typeof db.tools | null, string[] | null]> {
    await delay(500)
    return [clone(db.tools), []]
}

export async function Detect(): Promise<typeof db.tools | null> {
    await delay(300)
    return clone(db.tools)
}

// GetApiKey 返回掩码投影（ADR-0015：明文不出后端）——已设置回非空
// 掩码串（掩码点 + 末四位），与后端语义一致，判空用法不受影响。
export async function GetApiKey(): Promise<string> {
    await delay(100)
    const entry = db.getSlot('curseforge/api-key')
    return entry ? '••••' + entry.value.slice(-4) : ''
}

export async function MarkConfigCreated(): Promise<void> {
    db.configExists = true
}

// SetApiKey 保存钥匙（空串 = 清除，与后端口径一致）
export async function SetApiKey(key: string): Promise<void> {
    await delay(200)
    const v = key.trim()
    if (v) db.setSlot('curseforge/api-key', v)
    else db.clearSlot('curseforge/api-key')
}

// SetToolPath 保存手动路径：标记为已找到（来源 config），返回最新检测结果
export async function SetToolPath(name: string, path: string): Promise<typeof db.tools | null> {
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
