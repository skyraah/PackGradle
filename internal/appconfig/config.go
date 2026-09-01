package appconfig

import (
	"os"
	"path/filepath"
	"sync"

	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// CodeSettingsRetentionInvalid 是保留设置越界错误码（契约 06 §10：
// {0}=字段名，整体拒绝；加载层与写入层共用，文案由前端 locale 提供）。
const CodeSettingsRetentionInvalid = "err.settings.retention_invalid"

// ProjectEntry 是持久化在配置文件中的一个 packwiz 项目
type ProjectEntry struct {
	Name string `toml:"name"`
	Path string `toml:"path"` // pack.toml 所在目录
}

// legacyLink 是旧版全局 config.toml 中的项目 ↔ 实例关联（已迁移至项目级 packgradle.toml，
// 此处仅用于一次性迁移读取）
type legacyLink struct {
	Project  string `toml:"project"`
	Instance string `toml:"instance"`
}

// legacyDirLink 是旧版全局 config.toml 中的目录同步关联（同上一并迁移）
type legacyDirLink struct {
	Project     string `toml:"project"`
	Instance    string `toml:"instance"`
	ProjectDir  string `toml:"project_dir"`
	InstanceDir string `toml:"instance_dir"`
}

// RetentionConfig 是 config.toml [retention] 段的原始承载（ADR-0007 §8，票 #57）。
// 指针字段区分「未写」（nil → 默认值）与显式写 0——preserve_max_bytes=0＝不限，
// 是合法的显式取值（ADR-0007 §7），不能与未写混淆。编码器跳过 nil 指针，
// 未配置过的段不落盘。
type RetentionConfig struct {
	KeepCommits           *int   `toml:"keep_commits"`
	KeepDays              *int   `toml:"keep_days"`
	RelationCapacityBytes *int64 `toml:"relation_capacity_bytes"`
	PreserveMaxBytes      *int64 `toml:"preserve_max_bytes"`
	TrashDays             *int   `toml:"trash_days"`
}

// Config 是持久化在 %AppData%\PackGradle\config.toml 中的应用全局配置。
// 项目相关的配置（实例关联、目录同步关联）存放于各项目目录下的 packgradle.toml，
// 不进入全局配置。
type Config struct {
	// 用户手动指定的工具路径（覆盖自动检测）
	PackwizPath string         `toml:"packwiz_path"`
	PrismPath   string         `toml:"prism_path"`
	Projects    []ProjectEntry `toml:"projects"`
	// 用户手动指定的 Prism 实例根目录（覆盖自动定位，空串 = 自动检测）
	PrismInstancesPath string `toml:"prism_instances_path"`
	// 程序自动检测到的 Prism 实例根目录（回写持久化，供查看/修改）
	PrismInstancesDir string `toml:"prism_instances_dir"`
	// 用户自行填写的 CurseForge API Key（用于按需查询 mod 版本等）
	CurseforgeApiKey string `toml:"curseforge_api_key"`
	// 保留策略设置（ADR-0007 §8；范围校验见 Retention/SetRetention）
	Retention RetentionConfig `toml:"retention"`
	// 旧版字段（v1）：项目 ↔ 实例关联与目录同步关联，启动时一次性迁移到项目级配置后清空
	LegacyLinks    []legacyLink    `toml:"links"`
	LegacyDirLinks []legacyDirLink `toml:"dir_links"`
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

// Exists 判断配置文件是否已存在于磁盘。
// 首次运行时磁盘上尚无 config.toml（只有首次保存才会写出），用于前端首次引导判定。
func (m *ConfigManager) Exists() bool {
	m.mu.Lock()
	path := m.path
	m.mu.Unlock()
	_, err := os.Stat(path)
	return err == nil
}

// EnsureCreated 将当前配置写盘（已存在时同样重写，内容为当前内存态）。
// 供首次引导完成/跳过后落一个 config.toml，下次启动不再弹出引导。
func (m *ConfigManager) EnsureCreated() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.save()
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

// Retention 读取并归一保留策略设置（加载层校验，ADR-0007 §8；契约 06 §3.6）：
// 未写键取默认值；任一键越界整体拒绝，返回 err.settings.retention_invalid
// （{0}=字段名）。满足 ports.RetentionSettingsStore（settings 应用层端口）。
func (m *ConfigManager) Retention() (model.RetentionSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Retention.materialize()
}

// SetRetention 校验后整体替换 [retention] 五键并落盘（写入层与加载层同款校验，
// 契约 06 §3.6）。任一键越界整体拒绝（不落任何键），返回 err.settings.retention_invalid。
func (m *ConfigManager) SetRetention(s model.RetentionSettings) (model.RetentionSettings, error) {
	if field, ok := model.ValidateRetention(s); !ok {
		return model.RetentionSettings{}, errs.New(CodeSettingsRetentionInvalid, field)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Retention = retentionConfigOf(s)
	if err := m.save(); err != nil {
		return model.RetentionSettings{}, err
	}
	return s, nil
}

// materialize 把原始 [retention] 段归一为设置值：nil 键取默认，越界整体拒绝。
func (r RetentionConfig) materialize() (model.RetentionSettings, error) {
	out := model.DefaultRetention()
	if r.KeepCommits != nil {
		out.KeepCommits = *r.KeepCommits
	}
	if r.KeepDays != nil {
		out.KeepDays = *r.KeepDays
	}
	if r.RelationCapacityBytes != nil {
		out.RelationCapacityBytes = *r.RelationCapacityBytes
	}
	if r.PreserveMaxBytes != nil {
		out.PreserveMaxBytes = *r.PreserveMaxBytes
	}
	if r.TrashDays != nil {
		out.TrashDays = *r.TrashDays
	}
	if field, ok := model.ValidateRetention(out); !ok {
		return model.RetentionSettings{}, errs.New(CodeSettingsRetentionInvalid, field)
	}
	return out, nil
}

// retentionConfigOf 把设置值写回原始段（五键全量非 nil，落盘显式化）。
func retentionConfigOf(s model.RetentionSettings) RetentionConfig {
	keepCommits, keepDays, trashDays := s.KeepCommits, s.KeepDays, s.TrashDays
	capacity, preserveMax := s.RelationCapacityBytes, s.PreserveMaxBytes
	return RetentionConfig{
		KeepCommits:           &keepCommits,
		KeepDays:              &keepDays,
		RelationCapacityBytes: &capacity,
		PreserveMaxBytes:      &preserveMax,
		TrashDays:             &trashDays,
	}
}

// MigrateLegacyProjectConfigs 将旧版全局 config.toml 中的 [[links]] / [[dir_links]]
// 一次性迁移到各项目目录下的 packgradle.toml，随后清空全局旧字段。
// 幂等：无旧数据时直接返回 nil；追加前对既有目录关联去重，
// 部分失败重跑不会产生重复条目。
func (m *ConfigManager) MigrateLegacyProjectConfigs() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cfg.LegacyLinks) == 0 && len(m.cfg.LegacyDirLinks) == 0 {
		return nil
	}

	handled := map[string]bool{}
	for _, l := range m.cfg.LegacyLinks {
		entry, ok := findProjectEntry(m.cfg.Projects, l.Project)
		if !ok {
			continue // 项目已不存在，跳过
		}
		pc, err := LoadProjectConfig(entry.Path)
		if err != nil {
			return err
		}
		if pc.Instance == "" {
			pc.Instance = l.Instance
		}
		for _, dl := range m.cfg.LegacyDirLinks {
			if dl.Project == l.Project {
				appendLegacyDirLink(&pc.DirLinks, ProjectDirLink{
					ProjectDir:  dl.ProjectDir,
					InstanceDir: dl.InstanceDir,
				})
			}
		}
		if err := SaveProjectConfig(entry.Path, pc); err != nil {
			return err
		}
		handled[l.Project] = true
	}
	// [[dir_links]] 中不属于任何 [[links]] 的项目也要迁移（不遗漏旧数据）
	for _, dl := range m.cfg.LegacyDirLinks {
		if handled[dl.Project] {
			continue
		}
		entry, ok := findProjectEntry(m.cfg.Projects, dl.Project)
		if !ok {
			continue
		}
		pc, err := LoadProjectConfig(entry.Path)
		if err != nil {
			return err
		}
		appendLegacyDirLink(&pc.DirLinks, ProjectDirLink{
			ProjectDir:  dl.ProjectDir,
			InstanceDir: dl.InstanceDir,
		})
		if err := SaveProjectConfig(entry.Path, pc); err != nil {
			return err
		}
		handled[dl.Project] = true
	}

	m.cfg.LegacyLinks = nil
	m.cfg.LegacyDirLinks = nil
	return m.save()
}

// appendLegacyDirLink 迁移用追加：同一 (project_dir, instance_dir) 已存在时不重复
func appendLegacyDirLink(links *[]ProjectDirLink, link ProjectDirLink) {
	for _, l := range *links {
		if l.ProjectDir == link.ProjectDir && l.InstanceDir == link.InstanceDir {
			return
		}
	}
	*links = append(*links, link)
}

// findProjectEntry 在项目列表中按名称查找
func findProjectEntry(projects []ProjectEntry, name string) (ProjectEntry, bool) {
	for _, p := range projects {
		if p.Name == name {
			return p, true
		}
	}
	return ProjectEntry{}, false
}
