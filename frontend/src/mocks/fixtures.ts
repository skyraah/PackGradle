// Mock 层内存数据库：模拟 packwiz 项目 / Prism 实例 / 关联 / 差异的全部只读与写后状态。
// 数据只存在于内存（刷新页面即重置），写操作真实变更状态，
// 让「添加关联 / 拉取 meta / 更新 mod」等流程能在无后端环境下完整走通。
import type { PackProject, ModInfo, ModUpdateInfo } from '../../bindings/packgradle/internal/packwiz'
import type { Instance, LinkView, DirLinkView, MetaDiff, LinkResult } from '../../bindings/packgradle/internal/prism'
import type { ToolInfo } from '../../bindings/packgradle/internal/service'

// 模拟网络/磁盘延迟：读快写慢，让 loading 状态可见
export function delay(ms = 200): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms))
}

// 读取时深拷贝，避免视图意外改动 fixture
export function clone<T>(v: T): T {
    return structuredClone(v)
}

// —— 环境工具 ——
const tools: ToolInfo[] = [
    {
        name: 'packwiz',
        found: true,
        path: 'C:\\Users\\SkyeJ\\go\\bin\\packwiz.exe',
        source: 'env',
        env_dir: 'C:\\Users\\SkyeJ\\go\\bin',
        env_ok: true,
    },
    {
        name: 'prism-launcher',
        found: true,
        path: 'C:\\Users\\SkyeJ\\AppData\\Roaming\\PrismLauncher',
        source: 'default-dir',
        env_dir: '',
        env_ok: true,
    },
]

// —— 项目（含 mods）——
function mod(
    id: string,
    name: string,
    version: string,
    side: 'client' | 'server' | 'both',
    cf: { pid: number; fid: number; ver?: string; date?: string; release?: number } | null,
): ModInfo {
    const file = cf ? `${id}-${version}.jar` : `${id}-${version}.jar`
    return {
        id,
        name,
        side,
        version,
        file,
        path: `C:\\mock\\projects\\Project-Collapse\\mods\\${file}`,
        cf_project_id: cf?.pid ?? 0,
        cf_file_id: cf?.fid ?? 0,
        cf_version: cf?.ver ?? '',
        cf_file_date: cf?.date ?? '',
        cf_release_type: cf?.release ?? 0,
    }
}

const projects: PackProject[] = [
    {
        name: 'Project-Collapse',
        path: 'C:\\mock\\projects\\Project-Collapse',
        pack_toml: 'C:\\mock\\projects\\Project-Collapse\\pack.toml',
        version: '1.4.2',
        author: 'SkyeJ',
        pack_format: 'packwiz:1.1.0',
        minecraft: '1.21.1',
        modloader: 'fabric',
        modloader_version: '0.16.9',
        error: '',
        mods: [
            mod('fabric-api', 'Fabric API', '0.115.0+1.21.1', 'both', { pid: 306612, fid: 6480001, ver: '0.115.0+1.21.1', date: '2026-06-02T10:00:00Z' }),
            mod('sodium', 'Sodium', '0.5.11', 'client', { pid: 357704, fid: 6480012, ver: '0.5.11', date: '2026-05-18T08:30:00Z' }),
            mod('lithium', 'Lithium', '0.13.1', 'both', { pid: 360417, fid: 6480020, ver: '0.13.1', date: '2026-04-30T12:00:00Z' }),
            mod('cloth-config', 'Cloth Config API', '15.0.127', 'both', { pid: 319057, fid: 6480030 }),
            mod('modmenu', 'Mod Menu', '11.0.3', 'client', { pid: 306707, fid: 6480040 }),
            mod('entityculling', 'Entity Culling', '1.7.0', 'client', { pid: 515285, fid: 6480050, ver: '1.7.0', date: '2026-03-21T09:00:00Z' }),
            mod('ferrite-core', 'FerriteCore', '7.0.0', 'both', { pid: 429235, fid: 6480060, ver: '7.0.0', date: '2026-02-14T00:00:00Z' }),
            mod('reeses-sodium-options', "Reese's Sodium Options", '1.8.0', 'client', { pid: 511319, fid: 6480070 }),
            mod('customcore', 'CustomCore（本地 jar）', '2.3.0', 'both', null),
        ],
    },
    {
        name: 'Skyblock-Rework',
        path: 'C:\\mock\\projects\\Skyblock-Rework',
        pack_toml: 'C:\\mock\\projects\\Skyblock-Rework\\pack.toml',
        version: '0.9.0',
        author: 'SkyeJ',
        pack_format: 'packwiz:1.1.0',
        minecraft: '1.21.5',
        modloader: 'neoforge',
        modloader_version: '21.5.6-beta',
        error: '',
        mods: [
            mod('jei', 'Just Enough Items', '19.8.4.115', 'both', { pid: 238222, fid: 6490001, ver: '19.8.4.115', date: '2026-07-01T00:00:00Z' }),
            mod('jade', 'Jade', '15.5.2', 'both', { pid: 324717, fid: 6490002 }),
            mod('ae2', 'Applied Energistics 2', '19.4.4', 'both', { pid: 223794, fid: 6490003 }),
        ],
    },
    {
        name: 'Legacy-1.18',
        path: 'C:\\mock\\projects\\Legacy-1.18',
        pack_toml: 'C:\\mock\\projects\\Legacy-1.18\\pack.toml',
        version: '',
        author: '',
        pack_format: '',
        minecraft: '',
        modloader: '',
        modloader_version: '',
        // 解析失败态：错误码 JSON（前端 displayText 会翻译）
        error: '{"code":"err.toml.parse","detail":"pack.toml"}',
        mods: null,
    },
]

