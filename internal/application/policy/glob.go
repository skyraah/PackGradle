package policy

import (
	"fmt"
	"regexp"
	"strings"
)

// Glob 是编译后的 root-relative glob 匹配器。
// 语义（root-relative glob 编译规约，检视报告 P0-5）：
//   - 模式与路径均为小写、'/' 分隔的 root 相对路径；
//   - '*' 匹配单段内任意字符序列（不跨 '/'），'?' 匹配单个非 '/' 字符；
//   - 整段 '**' 跨任意深度（含零段）；"前缀/**" 至少要求前缀后还有一层；
//   - "[...]" 字符类支持区间与 '^' 取反（'!' 视为字面量）；
//   - 模式命中完整路径或其任一祖先目录（目录模式包含子树，与既有扫描语义一致）。
//
// 编译期拒绝：空模式、绝对路径、'..'/'.' 段、冒号（盘符/ADS）、未闭合或空字符类。
type Glob struct {
	pattern string
	re      *regexp.Regexp
}

// CompileGlob 编译一个 root-relative glob 模式；非法模式返回错误。
func CompileGlob(pattern string) (*Glob, error) {
	slash := strings.ReplaceAll(pattern, "\\", "/")
	if strings.HasPrefix(slash, "/") {
		return nil, fmt.Errorf("glob 模式为绝对路径: %q", pattern)
	}
	p := normalizeRelPath(pattern)
	if p == "" {
		return nil, fmt.Errorf("空 glob 模式")
	}
	if err := validateRelSegments(p, "glob 模式"); err != nil {
		return nil, err
	}
	re, err := translateGlob(p)
	if err != nil {
		return nil, fmt.Errorf("glob 模式 %q 非法: %w", pattern, err)
	}
	return &Glob{pattern: p, re: re}, nil
}

// Pattern 返回归一化后的模式。
func (g *Glob) Pattern() string { return g.pattern }

// MatchPath 报告归一化路径（小写、'/' 分隔）是否命中模式或其任一祖先目录。
func (g *Glob) MatchPath(path string) bool {
	if g.re.MatchString(path) {
		return true
	}
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 1; i-- {
		if g.re.MatchString(strings.Join(parts[:i], "/")) {
			return true
		}
	}
	return false
}

// normalizeRelPath 归一化：反斜杠转斜杠、小写、去首尾 '/'。
func normalizeRelPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.ToLower(p)
	p = strings.Trim(p, "/")
	return p
}

// validateRelSegments 校验 root 相对路径形状：非空段、无 '..'/'.'、无冒号
// （root 边界的编译期检查；运行时越界仍由 filesystem.Resolver 兜底）。
func validateRelSegments(p, what string) error {
	if strings.Contains(p, ":") {
		return fmt.Errorf("%s 含冒号（盘符或 ADS 路径不合法）: %q", what, p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".", "..":
			return fmt.Errorf("%s 含非法段 %q: %q", what, seg, p)
		}
	}
	return nil
}

// globClassChars 是字符类内允许的字符白名单（保守集合，拒绝转义与嵌套类）。
const globClassChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._!-"

// translateGlob 把归一化模式翻译为锚定正则。
func translateGlob(p string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		last := i == len(segs)-1
		if seg == "**" {
			if last {
				sb.WriteString(".+") // "前缀/**" 至少要求前缀后还有一层
			} else {
				sb.WriteString("(?:[^/]+/)*") // 零段或多段
			}
			continue
		}
		reSeg, err := translateSegment(seg)
		if err != nil {
			return nil, err
		}
		sb.WriteString(reSeg)
		if !last {
			sb.WriteString("/")
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return nil, err
	}
	return re, nil
}

// translateSegment 翻译单个 glob 段：'*'→[^/]*、'?'→[^/]、'[...]' 校验后透传、
// 其余字符按字面量转义。
func translateSegment(seg string) (string, error) {
	var sb strings.Builder
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch c {
		case '*':
			sb.WriteString("[^/]*")
		case '?':
			sb.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(seg[i+1:], ']')
			if end < 0 {
				return "", fmt.Errorf("未闭合的字符类")
			}
			class := seg[i+1 : i+1+end]
			if class == "" {
				return "", fmt.Errorf("空字符类")
			}
			body := class
			if body[0] == '^' {
				body = body[1:]
			}
			if body == "" {
				return "", fmt.Errorf("空字符类")
			}
			for j := 0; j < len(body); j++ {
				if !strings.ContainsRune(globClassChars, rune(body[j])) {
					return "", fmt.Errorf("字符类含不支持字符 %q", body[j:j+1])
				}
			}
			sb.WriteString("[" + class + "]")
			i += end + 1
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return sb.String(), nil
}
