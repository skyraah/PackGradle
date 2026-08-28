package service

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows"

	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
	"packgradle/internal/fsutil"
	"packgradle/internal/junction"
	"packgradle/internal/pgignore"
	"packgradle/internal/prism"
)

// CreateAllLinks 一键关联：将项目根下所有未被 .pgignore 忽略的顶层条目建链。
// 目录 → junction（实例游戏目录/<name> 指向 项目/<name>）；文件 → 硬链接。
// mods 目录始终排除（走 meta 推送机制）；实例侧已有真实内容时跳过并报告。
// 返回逐条目结果，单条目失败不中断其余条目。
func (s *PrismService) CreateAllLinks(projectName string) ([]prism.LinkResult, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil, errs.New("err.proj.not_found", projectName)
	}
	results := make([]prism.LinkResult, 0)
	err := appconfig.WithProjectConfigLock(entry.Path, func() error {
		pc, err := appconfig.LoadProjectConfig(entry.Path)
		if err != nil {
			return err
		}
		inst, err := s.linkedInstance(projectName, pc)
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(entry.Path)
		if err != nil {
			return errs.NewDetail("err.proj.invalid_path", err.Error(), entry.Path)
		}
		// 实例游戏目录必须存在（新建实例可能尚无 minecraft/ 骨架）
		if err := fsutil.MkdirAll(inst.GameDir); err != nil {
			return err
		}
		matcher := pgignore.Load(entry.Path)

		newDirLinks := append([]appconfig.ProjectDirLink{}, pc.DirLinks...)
		newFileLinks := append([]string{}, pc.FileLinks...)

		for _, e := range entries {
			name := e.Name()
			if name == "mods" && e.IsDir() {
				continue // mods 始终排除：meta 推送机制
			}
			if pgignore.CoreExcluded(name) {
				continue // 项目核心文件始终排除（不依赖 .pgignore 是否解析成功）
			}
			if matcher.Matches(name) {
				continue // .pgignore 命中
			}
			projSide := filepath.Join(entry.Path, name)
			instSide := filepath.Join(inst.GameDir, name)
			if e.IsDir() {
				// files 模式目录：不建整目录 junction，文件级链接单独处理
				if isFilesMode(pc.DirLinks, name) {
					continue
				}
				results = append(results, s.linkDir(instSide, projSide, name, &newDirLinks))
			} else {
				results = append(results, s.linkFile(instSide, projSide, name, &newFileLinks))
			}
		}

		// files 模式目录：对清单文件逐个建硬链接（未选中的文件实例侧保持独立）
		for _, dl := range pc.DirLinks {
			if dl.Mode != "files" {
				continue
			}
			results = append(results, s.linkDirFiles(inst, entry.Path, dl)...)
		}

		// 持久化本次建链记录（有变化才写）
		if len(newDirLinks) != len(pc.DirLinks) || len(newFileLinks) != len(pc.FileLinks) {
			pc.DirLinks = newDirLinks
			pc.FileLinks = newFileLinks
			if err := appconfig.SaveProjectConfig(entry.Path, pc); err != nil {
				return err
			}
		}
		return nil
	})
	return results, err
}

