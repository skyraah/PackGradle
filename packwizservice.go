package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
)

// ModInfo 描述 packwiz 项目中的一个 mod（对应 index.toml 中 mods/ 条目指向的文件）
type ModInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Side    string `json:"side"`     // client / server / both
	SideCN  string `json:"side_cn"`  // 中文标签
	Version string `json:"version"`  // pw.toml 中的 version（不一定存在）
	File    string `json:"file"`     // 下载文件名
	Path    string `json:"path"`     // pw.toml 完整路径
	// CurseForge 源信息（0 表示非 CurseForge 源）
	CfProjectID int64 `json:"cf_project_id"`
	CfFileID    int64 `json:"cf_file_id"`
	// 本地缓存的 CurseForge 文件信息（获取后填充）
	CfVersion     string `json:"cf_version"`      // displayName（版本）
	CfFileDate    string `json:"cf_file_date"`    // 发布日期
	CfReleaseType int    `json:"cf_release_type"` // 1=正式版 2=测试版 3=Alpha
}

// PackProject 描述一个已导入的 packwiz 项目
type PackProject struct {
	Name             string    `json:"name"`
	Path             string    `json:"path"`       // pack.toml 所在目录
	PackToml         string    `json:"pack_toml"`  // pack.toml 完整路径
	Version          string    `json:"version"`
	Author           string    `json:"author"`
	PackFormat       string    `json:"pack_format"`
	Minecraft        string    `json:"minecraft"`
	Modloader        string    `json:"modloader"`          // fabric / forge / neoforge / quilt ...
	ModloaderVersion string    `json:"modloader_version"`
	Mods             []ModInfo `json:"mods"`
	Error            string    `json:"error"` // 解析失败时的原因
}

// RefreshResult 是 packwiz CLI 执行结果
type RefreshResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// PackwizService 负责 packwiz 项目的导入、解析与管理
type PackwizService struct {
	config *ConfigManager
}

func NewPackwizService(config *ConfigManager) *PackwizService {
	return &PackwizService{config: config}
}

// ImportProject 导入一个 pack.toml 并返回解析结果（同名项目会覆盖路径）
func (s *PackwizService) ImportProject(packTomlPath string) (PackProject, error) {
	abs, err := filepath.Abs(packTomlPath)
	if err != nil {
		return PackProject{}, fmt.Errorf("无法解析路径 %s: %w", packTomlPath, err)
	}
	proj, err := parsePackToml(abs)
	if err != nil {
		return PackProject{}, err
	}
	s.applyCfCache(&proj)
	if err := s.config.AddProject(ProjectEntry{Name: proj.Name, Path: proj.Path}); err != nil {
		return PackProject{}, err
	}
	return proj, nil
}

// ListProjects 返回所有已导入项目的解析结果
func (s *PackwizService) ListProjects() []PackProject {
	entries := s.config.Get().Projects
	projects := make([]PackProject, 0, len(entries))
	for _, e := range entries {
		proj, err := parsePackToml(filepath.Join(e.Path, "pack.toml"))
		if err != nil {
			projects = append(projects, PackProject{
				Name:  e.Name,
				Path:  e.Path,
				Error: err.Error(),
			})
			continue
		}
		s.applyCfCache(&proj)
		projects = append(projects, proj)
	}
	return projects
}

// RemoveProject 按名称移除项目，返回剩余项目列表
func (s *PackwizService) RemoveProject(name string) []PackProject {
	_ = s.config.RemoveProject(name)
	return s.ListProjects()
}

// RefreshProject 在项目目录执行 `packwiz refresh` 并返回输出
func (s *PackwizService) RefreshProject(name string) RefreshResult {
	projectDir := ""
	for _, p := range s.config.Get().Projects {
		if p.Name == name {
			projectDir = p.Path
			break
		}
	}
	if projectDir == "" {
		return RefreshResult{OK: false, Output: "未找到项目: " + name}
	}

	packwiz, err := s.findPackwiz()
	if err != nil {
		return RefreshResult{OK: false, Output: err.Error()}
	}
	cmd := exec.Command(packwiz, "refresh")
	cmd.Dir = projectDir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // GUI 程序下隐藏子进程控制台窗口
	out, err := cmd.CombinedOutput()
	return RefreshResult{OK: err == nil, Output: strings.TrimSpace(string(out))}
}

