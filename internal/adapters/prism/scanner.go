// Package prism 实现 Prism Launcher 运行时端点适配器：
// DiscoverInstances 发现实例（含 instance.cfg 的一级子目录），
// Scanner 扫描游戏目录（mods/*.jar 与 MappingPolicy 受管文件规则）。
// 解析知识来自旧 internal/prism 与 internal/adapters/packwiz（重写实现，不复用代码）。
package prism

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// 编译期确认 Scanner 实现 ports.RuntimeScanner。
var _ ports.RuntimeScanner = (*Scanner)(nil)

// Scanner 实现 ports.RuntimeScanner，扫描 Prism 实例的游戏目录。
type Scanner struct{}

// New 构造 Prism 运行时扫描器。
func New() *Scanner { return &Scanner{} }

// Name 返回扫描器名。
func (s *Scanner) Name() string { return "prism" }

// Version 返回扫描器实现版本（语义变化时递增，参与快照记录但不参与 digest）。
func (s *Scanner) Version() string { return "1.0.0" }

// indexMeta 是 mods/.index/<jar文件名>.pw.toml 的字段子集。
// .index 是 packwiz 推送到 Prism 侧的元数据索引（Prism 兼容格式），
// 运行时扫描用它补全 version/side/声明 hash；条目缺失属正常情况（手工放入的 jar）。
type indexMeta struct {
	Name       string                    `toml:"name"`
	Side       string                    `toml:"side"`
	Version    string                    `toml:"version"`
	XPLVersion string                    `toml:"x-prismlauncher-version-number"` // Prism 扩展：显示用版本号
	Download   indexDownload             `toml:"download"`
	Update     map[string]map[string]any `toml:"update"`
}

// indexDownload 只取声明 hash 相关字段。
type indexDownload struct {
	HashFormat string `toml:"hash-format"`
	Hash       string `toml:"hash"`
}

// Scan 扫描运行时端点（root 为游戏目录，其下有 mods/）：
// A. mods 一级目录 *.jar（不递归）；B. MappingPolicy 受管文件规则；C. 排序输出。
func (s *Scanner) Scan(ctx context.Context, root string, opts ports.ScanOptions) (model.ScanReport, error) {
	report := model.ScanReport{}
	seen := map[model.ResourceID]bool{}

	// ---- A. mods 扫描 ----
	modsDir := filepath.Join(root, "mods")
	entries, err := os.ReadDir(modsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return report, fmt.Errorf("prism: 读取 mods 目录 %s: %w", modsDir, err)
	}
	// mods/ 不存在 → 空，不算错误（entries 为 nil，循环不执行）

	hint := lowercaseHintKeys(opts.Hint.FilenameToResourceID)
	modPolicyID := findModRuleID(opts.Policy)

	for _, entry := range entries {
		name := entry.Name()
		if strings.ToLower(filepath.Ext(name)) != ".jar" {
			continue // 非 .jar 文件（含 .index 目录）忽略，不诊断
		}
		relPath := "mods/" + name
		if !entry.Type().IsRegular() {
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.not_regular_file",
				Args: []string{relPath}, RelativePath: relPath,
				Detail: "mods 条目不是常规文件（目录/链接伪装 .jar），已跳过",
			})
			continue
		}
		id, identity := jarIdentity(hint, name)
		if seen[id] {
			report.Diagnostics = append(report.Diagnostics, duplicateIdentityDiag(id))
			continue
		}
		// .index 元数据失败不阻断观察，只是缺 metadata
		metadata := readIndexMetadata(modsDir, name, &report.Diagnostics)

		// Content 必填：无哈希函数或哈希失败的 jar 不产出观察，只落诊断
		if opts.HashFile == nil {
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.hasher_missing",
				Args: []string{relPath}, RelativePath: relPath,
				Detail: "扫描选项未注入哈希函数，jar 未纳入观察",
			})
			continue
		}
		content, _, herr := opts.HashFile(ctx, filepath.Join(modsDir, name))
		if herr != nil {
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.hash_failed",
				Args: []string{relPath}, RelativePath: relPath, Detail: herr.Error(),
			})
			continue
		}
		c := content
		seen[id] = true
		report.Observations = append(report.Observations, model.ResourceObservation{
			ResourceID: id,
			Kind:       model.ResourceMod,
			Identity:   identity,
			Representation: model.Representation{
				RelativePath: relPath,
				Format:       "jar",
				Content:      &c,
				Metadata:     metadata,
			},
			PolicyID: modPolicyID,
		})
	}

	// ---- B. 受管文件规则（text_file/binary_file）----
	fileObs, fileDiags, err := scanManagedFiles(ctx, root, opts, seen)
	if err != nil {
		return report, err
	}
	report.Observations = append(report.Observations, fileObs...)
	report.Diagnostics = append(report.Diagnostics, fileDiags...)

	// ---- C. 输出：observations 按 ResourceID 字节序排序；诊断保持出现序 ----
	sort.Slice(report.Observations, func(i, j int) bool {
		return report.Observations[i].ResourceID < report.Observations[j].ResourceID
	})
	return report, nil
}