// linkDir 为单个目录建 junction（实例侧链接指向项目侧目标）
func (s *PrismService) linkDir(instSide, projSide, name string, newDirLinks *[]appconfig.ProjectDirLink) prism.LinkResult {
	res := prism.LinkResult{Name: name, IsDir: true}
	// 已是 junction：指向本项目则视为已链，否则报告冲突
	if isJ, _ := s.junctions.IsJunction(instSide); isJ {
		if s.junctionTargets(instSide, projSide) {
			res.Status = "existing"
			appendDirLink(newDirLinks, name, name)
			return res
		}
		res.Status = "error"
		res.Detail = errs.New("err.junction.wrong_target", name).Error()
		return res
	}
	// 实例侧已有内容：空目录直接删除后建链（无内容可保留）；
	// 非空目录不自动处理（避免破坏游戏侧数据），标记为需手动链接——
	// 用户确认后由 ManualLinkDir 复制内容并入并建链
	switch state, err := inspectDirSide(instSide); {
	case err != nil:
		res.Status = "error"
		res.Detail = errs.NewDetail("err.toml.read", err.Error(), instSide).Error()
		return res
	case state == sideIsFile:
		res.Status = "error"
		res.Detail = errs.New("err.sync.dir_conflict", name).Error()
		return res
	case state == sideNonEmptyDir:
		res.Status = "manual"
		res.Detail = errs.New("err.sync.manual_required", name).Error()
		return res
	case state == sideEmptyDir:
		// 空目录：直接删除后继续建链
		if err := os.Remove(instSide); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.sync.remove_failed", err.Error(), name).Error()
			return res
		}
	}
	if !fsutil.IsDir(projSide) {
		res.Status = "skipped"
		res.Detail = errs.New("err.junction.target_missing", name).Error()
		return res
	}
	if err := s.junctions.Create(instSide, projSide); err != nil {
		res.Status = "error"
		res.Detail = errs.NewDetail("err.junction.create", err.Error(), name).Error()
		return res
	}
	res.Status = "linked"
	appendDirLink(newDirLinks, name, name)
	return res
}

