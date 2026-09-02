package model

import (
	"errors"
	"path/filepath"
	"strings"
)

// 别名路径（ADR-0011 §7 R1；CONTEXT.md「别名路径」词条）：诊断详情与错误
// detail 里端点内绝对路径的存储默认形态——端点根前缀统一替换为角色前缀
//（<project>/<runtime>），新写入即别名、历史行不追溯；端点根之外的绝对
// 路径不进诊断面。落点在构造侧（诊断/错误串构造处），非查询侧后处理：
// 单一事实源，UI 与将来导出同数据、无分档渲染。
const (
	// AliasProject 是项目源端点的角色前缀。
	AliasProject = "<project>"
	// AliasRuntime 是运行实例端点的角色前缀。
	AliasRuntime = "<runtime>"
)

// AliasFor 返回端点侧对应的角色前缀（project→<project>，runtime→<runtime>；
// 其他侧防御性退 <project>）。
func AliasFor(side Side) string {
	if side == SideRuntime {
		return AliasRuntime
	}
	return AliasProject
}

// AliasPath 把 root 下绝对路径转换为别名路径：root 前缀（大小写不敏感、
// 分隔符 / 与 \ 等价）替换为 alias，剩余段分隔符统一为 `/`（<project>/mods/a.toml）。
// path 即 root 本身时返回 alias；不在 root 内（或无法相对化）时返回 alias+"/…"——
// 端点根之外的绝对路径不进诊断面（ADR-0011 §7 R6 并入 R1）。
func AliasPath(root, alias, path string) string {
	if root == "" || path == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return alias + "/…"
	}
	if rel == "." {
		return alias
	}
	// filepath.Rel 在 Windows 大小写不敏感且感知卷名；`..` 前缀即越出 root
	if relSlash := filepath.ToSlash(rel); relSlash == ".." || strings.HasPrefix(relSlash, "../") {
		return alias + "/…"
	}
	return alias + "/" + filepath.ToSlash(rel)
}

// AliasDetail 把端点错误文本（error 字符串、诊断 detail）中出现的端点根
// 绝对路径统一替换为角色别名，供诊断构造侧在嵌入前调用（ADR-0011 §7 R1）。
// 覆盖 canonical（realpath 规范化）、用户输入原样、大小写与 `/`/`\` 分隔符
// 变体——同一端点经链接或写法差异访问得到的内嵌路径同样被别名化。
// 替换要求匹配是完整路径前缀（后界为分隔符/边界），root="…/proj" 不会误伤
// "…/project"。root 外的绝对路径不在替换范围（构造侧不得把这类路径嵌入诊断面）。
func AliasDetail(root, alias, text string) string {
	if root == "" || text == "" {
		return text
	}
	for _, cand := range rootVariants(root) {
		text = replacePathPrefix(text, cand, alias)
	}
	return text
}

// replacePathPrefix 把 text 中以 cand 为完整路径前缀的出现替换为 alias，
// 匹配大小写不敏感（Windows 卷语义；非常规 Unicode 退回字面匹配保字节安全）。
func replacePathPrefix(text, cand, alias string) string {
	lower := strings.ToLower(text)
	candLower := strings.ToLower(cand)
	if len(lower) != len(text) || len(candLower) != len(cand) {
		// ToLower 变长的非常规字符：退回区分大小写的字面匹配
		return replacePathPrefixFold(text, cand, cand, alias)
	}
	return replacePathPrefixFold(text, lower, candLower, alias)
}

// replacePathPrefixFold 在 probe（文本的匹配视图）中定位 candView 的出现，
// 按原文本切片替换为 alias：probe 与 candView 必须与 text 逐字节等长。
func replacePathPrefixFold(text, probe, candView, alias string) string {
	var b strings.Builder
	for {
		i := strings.Index(probe, candView)
		if i < 0 {
			b.WriteString(text)
			return b.String()
		}
		end := i + len(candView)
		if end < len(text) && isPathContinuation(text[end]) {
			// 前缀重叠（如 root="…/proj"、文本="…/project"）：跳过本次匹配
			b.WriteString(text[:end])
			text, probe = text[end:], probe[end:]
			continue
		}
		b.WriteString(text[:i])
		b.WriteString(alias)
		text, probe = text[end:], probe[end:]
	}
}

// isPathContinuation 报告 cand 之后的字节是否把匹配延长为更长的路径组件
//（字母/数字/下划线/点/连字符连接，如 proj→project、proj→proj.toml）。
// 路径分隔符不算延续——root 分隔符后正是 root 内相对段的起点，应替换。
func isPathContinuation(c byte) bool {
	switch {
	case c == '\\' || c == '/':
		return false
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_' || c == '.' || c == '-':
		return true
	default:
		return false
	}
}

// AliasError 以别名路径重写 err 文本并返回普通错误（ADR-0011 §7 R1）。
// 供错误串仅作展示透传（任务 Problem.Detail、诊断 Detail）的构造点使用；
// 需要保留哨兵/包装链的错误不得经此改写。
func AliasError(root, alias string, err error) error {
	if err == nil {
		return nil
	}
	return errors.New(AliasDetail(root, alias, err.Error()))
}

// rootVariants 枚举端点根在错误文本中可能出现的字面形态（大小写交由匹配器
// 折叠处理）：native（`\`）与 `/` 分隔符两形态 × 输入原样与 Abs 展开（相对
// 输入经规范化管线 Abs 后与输入字面不同）。
func rootVariants(root string) []string {
	clean := filepath.Clean(root)
	seen := map[string]struct{}{}
	out := make([]string, 0, 4)
	add := func(s string) {
		if s == "" {
			return
		}
		if _, dup := seen[s]; dup {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(clean)
	add(filepath.ToSlash(clean))
	if abs, err := filepath.Abs(clean); err == nil && abs != clean {
		add(abs)
		add(filepath.ToSlash(abs))
	}
	return out
}
