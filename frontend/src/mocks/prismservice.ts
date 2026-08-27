// PrismService 的 Mock 实现：实例目录定位、项目关联、目录级同步（junction / 文件级）、
// mods 元数据差异与推拉。方法名与参数严格对齐 prismservice.ts 绑定。
// 写操作真实变更 fixtures 内存库：添加关联后 Overview/Links 立即反映新状态。
import { clone, delay, db, findProject, findLink, linkResult } from './fixtures'
import type { Instance, LinkView, DirLinkView, MetaDiff, LinkResult } from '../../bindings/packgradle/internal/prism'
import type { PrismOverview } from '../../bindings/packgradle/internal/service'

// —— 目录级同步 ——
export async function AddDirLink(projectName: string, projectDir: string): Promise<void> {
    await delay(400)
    const link = findLink(projectName)
    if (!link) throw new Error(`{"code":"err.link.not_found","detail":"${projectName}"}`)
    db.dirLinks.push({
        project: projectName,
        instance: link.instance_id,
        project_dir: projectDir,
        instance_dir: projectDir,
        mode: '',
        files: null,
        project_exists: true,
        instance_exists: false,
    })
}

// CreateAllLinks 一键关联：junction 目录建链，实例侧缺失的转 manual，文件级目录跳过
export async function CreateAllLinks(projectName: string): Promise<LinkResult[] | null> {
    await delay(1200)
    const results: LinkResult[] = [linkResult('pack.toml', false, 'existing', '已通过硬链接关联')]
    for (const dl of db.dirLinks.filter(d => d.project === projectName)) {
        if (dl.mode === 'files') {
            results.push(linkResult(dl.project_dir, true, 'skipped', '文件级同步目录不参与一键关联'))
        } else if (!dl.instance_exists) {
            results.push(linkResult(dl.project_dir, true, 'manual', '实例侧目录不存在，需手动处理'))
        } else {
            results.push(linkResult(dl.project_dir, true, 'linked'))
        }
    }
    return clone(results)
}

// CreateInstance 为项目创建同名开发实例并自动关联
export async function CreateInstance(projectName: string): Promise<Instance> {
    await delay(1500)
    const project = findProject(projectName)
    if (!project) throw new Error(`{"code":"err.proj.not_found","detail":"${projectName}"}`)
    const id = `${projectName.toLowerCase().replace(/[^a-z0-9-]+/g, '-')}-dev`
    const existing = db.instances.find(i => i.id === id)
    if (existing) throw new Error(`{"code":"err.prism.instance_exists","detail":"${id}"}`)
    const inst: Instance = {
        id,
        name: `${projectName} 开发`,
        path: `${db.instancesDir}\\${id}`,
        game_dir: `${db.instancesDir}\\${id}\\minecraft`,
        group: '开发',
        minecraft: project.minecraft || '1.21.1',
        modloader: project.modloader || 'fabric',
        modloader_version: project.modloader_version || '0.16.9',
        error: '',
    }
    db.instances.push(inst)
    db.links.push({
        project: projectName,
        project_path: project.path,
        instance_id: id,
        instance_name: inst.name,
        instance_path: inst.path,
        instance_valid: true,
    })
    return clone(inst)
}

export async function EnsurePGIgnore(projectName: string): Promise<boolean> {
    await delay(300)
    db.pgignore[projectName] = true
    return true
}

export async function GetInstancesPath(): Promise<string> {
    await delay(150)
    return db.instancesDir
}

export async function GetLinks(): Promise<LinkView[] | null> {
    await delay(300)
    return clone(db.links)
}

export async function HasPGIgnore(projectName: string): Promise<boolean> {
    await delay(150)
    return !!db.pgignore[projectName]
}

export async function InstancesDir(): Promise<string> {
    await delay(150)
    return db.instancesDir
}

export async function LinkProject(projectName: string, instanceID: string): Promise<void> {
    await delay(400)
    const inst = db.instances.find(i => i.id === instanceID)
    if (!inst) throw new Error(`{"code":"err.prism.instance_not_found","detail":"${instanceID}"}`)
    const existing = findLink(projectName)
    if (existing) {
        existing.instance_id = instanceID
        existing.instance_name = inst.name
        existing.instance_path = inst.path
        existing.instance_valid = true
        return
    }
    const project = findProject(projectName)
    db.links.push({
        project: projectName,
        project_path: project?.path ?? '',
        instance_id: instanceID,
        instance_name: inst.name,
        instance_path: inst.path,
        instance_valid: true,
    })
}

export async function ListDirFiles(projectName: string, dir: string): Promise<string[] | null> {
    await delay(200)
    return clone(db.projectDirFiles[`${projectName}/${dir}`] ?? [])
}

export async function ListDirLinks(projectName: string): Promise<DirLinkView[] | null> {
    await delay(250)
    return clone(db.dirLinks.filter(d => d.project === projectName))
}

export async function ListInstanceDirFiles(projectName: string, dir: string): Promise<string[] | null> {
    await delay(200)
    return clone(db.instanceDirFiles[`${projectName}/${dir}`] ?? [])
}

export async function ListInstances(): Promise<Instance[] | null> {
    await delay(350)
    return clone(db.instances)
}

export async function ListProjectDirs(projectName: string): Promise<string[] | null> {
    await delay(200)
    return clone(db.projectDirs[projectName] ?? [])
}

export async function ManualLinkDir(projectName: string, dir: string): Promise<LinkResult> {
    await delay(700)
    const dl = db.dirLinks.find(d => d.project === projectName && d.project_dir === dir)
    if (dl) dl.instance_exists = true
    return linkResult(dir, true, 'linked')
}

