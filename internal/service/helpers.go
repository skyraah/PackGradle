package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/appconfig"
	"packgradle/internal/envutil"
	"packgradle/internal/errs"
	"packgradle/internal/fsutil"
	"packgradle/internal/packwiz"
	"packgradle/internal/prism"
)

// findProjectByName 按名称查找项目并解析 pack.toml。
// PackwizService 与 PrismService 共用，避免各自维护一份查找逻辑。
func findProjectByName(mgr *appconfig.ConfigManager, projectName string) (packwiz.PackProject, error) {
	entry, ok := mgr.FindProject(projectName)
	if !ok {
		return packwiz.PackProject{}, errs.New("err.proj.not_found", projectName)
	}
	return packwiz.ParseProject(filepath.Join(entry.Path, "pack.toml"))
}

// findPackwizExecutable 按统一查找链定位 packwiz 可执行文件：
// config → PACKWIZ 环境变量 → PATH → %USERPROFILE%\go\bin。
// 环境检测（detectPackwiz）与 CLI 调用（findPackwiz）共用。
func findPackwizExecutable(cfg appconfig.Config) (string, string, bool) {
	goBin := filepath.Join(os.Getenv("USERPROFILE"), "go", "bin")
	return envutil.FindExecutable(cfg.PackwizPath, "packwiz", "PACKWIZ", goBin)
}

// resolveInstancesDir 定位 Prism 实例根目录，优先级：
//  1. config.toml 中用户手动指定的路径（prism_instances_path，覆盖自动定位）
//  2. 自动定位：%APPDATA%\PrismLauncher → prismlauncher.cfg 的 InstanceDir
//
// 手动路径失效（目录被删/改名）时静默回退自动定位；两者都失败才返回错误。
// fromAuto 标识结果来自自动定位（调用方据此决定是否回写持久化）。
func resolveInstancesDir(cfg appconfig.Config) (instDir string, fromAuto bool, err error) {
	if p := cfg.PrismInstancesPath; p != "" {
		if fsutil.IsDir(p) {
			return p, false, nil
		}
		// 手动路径失效：回退自动定位
	}
	dataDir := prism.DataDir()
	if _, err := os.Stat(dataDir); err != nil {
		return "", false, errs.New("err.prism.not_found")
	}
	dir, err := prism.InstancesDir(dataDir)
	if err != nil {
		return "", false, err
	}
	if !fsutil.IsDir(dir) {
		return "", false, errs.NewDetail("err.prism.instances_dir_not_found", "目录不存在", dir)
	}
	return dir, true, nil
}

// indexInstances 将实例列表转为 ID → 实例 索引（实时组装视图用）
func indexInstances(instances []prism.Instance) map[string]prism.Instance {
	out := make(map[string]prism.Instance, len(instances))
	for _, inst := range instances {
		out[inst.ID] = inst
	}
	return out
}

// findInstanceByID 在扫描结果中按实例 ID 查找
func findInstanceByID(instances []prism.Instance, id string) (prism.Instance, bool) {
	for _, inst := range instances {
		if inst.ID == id {
			return inst, true
		}
	}
	return prism.Instance{}, false
}

// removeHardlinkFiles 删除实例侧的顶层文件硬链接（只减引用计数，项目侧内容不受影响）
func removeHardlinkFiles(inst prism.Instance, files []string) {
	for _, f := range files {
		_ = os.Remove(filepath.Join(inst.GameDir, filepath.FromSlash(f)))
	}
}

// normalizeFileList 规范化相对文件清单：去首尾空白、转斜杠、剔除空项、去重、排序
func normalizeFileList(files []string) []string {
	seen := make(map[string]bool, len(files))
	clean := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimSpace(filepath.ToSlash(f))
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		clean = append(clean, f)
	}
	sort.Strings(clean)
	return clean
}

// mergeUniqueSorted 合并两个已排序、无重复的字符串切片（保持有序去重）。
// 用于「既有清单 + 新选择」的合并，避免整体重排。
func mergeUniqueSorted(a, b []string) []string {
	if len(a) == 0 {
		return append([]string{}, b...)
	}
	if len(b) == 0 {
		return append([]string{}, a...)
	}
	out := make([]string, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			out = append(out, a[i])
			i++
		case a[i] > b[j]:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

// findDirLinkIndex 返回目录关联在列表中的下标（未找到返回 -1）
func findDirLinkIndex(links []appconfig.ProjectDirLink, dir string) int {
	for i := range links {
		if links[i].ProjectDir == dir {
			return i
		}
	}
	return -1
}

// dirSideState 描述实例侧位置在建链前的实态
type dirSideState int

const (
	sideAbsent      dirSideState = iota // 位置不存在（可直接建链）
	sideIsFile                          // 位置是文件（冲突）
	sideEmptyDir                        // 空目录（可删除后建链）
	sideNonEmptyDir                     // 非空目录（需手动处理/并入）
)

// inspectDirSide 检查实例侧位置的实态。Lstat 失败一律视为不存在
// （交由后续建链步骤报错，与原行为一致）；ReadDir 失败返回错误。
func inspectDirSide(path string) (dirSideState, error) {
	if _, err := os.Lstat(path); err != nil {
		return sideAbsent, nil
	}
	if !fsutil.IsDir(path) {
		return sideIsFile, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return sideAbsent, err
	}
	if len(entries) == 0 {
		return sideEmptyDir, nil
	}
	return sideNonEmptyDir, nil
}

// hardlinkFile 将项目侧文件硬链接到实例侧（同卷，无需管理员权限）。
// 项目侧文件缺失或实例侧位置已存在时跳过并报告，失败不中断。
func hardlinkFile(projSide, instSide, name string) prism.LinkResult {
	res := prism.LinkResult{Name: name, IsDir: false}
	if !fsutil.IsFile(projSide) {
		res.Status = "skipped"
		res.Detail = errs.New("err.junction.target_missing", name).Error()
		return res
	}
	if _, err := os.Lstat(instSide); err == nil {
		res.Status = "skipped"
		res.Detail = errs.New("err.link.file_exists", name).Error()
		return res
	}
	if err := fsutil.MkdirAll(filepath.Dir(instSide)); err != nil {
		res.Status = "error"
		res.Detail = err.Error()
		return res
	}
	if err := os.Link(projSide, instSide); err != nil {
		res.Status = "error"
		res.Detail = errs.NewDetail("err.link.hardlink_failed", err.Error(), name).Error()
		return res
	}
	res.Status = "linked"
	return res
}
