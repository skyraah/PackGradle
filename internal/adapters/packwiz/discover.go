package packwiz

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"packgradle/internal/application/ports"
)

// maxDiscoverDepth 是项目源发现的目录深度上限（parentDir 自身为深度 0）。
// 命中 pack.toml 的目录视为项目根，不再向内递归（嵌套整合包不是合法布局）。
const maxDiscoverDepth = 3

// discoverPriority 是 [versions] 中加载器键的判定顺序（先到先得，保证确定性）。
var discoverPriority = [...]string{"fabric", "quilt", "forge", "neoforge"}

// packTomlMeta 是 pack.toml 中发现所需的元数据子集。
type packTomlMeta struct {
	Name     string            `toml:"name"`
	Versions map[string]string `toml:"versions"`
}

// DiscoverProjects 在 parentDir 内有限深度查找含 pack.toml 的项目根目录，
// 实现 ports.ProjectDiscovery。单个项目元数据读取失败时跳过（不影响其余候选）；
// parentDir 自身不可读返回错误（避免把故障误报成空列表）。
func (s *Scanner) DiscoverProjects(ctx context.Context, parentDir string) ([]ports.ProjectCandidate, error) {
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil, fmt.Errorf("packwiz: 读取发现目录 %s: %w", parentDir, err)
	}
	candidates := make([]ports.ProjectCandidate, 0, len(entries))

	// 深度 0：parentDir 自身即项目根时直接返回（不向项目内部递归）
	if cand, ok := projectCandidate(parentDir); ok {
		return []ports.ProjectCandidate{cand}, nil
	}
	if err := walkForProjects(ctx, parentDir, 1, &candidates); err != nil {
		return nil, err
	}

	sort.Slice(candidates, func(i, j int) bool {
		return strings.ToLower(candidates[i].DisplayName) < strings.ToLower(candidates[j].DisplayName)
	})
	return candidates, nil
}

// walkForProjects 递归查找项目根（不深入已是项目根的目录；单目录读取失败跳过）。
func walkForProjects(ctx context.Context, dir string, depth int, out *[]ports.ProjectCandidate) error {
	if depth > maxDiscoverDepth {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // 不可读子目录：跳过，不中断其余发现
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if cand, ok := projectCandidate(sub); ok {
			*out = append(*out, cand)
			continue // 命中项目根：不再向内递归
		}
		if err := walkForProjects(ctx, sub, depth+1, out); err != nil {
			return err
		}
	}
	return nil
}

// projectCandidate 判定 dir 是否为 Packwiz 项目根并提取候选元数据。
func projectCandidate(dir string) (ports.ProjectCandidate, bool) {
	packPath := filepath.Join(dir, "pack.toml")
	data, err := os.ReadFile(packPath)
	if err != nil {
		return ports.ProjectCandidate{}, false
	}
	cand := ports.ProjectCandidate{
		RootPath:     dir,
		PackTomlPath: packPath,
		DisplayName:  filepath.Base(dir),
	}
	var meta packTomlMeta
	if err := toml.Unmarshal(data, &meta); err == nil {
		if meta.Name != "" {
			cand.DisplayName = meta.Name
		}
		cand.Minecraft = meta.Versions["minecraft"]
		for _, key := range discoverPriority {
			if v := meta.Versions[key]; v != "" {
				cand.Modloader = key
				break
			}
		}
	}
	// pack.toml 解析失败仍报候选（低置信度）：路径事实成立，元数据留空
	return cand, true
}
