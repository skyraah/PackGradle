//go:build !windows

package fsutil

import (
	"syscall"
)

// FreeDiskBytes 返回 path 所在卷的剩余字节数（ADR-0011 §8 free_disk_bytes 口径）。
// 非 Windows 平台经 statfs 读取（f_bavail × f_bsize，普通用户可用剩余）。
func FreeDiskBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
