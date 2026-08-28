// Package packwiz 实现 Project 端点扫描器：以 index.toml 为权威 mod 列表，
// 解析 mods/*.pw.toml 的完整身份信息（provider id、声明 hash、filename）。
// 解析知识来自旧 internal/packwiz（重写实现，不复用代码）。
package packwiz

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"packgradle/internal/core/model"
)

// modMeta 是 mods/*.pw.toml 的全量字段。
type modMeta struct {
	Name     string                    `toml:"name"`
	Filename string                    `toml:"filename"`
	Side     string                    `toml:"side"`
	Version  string                    `toml:"version"`
	Download downloadMeta              `toml:"download"`
	Update   map[string]map[string]any `toml:"update"`
}

type downloadMeta struct {
	URL        string `toml:"url"`
	HashFormat string `toml:"hash-format"`
	Hash       string `toml:"hash"`
}

// indexDoc / indexFile 是 index.toml 的 [[files]] 结构。
type indexDoc struct {
	Files []indexFile `toml:"files"`
	Hash  struct {
		Format string `toml:"hash-format"`
	} `toml:"hash"`
}

type indexFile struct {
	File       string `toml:"file"`
	Hash       string `toml:"hash"`
	HashFormat string `toml:"hash-format"`
	Metafile   bool   `toml:"metafile"`
}

// packDoc 只取展示名。
type packDoc struct {
	Name string `toml:"name"`
}

// modIdentity 按优先级生成资源身份：modrinth > curseforge > 路径回退（低置信度）。
func modIdentity(m modMeta, relPathLower string) (model.ResourceID, model.Identity) {
	if mr, ok := m.Update["modrinth"]; ok {
		if id := anyToString(mr["mod-id"]); id != "" {
			return model.ResourceID("mod:modrinth:" + id), model.Identity{Provider: "modrinth", Key: id, Confidence: model.ConfidenceHigh}
		}
	}
	if cf, ok := m.Update["curseforge"]; ok {
		if id := anyToString(cf["project-id"]); id != "" {
			return model.ResourceID("mod:curseforge:" + id), model.Identity{Provider: "curseforge", Key: id, Confidence: model.ConfidenceHigh}
		}
	}
	return model.ResourceID("mod:path:" + relPathLower), model.Identity{Provider: "path", Key: relPathLower, Confidence: model.ConfidenceLow}
}

// modVersion 取版本显示/比较值：顶层 version 优先，
// 否则用 provider 侧的文件版本 id（modrinth version-id / curseforge file-id）。
func modVersion(m modMeta) string {
	if m.Version != "" {
		return m.Version
	}
	if mr, ok := m.Update["modrinth"]; ok {
		if v := anyToString(mr["version-id"]); v != "" {
			return v
		}
	}
	if cf, ok := m.Update["curseforge"]; ok {
		if v := anyToString(cf["file-id"]); v != "" {
			return v
		}
	}
	return ""
}

// anyToString 容忍 TOML 数字解码为 int64/float64 的 id 字段。
func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return ""
	}
}

func parseModMeta(path string) (modMeta, error) {
	var m modMeta
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return modMeta{}, fmt.Errorf("packwiz: 解析 %s: %w", path, err)
	}
	return m, nil
}

func parseIndex(path string) (indexDoc, error) {
	var d indexDoc
	if _, err := toml.DecodeFile(path, &d); err != nil {
		return indexDoc{}, fmt.Errorf("packwiz: 解析 %s: %w", path, err)
	}
	return d, nil
}

func parsePack(path string) (packDoc, error) {
	var d packDoc
	if _, err := toml.DecodeFile(path, &d); err != nil {
		return packDoc{}, fmt.Errorf("packwiz: 解析 %s: %w", path, err)
	}
	return d, nil
}

// isModMetafile 判断 index 条目是否为 mods/ 下的 metafile（大小写不敏感前缀）。
func isModMetafile(f indexFile) bool {
	if !f.Metafile {
		return false
	}
	lower := strings.ToLower(strings.ReplaceAll(f.File, "\\", "/"))
	return lower == "mods" || strings.HasPrefix(lower, "mods/")
}
