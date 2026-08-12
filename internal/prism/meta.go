package prism

import (
	"strconv"
	"strings"
)

// mod 元数据（pw.toml）在 packwiz 与 Prism 之间的格式转换。
//
// 推送（packwiz → Prism）：在 side 条目后插入 Prism 兼容的扩展字段，
// 供 Prism Launcher 的 mod 管理界面识别加载器/MC 版本/发布类型/版本号：
//
//	x-prismlauncher-loaders = "forge:47.4.10"
//	x-prismlauncher-mc-versions = "1.20.1"
//	x-prismlauncher-release-type = "release"
//	x-prismlauncher-version-number = "1.2.3"
//
// 拉取（Prism → packwiz）：删除上述扩展字段，以及 [download] 表中的 url 条目
// （packwiz 项目侧元数据由 update 源管理，不携带下载地址）。
// 采用行级文本处理以保持原文件键序与格式（不经过 TOML 重排）。

// PrismMeta 是 Prism 兼容扩展字段的值
type PrismMeta struct {
	Loaders     string `json:"loaders"`      // 如 "forge:47.4.10"
	MCVersions  string `json:"mc_versions"`  // 如 "1.20.1"
	ReleaseType string `json:"release_type"` // release / beta / alpha
	Version     string `json:"version"`      // mod 版本号
}

// prismlauncherFields 是 Prism 专属的四个扩展字段名（推送时插入、拉取时删除）
var prismlauncherFields = []string{
	"x-prismlauncher-loaders",
	"x-prismlauncher-mc-versions",
	"x-prismlauncher-release-type",
	"x-prismlauncher-version-number",
}

// ToPrismFormat 将 packwiz 格式的 pw.toml 内容转为 Prism 兼容格式：
// 在 side 条目后插入四个 x-prismlauncher-* 字段（无 side 条目时插入文件开头）。
func ToPrismFormat(content []byte, meta PrismMeta) ([]byte, error) {
	lines := splitTomlLines(content)

	addition := []string{
		"x-prismlauncher-loaders = " + tomlString(meta.Loaders),
		"x-prismlauncher-mc-versions = " + tomlString(meta.MCVersions),
		"x-prismlauncher-release-type = " + tomlString(meta.ReleaseType),
		"x-prismlauncher-version-number = " + tomlString(meta.Version),
	}

	// 找到 side 条目行（TrimSpace 前缀匹配，容忍缩进/注释后）
	insertAt := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "side =") {
			insertAt = i + 1
			break
		}
	}
	if insertAt == -1 {
		insertAt = 0 // 无 side 条目：插入文件开头
	}

	out := make([]string, 0, len(lines)+len(addition))
	out = append(out, lines[:insertAt]...)
	out = append(out, addition...)
	out = append(out, lines[insertAt:]...)
	return []byte(strings.Join(out, "\n")), nil
}

// FromPrismFormat 将 Prism 兼容格式转为 packwiz 格式：
// 删除 x-prismlauncher-* 扩展字段，以及 [download] 表中的 url 条目。
func FromPrismFormat(content []byte) ([]byte, error) {
	lines := splitTomlLines(content)

	out := make([]string, 0, len(lines))
	inDownload := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 表切换追踪
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inDownload = strings.Trim(trimmed, "[]") == "download"
			out = append(out, line)
			continue
		}
		// 删除 Prism 专属扩展字段
		if isPrismlauncherField(trimmed) {
			continue
		}
		// 删除 [download] 表中的 url 条目
		if inDownload && strings.HasPrefix(trimmed, "url =") {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n")), nil
}

// isPrismlauncherField 判断行是否为 x-prismlauncher-* 字段定义
func isPrismlauncherField(trimmed string) bool {
	for _, f := range prismlauncherFields {
		if strings.HasPrefix(trimmed, f+" =") {
			return true
		}
	}
	return false
}

// splitTomlLines 按 \n 拆分行（保留末尾换行语义，Join 后与原内容一致）
func splitTomlLines(content []byte) []string {
	s := string(content)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// tomlString 生成 TOML 字符串字面量（双引号 + Go 转义，TOML 兼容）
func tomlString(v string) string {
	return strconv.Quote(v)
}
