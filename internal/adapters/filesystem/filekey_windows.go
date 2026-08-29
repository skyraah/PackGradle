//go:build windows

package filesystem

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// fileKeyFromHandle 返回句柄所指文件的平台文件标识（卷序列号 + file index）。
// 与绑定指纹同源的 file identity：同路径被替换为不同文件时身份变化。
func fileKeyFromHandle(h syscall.Handle) string {
	var info byHandleFileInformation
	ret, _, _ := procGetFileInformationByHandle.Call(uintptr(h), uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return ""
	}
	return fmt.Sprintf("w:%d:%d", info.VolumeSerialNumber,
		uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow))
}

// fileKeyFromOpenFile 从已打开文件句柄取平台文件标识（零额外打开成本）。
func fileKeyFromOpenFile(f *os.File) string {
	return fileKeyFromHandle(syscall.Handle(f.Fd()))
}

// fileKey 返回文件的平台文件标识；文件不可达或身份取不到（网络盘/权限/FAT）
// 返回 ""，调用方按 size+mtime 降级。目录返回 ""（目录身份由 BindingFingerprinter 负责）。
func fileKey(absPath string) string {
	st, err := os.Stat(absPath)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	h, herr := openDirHandle(absPath)
	if herr != nil {
		return ""
	}
	defer procCloseHandle.Call(uintptr(h))
	return fileKeyFromHandle(h)
}
