package packwiz

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// 哨兵错误：application 映射为 err.* 结构化错误。
var (
	ErrNotPackwizProject = errors.New("packwiz: 缺少 pack.toml（不是 Packwiz 项目）")
	ErrIndexMissing      = errors.New("packwiz: 缺少 index.toml（权威列表不可用）")
)

// Scanner 实现 ports.ProjectScanner。
type Scanner struct{}

// New 构造 Packwiz 项目扫描器。
func New() *Scanner { return &Scanner{} }

// Name 返回扫描器名。
func (s *Scanner) Name() string { return "packwiz" }

// Version 返回扫描器实现版本（语义变化时递增，参与快照记录但不参与 digest）。
func (s *Scanner) Version() string { return "1.0.0" }

// Scan 扫描项目端点：index.toml 权威 mod 列表 + MappingPolicy 受管文件规则。
func (s *Scanner) Scan(ctx context.Context, root string, opts ports.ScanOptions) (model.ScanReport, error) {
	report := model.ScanReport{}

	if _, err := os.Stat(filepath.Join(root, "pack.toml")); err != nil {
		return report, ErrNotPackwizProject
	}
	idx, err := parseIndex(filepath.Join(root, "index.toml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return report, ErrIndexMissing
		}
		return report, fmt.Errorf("packwiz: index.toml 解析失败: %w", err)
	}

	obs := make([]model.ResourceObservation, 0, len(idx.Files))
	modPolicyID := findModRuleID(opts.Policy)

	for _, entry := range idx.Files {
		if !isModMetafile(entry) {
			continue
		}
		relLower, err := normalize.NormalizeRelativePath(entry.File, true)
		if err != nil {
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.path_escape",
				Args: []string{entry.File}, RelativePath: entry.File,
				Detail: "index.toml 条目路径非法（绝对路径或 .. 穿越），已忽略",
			})
			continue
		}
		absMeta := filepath.Join(root, filepath.FromSlash(entry.File))
		meta, metaErr := parseModMeta(absMeta)
		if metaErr != nil {
			// 容错哲学：条目保留（低置信度路径身份），错误落诊断
			obs = append(obs, model.ResourceObservation{
				ResourceID: model.ResourceID("mod:path:" + relLower),
				Kind:       model.ResourceMod,
				Identity:   model.Identity{Provider: "path", Key: relLower, Confidence: model.ConfidenceLow},
				Representation: model.Representation{
					RelativePath: entry.File,
					Format:       "packwiz-mod-toml",
				},
				PolicyID: modPolicyID,
			})
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.modmeta_unreadable",
				Args: []string{entry.File}, RelativePath: entry.File,
				Detail: metaErr.Error(),
			})
			continue
		}

		id, identity := modIdentity(meta, relLower)
		metadata := map[string]string{
			model.MetaDisplayName: meta.Name,
		}
		if v := modVersion(meta); v != "" {
			metadata[model.MetaVersion] = v
		}
		if side := normalize.NormalizeSide(meta.Side); side != "" {
			metadata[model.MetaSide] = side
		}
		if meta.Download.HashFormat != "" && meta.Download.Hash != "" {
			metadata[model.MetaDeclaredHashAlgo] = strings.ToLower(meta.Download.HashFormat)
			metadata[model.MetaDeclaredHashValue] = meta.Download.Hash
		}
		if meta.Filename != "" {
			metadata[model.MetaFilename] = meta.Filename
		}

		obs = append(obs, model.ResourceObservation{
			ResourceID: id,
			Kind:       model.ResourceMod,
			Identity:   identity,
			Representation: model.Representation{
				RelativePath: entry.File,
				Format:       "packwiz-mod-toml",
				Metadata:     metadata,
			},
			PolicyID: modPolicyID,
		})
	}

	// 受管文件规则（text_file/binary_file）
	fileObs, fileDiags, err := scanManagedFiles(ctx, root, opts)
	if err != nil {
		return report, err
	}
	obs = append(obs, fileObs...)
	report.Diagnostics = append(report.Diagnostics, fileDiags...)

	sort.Slice(obs, func(i, j int) bool { return obs[i].ResourceID < obs[j].ResourceID })
	report.Observations = obs
	return report, nil
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

// scanManagedFiles 按 MappingPolicy 的文件规则扫描受管文件。
func scanManagedFiles(ctx context.Context, root string, opts ports.ScanOptions) ([]model.ResourceObservation, []model.Diagnostic, error) {
	var obs []model.ResourceObservation
	var diags []model.Diagnostic

	for _, rule := range opts.Policy.Rules {
		kind := model.ResourceKind(rule.ResourceKind)
		if kind != model.ResourceTextFile && kind != model.ResourceBinaryFile {
			continue
		}
		if rule.ProjectPrefix == "" {
			continue
		}
		prefix := strings.ToLower(strings.ReplaceAll(strings.Trim(rule.ProjectPrefix, "/"), "\\", "/"))
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
			obs = append(obs, model.ResourceObservation{
				ResourceID: model.ResourceID("file:" + relLower),
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
			return nil, diags, fmt.Errorf("packwiz: 扫描前缀 %s: %w", rule.ProjectPrefix, err)
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
