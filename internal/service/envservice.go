package service

import (
	"os"
	"strings"

	"packgradle/internal/appconfig"
	"packgradle/internal/envutil"
	"packgradle/internal/errs"
)

// EnvService 负责检测 packwiz / Prism Launcher 的装载状态，
// 并将工具所在目录写入用户级 PATH。
//
// config.toml 是工具路径的唯一持久化来源：无论是用户手动输入
// 还是程序自动检测到的路径，都会写入 config.toml，方便用户
// 随时查看与修改。
type EnvService struct {
	config *appconfig.ConfigManager
}

func NewEnvService(config *appconfig.ConfigManager) *EnvService {
	return &EnvService{config: config}
}

// Detect 检测两个工具的装载状态
func (s *EnvService) Detect() []ToolInfo {
	packwiz := s.detectPackwiz()
	prism := s.detectPrism()
	return []ToolInfo{packwiz, prism}
}

// Configure 将检测到的工具所在目录写入用户级 PATH（幂等），
// 返回配置后的最新检测结果与实际新增的目录列表（由前端渲染提示文案）
func (s *EnvService) Configure() ([]ToolInfo, []string, error) {
	tools := s.Detect()
	dirs := []string{}
	for _, t := range tools {
		if t.Found && t.EnvDir != "" {
			dirs = append(dirs, t.EnvDir)
		}
	}
	if len(dirs) == 0 {
		return tools, nil, nil
	}

	added, err := envutil.AddDirsToUserPath(dirs)
	if err != nil {
		return tools, nil, errs.NewDetail("err.env.write_user_path", err.Error())
	}

	// 更新当前进程环境变量，让本次会话内的子进程（如 packwiz 调用）立即生效
	if len(added) > 0 {
		os.Setenv("Path", envutil.JoinPathWith(added, os.Getenv("Path")))
	}
	return s.Detect(), added, nil
}

// SetToolPath 保存用户手动指定的工具路径（空串清除），返回最新检测结果
func (s *EnvService) SetToolPath(name, path string) ([]ToolInfo, error) {
	if err := s.config.SetToolPath(name, strings.TrimSpace(path)); err != nil {
		return nil, err
	}
	return s.Detect(), nil
}

// GetApiKey 返回已保存的 CurseForge API Key（未配置时为空串）
func (s *EnvService) GetApiKey() string {
	return s.config.Get().CurseforgeApiKey
}

// SetApiKey 保存用户填写的 CurseForge API Key（空串清除）
func (s *EnvService) SetApiKey(key string) error {
	return s.config.SetApiKey(strings.TrimSpace(key))
}
