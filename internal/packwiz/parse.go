package packwiz

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/errs"

	"github.com/BurntSushi/toml"
)

// ParseProject 解析 pack.toml 并扫描 mod 列表
func ParseProject(packToml string) (PackProject, error) {
	var raw struct {
		Name       string            `toml:"name"`
		Author     string            `toml:"author"`
		Version    string            `toml:"version"`
		PackFormat string            `toml:"pack-format"`
		Versions   map[string]string `toml:"versions"`
		Index      struct {
			File string `toml:"file"` // meta 文件索引（index.toml）文件名，默认 index.toml
		} `toml:"index"`
	}
	if _, err := toml.DecodeFile(packToml, &raw); err != nil {
		return PackProject{}, errs.NewDetail("err.toml.parse", err.Error(), packToml)
	}
	if raw.Name == "" {
		return PackProject{}, errs.New("err.toml.missing_name", packToml)
	}

	proj := PackProject{
		Name:       raw.Name,
		Author:     raw.Author,
		Version:    raw.Version,
		PackFormat: raw.PackFormat,
		Path:       filepath.Dir(packToml),
		PackToml:   packToml,
	}
	proj.Minecraft = raw.Versions["minecraft"]
	// 常见 modloader 版本条目（键名即 loader 名）
	for _, loader := range []string{"fabric", "forge", "neoforge", "quilt", "liteloader"} {
		if v, ok := raw.Versions[loader]; ok {
			proj.Modloader = loader
			proj.ModloaderVersion = v
			break
		}
	}

	indexName := raw.Index.File
	if indexName == "" {
		indexName = "index.toml"
	}
	mods, err := scanMods(proj.Path, indexName)
	if err != nil {
		proj.Error = err.Error()
	} else {
		proj.Mods = mods
	}
	return proj, nil
}

// modTomlFields 是单个 mod 元数据文件（.pw.toml / pw.toml）中的公共字段。
// packwiz 通常不把版本写在顶层，而是存在 [update.<来源>] 表中
type modTomlFields struct {
	Name     string                    `toml:"name"`
	Filename string                    `toml:"filename"`
	Side     string                    `toml:"side"`
	Version  string                    `toml:"version"`
	Update   map[string]map[string]any `toml:"update"`
}

