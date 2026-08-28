package envutil

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"packgradle/internal/errs"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// FindExecutable 按统一查找链定位可执行文件：
// configPath（config 中保存的路径）→ 环境变量 envVar → PATH → 候选目录 candidates。
// 返回（路径, 来源, 是否找到）；来源取值 config / env / path / default-dir。
// Detect 与服务调用方共用，避免各自维护一套查找逻辑。
func FindExecutable(configPath, exeName, envVar string, candidates ...string) (string, string, bool) {
	// 0. config.toml 中保存的路径（用户输入或之前检测到的）
	if p := strings.TrimSpace(configPath); p != "" {
		if resolved, ok := resolveToolPath(p, exeName); ok {
			return resolved, "config", true
		}
	}

	// 1. 环境变量（用户可用 %VAR% 配置并调用；值中的 %VAR% 同样展开）
	if p := strings.TrimSpace(expandEnv(os.Getenv(envVar))); p != "" {
		if resolved, ok := resolveToolPath(p, exeName); ok {
			return resolved, "env", true
		}
	}

	// 2. 环境变量 PATH
	if p, err := exec.LookPath(exeName); err == nil {
		return p, "path", true
	}

	// 3. 候选默认目录
	for _, dir := range candidates {
		if resolved, ok := resolveToolPath(dir, exeName); ok {
			return resolved, "default-dir", true
		}
	}
	return "", "", false
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

// expandEnv 展开字符串中的 %VAR% 环境变量（Windows 语法）。
// 注意：os.ExpandEnv 只支持 $VAR 语法，不能用于 Windows 的 %VAR%。
func expandEnv(s string) string {
	src, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return s
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("ExpandEnvironmentStringsW")
	// 第一次调用（dst=nil, size=0）返回所需缓冲大小（含结尾空字符）
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(src)), 0, 0)
	if n == 0 {
		return s
	}
	buf := make([]uint16, n)
	ret, _, _ := proc.Call(uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(&buf[0])), n)
	if ret == 0 {
		return s
	}
	return syscall.UTF16ToString(buf)
}

// inUserPath 判断目录是否已存在于用户级 PATH（忽略大小写）。
// PATH 条目可能是 %VAR% 未展开形式，比较前先展开。
func inUserPath(dir string) bool {
	dir = normalizePathEntry(dir)
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
		if strings.EqualFold(normalizePathEntry(expandEnv(part)), dir) {
			return true
		}
	}
	return false
}

// normalizePathEntry 规范化 PATH 条目用于比较：去空白、去尾部反斜杠、转小写
func normalizePathEntry(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(p, `\`)
	return strings.ToLower(p)
}

// InUserPath 判断目录是否已存在于用户级 PATH（envutil 包外调用）
func InUserPath(dir string) bool {
	return inUserPath(dir)
}

// AddDirsToUserPath 将目录写入 HKCU\Environment 的 Path 并广播变更，
// 返回实际新增的目录列表
func AddDirsToUserPath(dirs []string) ([]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()

	// 读取现有用户级 PATH。REG_SZ / REG_EXPAND_SZ 由 GetStringValue
	// 正确解码 UTF-16；PATH 不存在时按可展开字符串创建。
	cur := ""
	vtype := uint32(registry.EXPAND_SZ)
	if v, t, err := k.GetStringValue("Path"); err == nil {
		cur = v
		vtype = t
	} else if !errors.Is(err, registry.ErrNotExist) {
		return nil, errs.NewDetail("err.env.read_user_path", err.Error())
	}

	newCur, added := mergePathDirs(cur, dirs)
	if len(added) == 0 {
		return nil, nil
	}

	if vtype == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", newCur)
	} else {
		err = k.SetStringValue("Path", newCur)
	}
	if err != nil {
		return nil, err
	}

	broadcastEnvironmentChange()
	return added, nil
}

// mergePathDirs 将目录去重合并进 PATH 字符串，返回新 PATH 与新增目录列表。
// 去重前展开 %VAR% 条目（如 %PRISM%），大小写不敏感。
func mergePathDirs(cur string, dirs []string) (string, []string) {
	existing := map[string]bool{}
	for _, p := range strings.Split(cur, ";") {
		if p = normalizePathEntry(expandEnv(p)); p != "" {
			existing[p] = true
		}
	}

	added := []string{}
	for _, d := range dirs {
		key := normalizePathEntry(expandEnv(d))
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
	return cur, added
}

// JoinPathWith 将新增目录合并进现有 PATH 字符串
func JoinPathWith(dirs []string, existing string) string {
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
