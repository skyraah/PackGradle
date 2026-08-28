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

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/application/policy"
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
// 全部端点内路径访问经 Resolver（realpath + root containment）强制入口；
// 映射策略先经 policy.Compile（编译期校验 + glob 编译 + 决议器），编译失败即错误。
func (s *Scanner) Scan(ctx context.Context, root string, opts ports.ScanOptions) (model.ScanReport, error) {
	report := model.ScanReport{}

	compiled, cerr := policy.Compile(opts.Policy)
	if cerr != nil {
		return report, fmt.Errorf("packwiz: 映射策略编译失败: %w", cerr)
	}

	rslv, err := filesystem.NewResolver(root)
	if err != nil {
		return report, fmt.Errorf("packwiz: 端点根不可达: %w", err)
	}
	packPath, err := rslv.Resolve("pack.toml")
	if err != nil {
		return report, fmt.Errorf("packwiz: pack.toml 解析失败: %w", err)
	}
	if _, err := os.Stat(packPath); err != nil {
		return report, ErrNotPackwizProject
	}
	idxPath, err := rslv.Resolve("index.toml")
	if err != nil {
		return report, fmt.Errorf("packwiz: index.toml 解析失败: %w", err)
	}
	idx, err := parseIndex(idxPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return report, ErrIndexMissing
		}
		return report, fmt.Errorf("packwiz: index.toml 解析失败: %w", err)
	}

	obs := make([]model.ResourceObservation, 0, len(idx.Files))
	modPolicyID := compiled.ModRuleID()

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
		absMeta, rerr := rslv.Resolve(entry.File)
		if rerr != nil {
			// 条目解析后越出端点根目录（symlink/junction 指向 root 外）：整条跳过
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.path_escape",
				Args: []string{entry.File}, RelativePath: entry.File,
				Detail: "index.toml 条目解析后越出端点根目录，已忽略: " + rerr.Error(),
			})
			continue
		}
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

	// 受管文件规则（text_file/binary_file）：编译产物驱动，含碰撞决议
	fileObs, fileDiags, err := scanManagedFiles(ctx, rslv, compiled, opts)
	if err != nil {
		return report, err
	}
	obs = append(obs, fileObs...)
	report.Diagnostics = append(report.Diagnostics, fileDiags...)

	sort.Slice(obs, func(i, j int) bool { return obs[i].ResourceID < obs[j].ResourceID })
	report.Observations = obs
	return report, nil
}

// scanManagedFiles 按 MappingPolicy 的文件规则扫描受管文件，分两阶段：
//  1. 逐规则经 Resolver 解析前缀并遍历，收集「路径 → 命中规则」候选
//     （include/exclude 过滤在编译产物的 Matches 上进行）；
//  2. 对每个路径按「最具体前缀优先」决议唯一规则后哈希观察；最长前缀并列
//     无法唯一决议 → diag.mapping.collision（证据：并列规则 ID + 命中路径），
//     该路径从观察中剔除，快照与计划保留诊断证据（检视报告 P0-5）。
func scanManagedFiles(ctx context.Context, rslv *filesystem.Resolver, compiled *policy.Compiled, opts ports.ScanOptions) ([]model.ResourceObservation, []model.Diagnostic, error) {
	var diags []model.Diagnostic

	type candidate struct {
		rule    *policy.CompiledFileRule
		abs     string
		relPath string // 原大小写的 root 相对路径（诊断与表示用）
	}
	cands := make(map[string][]candidate)

	rules := compiled.FileRules()
	for i := range rules {
		rule := &rules[i]
		prefix := rule.SidePrefix(model.SideProject)
		if prefix == "" {
			continue
		}
		base, rerr := rslv.Resolve(prefix)
		if rerr != nil {
			diags = append(diags, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.path_escape",
				Args: []string{prefix},
				Detail: "策略前缀解析后越出端点根目录，规则已忽略: " + rerr.Error(),
			})
			continue
		}
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
			// 防御性复核：WalkDir 不跟随链接，但防未来实现变化导致越界
			if !filesystem.WithinRoot(rslv.Root(), path) {
				diags = append(diags, model.Diagnostic{
					Severity: "warning", Code: "diag.scan.path_escape",
					Args: []string{path},
					Detail: "遍历路径越出端点根目录，已忽略",
				})
				return nil
			}
			relToRoot, rerr := filepath.Rel(rslv.Root(), path)
			if rerr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(relToRoot)
			relLower := strings.ToLower(relSlash)
			// mods/ 恒由 mod 语义规则管理（编译器亦拒绝文件规则进入 mods 前缀）
			if relLower == "mods" || strings.HasPrefix(relLower, "mods/") {
				return nil
			}
			if !rule.Matches(relLower) {
				return nil
			}
			cands[relLower] = append(cands[relLower], candidate{rule: rule, abs: path, relPath: relSlash})
			return nil
		})
		if err != nil {
			return nil, diags, fmt.Errorf("packwiz: 扫描前缀 %s: %w", prefix, err)
		}
	}

	var obs []model.ResourceObservation
	paths := make([]string, 0, len(cands))
	for p := range cands {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		cs := cands[p]
		ruleCands := make([]*policy.CompiledFileRule, len(cs))
		for i, c := range cs {
			ruleCands[i] = c.rule
		}
		winner, collision := policy.ResolveFileRule(model.SideProject, ruleCands)
		if collision != nil {
			diags = append(diags, model.Diagnostic{
				Severity: "warning", Code: "diag.mapping.collision",
				Args:     collision, RelativePath: cs[0].relPath,
				ResourceID: model.ResourceID("file:" + p),
				Detail:     "路径被多条映射规则同时命中且无法唯一决议，已从本次观察中剔除",
			})
			continue
		}
		if opts.HashFile == nil {
			diags = append(diags, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.hasher_missing",
				Args: []string{cs[0].relPath}, RelativePath: cs[0].relPath,
				Detail: "扫描选项未注入哈希函数，文件未纳入观察",
			})
			continue
		}
		content, _, herr := opts.HashFile(ctx, cs[0].abs)
		if herr != nil {
			diags = append(diags, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.hash_failed",
				Args: []string{cs[0].relPath}, RelativePath: cs[0].relPath, Detail: herr.Error(),
			})
			continue
		}
		c := content
		kind := model.ResourceKind(winner.Rule.ResourceKind)
		obs = append(obs, model.ResourceObservation{
			ResourceID: model.ResourceID("file:" + p),
			Kind:       kind,
			Identity:   model.Identity{},
			Representation: model.Representation{
				RelativePath: cs[0].relPath,
				Format:       formatOf(cs[0].relPath, kind),
				Content:      &c,
			},
			PolicyID: winner.Rule.ID,
		})
	}
	return obs, diags, nil
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