// UpdateVersion 从 [update.<来源>] 表中提取 mod 版本号。
// 按来源优先级取第一个非空 version；curseforge 表只有 file-id/project-id，没有版本。
// service 层解析实例侧 pw.toml 版本时复用同一优先级链（保持两端口径一致）。
func UpdateVersion(update map[string]map[string]any) string {
	for _, src := range []string{"modrinth", "fabric", "forge", "neoforge", "quilt", "liteloader", "curseforge"} {
		if v, ok := update[src]["version"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// CfIDsFromUpdate 从 [update.curseforge] 表提取 project-id / file-id
func CfIDsFromUpdate(update map[string]map[string]any) (int64, int64) {
	cf, ok := update["curseforge"]
	if !ok {
		return 0, 0
	}
	var projectID, fileID int64
	if v, ok := cf["project-id"].(int64); ok {
		projectID = v
	}
	if v, ok := cf["file-id"].(int64); ok {
		fileID = v
	}
	return projectID, fileID
}

// scanMods 扫描项目的 mod 列表：优先以 pack 根目录的 index.toml（meta 文件索引）
// 为权威来源，按其 [[files]] 中 mods/ 前缀的条目在 mods 目录下找到对应文件解析；
// 无 index.toml 时回退到旧式目录扫描
func scanMods(projectDir, indexName string) ([]ModInfo, error) {
	indexPath := filepath.Join(projectDir, filepath.FromSlash(indexName))
	if _, err := os.Stat(indexPath); err != nil {
		if os.IsNotExist(err) {
			return scanModsLegacy(projectDir) // 旧式结构：mods/<name>/pw.toml
		}
		return nil, errs.NewDetail("err.toml.read", err.Error(), indexName)
	}

	var idx struct {
		Files []struct {
			File string `toml:"file"`
		} `toml:"files"`
	}
	if _, err := toml.DecodeFile(indexPath, &idx); err != nil {
		return nil, errs.NewDetail("err.toml.parse", err.Error(), indexName)
	}

	mods := []ModInfo{}
	for _, f := range idx.Files {
		if !strings.HasPrefix(f.File, "mods/") {
			continue // 只关注 mods 目录下的条目
		}
		mods = append(mods, scanIndexEntry(projectDir, f.File))
	}
	sortMods(mods)
	return mods, nil
}

// scanIndexEntry 按 index.toml 中的一条 mods/ 条目解析对应文件为 ModInfo。
// 条目中的文件缺失或解析失败时保留条目并以文件名展示，保证列表完整。
func scanIndexEntry(projectDir, relPath string) ModInfo {
	absPath := filepath.Join(projectDir, filepath.FromSlash(relPath))
	relName := filepath.Base(relPath)
	// packwiz 的 mod 元数据约定为 <id>.pw.toml，ID 取去掉后缀的文件名
	id := relName
	if strings.HasSuffix(strings.ToLower(id), ".pw.toml") {
		id = strings.TrimSuffix(id, ".pw.toml")
	} else {
		id = strings.TrimSuffix(id, filepath.Ext(id))
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		// 索引有条目但文件缺失：保留条目，提示元数据文件丢失
		return ModInfo{ID: id, Name: id, File: relName}
	}

	var raw modTomlFields
	if err := toml.Unmarshal(content, &raw); err == nil {
		name := raw.Name
		if name == "" {
			name = id
		}
		version := raw.Version
		if version == "" {
			version = UpdateVersion(raw.Update)
		}
		side := normalizeSide(raw.Side)
		cfProjectID, cfFileID := CfIDsFromUpdate(raw.Update)
		return ModInfo{
			ID:          id,
			Name:        name,
			Side:        side,
			Version:     version,
			File:        raw.Filename,
			Path:        absPath,
			CfProjectID: cfProjectID,
			CfFileID:    cfFileID,
		}
	}

	// 非 TOML 文件（如直接放入的 jar）或元数据解析失败：以文件名展示
	return ModInfo{ID: id, Name: id, File: relName}
}

// scanModsLegacy 扫描旧式项目结构 mods/<name>/pw.toml（无 index.toml 时回退）
func scanModsLegacy(projectDir string) ([]ModInfo, error) {
	modsDir := filepath.Join(projectDir, "mods")
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ModInfo{}, nil // 项目还没有 mods 目录
		}
		return nil, errs.NewDetail("err.toml.mods_dir", err.Error())
	}

	mods := []ModInfo{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pw := filepath.Join(modsDir, e.Name(), "pw.toml")
		var raw modTomlFields
		if _, err := toml.DecodeFile(pw, &raw); err != nil {
			continue // 非 packwiz mod 目录，跳过
		}
		version := raw.Version
		if version == "" {
			version = UpdateVersion(raw.Update)
		}
		mods = append(mods, ModInfo{
			ID:      e.Name(),
			Name:    raw.Name,
			Side:    normalizeSide(raw.Side),
			Version: version,
			File:    raw.Filename,
			Path:    pw,
		})
	}
	sortMods(mods)
	return mods, nil
}

// sortMods 按名称不区分大小写排序 mod 列表
func sortMods(mods []ModInfo) {
	sort.Slice(mods, func(i, j int) bool {
		return strings.ToLower(mods[i].Name) < strings.ToLower(mods[j].Name)
	})
}

// normalizeSide 将 packwiz 的 side 值归一化（client/server/both），
// 中文标签由前端按翻译键渲染
func normalizeSide(side string) string {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "client":
		return "client"
	case "server":
		return "server"
	case "both", "universal", "":
		return "both"
	default:
		return side
	}
}
