// CurseForge 相关的展示工具与常量映射
import type { ModInfo } from '../../bindings/packgradle/internal/packwiz'

// modloader 标签与颜色（产品专名不翻译）
export const loaderChips: Record<string, { label: string; color: string }> = {
    fabric: { label: 'Fabric', color: 'orange' },
    forge: { label: 'Forge', color: 'green' },
    neoforge: { label: 'NeoForge', color: 'blue' },
    quilt: { label: 'Quilt', color: 'pink' },
    liteloader: { label: 'LiteLoader', color: 'teal' },
}

// side 颜色（文案由 i18n 的 side.* 键渲染，这里只补颜色）
export const sideColors: Record<string, string> = {
    client: 'blue',
    server: 'orange',
    both: 'green',
}

// 是否 CurseForge 源 mod
export function isCfMod(mod: ModInfo): boolean {
    return (mod.cf_project_id ?? 0) > 0 && (mod.cf_file_id ?? 0) > 0
}

// releaseType → 翻译键（1=正式版 2=测试版 3=Alpha），模板用 $t() 渲染
export function cfReleaseKey(t: number): string {
    return t === 1 ? 'cf.release.stable' : t === 2 ? 'cf.release.beta' : t === 3 ? 'cf.release.alpha' : ''
}

// ISO 时间 → yyyy-MM-dd
export function cfDateText(iso: string): string {
    if (!iso) return ''
    const d = new Date(iso)
    if (isNaN(d.getTime())) return ''
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
