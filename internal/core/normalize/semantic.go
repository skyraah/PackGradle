package normalize

import (
	"fmt"
	"strings"

	"packgradle/internal/core/model"
)

// SemanticDigest 计算单个 representation 的语义摘要（架构文档 §6.2.1 第 4 条：
// 使用 canonical semantic object，不含显示名、JAR 文件名（高置信度 mod 时）、
// [download] url 等不稳定信息）。
//
// 规则（normalization_version = 1）：
//   - text_file / binary_file：{"content":{"algorithm","digest","size"}}
//   - mod 高置信度（provider identity）：
//     {"identity":{provider,key},"version":v,"side":s,"hash":{algorithm,digest}|null}
//     hash 优先取声明值（pw.toml [download] / .index 元数据），否则取实际内容 sha256；
//     modrinth sha256 声明与 runtime 实测 sha256 天然可比，murmur2/sha1 两侧都有声明时可比。
//   - mod 低置信度（jar/path 本地身份）：{"local":{"filename":小写路径},"hash":...|null}
//     此时文件名是身份的一部分，进入摘要。
func SemanticDigest(kind model.ResourceKind, rep model.Representation, id model.Identity) (string, error) {
	switch kind {
	case model.ResourceTextFile, model.ResourceBinaryFile:
		if rep.Content == nil {
			return "", fmt.Errorf("semantic: 文件资源 %s 缺少内容指纹", rep.RelativePath)
		}
		return Digest(map[string]any{
			"content": contentObject(*rep.Content),
		})
	case model.ResourceMod:
		hashObj := modHashObject(rep)
		if id.Provider == "modrinth" || id.Provider == "curseforge" {
			side, _ := rep.Metadata[model.MetaSide]
			version, _ := rep.Metadata[model.MetaVersion]
			obj := map[string]any{
				"identity": identityObject(id),
				"version":  version,
				"side":     NormalizeSide(side),
			}
			if hashObj != nil {
				obj["hash"] = hashObj
			}
			return Digest(obj)
		}
		// 低置信度本地身份：文件名（小写）参与摘要
		filename := strings.ToLower(rep.RelativePath)
		obj := map[string]any{
			"local": map[string]any{"filename": filename},
		}
		if hashObj != nil {
			obj["hash"] = hashObj
		}
		return Digest(obj)
	default:
		return "", fmt.Errorf("semantic: 未知资源类别 %q", kind)
	}
}

// modHashObject 提取 mod 的比较指纹：优先声明 hash（保留原算法），
// 否则用实际内容（sha256）。
func modHashObject(rep model.Representation) any {
	algo, hasAlgo := rep.Metadata[model.MetaDeclaredHashAlgo]
	value, hasValue := rep.Metadata[model.MetaDeclaredHashValue]
	if hasAlgo && hasValue && value != "" {
		return map[string]any{
			"algorithm": strings.ToLower(algo),
			"digest":    value,
		}
	}
	if rep.Content != nil {
		return map[string]any{
			"algorithm": rep.Content.Algorithm,
			"digest":    rep.Content.Digest,
		}
	}
	return nil
}

func contentObject(c model.ContentRef) map[string]any {
	return map[string]any{
		"algorithm": c.Algorithm,
		"digest":    c.Digest,
		"size":      c.Size,
	}
}

func identityObject(id model.Identity) map[string]any {
	return map[string]any{
		"provider":   id.Provider,
		"key":        id.Key,
		"confidence": id.Confidence,
	}
}

// NormalizeSide 归一化 side 值：空值视为 both（packwiz 语义）。
func NormalizeSide(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "client", "server", "both":
		return v
	default:
		return "both"
	}
}

// KindOfResourceID 从资源 ID 前缀推导类别（mod: 前缀 → mod，其余按文件处理）。
// 仅用于 baseline 等未携带 Kind 的场景；文件两种类别共享同一语义规则。
func KindOfResourceID(id model.ResourceID) model.ResourceKind {
	if strings.HasPrefix(string(id), "mod:") {
		return model.ResourceMod
	}
	return model.ResourceTextFile
}

// IdentityFromResourceID 从资源 ID 反解身份（与扫描器生成规则保持一致）。
func IdentityFromResourceID(id model.ResourceID) model.Identity {
	s := string(id)
	switch {
	case strings.HasPrefix(s, "mod:modrinth:"):
		return model.Identity{Provider: "modrinth", Key: s[len("mod:modrinth:"):], Confidence: model.ConfidenceHigh}
	case strings.HasPrefix(s, "mod:curseforge:"):
		return model.Identity{Provider: "curseforge", Key: s[len("mod:curseforge:"):], Confidence: model.ConfidenceHigh}
	case strings.HasPrefix(s, "mod:jar:"):
		return model.Identity{Provider: "jar", Key: s[len("mod:jar:"):], Confidence: model.ConfidenceLow}
	case strings.HasPrefix(s, "mod:path:"):
		return model.Identity{Provider: "path", Key: s[len("mod:path:"):], Confidence: model.ConfidenceLow}
	default:
		return model.Identity{}
	}
}

// LogicalDigest 计算基线单资源的双端逻辑指纹：
// digest({"project": <sem>|null, "runtime": <sem>|null})。
func LogicalDigest(projectSem, runtimeSem string) string {
	obj := map[string]any{}
	if projectSem != "" {
		obj["project"] = projectSem
	} else {
		obj["project"] = nil
	}
	if runtimeSem != "" {
		obj["runtime"] = runtimeSem
	} else {
		obj["runtime"] = nil
	}
	d, err := Digest(obj)
	if err != nil {
		// 纯字符串输入不会失败；兜底返回 tombstone 语义之外的错误值不可行，直接 panic 之外选择常量
		return AbsentTombstone()
	}
	return d
}
