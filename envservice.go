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
	Name    string `json:"name"`     // packwiz / prism-launcher
	Found   bool   `json:"found"`    // 是否已安装
	Path    string `json:"path"`     // 可执行文件或配置目录的完整路径
	Source  string `json:"source"`   // 发现来源: custom / path / registry / config-dir
	Message string `json:"message"`  // 对用户的中文说明
	EnvDir  string `json:"env_dir"`  // 需要加入 PATH 的目录（无可加目录时为空）
	EnvOK   bool   `json:"env_ok"`   // 该目录是否已在用户 PATH 中
}

// EnvService 负责检测 packwiz / Prism Launcher 的装载状态，
// 并将工具所在目录写入用户级 PATH。
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

// detectPackwiz 依次尝试：自定义路径 → PATH → 常见安装位置
func (s *EnvService) detectPackwiz() ToolInfo {
	info := ToolInfo{Name: "packwiz"}

	// 1. 用户自定义路径（可为文件或目录）
	if p := strings.TrimSpace(s.config.Get().PackwizPath); p != "" {
		if resolved, ok := resolveToolPath(p, "packwiz"); ok {
			info.Path = resolved
			info.Source = "custom"
		}
	}

	// 2. PATH 中查找
	if info.Path == "" {
		if p, err := exec.LookPath("packwiz"); err == nil {
			info.Path = p
			info.Source = "path"
		}
	}

	// 3. 常见安装位置
	if info.Path == "" {
		for _, dir := range []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "packwiz"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "packwiz"),
		} {
			for _, name := range []string{"packwiz.exe", "packwiz"} {
				p := filepath.Join(dir, name)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					info.Path = p
					info.Source = "common"
					break
				}
			}
			if info.Path != "" {
				break
			}
		}
	}

	if info.Path != "" {
		info.Found = true
		info.Message = toolMessage(info.Source, info.Path)
		info.EnvDir = filepath.Dir(info.Path)
		info.EnvOK = inUserPath(info.EnvDir)
	} else {
		info.Message = "未检测到 packwiz。可手动指定路径，或先安装 packwiz 并加入 PATH"
	}
	return info
}

// detectPrism 依次尝试：自定义路径 → PATH → 注册表卸载信息 → 默认配置目录
func (s *EnvService) detectPrism() ToolInfo {
	info := ToolInfo{Name: "prism-launcher"}

	// 1. 用户自定义路径
	if p := strings.TrimSpace(s.config.Get().PrismPath); p != "" {
		if resolved, ok := resolveToolPath(p, "prismlauncher"); ok {
			info.Path = resolved
			info.Source = "custom"
		}
	}

	// 2. PATH 中查找
	if info.Path == "" {
		for _, name := range []string{"prismlauncher", "PrismLauncher"} {
			if p, err := exec.LookPath(name); err == nil {
				info.Path = p
				info.Source = "path"
				break
			}
		}
	}

	// 3. 注册表卸载信息（参考旧版 PrismLauncherDetector.java）
	if info.Path == "" {
		if loc, err := findPrismInstallLocation(); err == nil && loc != "" {
			if resolved, ok := resolveToolPath(loc, "prismlauncher"); ok {
				info.Path = resolved
				info.Source = "registry"
			}
		}
	}

	if info.Path != "" {
		info.Found = true
		info.Message = toolMessage(info.Source, info.Path)
		info.EnvDir = filepath.Dir(info.Path)
		info.EnvOK = inUserPath(info.EnvDir)
		return info
	}

	// 4. 默认配置目录存在（如 %AppData%\PrismLauncher），说明已运行过
	configDir := filepath.Join(os.Getenv("APPDATA"), "PrismLauncher")
	if st, err := os.Stat(configDir); err == nil && st.IsDir() {
		info.Found = true
		info.Path = configDir
		info.Source = "config-dir"
		info.Message = "在配置目录检测到 Prism Launcher（未找到启动器本体）"
	} else {
		info.Message = "未检测到 Prism Launcher。可手动指定路径，或先安装 Prism Launcher"
	}
	return info
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
	case "custom":
		return "使用自定义路径: " + path
	case "path":
		return "已存在于 PATH: " + path
	case "registry":
		return "从注册表安装信息找到: " + path
	case "common":
		return "常见安装位置找到: " + path
	default:
		return path
	}
}

// findPrismInstallLocation 在注册表卸载信息中查找 Prism Launcher 的安装位置
func findPrismInstallLocation() (string, error) {
	roots := []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE, registry.LOCAL_MACHINE}
	paths := []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
	}
	for i, root := range roots {
		base := paths[i]
		k, err := registry.OpenKey(root, base, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		subs, err := k.ReadSubKeyNames(-1)
		k.Close()
		if err != nil {
			continue
		}
		for _, sub := range subs {
			sk, err := registry.OpenKey(root, base+`\`+sub, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			name, _, _ := sk.GetStringValue("DisplayName")
			loc, _, _ := sk.GetStringValue("InstallLocation")
			sk.Close()
			if loc != "" && strings.Contains(strings.ToLower(name), "prism") {
				return loc, nil
			}
		}
	}
	return "", fmt.Errorf("注册表中未找到 Prism Launcher")
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
