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

// Config 是持久化在 %AppData%\PackGradle\config.toml 中的应用配置
type Config struct {
	// 用户手动指定的工具路径（覆盖自动检测）
	PackwizPath string         `toml:"packwiz_path"`
	PrismPath   string         `toml:"prism_path"`
	Projects    []ProjectEntry `toml:"projects"`
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
