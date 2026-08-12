package service

import (
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/envutil"
)

// ToolInfo 描述一个工具的检测结果。
// 不包含用户可见文案：检测来源（Source）由前端按翻译键渲染提示
type ToolInfo struct {
	Name   string `json:"name"`   // packwiz / prism-launcher
	Found  bool   `json:"found"`  // 是否已安装
	Path   string `json:"path"`   // 可执行文件或配置目录的完整路径
	Source string `json:"source"` // 发现来源: config / env / path / default-dir
	EnvDir string `json:"env_dir"` // 需要加入 PATH 的目录（无可加目录时为空）
	EnvOK  bool   `json:"env_ok"`  // 该目录是否已在用户 PATH 中
}

// detectPackwiz 检测 packwiz：统一查找链（config → PACKWIZ → PATH）+
// 默认目录 %USERPROFILE%\go\bin（go install 的默认安装位置）
func (s *EnvService) detectPackwiz() ToolInfo {
	info := ToolInfo{Name: "packwiz"}
	cfg := s.config.Get()
	goBin := filepath.Join(os.Getenv("USERPROFILE"), "go", "bin")
	if path, source, ok := envutil.FindExecutable(cfg.PackwizPath, "packwiz", "PACKWIZ", goBin); ok {
		info.Path, info.Source = path, source
	}
	s.finishDetection(&info, cfg.PackwizPath)
	return info
}

// detectPrism 检测 prism-launcher：统一查找链（config → PRISM）+
// 默认安装位置（见 prismCandidateDirs）
func (s *EnvService) detectPrism() ToolInfo {
	info := ToolInfo{Name: "prism-launcher"}
	cfg := s.config.Get()
	if path, source, ok := envutil.FindExecutable(cfg.PrismPath, "prismlauncher", "PRISM", prismCandidateDirs()...); ok {
		info.Path, info.Source = path, source
	}
	s.finishDetection(&info, cfg.PrismPath)
	return info
}

// prismCandidateDirs 列出 Prism Launcher 的候选安装目录：
// %LOCALAPPDATA%\Programs\PrismLauncher、Program Files / Program Files (x86)
// 下的 PrismLauncher 目录，含 Program Files 的浅层扫描（目录名可能带空格或大小写不同）
func prismCandidateDirs() []string {
	dirs := []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "PrismLauncher"),
		filepath.Join(os.Getenv("ProgramFiles"), "PrismLauncher"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "PrismLauncher"),
	}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && strings.Contains(strings.ToLower(e.Name()), "prism") {
				dirs = append(dirs, filepath.Join(root, e.Name()))
			}
		}
	}
	return dirs
}

// finishDetection 处理检测结果：将找到的路径写入 config.toml、
// 填充 PATH 状态。config 中已保存的有效路径不会被覆盖。
func (s *EnvService) finishDetection(info *ToolInfo, savedPath string) {
	if info.Path != "" {
		info.Found = true
		info.EnvDir = filepath.Dir(info.Path)
		info.EnvOK = envutil.InUserPath(info.EnvDir)
		// 程序检测到的路径同样持久化，方便用户查看/修改；
		// 值未变化时跳过写入。
		if strings.TrimSpace(savedPath) != info.Path {
			_ = s.config.SetToolPath(info.Name, info.Path)
		}
	}
}
