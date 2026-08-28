//go:build windows

package filesystem

import (
	"strings"
	"syscall"
	"unsafe"
)

const (
	_fileFlagBackupSemantics = 0x02000000
	_openExisting            = 3
	_fileShareReadWriteDel   = 0x7
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")

var (
	procCreateFileW               = kernel32.NewProc("CreateFileW")
	procCloseHandle               = kernel32.NewProc("CloseHandle")
	procGetFinalPathNameByHandleW = kernel32.NewProc("GetFinalPathNameByHandleW")
)

// openDirHandle 打开既有路径的句柄（无需访问权；FILE_FLAG_BACKUP_SEMANTICS
// 允许打开目录，默认跟随 reparse point，因此句柄指向最终目标）。
func openDirHandle(path string) (syscall.Handle, error) {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, _, callErr := procCreateFileW.Call(
		uintptr(unsafe.Pointer(p16)),
		0, // 仅查询属性，不申请读写
		_fileShareReadWriteDel,
		0,
		_openExisting,
		_fileFlagBackupSemantics,
		0,
	)
	if h == uintptr(syscall.InvalidHandle) {
		return 0, callErr
	}
	return syscall.Handle(h), nil
}

// realpath 返回 path 的 Windows 最终路径（GetFinalPathNameByHandle）。
// filepath.EvalSymlinks 在 Windows 不解析 junction（Go ≥1.25 报 ModeIrregular），
// 本 API 才是完整的 realpath：symlink/junction/卷挂载点/subst 盘符全部解析。
func realpath(path string) (string, error) {
	h, err := openDirHandle(path)
	if err != nil {
		return "", err
	}
	defer procCloseHandle.Call(uintptr(h))
	buf := make([]uint16, 1024)
	n, _, callErr := procGetFinalPathNameByHandleW.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0, // FILE_NAME_NORMALIZED | VOLUME_NAME_DOS
	)
	if n == 0 || n > uintptr(len(buf)) {
		return "", callErr
	}
	// 返回值不含结尾 NUL；UTF16ToString 自行在 NUL 处截断
	return normalizeFinalPath(syscall.UTF16ToString(buf[:n])), nil
}

// normalizeFinalPath 剥离 GetFinalPathNameByHandle 的 \\?\ 前缀，
// 与普通 Win32 路径空间对齐（withinRoot/落库比较都按此形态）。
func normalizeFinalPath(p string) string {
	if strings.HasPrefix(p, `\\?\UNC\`) {
		return `\\` + p[len(`\\?\UNC\`):]
	}
	return strings.TrimPrefix(p, `\\?\`)
}
