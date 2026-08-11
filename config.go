package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// ProjectEntry 是持久化在配置文件中的一个 packwiz 项目
type ProjectEntry struct {
	Name string `toml:"name"`
	Path string `toml:"path"` // pack.toml 所在目录
}

// appConfig 持久化在 %AppData%\PackGradle\config.toml
type appConfig struct {
	// 用户手动指定的工具路径（覆盖自动检测）
	PackwizPath string         `toml:"packwiz_path"`
	PrismPath   string         `toml:"prism_path"`
	Projects    []ProjectEntry `toml:"projects"`
}

// ConfigManager 负责配置文件的读写，所有服务共享同一实例
type ConfigManager struct {
	mu   sync.Mutex
	path string
	cfg  appConfig
}

func NewConfigManager() (*ConfigManager, error) {
	dir, err := os.UserConfigDir() // Windows 下为 %AppData%
	if err != nil {
		return nil, fmt.Errorf("无法获取用户配置目录: %w", err)
	}
	dir = filepath.Join(dir, "PackGradle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建配置目录 %s: %w", dir, err)
	}
	m := &ConfigManager{path: filepath.Join(dir, "config.toml")}
	if _, err := toml.DecodeFile(m.path, &m.cfg); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	return m, nil
}

// save 将当前配置写回磁盘
func (m *ConfigManager) save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := os.Create(m.path)
	if err != nil {
		return fmt.Errorf("无法写入配置文件: %w", err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(m.cfg); err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	return nil
}

// Get 返回当前配置的快照
func (m *ConfigManager) Get() appConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
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
		return fmt.Errorf("未知工具: %s", tool)
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
