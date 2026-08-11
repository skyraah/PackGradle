package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ToolInfo 描述一个工具的检测结果
type ToolInfo struct {
	Name    string `json:"name"`    // packwiz / prism-launcher
	Found   bool   `json:"found"`   // 是否已安装
	Path    string `json:"path"`    // 可执行文件或配置目录的完整路径
	Source  string `json:"source"`  // 发现来源: config / path / default-dir
	Message string `json:"message"` // 对用户的中文说明
	EnvDir  string `json:"env_dir"` // 需要加入 PATH 的目录（无可加目录时为空）
	EnvOK   bool   `json:"env_ok"`  // 该目录是否已在用户 PATH 中
}

// EnvService 负责检测 packwiz / Prism Launcher 的装载状态，
// 并将工具所在目录写入用户级 PATH。
//
// config.toml 是工具路径的唯一持久化来源：无论是用户手动输入
// 还是程序自动检测到的路径，都会写入 config.toml，方便用户
// 随时查看与修改。
type EnvService struct {
	config *ConfigManager
}

func NewEnvService(config *ConfigManager) *EnvService {
	return &EnvService{config: config}
}

// Detect 检测两个工具的装载状态
func (s *EnvService) Detect() []ToolInfo {
	packwiz := s.detectPackwiz()
	prism := s.detectPrism()
	return []ToolInfo{packwiz, prism}
}

// detectPackwiz 检测顺序：config 中保存的路径 → 环境变量(PATH) →
// 默认目录 %USERPROFILE%\go\bin；找不到则提示用户键入安装路径。
func (s *EnvService) detectPackwiz() ToolInfo {
	info := ToolInfo{Name: "packwiz"}
	cfg := s.config.Get()

	// 0. config.toml 中保存的路径（用户输入或之前检测到的）
	if p := strings.TrimSpace(cfg.PackwizPath); p != "" {
		if resolved, ok := resolveToolPath(p, "packwiz"); ok {
			info.Path = resolved
			info.Source = "config"
		}
	}

	// 1. 环境变量 PATH
	if info.Path == "" {
		if p, err := exec.LookPath("packwiz"); err == nil {
			info.Path = p
			info.Source = "path"
		}
	}

	// 2. 默认目录 %USERPROFILE%\go\bin（go install 的默认安装位置）
	if info.Path == "" {
		dir := filepath.Join(os.Getenv("USERPROFILE"), "go", "bin")
		if resolved, ok := resolveToolPath(dir, "packwiz"); ok {
			info.Path = resolved
			info.Source = "default-dir"
		}
	}

	s.finishDetection(&info, cfg.PackwizPath)
	return info
}

// detectPrism 检测顺序：config 中保存的路径 → 默认路径
// %LOCALAPPDATA%\Programs\PrismLauncher → Program Files / Program Files (x86)
// （含这两个目录下的子目录浅层扫描）；找不到则提示用户键入安装路径。
func (s *EnvService) detectPrism() ToolInfo {
	info := ToolInfo{Name: "prism-launcher"}
	cfg := s.config.Get()

	// 0. config.toml 中保存的路径（用户输入或之前检测到的）
	if p := strings.TrimSpace(cfg.PrismPath); p != "" {
		if resolved, ok := resolveToolPath(p, "prismlauncher"); ok {
			info.Path = resolved
			info.Source = "config"
		}
	}

	// 1. 默认安装路径
	if info.Path == "" {
		if p, source := detectPrismDefaultDirs(); p != "" {
			info.Path = p
			info.Source = source
		}
	}

	s.finishDetection(&info, cfg.PrismPath)
	return info
}

// finishDetection 处理检测结果：将找到的路径写入 config.toml、
// 填充提示信息。config 中已保存的有效路径不会被覆盖。
func (s *EnvService) finishDetection(info *ToolInfo, savedPath string) {
	if info.Path != "" {
		info.Found = true
		info.Message = toolMessage(info.Source, info.Path)
		info.EnvDir = filepath.Dir(info.Path)
		info.EnvOK = inUserPath(info.EnvDir)
		// 程序检测到的路径同样持久化，方便用户查看/修改；
		// 值未变化时跳过写入。
		if strings.TrimSpace(savedPath) != info.Path {
			_ = s.config.SetToolPath(info.Name, info.Path)
		}
		return
	}
	info.Message = "未检测到该工具，请键入安装路径"
}

// detectPrismDefaultDirs 在 Prism Launcher 的默认安装位置中查找
func detectPrismDefaultDirs() (string, string) {
	// 精确候选目录
	exact := []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "PrismLauncher"),
		filepath.Join(os.Getenv("ProgramFiles"), "PrismLauncher"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "PrismLauncher"),
	}
	for _, dir := range exact {
		if resolved, ok := resolveToolPath(dir, "prismlauncher"); ok {
			return resolved, "default-dir"
		}
	}

	// Program Files 下浅层扫描：目录名可能带空格或大小写不同
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.Contains(strings.ToLower(e.Name()), "prism") {
				continue
			}
			if resolved, ok := resolveToolPath(filepath.Join(root, e.Name()), "prismlauncher"); ok {
				return resolved, "default-dir"
			}
		}
	}
	return "", ""
}