// —— Prism 实例 / 关联 ——
let instancesDir = 'C:\\Users\\SkyeJ\\AppData\\Roaming\\PrismLauncher\\instances'

const instances: Instance[] = [
    {
        id: 'collapse-dev',
        name: 'Project-Collapse 开发',
        path: `${instancesDir}\\collapse-dev`,
        game_dir: `${instancesDir}\\collapse-dev\\minecraft`,
        group: '开发',
        minecraft: '1.21.1',
        modloader: 'fabric',
        modloader_version: '0.16.9',
        error: '',
    },
    {
        id: 'skyblock-test',
        name: 'Skyblock 测试',
        path: `${instancesDir}\\skyblock-test`,
        game_dir: `${instancesDir}\\skyblock-test\\minecraft`,
        group: '',
        minecraft: '1.21.5',
        modloader: 'neoforge',
        modloader_version: '21.5.6-beta',
        error: '',
    },
    {
        id: 'survival',
        name: '生存服务器',
        path: `${instancesDir}\\survival`,
        game_dir: `${instancesDir}\\survival\\minecraft`,
        group: '服务器',
        minecraft: '1.20.1',
        modloader: 'forge',
        modloader_version: '47.3.0',
        error: '',
    },
]

const links: LinkView[] = [
    {
        project: 'Project-Collapse',
        project_path: 'C:\\mock\\projects\\Project-Collapse',
        instance_id: 'collapse-dev',
        instance_name: 'Project-Collapse 开发',
        instance_path: `${instancesDir}\\collapse-dev`,
        instance_valid: true,
    },
    {
        project: 'Skyblock-Rework',
        project_path: 'C:\\mock\\projects\\Skyblock-Rework',
        instance_id: 'skyblock-test',
        instance_name: 'Skyblock 测试',
        instance_path: `${instancesDir}\\skyblock-test`,
        instance_valid: true,
    },
]

// —— 目录级同步（packgradle.toml dir_links）——
const dirLinks: DirLinkView[] = [
    {
        project: 'Project-Collapse',
        instance: 'collapse-dev',
        project_dir: 'mods',
        instance_dir: 'mods',
        mode: 'files',
        files: ['fabric-api-0.115.0.jar', 'sodium-0.5.11.jar', 'lithium-0.13.1.jar'],
        project_exists: true,
        instance_exists: true,
    },
    {
        project: 'Project-Collapse',
        instance: 'collapse-dev',
        project_dir: 'config',
        instance_dir: 'config',
        mode: '',
        files: null,
        project_exists: true,
        instance_exists: true,
    },
    {
        project: 'Project-Collapse',
        instance: 'collapse-dev',
        project_dir: 'kubejs',
        instance_dir: 'kubejs',
        mode: '',
        files: null,
        project_exists: true,
        instance_exists: true,
    },
    {
        project: 'Project-Collapse',
        instance: 'collapse-dev',
        project_dir: 'defaultconfigs',
        instance_dir: 'defaultconfigs',
        mode: '',
        files: null,
        project_exists: true,
        instance_exists: false,
    },
    {
        project: 'Skyblock-Rework',
        instance: 'skyblock-test',
        project_dir: 'config',
        instance_dir: 'config',
        mode: '',
        files: null,
        project_exists: true,
        instance_exists: true,
    },
]

