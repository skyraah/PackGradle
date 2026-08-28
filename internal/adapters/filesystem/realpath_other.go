//go:build !windows

package filesystem

import "path/filepath"

// realpath 返回 path 的真实路径（POSIX 平台 symlink 全解析）。
func realpath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
