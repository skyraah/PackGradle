// Package fsutil 汇集项目内多处重复的文件系统操作：
// 路径实态判断（是否存在/目录/文件）、带结构化错误的目录创建与文件写入、
// 递归复制/清理/列举与路径规范化比较。
//
// MkdirAll / WriteFile 直接在底层错误上包裹 errs 错误码（err.file.mkdir /
// err.file.write），调用方无需各自重复包裹。
package fsutil

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/errs"
)

// Exists 判断路径是否存在（文件或目录均可）
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsDir 判断路径是否为目录（不存在/文件返回 false）
func IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// IsFile 判断路径是否为普通文件（不存在/目录返回 false）
func IsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// MkdirAll 创建目录（自动创建父目录，已存在时无操作）。
// 失败返回 err.file.mkdir 结构化错误（参数为目录路径）。
func MkdirAll(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errs.NewDetail("err.file.mkdir", err.Error(), dir)
	}
	return nil
}

// WriteFile 以 0o644 权限写文件（父目录自动创建）。
// 失败返回 err.file.write 结构化错误（参数为文件路径）。
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.NewDetail("err.file.mkdir", err.Error(), filepath.Dir(path))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return errs.NewDetail("err.file.write", err.Error(), path)
	}
	return nil
}

// RemoveEmptyDirs 自底向上删除 root 下完全空的目录（含任何内容的目录保留不动）
func RemoveEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // 非空目录删除失败，静默跳过
	}
}

// SamePath 规范化比较两条路径是否指向同一位置（忽略大小写与分隔符差异）
func SamePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

// CopyDirMerge 递归复制 src 目录内容到 dst（dst 不存在时自动创建）。
// 同名文件跳过不覆盖——目标侧为权威，源侧内容仅并入缺失部分。
func CopyDirMerge(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := CopyDirMerge(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if _, err := os.Lstat(dstPath); err == nil {
			continue // 同名文件跳过，保留目标侧内容
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ListFilesRelative 递归列出 root 下全部普通文件（相对 root 的斜杠路径），
// 排除隐藏项（. 开头：文件跳过、目录整棵跳过），结果按路径排序。
// 单个文件读取失败跳过不中断；root 不存在时返回错误由调用方包裹。
func ListFilesRelative(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 单文件错误跳过
		}
		if path == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // 隐藏文件跳过
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
