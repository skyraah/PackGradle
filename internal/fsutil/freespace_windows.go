//go:build windows

package fsutil

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// FreeDiskBytes 返回 path 所在卷的剩余字节数（ADR-0011 §8 free_disk_bytes 口径：
// 数据根所在卷剩余空间，容量红线双指标之一）。
func FreeDiskBytes(path string) (int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	var free, total, avail uint64
	if err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(abs), &free, &total, &avail); err != nil {
		return 0, err
	}
	return int64(free), nil
}
