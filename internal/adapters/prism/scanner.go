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

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/application/policy"
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
// 全部端点内路径访问经 Resolver（realpath + root containment）强制入口；
// 映射策略先经 policy.Compile（编译期校验 + glob 编译 + 决议器），编译失败即错误。
func (s *Scanner) Scan(ctx context.Context, root string, opts ports.ScanOptions) (model.ScanReport, error) {
	report := model.ScanReport{}
	seen := map[model.ResourceID]bool{}

	compiled, cerr := policy.Compile(opts.Policy)
	if cerr != nil {
		return report, fmt.Errorf("prism: 映射策略编译失败: %w", cerr)
	}

	rslv, err := filesystem.NewResolver(root)
	if err != nil {
		return report, fmt.Errorf("prism: 端点根不可达: %w", err)
	}

	// ---- A. mods 扫描 ----
	modsDir, rerr := rslv.Resolve("mods")
	if rerr != nil {
		// mods/ 解析后越出端点根目录：跳过 mods 段并诊断（受管文件规则照常）
		report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
			Severity: "warning", Code: "diag.scan.path_escape",
			Args:   []string{"mods"},
			Detail: "mods 目录解析后越出端点根目录，已跳过: " + rerr.Error(),
		})
		modsDir = ""
	}
	var entries []os.DirEntry
	if modsDir != "" {
		entries, err = os.ReadDir(modsDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return report, fmt.Errorf("prism: 读取 mods 目录 %s: %w", modsDir, err)
		}
	}
	// mods/ 不存在 → 空，不算错误（entries 为 nil，循环不执行）

	hint := lowercaseHintKeys(opts.Hint.FilenameToResourceID)
	modPolicyID := compiled.ModRuleID()

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
		jarPath, jerr := rslv.Resolve(relPath)
		if jerr != nil {
			// ReadDir 来自已解析的 modsDir，此处仅为纵深防御
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.path_escape",
				Args: []string{relPath}, RelativePath: relPath,
				Detail: "mods 条目解析后越出端点根目录，已跳过: " + jerr.Error(),
			})
			continue
		}
		id, identity := jarIdentity(hint, name)
		if seen[id] {
			report.Diagnostics = append(report.Diagnostics, duplicateIdentityDiag(id))
			continue
		}
		// .index 元数据失败不阻断观察，只是缺 metadata
		metadata := readIndexMetadata(rslv, name, &report.Diagnostics)

		// Content 必填：无哈希函数或哈希失败的 jar 不产出观察，只落诊断
		if opts.HashFile == nil {
			report.Diagnostics = append(report.Diagnostics, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.hasher_missing",
				Args: []string{relPath}, RelativePath: relPath,
				Detail: "扫描选项未注入哈希函数，jar 未纳入观察",
			})
			continue
		}
		content, _, herr := opts.HashFile(ctx, jarPath)
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
	fileObs, fileDiags, err := scanManagedFiles(ctx, rslv, compiled, opts)
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
// 条目不存在 → nil（正常情况）；解析失败 → 追加 index_meta_unreadable 诊断并
// 返回 nil（资源仍产出，仅缺 metadata）；解析后越出端点根目录 → path_escape
// 诊断并返回 nil。
func readIndexMetadata(rslv *filesystem.Resolver, jarName string, diags *[]model.Diagnostic) map[string]string {
	indexPath, rerr := rslv.Resolve("mods/.index/" + jarName + ".pw.toml")
	if rerr != nil {
		*diags = append(*diags, model.Diagnostic{
			Severity: "warning", Code: "diag.scan.path_escape",
			Args: []string{"mods/.index/" + jarName + ".pw.toml"}, RelativePath: "mods/" + jarName,
			Detail: ".index 条目解析后越出端点根目录，元数据未读取: " + rerr.Error(),
		})
		return nil
	}
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
// text_file/binary_file）扫描受管文件，行为与 packwiz 侧对称，分两阶段：
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
		prefix := rule.SidePrefix(model.SideRuntime)
		if prefix == "" {
			continue
		}
		base, rerr := rslv.Resolve(prefix)
		if rerr != nil {
			diags = append(diags, model.Diagnostic{
				Severity: "warning", Code: "diag.scan.path_escape",
				Args:   []string{prefix},
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
					Args:   []string{path},
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
			return nil, diags, fmt.Errorf("prism: 扫描前缀 %s: %w", prefix, err)
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
		winner, collision := policy.ResolveFileRule(model.SideRuntime, ruleCands)
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
