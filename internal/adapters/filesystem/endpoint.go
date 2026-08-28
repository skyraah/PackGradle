package filesystem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"packgradle/internal/core/normalize"
)

// ErrEndpointUnreachable 表示端点路径不可达（不存在、IO 错误或不是目录）。
// 规范化管线的第一道失败：调用方映射为端点不可达语义（check.endpoint.readable /
// err.scan.endpoint_missing）。
var ErrEndpointUnreachable = errors.New("filesystem: 端点路径不可达")

// NormalizeEndpointPath 端点路径规范化管线的强制入口：相对输入绝对化 →
// realpath（symlink/junction/reparse 全解析）→ 必须是已存在的目录。
// 返回 canonical 绝对路径，绑定指纹与端点登记一律以它为准；同一端点经
// 链接或 `.`/尾分隔符等写法访问得到同一结果。
func NormalizeEndpointPath(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("%w: 空路径", ErrEndpointUnreachable)
	}
	abs, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	real, err := realpath(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrEndpointUnreachable, abs, err)
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrEndpointUnreachable, real, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%w: %s 不是目录", ErrEndpointUnreachable, real)
	}
	return filepath.Clean(real), nil
}

// Resolver 是端点内路径访问的唯一安全入口：构造时一次性解析 canonical
// real root，此后所有 root-relative 访问经 Resolve 做 realpath 解析与
// root containment 校验（symlink/junction/reparse 指向 root 外一律拒绝）。
type Resolver struct {
	realRoot string
}

// NewResolver 构造解析器；root 经 NormalizeEndpointPath 规范化，不可达即失败。
func NewResolver(root string) (*Resolver, error) {
	real, err := NormalizeEndpointPath(root)
	if err != nil {
		return nil, err
	}
	return &Resolver{realRoot: real}, nil
}

// Root 返回 canonical real root。
func (r *Resolver) Root() string { return r.realRoot }

// PathNormalizer 实现 ports.EndpointNormalizer：application 层经接口使用
// 规范化管线（不 import 本包）。
type PathNormalizer struct{}

// NormalizeEndpointPath 实现 ports.EndpointNormalizer。
func (PathNormalizer) NormalizeEndpointPath(rootPath string) (string, error) {
	return NormalizeEndpointPath(rootPath)
}

// Resolve 把 root-relative 路径安全解析为 root 内绝对路径；目标尚不存在
// （计划写入的新文件）时逐级解析最深已存在祖先并拼回尾部。
func (r *Resolver) Resolve(rel string) (string, error) {
	return resolveWithin(r.realRoot, rel)
}

// resolveWithin 在已规范化的 realRoot 内解析 rel（ResolveWithin 的核心）。
func resolveWithin(realRoot, rel string) (string, error) {
	// 相对路径预检：拒绝绝对路径/卷名/`..`/空路径，统一分隔符并移除
	// `.`/空组件（复用 core/normalize 的编码规则，保留大小写）。
	cleanRel, err := normalize.NormalizeRelativePath(rel, false)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
	}
	joined := filepath.Join(realRoot, filepath.FromSlash(cleanRel))
	resolved, err := realpath(joined)
	if err != nil {
		// 目标尚不存在：逐级解析已存在的最近祖先（祖先中的链接经 realpath
		// 解析），剩余尾部不含 `..`（normalizeRelativePath 已保证）。
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
// 支持任意深度的不存在前缀（如 mods/.index/new.toml）。
func resolvePartial(joined string) (string, error) {
	joined = filepath.Clean(joined)
	tail := ""
	for {
		if _, err := os.Lstat(joined); err == nil {
			real, rerr := realpath(joined)
			if rerr != nil {
				return "", rerr
			}
			return filepath.Join(real, tail), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent, last := filepath.Split(joined)
		if last == "" {
			return "", errors.New("filesystem: 路径解析到卷根仍不可达")
		}
		tail = filepath.Join(last, tail)
		joined = filepath.Clean(parent)
	}
}