// jarIdentity 生成 mod 身份：hint 命中 → 项目侧 ResourceID（高置信度 provider 身份，
// 唯一跨侧身份通道，大小写不敏感匹配）；未命中 → 本地 jar 身份（低置信度，
// 小写文件名是身份的一部分）。
func jarIdentity(hint map[string]string, fileName string) (model.ResourceID, model.Identity) {
	if id, ok := hint[strings.ToLower(fileName)]; ok && id != "" {
		rid := model.ResourceID(id)
		return rid, normalize.IdentityFromResourceID(rid)
	}
	lower := strings.ToLower(fileName)
	return model.ResourceID("mod:jar:" + lower),
		model.Identity{Provider: "jar", Key: lower, Confidence: model.ConfidenceLow}
}

// lowercaseHintKeys 把 hint 表的键统一小写。契约要求键为 pw.toml filename 的
// 小写值；这里防御非规范输入，保证 jar 文件名两侧大小写不敏感匹配。
func lowercaseHintKeys(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// readIndexMetadata 读取 mods/.index/<jar文件名>.pw.toml 并转为保留键元数据；
// 条目不存在 → nil（正常情况）；存在但解析失败 → 追加 index_meta_unreadable
// 诊断并返回 nil（资源仍产出，仅缺 metadata）。
func readIndexMetadata(modsDir, jarName string, diags *[]model.Diagnostic) map[string]string {
	indexPath := filepath.Join(modsDir, ".index", jarName+".pw.toml")
	if _, err := os.Stat(indexPath); err != nil {
		return nil
	}
	var m indexMeta
	if _, err := toml.DecodeFile(indexPath, &m); err != nil {
		*diags = append(*diags, model.Diagnostic{
			Severity: "warning", Code: "diag.scan.index_meta_unreadable",
			Args: []string{"mods/.index/" + jarName + ".pw.toml"}, RelativePath: "mods/" + jarName,
			Detail: err.Error(),
		})
		return nil
	}
	metadata := map[string]string{model.MetaSide: normalize.NormalizeSide(m.Side)}
	if v := indexVersion(m); v != "" {
		metadata[model.MetaVersion] = v
	}
	if m.Download.HashFormat != "" && m.Download.Hash != "" {
		metadata[model.MetaDeclaredHashAlgo] = strings.ToLower(m.Download.HashFormat)
		metadata[model.MetaDeclaredHashValue] = m.Download.Hash
	}
	if m.Name != "" {
		metadata[model.MetaDisplayName] = m.Name
	}
	return metadata
}

// indexVersion 取版本值，优先级：x-prismlauncher-version-number > 顶层 version >
// [update.*] 侧 version（modrinth version-id / curseforge file-id，数字转字符串）。
func indexVersion(m indexMeta) string {
	if m.XPLVersion != "" {
		return m.XPLVersion
	}
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

// findModRuleID 找 mod 规则的 ID（资源观察的 PolicyID）。
func findModRuleID(p model.MappingPolicy) string {
	for _, r := range p.Rules {
		if r.ResourceKind == string(model.ResourceMod) {
			return r.ID
		}
	}
	return ""
}

// duplicateIdentityDiag 构造重名冲突诊断（防御：Windows 大小写不敏感文件系统
// 不可能出现两个小写同名的 jar，但符号链接/挂载场景可能）。
func duplicateIdentityDiag(id model.ResourceID) model.Diagnostic {
	return model.Diagnostic{
		Severity: "warning", Code: "diag.scan.duplicate_identity",
		Args: []string{string(id)}, ResourceID: id,
		Detail: "资源 ID 重复（重名文件映射到同一身份），后出现者已跳过",
	}
}

// scanManagedFiles 按 MappingPolicy 的文件规则（RuntimePrefix 非空的
// text_file/binary_file）扫描受管文件，行为与 packwiz 侧对称。
func scanManagedFiles(ctx context.Context, root string, opts ports.ScanOptions, seen map[model.ResourceID]bool) ([]model.ResourceObservation, []model.Diagnostic, error) {
	var obs []model.ResourceObservation
	var diags []model.Diagnostic

	for _, rule := range opts.Policy.Rules {
		kind := model.ResourceKind(rule.ResourceKind)
		if kind != model.ResourceTextFile && kind != model.ResourceBinaryFile {
			continue
		}
		if rule.RuntimePrefix == "" {
			continue
		}
		prefix := strings.ToLower(strings.ReplaceAll(strings.Trim(rule.RuntimePrefix, "/"), "\\", "/"))
		base := filepath.Join(root, filepath.FromSlash(prefix))
		if _, err := os.Stat(base); err != nil {
			continue // 前缀目录不存在：无观察，不是错误
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				diags = append(diags, model.Diagnostic{
					Severity: "warning", Code: "diag.scan.walk_failed",
					Args: []string{path}, Detail: err.Error(),
				})
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			relToRoot, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(relToRoot)
			relLower := strings.ToLower(relSlash)
			// mods/ 恒由 mod 语义规则管理
			if relLower == "mods" || strings.HasPrefix(relLower, "mods/") {
				return nil
			}
			if excludedByRule(relLower, rule) || !includedByRule(relLower, rule) {
				return nil
			}
			id := model.ResourceID("file:" + relLower)
			if seen[id] {
				diags = append(diags, duplicateIdentityDiag(id))
				return nil
			}
			if opts.HashFile == nil {
				diags = append(diags, model.Diagnostic{
					Severity: "warning", Code: "diag.scan.hasher_missing",
					Args: []string{relSlash}, RelativePath: relSlash,
					Detail: "扫描选项未注入哈希函数，文件未纳入观察",
				})
				return nil
			}
			content, _, herr := opts.HashFile(ctx, path)
			if herr != nil {
				diags = append(diags, model.Diagnostic{
					Severity: "warning", Code: "diag.scan.hash_failed",
					Args: []string{relSlash}, RelativePath: relSlash, Detail: herr.Error(),
				})
				return nil
			}
			c := content
			seen[id] = true
			obs = append(obs, model.ResourceObservation{
				ResourceID: id,
				Kind:       kind,
				Identity:   model.Identity{},
				Representation: model.Representation{
					RelativePath: relSlash,
					Format:       formatOf(relSlash, kind),
					Content:      &c,
				},
				PolicyID: rule.ID,
			})
			return nil
		})
		if err != nil {
			return nil, diags, fmt.Errorf("prism: 扫描前缀 %s: %w", rule.RuntimePrefix, err)
		}
	}
	return obs, diags, nil
}

// excludedByRule：Exclude 任一 glob 命中文件或其祖先目录即排除。
func excludedByRule(relLower string, rule model.MappingRule) bool {
	for _, pattern := range rule.Exclude {
		pattern = strings.ToLower(pattern)
		if ok, _ := filepath.Match(pattern, relLower); ok {
			return true
		}
		// 目录模式：命中任何祖先
		parts := strings.Split(relLower, "/")
		for i := 1; i < len(parts); i++ {
			if ok, _ := filepath.Match(pattern, strings.Join(parts[:i], "/")); ok {
				return true
			}
		}
	}
	return false
}

// includedByRule：Include 为空表示全收；非空时至少一项命中文件或祖先目录。
func includedByRule(relLower string, rule model.MappingRule) bool {
	if len(rule.Include) == 0 {
		return true
	}
	for _, pattern := range rule.Include {
		pattern = strings.ToLower(pattern)
		if ok, _ := filepath.Match(pattern, relLower); ok {
			return true
		}
		parts := strings.Split(relLower, "/")
		for i := 1; i < len(parts); i++ {
			if ok, _ := filepath.Match(pattern, strings.Join(parts[:i], "/")); ok {
				return true
			}
		}
	}
	return false
}

// formatOf 由扩展名推断表示格式。
func formatOf(relLower string, kind model.ResourceKind) string {
	switch {
	case strings.HasSuffix(relLower, ".toml"):
		return "toml"
	case strings.HasSuffix(relLower, ".json"):
		return "json"
	case strings.HasSuffix(relLower, ".ini"), strings.HasSuffix(relLower, ".cfg"):
		return "ini"
	case strings.HasSuffix(relLower, ".js"), strings.HasSuffix(relLower, ".ts"):
		return "text"
	case kind == model.ResourceBinaryFile:
		return "binary"
	default:
		return "text"
	}
}