// ManualLinkDir 手动链接单个目录（前端二次确认后调用）：
//   - 实例侧已是 junction 指向项目侧 → existing（幂等）
//   - 实例侧为空目录 → 直接删除后建链
//   - 实例侧非空 → 先将内容复制到项目目录（同名文件跳过，项目侧权威），
//     再删除实例侧目录建立 junction
//   - 实例侧不存在 → 直接建链
func (s *PrismService) ManualLinkDir(projectName, dir string) (prism.LinkResult, error) {
	res := prism.LinkResult{Name: dir, IsDir: true}
	dir, err := validateRelDir(dir)
	if err != nil {
		return res, err
	}
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return res, errs.New("err.proj.not_found", projectName)
	}
	err = appconfig.WithProjectConfigLock(entry.Path, func() error {
		pc, err := appconfig.LoadProjectConfig(entry.Path)
		if err != nil {
			return err
		}
		inst, err := s.linkedInstance(projectName, pc)
		if err != nil {
			return err
		}
		projSide := filepath.Join(entry.Path, dir)
		instSide := filepath.Join(inst.GameDir, filepath.FromSlash(dir))

		// 已是 junction：指向项目侧幂等返回，否则冲突
		if isJ, _ := s.junctions.IsJunction(instSide); isJ {
			if s.junctionTargets(instSide, projSide) {
				res.Status = "existing"
			} else {
				res.Status = "error"
				res.Detail = errs.New("err.junction.wrong_target", dir).Error()
				return nil
			}
			appendDirLink(&pc.DirLinks, dir, dir)
			return appconfig.SaveProjectConfig(entry.Path, pc)
		}

		// 实例侧已有内容：空目录直接删除；非空先复制到项目目录再删除
		switch state, err := inspectDirSide(instSide); {
		case err != nil:
			res.Status = "error"
			res.Detail = errs.NewDetail("err.toml.read", err.Error(), instSide).Error()
			return nil
		case state == sideIsFile:
			res.Status = "error"
			res.Detail = errs.New("err.sync.dir_conflict", dir).Error()
			return nil
		case state == sideNonEmptyDir:
			if err := fsutil.CopyDirMerge(instSide, projSide); err != nil {
				res.Status = "error"
				res.Detail = errs.NewDetail("err.sync.copy_failed", err.Error(), dir).Error()
				return nil
			}
			fallthrough
		case state == sideEmptyDir:
			if err := os.RemoveAll(instSide); err != nil {
				res.Status = "error"
				res.Detail = errs.NewDetail("err.sync.remove_failed", err.Error(), dir).Error()
				return nil
			}
		}

		if !fsutil.IsDir(projSide) {
			res.Status = "error"
			res.Detail = errs.New("err.junction.target_missing", dir).Error()
			return nil
		}
		if err := s.junctions.Create(instSide, projSide); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.junction.create", err.Error(), dir).Error()
			return nil
		}
		res.Status = "linked"
		appendDirLink(&pc.DirLinks, dir, dir)
		return appconfig.SaveProjectConfig(entry.Path, pc)
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// linkFile 为单个顶层文件建硬链接（同卷，无需管理员权限）
func (s *PrismService) linkFile(instSide, projSide, name string, newFileLinks *[]string) prism.LinkResult {
	res := hardlinkFile(projSide, instSide, name)
	if res.Status == "linked" {
		*newFileLinks = append(*newFileLinks, name)
	}
	return res
}

// appendDirLink 追加目录关联记录（已存在同名则不重复）
func appendDirLink(links *[]appconfig.ProjectDirLink, projectDir, instanceDir string) {
	for i := range *links {
		if (*links)[i].ProjectDir == projectDir {
			return
		}
	}
	*links = append(*links, appconfig.ProjectDirLink{ProjectDir: projectDir, InstanceDir: instanceDir})
}

// isFilesMode 判断目录关联是否为文件级同步模式
func isFilesMode(links []appconfig.ProjectDirLink, dir string) bool {
	for _, dl := range links {
		if dl.ProjectDir == dir && dl.Mode == "files" {
			return true
		}
	}
	return false
}

// linkDirFiles 为 files 模式目录的清单文件逐个建硬链接（项目 → 实例）。
// 项目侧文件缺失或实例侧已存在时跳过并报告，不中断其余文件。
func (s *PrismService) linkDirFiles(inst prism.Instance, projectPath string, dl appconfig.ProjectDirLink) []prism.LinkResult {
	results := make([]prism.LinkResult, 0, len(dl.Files))
	for _, f := range dl.Files {
		name := dl.ProjectDir + "/" + f
		projSide := filepath.Join(projectPath, dl.ProjectDir, filepath.FromSlash(f))
		instSide := filepath.Join(inst.GameDir, filepath.FromSlash(dl.InstanceDir), filepath.FromSlash(f))
		results = append(results, hardlinkFile(projSide, instSide, name))
	}
	return results
}

// removeDirLinkTargets 删除某目录关联已建立的链接：
// files 模式删除文件硬链接（仅当实例侧文件与项目侧文件仍是同一文件）并清理残留空目录；
// junction 模式删除目录链接（仅当指向项目侧）
func removeDirLinkTargets(jm junction.Manager, inst prism.Instance, projectPath string, dl appconfig.ProjectDirLink) error {
	gameDir := inst.GameDir
	instSide := filepath.Join(gameDir, filepath.FromSlash(dl.InstanceDir))
	if dl.Mode == "files" {
		for _, f := range dl.Files {
			instFile := filepath.Join(instSide, filepath.FromSlash(f))
			projFile := filepath.Join(projectPath, dl.ProjectDir, filepath.FromSlash(f))
			if hardlinkPointsTo(instFile, projFile) {
				_ = os.Remove(instFile)
			}
		}
		// 清理建链时创建的残留空目录结构（含用户内容的目录保留）
		fsutil.RemoveEmptyDirs(instSide)
		return nil
	}
	// junction：仅删除指向项目侧的链接
	if junctionPointsTo(jm, instSide, filepath.Join(projectPath, dl.ProjectDir)) {
		_ = jm.Remove(instSide)
	}
	return nil
}

// relinkDirAsJunction 以整目录 junction 重建某目录关联的链接：
// 实例侧已有内容时报手动处理提示，建链失败时透传错误。
func (s *PrismService) relinkDirAsJunction(inst prism.Instance, projectPath string, dl appconfig.ProjectDirLink, dir string, dirLinks *[]appconfig.ProjectDirLink) error {
	instSide := filepath.Join(inst.GameDir, filepath.FromSlash(dl.InstanceDir))
	projSide := filepath.Join(projectPath, dl.ProjectDir)
	r := s.linkDir(instSide, projSide, dir, dirLinks)
	if r.Status == "manual" {
		// 实例侧已有内容：切回 junction 需先手动处理
		return errs.New("err.sync.manual_required", dir)
	}
	if r.Status == "error" {
		return errs.NewDetail("err.junction.create", r.Detail, dir)
	}
	return nil
}

// SetDirLinkMode 切换目录关联的同步模式（""=整目录 junction / "files"=文件级）。
// 切换时自动重建链接：清理旧模式链接，按新模式建链；
// 切回 junction 且实例侧已有内容时返回错误提示先手动处理。
func (s *PrismService) SetDirLinkMode(projectName, dir, mode string) error {
	if mode != "" && mode != "files" {
		return errs.New("err.sync.invalid_mode", mode)
	}
	dir, err := validateRelDir(dir)
	if err != nil {
		return err
	}
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return errs.New("err.proj.not_found", projectName)
	}
	return appconfig.WithProjectConfigLock(entry.Path, func() error {
		pc, err := appconfig.LoadProjectConfig(entry.Path)
		if err != nil {
			return err
		}
		inst, err := s.linkedInstance(projectName, pc)
		if err != nil {
			return err
		}
		idx := findDirLinkIndex(pc.DirLinks, dir)
		if idx == -1 {
			return errs.New("err.sync.dir_not_linked", dir)
		}
		if pc.DirLinks[idx].Mode == mode {
			return nil // 无变化
		}

		// 清理旧模式链接
		if err := removeDirLinkTargets(s.junctions, inst, entry.Path, pc.DirLinks[idx]); err != nil {
			return err
		}
		// 更新模式并重建链接
		pc.DirLinks[idx].Mode = mode
		if mode == "files" {
			s.linkDirFiles(inst, entry.Path, pc.DirLinks[idx]) // 对已有 Files 清单建链（结果由前端后续刷新）
		} else {
			if err := s.relinkDirAsJunction(inst, entry.Path, pc.DirLinks[idx], dir, &pc.DirLinks); err != nil {
				return err
			}
		}
		return appconfig.SaveProjectConfig(entry.Path, pc)
	})
}

