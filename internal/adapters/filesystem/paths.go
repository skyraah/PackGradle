package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscape 表示相对路径非法或解析后逃逸出 root
// （`..`、绝对路径、symlink/junction 指向 root 外）。调用方映射为 err.scan.path_escape。
var ErrPathEscape = errors.New("filesystem: 路径逃逸或非法")

// ResolveWithin 把 root-relative 路径安全解析为 root 内的绝对路径。
// root 先经 NormalizeEndpointPath 规范化（绝对化 + realpath），随后按
// Resolver.Resolve 语义做 containment 校验。批量访问请用 Resolver（root 只解析一次）。
func ResolveWithin(root, rel string) (string, error) {
	realRoot, err := NormalizeEndpointPath(root)
	if err != nil {
		return "", err
	}
	return resolveWithin(realRoot, rel)
}

// IsPlainFile 报告 path 是否为普通文件且不经过任何重解析点（junction/symlink）。
// 删除/覆盖前的所有权检查使用。
func IsPlainFile(path string) (bool, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return st.Mode()&os.ModeSymlink == 0 && st.Mode().IsRegular(), nil
}

// WithinRoot 报告 target 是否位于 root 内（含 root 本身），大小写不敏感。
// 从 resolver 解析出的 base 展开批量遍历（WalkDir）时的逐项防御性复核用。
func WithinRoot(root, target string) bool {
	return withinRoot(root, target)
}

// withinRoot 报告 target（解析后）是否落在 root 之内或就是 root。
// 大小写不敏感卷上前缀比较统一小写。
func withinRoot(root, target string) bool {
	rootSlash := strings.ToLower(filepath.ToSlash(filepath.Clean(root))) + "/"
	targetSlash := strings.ToLower(filepath.ToSlash(filepath.Clean(target)))
	if targetSlash+"/" == rootSlash {
		return true // target 即 root 本身
	}
	return strings.HasPrefix(targetSlash, rootSlash)
}