// resolveToolPath 将用户给出的路径解析为工具可执行文件：
// 若本身就是可执行文件则直接返回；若是目录则尝试其中的可执行文件。
func resolveToolPath(p, exeName string) (string, bool) {
	if st, err := os.Stat(p); err == nil {
		if !st.IsDir() {
			return p, true
		}
		for _, name := range []string{exeName + ".exe", exeName} {
			candidate := filepath.Join(p, name)
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				return candidate, true
			}
		}
	}
	return "", false
}

func toolMessage(source, path string) string {
	switch source {
	case "config":
		return "使用 config.toml 中保存的路径: " + path
	case "path":
		return "已存在于环境变量 PATH: " + path
	case "default-dir":
		return "在默认安装目录找到: " + path
	default:
		return path
	}
}

// inUserPath 判断目录是否已存在于用户级 PATH（忽略大小写）
func inUserPath(dir string) bool {
	dir = strings.TrimSuffix(strings.TrimSpace(dir), `\`)
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("Path")
	if err != nil {
		return false
	}
	for _, part := range strings.Split(cur, ";") {
		if strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(part), `\`), dir) {
			return true
		}
	}
	return false
}

// Configure 将检测到的工具所在目录写入用户级 PATH（幂等），
// 并返回配置后的最新检测结果与操作说明。
func (s *EnvService) Configure() ([]ToolInfo, string, error) {
	tools := s.Detect()
	dirs := []string{}
	for _, t := range tools {
		if t.Found && t.EnvDir != "" {
			dirs = append(dirs, t.EnvDir)
		}
	}
	if len(dirs) == 0 {
		return tools, "未检测到任何工具，无需配置", nil
	}

	added, err := addDirsToUserPath(dirs)
	if err != nil {
		return tools, "", fmt.Errorf("写入用户 PATH 失败: %w", err)
	}

	// 更新当前进程环境变量，让本次会话内的子进程（如 packwiz 调用）立即生效
	if len(added) > 0 {
		os.Setenv("Path", joinPathWith(added, os.Getenv("Path")))
	}

	msg := "已配置，无新增"
	if len(added) > 0 {
		msg = "已将以下目录加入用户 PATH: " + strings.Join(added, "; ")
	}
	return s.Detect(), msg, nil
}

// addDirsToUserPath 将目录写入 HKCU\Environment 的 Path 并广播变更
func addDirsToUserPath(dirs []string) ([]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()

	cur := ""
	vtype := uint32(registry.EXPAND_SZ) // 默认按可展开字符串处理
	if n, t, err := k.GetValue("Path", nil); err == nil && n > 0 {
		buf := make([]byte, n)
		if _, _, err := k.GetValue("Path", buf); err == nil {
			cur = strings.TrimSpace(string(buf))
		}
		vtype = t
	}

	// 去重（大小写不敏感）
	existing := map[string]bool{}
	for _, p := range strings.Split(cur, ";") {
		if p = strings.TrimSuffix(strings.TrimSpace(p), `\`); p != "" {
			existing[strings.ToLower(p)] = true
		}
	}

	added := []string{}
	for _, d := range dirs {
		key := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), `\`))
		if existing[key] {
			continue
		}
		if cur != "" {
			cur += ";"
		}
		cur += d
		existing[key] = true
		added = append(added, d)
	}

	if len(added) == 0 {
		return nil, nil
	}

	if vtype == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", cur)
	} else {
		err = k.SetStringValue("Path", cur)
	}
	if err != nil {
		return nil, err
	}

	broadcastEnvironmentChange()
	return added, nil
}

// joinPathWith 将新增目录合并进现有 PATH 字符串
func joinPathWith(dirs []string, existing string) string {
	parts := []string{}
	if existing != "" {
		parts = append(parts, existing)
	}
	return strings.Join(append(parts, dirs...), ";")
}

// broadcastEnvironmentChange 广播 WM_SETTINGCHANGE，
// 让 Explorer 等进程立即感知环境变量变更（无需重启）。
func broadcastEnvironmentChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	env, _ := syscall.UTF16PtrFromString("Environment")
	proc.Call(
		uintptr(0xFFFF), // HWND_BROADCAST
		uintptr(0x001A), // WM_SETTINGCHANGE
		0,
		uintptr(unsafe.Pointer(env)),
		uintptr(0x0002), // SMTO_ABORTIFHUNG
		5000,
		0,
	)
}

// SetToolPath 保存用户手动指定的工具路径（空串清除），返回最新检测结果
func (s *EnvService) SetToolPath(name, path string) ([]ToolInfo, error) {
	if err := s.config.SetToolPath(name, strings.TrimSpace(path)); err != nil {
		return nil, err
	}
	return s.Detect(), nil
}