// SetDirLinkFiles 设置文件级同步的清单（自动切换为 files 模式并重建链接）。
// 清单为空时视为退出文件级模式（回到 junction）。
func (s *PrismService) SetDirLinkFiles(projectName, dir string, files []string) error {
	dir, err := validateRelDir(dir)
	if err != nil {
		return err
	}
	clean, err := normalizeFileListStrict(files)
	if err != nil {
		return err
	}
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return errs.New("err.proj.not_found", projectName)
	}
	return appconfig.WithProjectConfigLock(entry.Path, func() error {
		pc, err := appconfig.LoadProjectConfig(entry.Path)
		if err != nil {
			return err
		}
		inst, err := s.linkedInstance(projectName, pc)
		if err != nil {
			return err
		}
		idx := findDirLinkIndex(pc.DirLinks, dir)
		if idx == -1 {
			return errs.New("err.sync.dir_not_linked", dir)
		}

		// 清理旧链接（无论旧模式）
		if err := removeDirLinkTargets(s.junctions, inst, entry.Path, pc.DirLinks[idx]); err != nil {
			return err
		}

		pc.DirLinks[idx].Files = clean
		if len(clean) == 0 {
			// 空清单：退出文件级模式，回到整目录 junction
			pc.DirLinks[idx].Mode = ""
			if err := s.relinkDirAsJunction(inst, entry.Path, pc.DirLinks[idx], dir, &pc.DirLinks); err != nil {
				return err
			}
		} else {
			pc.DirLinks[idx].Mode = "files"
			s.linkDirFiles(inst, entry.Path, pc.DirLinks[idx])
		}
		return appconfig.SaveProjectConfig(entry.Path, pc)
	})
}