// findPackwiz 返回 packwiz 可执行文件路径：自定义路径优先，否则在 PATH 中查找
func (s *PackwizService) findPackwiz() (string, error) {
	if p := strings.TrimSpace(s.config.Get().PackwizPath); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath("packwiz"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("未找到 packwiz，请先在「环境配置」中配置")
}

// parsePackToml 解析 pack.toml 并扫描 mod 列表
func parsePackToml(packToml string) (PackProject, error) {
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
		return PackProject{}, fmt.Errorf("解析 %s 失败: %w", packToml, err)
	}
	if raw.Name == "" {
		return PackProject{}, fmt.Errorf("%s 中缺少 name 字段", packToml)
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

// updateVersion 从 [update.<来源>] 表中提取 mod 版本号。
// 按来源优先级取第一个非空 version；curseforge 表只有 file-id/project-id，没有版本
func updateVersion(update map[string]map[string]any) string {
	for _, src := range []string{"modrinth", "fabric", "forge", "neoforge", "quilt", "liteloader", "curseforge"} {
		if v, ok := update[src]["version"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// cfIDsFromUpdate 从 [update.curseforge] 表提取 project-id / file-id
func cfIDsFromUpdate(update map[string]map[string]any) (int64, int64) {
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

// cfCacheKey 生成 CurseForge 文件缓存键
func cfCacheKey(projectID, fileID int64) string {
	return fmt.Sprintf("%d:%d", projectID, fileID)
}

// cfCacheStore 返回项目对应的缓存存储（<项目目录>/.cache）
func (s *PackwizService) cfCacheStore(proj PackProject) *CfCacheStore {
	return NewCfCacheStore(filepath.Join(proj.Path, ".cache"))
}

// applyCfCache 将项目缓存的 CurseForge 文件信息回填到 mod 列表
func (s *PackwizService) applyCfCache(proj *PackProject) {
	cache, err := s.cfCacheStore(*proj).Load()
	if err != nil {
		return // 缓存不可读时静默（仅影响版本显示）
	}
	for i := range proj.Mods {
		m := &proj.Mods[i]
		if m.CfProjectID == 0 || m.CfFileID == 0 {
			continue
		}
		entry, ok := cache[cfCacheKey(m.CfProjectID, m.CfFileID)]
		if !ok {
			continue
		}
		m.CfVersion = entry.DisplayName
		m.CfFileDate = entry.FileDate
		m.CfReleaseType = entry.ReleaseType
	}
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
		return nil, fmt.Errorf("读取 %s 失败: %w", indexName, err)
	}

	var idx struct {
		Files []struct {
			File string `toml:"file"`
		} `toml:"files"`
	}
	if _, err := toml.DecodeFile(indexPath, &idx); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", indexName, err)
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
			version = updateVersion(raw.Update)
		}
		side, sideCN := normalizeSide(raw.Side)
		cfProjectID, cfFileID := cfIDsFromUpdate(raw.Update)
		return ModInfo{
			ID:          id,
			Name:        name,
			Side:        side,
			SideCN:      sideCN,
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
		return nil, fmt.Errorf("读取 mods 目录失败: %w", err)
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
			version = updateVersion(raw.Update)
		}
		side, sideCN := normalizeSide(raw.Side)
		mods = append(mods, ModInfo{
			ID:      e.Name(),
			Name:    raw.Name,
			Side:    side,
			SideCN:  sideCN,
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

// normalizeSide 将 packwiz 的 side 值归一化为中文标签
func normalizeSide(side string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "client":
		return "client", "客户端"
	case "server":
		return "server", "服务端"
	case "both", "universal", "":
		return "both", "通用"
	default:
		return side, side
	}
}
