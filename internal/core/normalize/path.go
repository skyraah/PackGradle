// Package normalize 实现资源身份与规范化状态的 canonical 编码规则
// （架构文档 §6.2.1，normalization_version = 1）。
package normalize

import (
	"errors"
	"strings"
)

// NormalizationVersion 是本包实现的 canonical 编码版本。
// 算法变化必须产生新版本，不能静默改变旧 digest 含义。
const NormalizationVersion = 1

// ErrInvalidPath 表示路径不是合法的 root-relative 路径
// （绝对路径、卷名、`..` 穿越、空路径等）。调用方应映射为 err.scan.path_escape。
var ErrInvalidPath = errors.New("normalize: invalid root-relative path")

// NormalizeRelativePath 规范化 root-relative 路径：
// 统一为 '/'、移除 '.' 组件与空组件、拒绝绝对路径/卷名/'..'；
// lower=true 时输出小写（identity 路径用），展示路径保留原大小写。
func NormalizeRelativePath(p string, lower bool) (string, error) {
	if p == "" {
		return "", ErrInvalidPath
	}
	s := strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(s, "/") {
		return "", ErrInvalidPath
	}
	// Windows 路径只允许盘符处出现冒号；统一拒绝任何冒号，
	// 同时防止资源 ID（mod:xxx）被误当作路径传入。
	if strings.Contains(s, ":") {
		return "", ErrInvalidPath
	}
	parts := strings.Split(s, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			// 跳过空组件与当前目录
		case "..":
			return "", ErrInvalidPath
		default:
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return "", ErrInvalidPath
	}
	result := strings.Join(out, "/")
	if lower {
		result = strings.ToLower(result)
	}
	return result, nil
}
