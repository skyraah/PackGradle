package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// ModInfo 描述 packwiz 项目中的一个 mod（对应 mods/<id>/pw.toml）
type ModInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Side    string `json:"side"`     // client / server / both
	SideCN  string `json:"side_cn"`  // 中文标签
	Version string `json:"version"`  // pw.toml 中的 version（不一定存在）
	File    string `json:"file"`     // 下载文件名
	Path    string `json:"path"`     // pw.toml 完整路径
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

	mods, err := scanMods(proj.Path)
	if err != nil {
		proj.Error = err.Error()
	} else {
		proj.Mods = mods
	}
	return proj, nil
}

// scanMods 扫描项目 mods/ 目录下每个 mod 的 pw.toml
func scanMods(projectDir string) ([]ModInfo, error) {
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
		var raw struct {
			Name     string `toml:"name"`
			Filename string `toml:"filename"`
			Side     string `toml:"side"`
			Version  string `toml:"version"`
		}
		if _, err := toml.DecodeFile(pw, &raw); err != nil {
			continue // 非 packwiz mod 目录，跳过
		}
		side, sideCN := normalizeSide(raw.Side)
		mods = append(mods, ModInfo{
			ID:      e.Name(),
			Name:    raw.Name,
			Side:    side,
			SideCN:  sideCN,
			Version: raw.Version,
			File:    raw.Filename,
			Path:    pw,
		})
	}
	sort.Slice(mods, func(i, j int) bool {
		return strings.ToLower(mods[i].Name) < strings.ToLower(mods[j].Name)
	})
	return mods, nil
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