// MetaDiff 返回差异快照（fetched_at 取当前时间，模拟实时计算）
export async function MetaDiff(projectName: string): Promise<MetaDiff> {
    await delay(600)
    const diff = db.metaDiff[projectName]
    if (!diff) return { fetched_at: new Date().toISOString(), instance_only: [], project_only: [], version_diff: [] }
    return { ...clone(diff), fetched_at: new Date().toISOString() }
}

export async function Overview(): Promise<PrismOverview> {
    await delay(500)
    return {
        instances_dir: db.instancesDir,
        locate_error: '',
        instances: clone(db.instances),
        links: clone(db.links),
    }
}

// PullMeta 实例 → 项目：instance_only 清空（对应 mod 补进项目 mods）、
// version_diff 对齐实例版本，返回处理条数
export async function PullMeta(projectName: string, modID: string): Promise<number> {
    await delay(900)
    const diff = db.metaDiff[projectName]
    const project = findProject(projectName)
    if (!diff) return 0
    const mods = project?.mods ?? (project ? (project.mods = []) : [])

    const pullOne = (id: string, instanceVersion?: string): boolean => {
        const ioIdx = (diff.instance_only ?? []).indexOf(id)
        const vdIdx = (diff.version_diff ?? []).findIndex(v => v.id === id)
        let hit = false
        if (ioIdx >= 0) {
            diff.instance_only!.splice(ioIdx, 1)
            mods.push({
                id,
                name: id,
                side: 'both',
                version: instanceVersion ?? id,
                file: `${id}.jar`,
                path: `C:\\mock\\projects\\${projectName}\\mods\\${id}.jar`,
                cf_project_id: 0,
                cf_file_id: 0,
                cf_version: '',
                cf_file_date: '',
                cf_release_type: 0,
            })
            hit = true
        }
        if (vdIdx >= 0) {
            const vd = diff.version_diff![vdIdx]
            const target = mods.find(m => m.id === id)
            if (target) target.version = vd.instance_version
            diff.version_diff!.splice(vdIdx, 1)
            hit = true
        }
        return hit
    }

    if (!modID) {
        let count = 0
        for (const id of [...(diff.instance_only ?? [])]) {
            if (pullOne(id)) count++
        }
        for (const vd of [...(diff.version_diff ?? [])]) {
            if (pullOne(vd.id, vd.instance_version)) count++
        }
        return count
    }
    return pullOne(modID) ? 1 : 0
}

// PushMeta 项目 → 实例：project_only 清空、version_diff 对齐项目版本，返回处理条数
export async function PushMeta(projectName: string, modID: string): Promise<number> {
    await delay(900)
    const diff = db.metaDiff[projectName]
    if (!diff) return 0
    let count = 0
    if (!modID) {
        count += diff.project_only?.length ?? 0
        count += diff.version_diff?.length ?? 0
        diff.project_only = []
        diff.version_diff = []
        return count
    }
    const po = diff.project_only ?? []
    const vd = diff.version_diff ?? []
    const poIdx = po.indexOf(modID)
    const vdIdx = vd.findIndex(v => v.id === modID)
    if (poIdx >= 0) {
        po.splice(poIdx, 1)
        count++
    }
    if (vdIdx >= 0) {
        vd.splice(vdIdx, 1)
        count++
    }
    return count
}

export async function RemoveDirLink(projectName: string, projectDir: string): Promise<void> {
    await delay(300)
    const idx = db.dirLinks.findIndex(d => d.project === projectName && d.project_dir === projectDir)
    if (idx >= 0) db.dirLinks.splice(idx, 1)
}

// SelectInstanceFiles 从实例侧挑选文件建立文件级同步（新文件 linked，已存在 existing）
export async function SelectInstanceFiles(projectName: string, dir: string, files: string[] | null): Promise<LinkResult[] | null> {
    await delay(800)
    const dl = db.dirLinks.find(d => d.project === projectName && d.project_dir === dir)
    if (!dl) throw new Error(`{"code":"err.sync.dir_not_linked","detail":"${dir}"}`)
    const before = new Set(dl.files ?? [])
    dl.mode = 'files'
    dl.files = files ?? []
    return (files ?? []).map(f =>
        before.has(f) ? linkResult(f, false, 'existing') : linkResult(f, false, 'linked'),
    )
}

export async function SetDirLinkFiles(projectName: string, dir: string, files: string[] | null): Promise<void> {
    await delay(300)
    const dl = db.dirLinks.find(d => d.project === projectName && d.project_dir === dir)
    if (dl) dl.files = files ?? []
}

export async function SetDirLinkMode(projectName: string, dir: string, mode: string): Promise<void> {
    await delay(300)
    const dl = db.dirLinks.find(d => d.project === projectName && d.project_dir === dir)
    if (dl) {
        dl.mode = mode
        dl.files = mode === 'files' ? (dl.files ?? []) : null
    }
}

export async function SetInstancesPath(path: string): Promise<void> {
    await delay(500)
    db.instancesDir = path
}

export async function UnlinkProject(projectName: string): Promise<void> {
    await delay(400)
    const idx = db.links.findIndex(l => l.project === projectName)
    if (idx >= 0) db.links.splice(idx, 1)
}

// WatchMods 返回正在监听 mods 目录的项目（= 已关联项目）
export async function WatchMods(): Promise<string[] | null> {
    await delay(200)
    return db.links.map(l => l.project)
}