// SelectInstanceFiles 将实例侧选中的文件纳入文件级同步：
//  1. 实例侧文件移动到项目目录（成为项目权威内容；项目侧已有同名文件时跳过不覆盖）；
//     若文件已在项目侧（例如刚由整目录 junction 切换为文件级同步），跳过移动直接建链；
//  2. 移动后从项目目录建硬链接回实例侧（同步生效）；链接失败时回滚移动；
//  3. 仅把真正建链成功的文件记录到清单（mode=files），失败项不写清单。
//
// 实例侧目录为 junction（整目录模式）时拒绝——需先切换为文件级同步。
func (s *PrismService) SelectInstanceFiles(projectName, dir string, files []string) ([]prism.LinkResult, error) {
	dir, err := validateRelDir(dir)
	if err != nil {
		return nil, err
	}
	clean, err := normalizeFileListStrict(files)
	if err != nil {
		return nil, err
	}
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil, errs.New("err.proj.not_found", projectName)
	}

	results := make([]prism.LinkResult, 0, len(clean))
	err = appconfig.WithProjectConfigLock(entry.Path, func() error {
		pc, err := appconfig.LoadProjectConfig(entry.Path)
		if err != nil {
			return err
		}
		inst, err := s.linkedInstance(projectName, pc)
		if err != nil {
			return err
		}
		idx := findDirLinkIndex(pc.DirLinks, dir)
		if idx == -1 {
			return errs.New("err.sync.dir_not_linked", dir)
		}
		instDir := filepath.Join(inst.GameDir, filepath.FromSlash(dir))
		if isJ, _ := s.junctions.IsJunction(instDir); isJ {
			return errs.New("err.sync.dir_is_junction", dir)
		}

		linked := make([]string, 0, len(clean))
		for _, f := range clean {
			res := prism.LinkResult{Name: dir + "/" + f, IsDir: false}
			instSide := filepath.Join(instDir, filepath.FromSlash(f))
			projSide := filepath.Join(entry.Path, dir, filepath.FromSlash(f))
			instExists := fsutil.IsFile(instSide)
			projExists := fsutil.IsFile(projSide)

			switch {
			case instExists && projExists:
				// 项目侧已有同名文件：跳过不覆盖（两侧保持独立）
				res.Status = "skipped"
				res.Detail = errs.New("err.sync.file_conflict", res.Name).Error()
			case instExists && !projExists:
				// 移动到项目目录（同卷 Rename 原子移动；跨卷明确报错不破坏文件）
				if err := fsutil.MkdirAll(filepath.Dir(projSide)); err != nil {
					res.Status = "error"
					res.Detail = err.Error()
					results = append(results, res)
					continue
				}
				if err := os.Rename(instSide, projSide); err != nil {
					res.Status = "error"
					if isCrossDeviceError(err) {
						res.Detail = errs.New("err.sync.cross_volume", res.Name).Error()
					} else {
						res.Detail = errs.NewDetail("err.sync.move_failed", err.Error(), res.Name).Error()
					}
					results = append(results, res)
					continue
				}
				// 硬链接回实例侧（同步生效）；失败则回滚移动，避免实例侧文件消失
				if err := os.Link(projSide, instSide); err != nil {
					_ = os.Rename(projSide, instSide)
					res.Status = "error"
					res.Detail = errs.NewDetail("err.link.hardlink_failed", err.Error(), res.Name).Error()
					results = append(results, res)
					continue
				}
				res.Status = "linked"
				linked = append(linked, f)
			case !instExists && projExists:
				// 项目侧已是权威内容（junction 切 files 后的场景）：直接建硬链接
				if err := fsutil.MkdirAll(filepath.Dir(instSide)); err != nil {
					res.Status = "error"
					res.Detail = err.Error()
					results = append(results, res)
					continue
				}
				if err := os.Link(projSide, instSide); err != nil {
					res.Status = "error"
					res.Detail = errs.NewDetail("err.link.hardlink_failed", err.Error(), res.Name).Error()
					results = append(results, res)
					continue
				}
				res.Status = "linked"
				linked = append(linked, f)
			default:
				res.Status = "skipped"
				res.Detail = errs.New("err.junction.target_missing", res.Name).Error()
			}
			results = append(results, res)
		}

		// 更新配置：files 模式 + 只合并真正建链成功的清单（既有清单保持有序去重）
		pc.DirLinks[idx].Mode = "files"
		pc.DirLinks[idx].Files = mergeUniqueSorted(pc.DirLinks[idx].Files, linked)
		// 清理移动后残留的空目录结构（实例侧）
		fsutil.RemoveEmptyDirs(instDir)
		return appconfig.SaveProjectConfig(entry.Path, pc)
	})
	if err != nil {
		return results, err
	}
	return results, nil
}

