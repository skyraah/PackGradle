//go:build windows

package junction

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows NTFS Junction 的实现：
//   - 创建：cmd /c mklink /J（Windows 内建命令，无需管理员权限；Go exec 以参数数组
//     传递路径，天然处理空格/中文，无引号转义问题）
//   - 检测/目标解析：FSCTL_GET_REPARSE_POINT 直查重解析标签（不依赖 os.Lstat 的 Mode，
//     Go 1.23+ 对 junction 返回 ModeIrregular）
//   - 删除：os.Remove（Windows 上仅删除重解析点本身，不触碰目标内容）

const (
	reparseTagMountPoint = 0xA0000003
	fsctlGetReparsePoint = 0x000900A8
)

// reparseDataBuffer 对应 REPARSE_DATA_BUFFER（MountPointReparseBuffer 布局）
type reparseDataBuffer struct {
	ReparseTag            uint32
	ReparseDataLength     uint16
	Reserved              uint16
	SubstituteNameOffset  uint16
	SubstituteNameLength  uint16
	PrintNameOffset       uint16
	PrintNameLength       uint16
	PathBuffer            [windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE]uint16
}

type windowsManager struct{}

// NewWindowsManager 返回 Windows 原生 junction 管理器
func NewWindowsManager() Manager {
	return &windowsManager{}
}

// createNoWindow 隐藏控制台窗口（与 internal/packwiz 的 newHiddenCmd 同一模式）
func createNoWindow() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// Create 将 link 创建为指向 target 的 junction。
// 要求：target 为已存在的绝对路径；link 不存在（调用方需先校验）。
func (m *windowsManager) Create(link, target string) error {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	// 目标必须存在且为目录
	info, err := os.Stat(targetAbs)
	if err != nil || !info.IsDir() {
		return &os.PathError{Op: "junction.create", Path: targetAbs, Err: os.ErrNotExist}
	}
	// 链接位置必须不存在（拒绝覆盖任何已有内容）
	if _, err := os.Lstat(link); err == nil {
		return &os.PathError{Op: "junction.create", Path: link, Err: os.ErrExist}
	} else if !os.IsNotExist(err) {
		return err
	}

	// cmd /c mklink /J <link> <target>：参数数组传递，无需手动引号转义
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, targetAbs)
	cmd.SysProcAttr = createNoWindow()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J 失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Remove 删除链接本身。防御：仅当位置确实是 junction 时才删除，
// 避免误删真实目录（普通目录 os.Remove 仅对空目录生效，但这里显式拒绝）。
func (m *windowsManager) Remove(link string) error {
	isJ, err := m.IsJunction(link)
	if err != nil {
		return err
	}
	if !isJ {
		return &os.PathError{Op: "junction.remove", Path: link, Err: os.ErrInvalid}
	}
	return os.Remove(link) // Windows 上删除 reparse point 本身，不触碰目标
}

// IsJunction 通过 FSCTL_GET_REPARSE_POINT 直查重解析标签。
// 不依赖 os.Lstat 的 Mode（Go 1.23+ 对 junction 返回 ModeIrregular）。
func (m *windowsManager) IsJunction(link string) (bool, error) {
	rb, err := m.queryReparse(link)
	if err != nil {
		return false, nil // 不存在 / 普通目录 / 无重解析数据
	}
	return rb.ReparseTag == reparseTagMountPoint, nil
}

// TargetOf 返回 junction 的目标绝对路径（去掉 \??\ NT 前缀）
func (m *windowsManager) TargetOf(link string) (string, error) {
	rb, err := m.queryReparse(link)
	if err != nil {
		return "", err
	}
	if rb.ReparseTag != reparseTagMountPoint {
		return "", &os.PathError{Op: "junction.targetof", Path: link, Err: os.ErrInvalid}
	}
	buf := rb.PathBuffer[rb.SubstituteNameOffset/2 : (rb.SubstituteNameOffset+rb.SubstituteNameLength)/2]
	target := windows.UTF16ToString(buf)
	return strings.TrimPrefix(target, `\??\`), nil
}

// openReparse 以重解析点方式打开路径（OPEN_REPARSE_POINT 打开链接本身而非目标）
func openReparse(path string, access uint32) (windows.Handle, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(ptr, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

// queryReparse 读取路径的重解析数据（普通目录/不存在返回错误）。
// 注意：不能以 os.Lstat 的 IsDir() 做快速短路——Go 1.23+ 对 junction 报
// ModeIrregular（非 ModeDir），误判会漏掉 junction 本身。
func (m *windowsManager) queryReparse(path string) (*reparseDataBuffer, error) {
	if _, err := os.Lstat(path); err != nil {
		return nil, err
	}

	h, err := openReparse(path, windows.GENERIC_READ)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)

	rb := &reparseDataBuffer{}
	var bytesReturned uint32
	err = windows.DeviceIoControl(h, fsctlGetReparsePoint,
		nil, 0, (*byte)(unsafe.Pointer(rb)), uint32(unsafe.Sizeof(*rb)), &bytesReturned, nil)
	if err != nil {
		return nil, err
	}
	return rb, nil
}
