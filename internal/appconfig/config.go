package appconfig

import (
	"os"
	"path/filepath"
	"sync"

	"packgradle/internal/errs"
)

// ProjectEntry 是持久化在配置文件中的一个 packwiz 项目
type ProjectEntry struct {
	Name string `toml:"name"`
	Path string `toml:"path"` // pack.toml 所在目录
}

// ProjectLink 是「packwiz 项目 → Prism 实例」的对应关系（一项目一实例）
type ProjectLink struct {
	Project  string `toml:"project"`  // packwiz 项目名
	Instance string `toml:"instance"` // Prism 实例 ID（实例目录名）
}

// DirLink 是一个「项目目录 ↔ 实例目录」的同步关联对（junction 目标，如 config/kubejs）
type DirLink struct {
	Project     string `toml:"project"`     // packwiz 项目名
	Instance    string `toml:"instance"`    // Prism 实例 ID
	ProjectDir  string `toml:"project_dir"` // 项目根下目录名（如 config）
	InstanceDir string `toml:"instance_dir"` // 实例游戏目录（minecraft/）下相对路径（默认与 ProjectDir 同名）
}

// Config 是持久化在 %AppData%\PackGradle\config.toml 中的应用配置
type Config struct {
	// 用户手动指定的工具路径（覆盖自动检测）
	PackwizPath string         `toml:"packwiz_path"`
	PrismPath   string         `toml:"prism_path"`
	Projects    []ProjectEntry `toml:"projects"`
	// 用户手动指定的 Prism 实例根目录（覆盖自动定位，空串 = 自动检测）
	PrismInstancesPath string `toml:"prism_instances_path"`
	// 程序自动检测到的 Prism 实例根目录（回写持久化，供查看/修改）
	PrismInstancesDir string `toml:"prism_instances_dir"`
	// packwiz 项目 ↔ Prism 实例 关联
	Links []ProjectLink `toml:"links"`
	// 项目目录 ↔ 实例目录 同步关联对（junction）
	DirLinks []DirLink `toml:"dir_links"`
	// 用户自行填写的 CurseForge API Key（用于按需查询 mod 版本等）
	CurseforgeApiKey string `toml:"curseforge_api_key"`
}

// ConfigManager 负责配置文件的读写，所有服务共享同一实例
type ConfigManager struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// NewConfigManager 在用户配置目录（%AppData%\PackGradle）创建配置管理器并加载已有配置
func NewConfigManager() (*ConfigManager, error) {
	dir, err := os.UserConfigDir() // Windows 下为 %AppData%
	if err != nil {
		return nil, errs.NewDetail("err.config.user_dir", err.Error())
	}
	dir = filepath.Join(dir, "PackGradle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, errs.NewDetail("err.config.mkdir", err.Error(), dir)
	}
	m := &ConfigManager{path: filepath.Join(dir, "config.toml")}
	if err := ReadToml(m.path, &m.cfg); err != nil {
		return nil, errs.NewDetail("err.config.read", err.Error())
	}
	return m, nil
}

// NewConfigManagerAt 用指定路径构造配置管理器（不读取磁盘），供测试注入
func NewConfigManagerAt(path string) *ConfigManager {
	return &ConfigManager{path: path}
}

// Get 返回当前配置的快照
func (m *ConfigManager) Get() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// save 将当前配置写回磁盘。
// 注意：内部不加锁，调用方（SetToolPath/AddProject/RemoveProject）须已持有 m.mu。
func (m *ConfigManager) save() error {
	return WriteTomlAtomic(m.path, m.cfg)
}

// SetToolPath 保存用户手动指定的工具路径；传空串则清除自定义路径
func (m *ConfigManager) SetToolPath(tool, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch tool {
	case "packwiz":
		m.cfg.PackwizPath = path
	case "prism-launcher":
		m.cfg.PrismPath = path
	default:
		return errs.New("err.config.unknown_tool", tool)
	}
	return m.save()
}

// AddProject 添加项目；同名项目则更新路径
func (m *ConfigManager) AddProject(entry ProjectEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Projects {
		if m.cfg.Projects[i].Name == entry.Name {
			m.cfg.Projects[i].Path = entry.Path
			return m.save()
		}
	}
	m.cfg.Projects = append(m.cfg.Projects, entry)
	return m.save()
}

// RemoveProject 按名称移除项目
func (m *ConfigManager) RemoveProject(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.cfg.Projects[:0]
	for _, p := range m.cfg.Projects {
		if p.Name != name {
			out = append(out, p)
		}
	}
	m.cfg.Projects = out
	return m.save()
}

// FindProject 按名称查找项目，返回项目条目
func (m *ConfigManager) FindProject(name string) (ProjectEntry, bool) {
	for _, p := range m.Get().Projects {
		if p.Name == name {
			return p, true
		}
	}
	return ProjectEntry{}, false
}

// SetApiKey 保存 CurseForge API Key；传空串则清除
func (m *ConfigManager) SetApiKey(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.CurseforgeApiKey = key
	return m.save()
}

// SetPrismInstancesPath 保存用户手动指定的 Prism 实例根目录；传空串则清除（恢复自动定位）
func (m *ConfigManager) SetPrismInstancesPath(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.PrismInstancesPath = path
	return m.save()
}

// SetPrismInstancesDir 保存程序自动检测到的实例根目录（值未变化时跳过写入）
func (m *ConfigManager) SetPrismInstancesDir(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.PrismInstancesDir == dir {
		return nil
	}
	m.cfg.PrismInstancesDir = dir
	return m.save()
}

// SetLink 保存项目 → 实例关联（同名项目覆盖，一项目一实例）
func (m *ConfigManager) SetLink(link ProjectLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Links {
		if m.cfg.Links[i].Project == link.Project {
			m.cfg.Links[i] = link
			return m.save()
		}
	}
	m.cfg.Links = append(m.cfg.Links, link)
	return m.save()
}

// RemoveLink 按项目名解除关联（不存在时静默成功）
func (m *ConfigManager) RemoveLink(project string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.cfg.Links[:0]
	for _, l := range m.cfg.Links {
		if l.Project != project {
			out = append(out, l)
		}
	}
	m.cfg.Links = out
	return m.save()
}

// FindLink 按项目名查找关联
func (m *ConfigManager) FindLink(project string) (ProjectLink, bool) {
	for _, l := range m.Get().Links {
		if l.Project == project {
			return l, true
		}
	}
	return ProjectLink{}, false
}

// AddDirLink 添加目录关联对（同项目同目录名时覆盖）
func (m *ConfigManager) AddDirLink(link DirLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.DirLinks {
		if m.cfg.DirLinks[i].Project == link.Project && m.cfg.DirLinks[i].ProjectDir == link.ProjectDir {
			m.cfg.DirLinks[i] = link
			return m.save()
		}
	}
	m.cfg.DirLinks = append(m.cfg.DirLinks, link)
	return m.save()
}

// RemoveDirLink 移除目录关联对
func (m *ConfigManager) RemoveDirLink(project, projectDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.cfg.DirLinks[:0]
	for _, l := range m.cfg.DirLinks {
		if l.Project == project && l.ProjectDir == projectDir {
			continue
		}
		out = append(out, l)
	}
	m.cfg.DirLinks = out
	return m.save()
}

// FindDirLinks 返回某项目的全部目录关联对
func (m *ConfigManager) FindDirLinks(project string) []DirLink {
	var out []DirLink
	for _, l := range m.Get().DirLinks {
		if l.Project == project {
			out = append(out, l)
		}
	}
	return out
}