// —— 项目根下的目录（添加同步时的候选来源）——
const projectDirs: Record<string, string[]> = {
    'Project-Collapse': ['config', 'defaultconfigs', 'kubejs', 'mods', 'scripts'],
    'Skyblock-Rework': ['config', 'mods'],
}

// 文件级同步候选：项目侧 / 实例侧文件清单
const projectDirFiles: Record<string, string[]> = {
    'Project-Collapse/mods': [
        'fabric-api-0.115.0.jar',
        'sodium-0.5.11.jar',
        'lithium-0.13.1.jar',
        'cloth-config-15.0.127.jar',
        'modmenu-11.0.3.jar',
    ],
    'Skyblock-Rework/mods': ['jei-19.8.4.115.jar', 'jade-15.5.2.jar'],
}

const instanceDirFiles: Record<string, string[]> = {
    'Project-Collapse/mods': [
        'fabric-api-0.116.0.jar',
        'sodium-0.6.0.jar',
        'lithium-0.13.1.jar',
        'krypton-0.2.8.jar',
        'dynamic-fps-3.7.1.jar',
    ],
    'Skyblock-Rework/mods': ['jei-19.8.4.115.jar', 'jade-15.5.2.jar', 'ae2-19.4.4.jar'],
}

// —— mods 元数据差异（项目 index.toml vs 实例 .index）——
const metaDiff: Record<string, MetaDiff> = {
    'Project-Collapse': {
        fetched_at: '',
        instance_only: ['krypton', 'dynamic-fps'],
        project_only: ['reeses-sodium-options'],
        version_diff: [
            { id: 'fabric-api', project_version: '0.115.0+1.21.1', instance_version: '0.116.0+1.21.1' },
            { id: 'sodium', project_version: '0.5.11', instance_version: '0.6.0' },
        ],
    },
}

// —— 更新检查（packwiz update 结果）——
const updateCheck: Record<string, { updates: ModUpdateInfo[]; errors: ModUpdateInfo[] }> = {
    'Project-Collapse': {
        updates: [
            {
                name: 'sodium',
                has_update: true,
                current_file: 'sodium-0.5.11.jar',
                latest_file: 'sodium-fabric-0.6.0+mc1.21.1.jar',
                error: '',
            },
            {
                name: 'fabric-api',
                has_update: true,
                current_file: 'fabric-api-0.115.0+1.21.1.jar',
                latest_file: 'fabric-api-0.116.0+1.21.1.jar',
                error: '',
            },
        ],
        errors: [
            {
                name: 'customcore',
                has_update: false,
                current_file: 'customcore-2.3.0.jar',
                latest_file: '',
                error: '{"code":"err.update.no_updater","detail":"customcore-2.3.0.jar"}',
            },
        ],
    },
}

// —— 环境杂项 ——
let apiKey = 'mock$cf-api-key-0000'
let configExists = true
let pgignore: Record<string, boolean> = { 'Project-Collapse': true }

// —— 导出可变数据库（mock 服务直接读写，返回前 clone）——
export const db = {
    tools,
    projects,
    instances,
    links,
    dirLinks,
    projectDirs,
    projectDirFiles,
    instanceDirFiles,
    metaDiff,
    updateCheck,
    get apiKey() {
        return apiKey
    },
    set apiKey(v: string) {
        apiKey = v
    },
    get instancesDir() {
        return instancesDir
    },
    set instancesDir(v: string) {
        instancesDir = v
    },
    get configExists() {
        return configExists
    },
    set configExists(v: boolean) {
        configExists = v
    },
    pgignore,
}

// findProject / findInstance / findLink / findDirLinks 等便捷查找
export function findProject(name: string): PackProject | undefined {
    return projects.find(p => p.name === name)
}

export function findInstance(id: string): Instance | undefined {
    return instances.find(i => i.id === id)
}

export function findLink(project: string): LinkView | undefined {
    return links.find(l => l.project === project)
}

// LinkResult 构造助手
export function linkResult(name: string, isDir: boolean, status: string, detail = ''): LinkResult {
    return { name, is_dir: isDir, status, detail }
}
