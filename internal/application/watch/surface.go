package watch

import (
	"path/filepath"
	"strings"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/fsutil"
)

// SurfaceTarget 是一个监听语义目标：relation 的一个管辖目录（或其中的单个
// 文件）。语义面与实际注册点分离——目录不存在时注册其最近存在父目录
//（engine.nearestExistingDir），事件按语义面匹配。
type SurfaceTarget struct {
	RelationID string
	// Dir 是语义管辖目录（绝对路径、Clean、保留原大小写）。
	Dir string
	// File 非空时只匹配 Dir 下该文件名（项目侧 pack.toml 语境：项目根本身
	// 不是管辖树，只有 pack.toml 一个管辖文件）。
	File string
}

// SurfaceFor 计算 MappingPolicy 的监听面（ADR-0010 §3「监听面是 policy 的函数」）：
// 项目侧 pack.toml + 各管辖前缀目录，运行侧 minecraft/（Runtime.RootPath）下
// 同名管辖目录。direction=ignore 的规则不受管辖、不监听；单向规则也监听双端
//（变化察觉是全枚举扫描的事，监听只负责「哪里动了」）。policy 修改后由引擎
// 重算本函数并重挂（ADR-0010 §3）。
func SurfaceFor(policy model.MappingPolicy, projectRoot, runtimeRoot, relationID string) []SurfaceTarget {
	var out []SurfaceTarget
	if projectRoot != "" {
		// 项目侧 pack.toml：packwiz 版本决策/git 拉取的变更入口
		out = append(out, SurfaceTarget{
			RelationID: relationID,
			Dir:        filepath.Clean(projectRoot),
			File:       "pack.toml",
		})
	}
	for _, r := range policy.Rules {
		if r.Direction == "ignore" {
			continue
		}
		if projectRoot != "" && r.ProjectPrefix != "" {
			out = append(out, SurfaceTarget{
				RelationID: relationID,
				Dir:        filepath.Join(filepath.Clean(projectRoot), filepath.FromSlash(r.ProjectPrefix)),
			})
		}
		if runtimeRoot != "" && r.RuntimePrefix != "" {
			out = append(out, SurfaceTarget{
				RelationID: relationID,
				Dir:        filepath.Join(filepath.Clean(runtimeRoot), filepath.FromSlash(r.RuntimePrefix)),
			})
		}
	}
	return out
}

// excludedDirNames 是监听面排除的目录段名（ADR-0010 §3 排除集）：
// `mods/.index`（Prism 事后自行重写只产噪声，其变化由 mods 文件变化本身反映）、
// logs/saves 等非管辖树、Prism 自有元数据。作用两面：触发语义匹配不认穿越
// 排除段的事件路径（crossesExcludedDir），递归补挂不下探排除段目录
//（engine.expandLocked）。管辖面本身由 policy 前缀约束。
var excludedDirNames = map[string]bool{
	".index":  true, // mods/.index
	"logs":    true,
	"saves":   true,
	".mmc":    true, // Prism 自有元数据
	".fabric": true,
}

// ExcludedDirName 判断目录段名是否在递归补挂排除集内。
func ExcludedDirName(name string) bool { return excludedDirNames[strings.ToLower(name)] }

// eventMatchesTarget 判断事件路径是否落在语义目标管辖范围内（含目标本身）。
// Windows 路径大小写不敏感，经 fsutil.SamePath/小写前缀归一。File 非空时
// 只认该文件自身的事件（目录其余部分不受管辖）；事件路径自目标目录以下
// 穿越排除集段不构成管辖触发（ADR-0010 §3：写 `mods/.index` 不触发）。
func eventMatchesTarget(eventPath string, t SurfaceTarget) bool {
	if !matchUnder(eventPath, t.Dir) {
		return false
	}
	if t.File != "" && !sameFileName(eventPath, t.File) {
		return false
	}
	return !crossesExcludedDir(eventPath, t.Dir)
}

// crossesExcludedDir 判断事件路径自 dir 以下是否穿越排除集目录段（段边界
// 严格、大小写不敏感；路径等于 dir 本身不算穿越）。
func crossesExcludedDir(eventPath, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(eventPath))
	if err != nil || rel == "." {
		return false
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if ExcludedDirName(seg) {
			return true
		}
	}
	return false
}

// matchUnder 判断 p 是否等于 dir 或位于 dir 之下（段边界严格，大小写不敏感）。
func matchUnder(p, dir string) bool {
	if fsutil.SamePath(p, dir) {
		return true
	}
	return isStrictUnder(p, dir)
}

// isStrictUnder 判断 p 是否严格位于 dir 之下（不含相等）。
func isStrictUnder(p, dir string) bool {
	pc := strings.ToLower(filepath.Clean(p))
	dc := strings.ToLower(filepath.Clean(dir))
	return strings.HasPrefix(pc, dc+string(filepath.Separator))
}

// isStrictAncestor 判断 anc 是否为 p 的严格祖先目录（p 在 anc 之下）。
func isStrictAncestor(anc, p string) bool {
	return isStrictUnder(p, anc)
}

// sameFileName 判断事件路径的文件名段与 name 是否一致（大小写不敏感）。
func sameFileName(eventPath, name string) bool {
	return strings.EqualFold(filepath.Base(filepath.Clean(eventPath)), name)
}

// isDirEvent 判断事件是否携带目录出现语义（Create/Rename 到达）。
func isDirEvent(op ports.DirOp) bool { return op.Has(ports.DirCreate) || op.Has(ports.DirRename) }

// isGoneEvent 判断事件是否携带消失语义（Remove/Rename 离开）。
func isGoneEvent(op ports.DirOp) bool { return op.Has(ports.DirRemove) || op.Has(ports.DirRename) }
