// Package managedfiles 实现 MappingPolicy 文件规则（text_file/binary_file）与
// 端点文件系统的对接，供 packwiz/prism 扫描器共用。行为分两阶段：
//  1. 逐规则经 Resolver 解析前缀并遍历，收集「路径 → 命中规则」候选
//     （include/exclude 过滤在编译产物的 Matches 上进行）；
//  2. 对每个路径按「最具体前缀优先」决议唯一规则后哈希观察；最长前缀并列
//     无法唯一决议 → diag.mapping.collision（证据：并列规则 ID 字节序 + 命中路径），
//     该路径从观察中剔除，快照与计划保留诊断证据（检视报告 P0-5）。
package managedfiles

import (
	"context"
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
)

// candidate 是一条「路径被某规则命中」的候选。
type candidate struct {
	rule    *policy.CompiledFileRule
	abs     string
	relPath string // 原大小写的 root 相对路径（诊断与表示用）
}

// Scan 扫描指定端的受管文件；side 决定使用规则的哪一侧前缀。
// 全部端点内路径访问经 Resolver（realpath + root containment）强制入口。
func Scan(ctx context.Context, rslv *filesystem.Resolver, compiled *policy.Compiled, side model.Side, opts ports.ScanOptions) ([]model.ResourceObservation, []model.Diagnostic, error) {
	var diags []model.Diagnostic
	cands := make(map[string][]candidate)

	rules := compiled.FileRules()
	for i := range rules {
		rule := &rules[i]
		prefix := rule.SidePrefix(side)
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
			return nil, diags, fmt.Errorf("扫描前缀 %s: %w", prefix, err)
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
		winner, collision := policy.ResolveFileRule(side, ruleCands)
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
