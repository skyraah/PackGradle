//go:build !windows

package filesystem

import (
	"fmt"
	"os"
	"syscall"
)

// fileKeyFromStat 返回文件的平台文件标识（dev + inode）。
// 与绑定指纹同源的 file identity：同路径被替换为不同文件时身份变化。
func fileKeyFromStat(st os.FileInfo) string {
	if statT, ok := st.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("u:%d:%d", statT.Dev, statT.Ino)
	}
	return ""
}

// fileKeyFromOpenFile 从已打开文件取平台文件标识。
func fileKeyFromOpenFile(f *os.File) string {
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	return fileKeyFromStat(st)
}

// fileKey 返回文件的平台文件标识；文件不可达或 FS 不支持（身份取不到）
// 返回 ""，调用方按 size+mtime 降级。目录返回 ""（目录身份由 BindingFingerprinter 负责）。
func fileKey(absPath string) string {
	st, err := os.Stat(absPath)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return fileKeyFromStat(st)
}
