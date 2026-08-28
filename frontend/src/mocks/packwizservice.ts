// PackwizService 的 Mock 实现：项目导入/移除/刷新、更新检查、CF 版本获取。
// 方法名与参数严格对齐 bindings/packgradle/internal/service/packwizservice.ts。
// 写操作真实变更 fixtures 内存库，返回值保持后端语义（如 RemoveProject 返回最新列表）。
import { clone, delay, db, findProject } from './fixtures'
import type { PackProject, ModInfo, RefreshResult, UpdateCheckResult } from '../../bindings/packgradle/internal/packwiz'
import type { ModVersionResult } from '../../bindings/packgradle/internal/service'

export async function CheckUpdates(projectName: string): Promise<UpdateCheckResult> {
    await delay(1500)
    const fixture = db.updateCheck[projectName]
    return {
        ok: true,
        output: fixture ? `Checked ${fixture.updates.length + fixture.errors.length} mods for updates` : 'All mods are up to date!',
        updates: fixture ? clone(fixture.updates) : [],
        errors: fixture ? clone(fixture.errors) : [],
    }
}

// FetchAllModVersions 批量补全 CF 版本信息：个别 mod 模拟失败（err.cf.*）
export async function FetchAllModVersions(projectName: string): Promise<ModVersionResult[] | null> {
    await delay(1800)
    const project = findProject(projectName)
    if (!project) return null
    return (project.mods ?? []).map(m => {
        if (m.cf_project_id === 0) {
            return { id: m.id, name: m.name, version: m.version, ok: false, error: '{"code":"err.cf.not_cf_source"}' }
        }
        return { id: m.id, name: m.name, version: m.cf_version || m.version, ok: true, error: '' }
    })
}

// FetchModVersion 单个 mod 补全 CF 版本信息（返回填充后的 ModInfo）
export async function FetchModVersion(projectName: string, modID: string): Promise<ModInfo> {
    await delay(800)
    const project = findProject(projectName)
    const target = project?.mods?.find(m => m.id === modID)
    if (!target) throw new Error(`[mock] ${'{"code":"err.mod.not_found","detail":"' + modID + '"}'}`)
    // 写回内存库：后续列表读取即带上版本信息
    target.cf_version = target.cf_version || target.version
    target.cf_file_date = target.cf_file_date || '2026-07-15T00:00:00Z'
    target.cf_release_type = target.cf_release_type || 1
    return clone(target)
}

// ImportProject 从 pack.toml 路径导入：按路径尾段命名，追加一个新项目
export async function ImportProject(packTomlPath: string): Promise<PackProject> {
    await delay(900)
    const name = packTomlPath.split(/[\\/]/).filter(Boolean).slice(-2, -1)[0] || 'MockPack'
    const existing = findProject(name)
    if (existing) return clone(existing)
    const project: PackProject = {
        name,
        path: packTomlPath.replace(/[\\/]pack\.toml$/, ''),
        pack_toml: packTomlPath,
        version: '0.1.0',
        author: 'mock',
        pack_format: 'packwiz:1.1.0',
        minecraft: '1.21.1',
        modloader: 'fabric',
        modloader_version: '0.16.9',
        error: '',
        mods: [],
    }
    db.projects.push(project)
    return clone(project)
}

export async function ListProjects(): Promise<PackProject[] | null> {
    await delay(400)
    return clone(db.projects)
}

export async function RefreshProject(name: string): Promise<RefreshResult> {
    await delay(700)
    const project = findProject(name)
    if (!project) return { ok: false, output: `{"code":"err.proj.not_found","detail":"${name}"}` }
    return { ok: true, output: `Index refreshed!\nMETADATA: ${project.mods?.length ?? 0} files` }
}

export async function RemoveProject(name: string): Promise<PackProject[] | null> {
    await delay(500)
    const idx = db.projects.findIndex(p => p.name === name)
    if (idx >= 0) db.projects.splice(idx, 1)
    return clone(db.projects)
}

// UpdateMods 应用更新：把检查结果中的最新版本写回项目 mods（单个或全部）
export async function UpdateMods(projectName: string, modName: string): Promise<RefreshResult> {
    await delay(1200)
    const project = findProject(projectName)
    if (!project) return { ok: false, output: `{"code":"err.proj.not_found","detail":"${projectName}"}` }
    const updates = db.updateCheck[projectName]?.updates ?? []
    const applied = modName ? updates.filter(u => u.name === modName) : updates
    for (const u of applied) {
        const target = project.mods?.find(m => m.id === u.name || m.name === u.name)
        if (target) {
            const newVer = u.latest_file.match(/([\d.]+[-+][\w.+-]*|[\d.]+)\.jar$/)?.[1] ?? target.version
            target.version = newVer
            target.cf_version = newVer
            target.file = u.latest_file
        }
    }
    return {
        ok: true,
        output: applied.length > 0
            ? `Updated ${applied.length} mods:\n${applied.map(u => ` * ${u.name}: ${u.current_file} -> ${u.latest_file}`).join('\n')}`
            : 'No updates to apply.',
    }
}