// isCrossDeviceError 判断底层错误是否为“源与目标不在同一卷”
func isCrossDeviceError(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}

// ListDirFiles 递归列出项目目录下的全部文件（相对 projectDir，排除隐藏项），
// 供文件级同步的勾选界面使用
func (s *PrismService) ListDirFiles(projectName, dir string) ([]string, error) {
	dir, err := validateRelDir(dir)
	if err != nil {
		return nil, err
	}
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil, errs.New("err.proj.not_found", projectName)
	}
	root := filepath.Join(entry.Path, dir)
	out, err := fsutil.ListFilesRelative(root)
	if err != nil {
		return nil, errs.NewDetail("err.proj.invalid_path", err.Error(), root)
	}
	return out, nil
}

// ListInstanceDirFiles 递归列出实例侧游戏目录 <dir> 下的文件（相对 dir，排除隐藏项），
// 供文件级同步从目标侧选择要纳入同步的文件。
// 实例侧目录是 junction（整目录模式）时返回空列表——此时两侧为同一物理目录，无需选择；
// files 模式且实例侧目录不存在时（刚由 junction 切换过来），回退列出项目侧文件——
// 切换前两侧本就是同一物理目录，清单等价，保证“选择同步文件”流程可用。
func (s *PrismService) ListInstanceDirFiles(projectName, dir string) ([]string, error) {
	dir, err := validateRelDir(dir)
	if err != nil {
		return nil, err
	}
	lp, err := s.loadLinkedProject(projectName)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(lp.Inst.GameDir, filepath.FromSlash(dir))
	if isJ, _ := s.junctions.IsJunction(root); isJ {
		return nil, nil // 整目录模式：两侧同目录，无可选择文件
	}
	if !fsutil.IsDir(root) {
		if idx := findDirLinkIndex(lp.PC.DirLinks, dir); idx != -1 && lp.PC.DirLinks[idx].Mode == "files" {
			root = filepath.Join(lp.Entry.Path, dir) // 回退项目侧（junction 刚移除的场景）
		}
	}
	out, err := fsutil.ListFilesRelative(root)
	if err != nil {
		return nil, errs.NewDetail("err.proj.invalid_path", err.Error(), root)
	}
	return out, nil
}

// ListProjectDirs 返回项目根下的顶层目录名（排除 mods 与 .cache），
// 作为目录同步关联的候选列表
func (s *PrismService) ListProjectDirs(projectName string) ([]string, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil, errs.New("err.proj.not_found", projectName)
	}
	entries, err := os.ReadDir(entry.Path)
	if err != nil {
		return nil, errs.NewDetail("err.proj.invalid_path", err.Error(), entry.Path)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "mods" || strings.HasPrefix(e.Name(), ".") {
			continue // mods 走 meta 推送；. 开头为隐藏/系统目录（.git/.cache 等）
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}
