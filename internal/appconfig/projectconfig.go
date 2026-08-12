package appconfig

import (
	"path/filepath"

	"packgradle/internal/errs"
)

// 项目级配置：每个 packwiz 项目目录下的 packgradle.toml。
// 与项目相关的配置（Prism 实例关联、目录同步关联）随项目走，
// 全局 config.toml 只保留全局项（工具路径、项目索引、API Key 等），
// 避免实例一多时全局配置数据混乱。

// ProjectConfig 是持久化在 <项目目录>/packgradle.toml 的项目级配置
type ProjectConfig struct {
	// 关联的 Prism 实例 ID（实例目录名；空串 = 未关联）
	Instance string `toml:"instance"`
	// 目录同步关联对（junction 目标）
	DirLinks []ProjectDirLink `toml:"dir_links"`
	// 一键关联建立的顶层文件硬链接（相对项目根，如 "modlist.txt"）
	FileLinks []string `toml:"file_links"`
}

// ProjectDirLink 是一条「项目目录 ↔ 实例游戏目录」的同步关联
type ProjectDirLink struct {
	ProjectDir  string `toml:"project_dir"`  // 相对项目根（如 config）
	InstanceDir string `toml:"instance_dir"` // 相对实例游戏目录（minecraft/），默认与 ProjectDir 同名
}

// ProjectConfigPath 返回项目级配置文件路径（与 pack.toml 同目录）
func ProjectConfigPath(projectPath string) string {
	return filepath.Join(projectPath, "packgradle.toml")
}

// LoadProjectConfig 读取项目级配置；文件不存在时返回零值（未关联）
func LoadProjectConfig(projectPath string) (ProjectConfig, error) {
	var cfg ProjectConfig
	if err := ReadToml(ProjectConfigPath(projectPath), &cfg); err != nil {
		return ProjectConfig{}, errs.NewDetail("err.config.read", err.Error())
	}
	return cfg, nil
}

// SaveProjectConfig 原子写项目级配置
func SaveProjectConfig(projectPath string, cfg ProjectConfig) error {
	if err := WriteTomlAtomic(ProjectConfigPath(projectPath), cfg); err != nil {
		return errs.NewDetail("err.file.save", err.Error(), ProjectConfigPath(projectPath))
	}
	return nil
}
