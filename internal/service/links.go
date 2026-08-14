package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
	"packgradle/internal/fsutil"
	"packgradle/internal/pgignore"
	"packgradle/internal/prism"
)

// CreateAllLinks 一键关联：将项目根下所有未被 .pgignore 忽略的顶层条目建链。
// 目录 → junction（实例游戏目录/<name> 指向 项目/<name>）；文件 → 硬链接。
// mods 目录始终排除（走 meta 推送机制）；实例侧已有真实内容时跳过并报告。
// 返回逐条目结果，单条目失败不中断其余条目。
func (s *PrismService) CreateAllLinks(projectName string) ([]prism.LinkResult, error) {
	lp, err := s.loadLinkedProject(projectName)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(lp.Entry.Path)
	if err != nil {
		return nil, errs.NewDetail("err.proj.invalid_path", err.Error(), lp.Entry.Path)
	}
	// 实例游戏目录必须存在（新建实例可能尚无 minecraft/ 骨架）
	if err := fsutil.MkdirAll(lp.Inst.GameDir); err != nil {
		return nil, err
	}
	matcher := pgignore.Load(lp.Entry.Path)

	results := make([]prism.LinkResult, 0, len(entries))
	newDirLinks := append([]appconfig.ProjectDirLink{}, lp.PC.DirLinks...)
	newFileLinks := append([]string{}, lp.PC.FileLinks...)

	for _, e := range entries {
		name := e.Name()
		if name == "mods" && e.IsDir() {
			continue // mods 始终排除：meta 推送机制
		}
		if matcher.Matches(name) {
			continue // .pgignore 命中
		}
		projSide := filepath.Join(lp.Entry.Path, name)
		instSide := filepath.Join(lp.Inst.GameDir, name)
		if e.IsDir() {
			// files 模式目录：不建整目录 junction，文件级链接单独处理
			if isFilesMode(lp.PC.DirLinks, name) {
				continue
			}
			results = append(results, s.linkDir(instSide, projSide, name, &newDirLinks))
		} else {
			results = append(results, s.linkFile(instSide, projSide, name, &newFileLinks))
		}
	}

	// files 模式目录：对清单文件逐个建硬链接（未选中的文件实例侧保持独立）
	for _, dl := range lp.PC.DirLinks {
		if dl.Mode != "files" {
			continue
		}
		results = append(results, s.linkDirFiles(lp.Inst, lp.Entry.Path, dl)...)
	}

	// 持久化本次建链记录（有变化才写）
	if len(newDirLinks) != len(lp.PC.DirLinks) || len(newFileLinks) != len(lp.PC.FileLinks) {
		lp.PC.DirLinks = newDirLinks
		lp.PC.FileLinks = newFileLinks
		if err := appconfig.SaveProjectConfig(lp.Entry.Path, lp.PC); err != nil {
			return results, err
		}
	}
	return results, nil
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
	lp, err := s.loadLinkedProject(projectName)
	if err != nil {
		return res, err
	}
	projSide := filepath.Join(lp.Entry.Path, dir)
	instSide := filepath.Join(lp.Inst.GameDir, filepath.FromSlash(dir))

	// 已是 junction：指向项目侧幂等返回，否则冲突
	if isJ, _ := s.junctions.IsJunction(instSide); isJ {
		if s.junctionTargets(instSide, projSide) {
			res.Status = "existing"
		} else {
			res.Status = "error"
			res.Detail = errs.New("err.junction.wrong_target", dir).Error()
			return res, nil
		}
		appendDirLink(&lp.PC.DirLinks, dir, dir)
		return res, appconfig.SaveProjectConfig(lp.Entry.Path, lp.PC)
	}

	// 实例侧已有内容：空目录直接删除；非空先复制到项目目录再删除
	switch state, err := inspectDirSide(instSide); {
	case err != nil:
		res.Status = "error"
		res.Detail = errs.NewDetail("err.toml.read", err.Error(), instSide).Error()
		return res, nil
	case state == sideIsFile:
		res.Status = "error"
		res.Detail = errs.New("err.sync.dir_conflict", dir).Error()
		return res, nil
	case state == sideNonEmptyDir:
		if err := fsutil.CopyDirMerge(instSide, projSide); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.sync.copy_failed", err.Error(), dir).Error()
			return res, nil
		}
		fallthrough
	case state == sideEmptyDir:
		if err := os.RemoveAll(instSide); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.sync.remove_failed", err.Error(), dir).Error()
			return res, nil
		}
	}

	if !fsutil.IsDir(projSide) {
		res.Status = "error"
		res.Detail = errs.New("err.junction.target_missing", dir).Error()
		return res, nil
	}
	if err := s.junctions.Create(instSide, projSide); err != nil {
		res.Status = "error"
		res.Detail = errs.NewDetail("err.junction.create", err.Error(), dir).Error()
		return res, nil
	}
	res.Status = "linked"
	appendDirLink(&lp.PC.DirLinks, dir, dir)
	return res, appconfig.SaveProjectConfig(lp.Entry.Path, lp.PC)
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
// files 模式删除文件硬链接并清理残留空目录；junction 模式删除目录链接（仅当指向项目侧）
func (s *PrismService) removeDirLinkTargets(inst prism.Instance, projectPath string, dl appconfig.ProjectDirLink) error {
	gameDir := inst.GameDir
	instSide := filepath.Join(gameDir, filepath.FromSlash(dl.InstanceDir))
	if dl.Mode == "files" {
		for _, f := range dl.Files {
			_ = os.Remove(filepath.Join(instSide, filepath.FromSlash(f)))
		}
		// 清理建链时创建的残留空目录结构（含用户内容的目录保留）
		fsutil.RemoveEmptyDirs(instSide)
		return nil
	}
	// junction：仅删除指向项目侧的链接
	if s.junctionTargets(instSide, filepath.Join(projectPath, dl.ProjectDir)) {
		_ = s.junctions.Remove(instSide)
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
	lp, err := s.loadLinkedProject(projectName)
	if err != nil {
		return err
	}
	idx := findDirLinkIndex(lp.PC.DirLinks, dir)
	if idx == -1 {
		return errs.New("err.sync.dir_not_linked", dir)
	}
	if lp.PC.DirLinks[idx].Mode == mode {
		return nil // 无变化
	}

	// 清理旧模式链接
	if err := s.removeDirLinkTargets(lp.Inst, lp.Entry.Path, lp.PC.DirLinks[idx]); err != nil {
		return err
	}
	// 更新模式并重建链接
	lp.PC.DirLinks[idx].Mode = mode
	if mode == "files" {
		s.linkDirFiles(lp.Inst, lp.Entry.Path, lp.PC.DirLinks[idx]) // 对已有 Files 清单建链（结果由前端后续刷新）
	} else {
		if err := s.relinkDirAsJunction(lp.Inst, lp.Entry.Path, lp.PC.DirLinks[idx], dir, &lp.PC.DirLinks); err != nil {
			return err
		}
	}
	return appconfig.SaveProjectConfig(lp.Entry.Path, lp.PC)
}

// SetDirLinkFiles 设置文件级同步的清单（自动切换为 files 模式并重建链接）。
// 清单为空时视为退出文件级模式（回到 junction）。
func (s *PrismService) SetDirLinkFiles(projectName, dir string, files []string) error {
	lp, err := s.loadLinkedProject(projectName)
	if err != nil {
		return err
	}
	idx := findDirLinkIndex(lp.PC.DirLinks, dir)
	if idx == -1 {
		return errs.New("err.sync.dir_not_linked", dir)
	}

	// 清理旧链接（无论旧模式）
	if err := s.removeDirLinkTargets(lp.Inst, lp.Entry.Path, lp.PC.DirLinks[idx]); err != nil {
		return err
	}

	clean := normalizeFileList(files)
	lp.PC.DirLinks[idx].Files = clean
	if len(clean) == 0 {
		// 空清单：退出文件级模式，回到整目录 junction
		lp.PC.DirLinks[idx].Mode = ""
		if err := s.relinkDirAsJunction(lp.Inst, lp.Entry.Path, lp.PC.DirLinks[idx], dir, &lp.PC.DirLinks); err != nil {
			return err
		}
	} else {
		lp.PC.DirLinks[idx].Mode = "files"
		s.linkDirFiles(lp.Inst, lp.Entry.Path, lp.PC.DirLinks[idx])
	}
	return appconfig.SaveProjectConfig(lp.Entry.Path, lp.PC)
}

// SelectInstanceFiles 将实例侧选中的文件纳入文件级同步：
//  1. 从实例侧移动到项目目录（成为项目权威内容；项目侧已有同名文件时跳过不覆盖）
//  2. 移动后从项目目录建硬链接回实例侧（同步生效）
//  3. 记录到文件清单（mode=files）
//
// 实例侧目录为 junction（整目录模式）时拒绝——需先切换为文件级同步。
func (s *PrismService) SelectInstanceFiles(projectName, dir string, files []string) ([]prism.LinkResult, error) {
	lp, err := s.loadLinkedProject(projectName)
	if err != nil {
		return nil, err
	}

	idx := findDirLinkIndex(lp.PC.DirLinks, dir)
	if idx == -1 {
		return nil, errs.New("err.sync.dir_not_linked", dir)
	}
	instDir := filepath.Join(lp.Inst.GameDir, filepath.FromSlash(dir))
	if isJ, _ := s.junctions.IsJunction(instDir); isJ {
		return nil, errs.New("err.sync.dir_is_junction", dir)
	}

	clean := normalizeFileList(files)

	// 逐个：移动 → 硬链接 → 记录清单
	results := make([]prism.LinkResult, 0, len(clean))
	for _, f := range clean {
		res := prism.LinkResult{Name: dir + "/" + f, IsDir: false}
		instSide := filepath.Join(instDir, filepath.FromSlash(f))
		projSide := filepath.Join(lp.Entry.Path, dir, filepath.FromSlash(f))
		if !fsutil.IsFile(instSide) {
			res.Status = "skipped"
			res.Detail = errs.New("err.junction.target_missing", res.Name).Error()
			results = append(results, res)
			continue
		}
		if fsutil.IsFile(projSide) {
			res.Status = "skipped"
			res.Detail = errs.New("err.sync.file_conflict", res.Name).Error()
			results = append(results, res)
			continue
		}
		// 移动到项目目录（同卷 Rename 原子移动；异卷失败时报错）
		if err := fsutil.MkdirAll(filepath.Dir(projSide)); err != nil {
			res.Status = "error"
			res.Detail = err.Error()
			results = append(results, res)
			continue
		}
		if err := os.Rename(instSide, projSide); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.sync.move_failed", err.Error(), res.Name).Error()
			results = append(results, res)
			continue
		}
		// 硬链接回实例侧（同步生效）
		if err := os.Link(projSide, instSide); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.link.hardlink_failed", err.Error(), res.Name).Error()
			results = append(results, res)
			continue
		}
		res.Status = "linked"
		results = append(results, res)
	}

	// 更新配置：files 模式 + 合并清单（既有清单保持有序去重）
	lp.PC.DirLinks[idx].Mode = "files"
	lp.PC.DirLinks[idx].Files = mergeUniqueSorted(lp.PC.DirLinks[idx].Files, clean)
	// 清理移动后残留的空目录结构（实例侧）
	fsutil.RemoveEmptyDirs(instDir)
	if err := appconfig.SaveProjectConfig(lp.Entry.Path, lp.PC); err != nil {
		return results, err
	}
	return results, nil
}

// ListDirFiles 递归列出项目目录下的全部文件（相对 projectDir，排除隐藏项），
// 供文件级同步的勾选界面使用
func (s *PrismService) ListDirFiles(projectName, dir string) ([]string, error) {
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
// 实例侧目录是 junction（整目录模式）时返回空列表——此时两侧为同一物理目录，无需选择。
func (s *PrismService) ListInstanceDirFiles(projectName, dir string) ([]string, error) {
	lp, err := s.loadLinkedProject(projectName)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(lp.Inst.GameDir, filepath.FromSlash(dir))
	if isJ, _ := s.junctions.IsJunction(root); isJ {
		return nil, nil // 整目录模式：两侧同目录，无可选择文件
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
