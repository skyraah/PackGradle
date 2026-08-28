package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/core/normalize"
)

// ErrPathEscape 表示相对路径非法或解析后逃逸出 root
// （`..`、绝对路径、symlink/junction 指向 root 外）。调用方映射为 err.scan.path_escape。
var ErrPathEscape = errors.New("filesystem: 路径逃逸或非法")

// ResolveWithin 把 root-relative 路径安全解析为 root 内的绝对路径。
// 规则：先 normalize（拒绝绝对路径/../空路径），join 后做 EvalSymlinks，
// 解析结果必须仍在 root（同样 EvalSymlinks 后）之内；大小写不敏感卷上
// 前缀比较统一小写。
func ResolveWithin(root, rel string) (string, error) {
	cleanRel, err := normalize.NormalizeRelativePath(rel, false)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("filesystem: root 不可达 %s: %w", absRoot, err)
	}

	joined := filepath.Join(realRoot, filepath.FromSlash(cleanRel))
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// 目标尚不存在（如计划写入的新文件）：逐级解析已存在的最近父目录，
		// 剩余部分不允许携带分隔符之外的特殊成分（NormalizeRelativePath 已保证无 ..）。
		resolved, err = resolvePartial(joined)
		if err != nil {
			return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
		}
	}
	if !withinRoot(realRoot, resolved) {
		return "", fmt.Errorf("%w: %q 解析到 %s", ErrPathEscape, rel, resolved)
	}
	return resolved, nil
}

// resolvePartial 解析到最深可解析祖先，再拼回尚不存在的尾部。
func resolvePartial(joined string) (string, error) {
	dir, tail := filepath.Split(joined)
	if tail == "" {
		return "", errors.New("空路径")
	}
	realDir, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", err
	}
	return filepath.Join(realDir, tail), nil
}

func withinRoot(root, target string) bool {
	rootSlash := strings.ToLower(filepath.ToSlash(filepath.Clean(root))) + "/"
	targetSlash := strings.ToLower(filepath.ToSlash(filepath.Clean(target)))
	if targetSlash+"/" == rootSlash {
		return true // target 即 root 本身
	}
	return strings.HasPrefix(targetSlash, rootSlash)
}

// IsDirPlainFile 报告 path 是否为普通文件且不经过任何重解析点（junction/symlink）。
// 删除/覆盖前的所有权检查使用。
func IsPlainFile(path string) (bool, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return st.Mode()&os.ModeSymlink == 0 && st.Mode().IsRegular(), nil
}
