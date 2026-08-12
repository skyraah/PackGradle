package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"packgradle/internal/appconfig"
	"packgradle/internal/curseforge"
	"packgradle/internal/errs"
	"packgradle/internal/junction"
	"packgradle/internal/packwiz"
	"packgradle/internal/pgignore"
	"packgradle/internal/prism"

	"github.com/BurntSushi/toml"
)

// PrismService 负责 Prism Launcher 实例的定位、扫描与管理。
// 当前阶段：实例路径检测与实例列表（REQ-3.2）+ 项目关联与一键建链
type PrismService struct {
	config    *appconfig.ConfigManager
	junctions junction.Manager
}

func NewPrismService(config *appconfig.ConfigManager) *PrismService {
	return &PrismService{config: config, junctions: junction.NewWindowsManager()}
}

// ListInstances 返回 Prism 实例列表。
// 定位链：%APPDATA%\PrismLauncher（配置文件所在）→ prismlauncher.cfg 的 InstanceDir
// → 扫描实例目录。单个实例解析失败不中断列表（错误落入 Instance.Error），
// 整体定位失败才返回错误。
func (s *PrismService) ListInstances() ([]prism.Instance, error) {
	instDir, err := s.findInstancesDir()
	if err != nil {
		return nil, err
	}
	return prism.ScanInstances(instDir), nil
}

// InstancesDir 返回当前定位到的实例根目录（供前端展示与确认）
func (s *PrismService) InstancesDir() (string, error) {
	return s.findInstancesDir()
}

// findInstancesDir 定位实例根目录，优先级：
//  1. config.toml 中用户手动指定的路径（prism_instances_path，覆盖自动定位）
//  2. 自动定位：%APPDATA%\PrismLauncher → prismlauncher.cfg 的 InstanceDir，成功后回写
//     config.toml 的 prism_instances_dir（持久化供查看/修改，值未变化时跳过写入）
//
// 手动路径失效（目录被删/改名）时静默回退自动定位；两者都失败才返回错误。
func (s *PrismService) findInstancesDir() (string, error) {
	if p := s.config.Get().PrismInstancesPath; p != "" {
		if isDir(p) {
			return p, nil
		}
		// 手动路径失效：回退自动定位
	}
	dataDir := prism.DataDir()
	if _, err := os.Stat(dataDir); err != nil {
		return "", errs.New("err.prism.not_found")
	}
	instDir, err := prism.InstancesDir(dataDir)
	if err != nil {
		return "", err
	}
	if !isDir(instDir) {
		return "", errs.NewDetail("err.prism.instances_dir_not_found", "目录不存在", instDir)
	}
	// 回写持久化（失败仅影响审计展示，不影响本次定位）
	_ = s.config.SetPrismInstancesDir(instDir)
	return instDir, nil
}

// SetInstancesPath 保存用户手动指定的实例根目录（空串清除，恢复自动定位）。
// 自动定位失败时前端据此引导用户手动输入路径。
func (s *PrismService) SetInstancesPath(path string) error {
	path = strings.TrimSpace(path)
	if path != "" && !isDir(path) {
		return errs.New("err.prism.path_invalid", path)
	}
	return s.config.SetPrismInstancesPath(path)
}

// GetInstancesPath 返回用户手动指定的实例根目录（空串 = 未指定，走自动定位）
func (s *PrismService) GetInstancesPath() string {
	return s.config.Get().PrismInstancesPath
}

// findProject 按名称查找项目并解析（pack.toml）
func (s *PrismService) findProject(projectName string) (packwiz.PackProject, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return packwiz.PackProject{}, errs.New("err.proj.not_found", projectName)
	}
	return packwiz.ParseProject(filepath.Join(entry.Path, "pack.toml"))
}

// scanInstancesSafe 扫描实例（定位失败返回 nil），供 GetLinks 等实时组装使用
func (s *PrismService) scanInstancesSafe() map[string]prism.Instance {
	instDir, err := s.findInstancesDir()
	if err != nil {
		return nil
	}
	out := map[string]prism.Instance{}
	for _, inst := range prism.ScanInstances(instDir) {
		out[inst.ID] = inst
	}
	return out
}

// ensureInstanceExists 校验实例 ID 存在于当前实例目录
func (s *PrismService) ensureInstanceExists(instanceID string) error {
	if _, ok := s.scanInstancesSafe()[instanceID]; ok {
		return nil
	}
	return errs.New("err.prism.instance_not_found", instanceID)
}

// LinkProject 关联 packwiz 项目到 Prism 实例（一项目一实例，重复关联覆盖）。
// 实例须存在于当前实例目录；关联持久化在项目目录下的 packgradle.toml。
func (s *PrismService) LinkProject(projectName, instanceID string) error {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return errs.New("err.proj.not_found", projectName)
	}
	if err := s.ensureInstanceExists(instanceID); err != nil {
		return err
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return err
	}
	pc.Instance = instanceID
	return appconfig.SaveProjectConfig(entry.Path, pc)
}

// UnlinkProject 解除项目关联（连同其目录关联与已建链接）
func (s *PrismService) UnlinkProject(projectName string) error {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return err
	}
	if pc.Instance == "" {
		return errs.New("err.link.not_found", projectName)
	}
	// 先删除已建链接（junction 与硬链接），实例已不存在时跳过
	if inst, ok := s.scanInstancesSafe()[pc.Instance]; ok {
		for _, dl := range pc.DirLinks {
			_ = s.removeDirLinkTargets(inst, entry.Path, dl)
		}
		for _, f := range pc.FileLinks {
			_ = os.Remove(filepath.Join(inst.GameDir, filepath.FromSlash(f))) // 硬链接删除只减引用
		}
	}
	pc.Instance = ""
	pc.DirLinks = nil
	pc.FileLinks = nil
	return appconfig.SaveProjectConfig(entry.Path, pc)
}

// GetLinks 返回全部项目 ↔ 实例关联的组装视图（读取各项目 packgradle.toml，
// 实时扫描实例，实例被删时标记失效）
func (s *PrismService) GetLinks() []prism.LinkView {
	instances := s.scanInstancesSafe()
	var views []prism.LinkView
	for _, e := range s.config.Get().Projects {
		pc, err := appconfig.LoadProjectConfig(e.Path)
		if err != nil || pc.Instance == "" {
			continue
		}
		view := prism.LinkView{Project: e.Name, ProjectPath: e.Path, InstanceID: pc.Instance}
		if inst, ok := instances[pc.Instance]; ok {
			view.InstanceName = inst.Name
			view.InstancePath = inst.Path
			view.InstanceValid = inst.Error == ""
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Project < views[j].Project })
	return views
}

// CreateInstance 为项目程序创建 Prism 实例（组件取自项目 pack.toml 的版本信息）。
// 创建成功后可调用 LinkProject 完成关联。
func (s *PrismService) CreateInstance(projectName string) (prism.Instance, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return prism.Instance{}, err
	}
	if proj.Error != "" {
		return prism.Instance{}, errs.New("err.proj.not_found", projectName)
	}
	instDir, err := s.findInstancesDir()
	if err != nil {
		return prism.Instance{}, err
	}
	return prism.CreateMinimalInstance(instDir, prism.CreateRequest{
		Name:             proj.Name,
		Minecraft:        proj.Minecraft,
		Modloader:        proj.Modloader,
		ModloaderVersion: proj.ModloaderVersion,
	})
}

// AddDirLink 添加目录关联对：项目侧目录名 + 实例侧相对游戏目录路径（默认同名）。
// 要求项目已关联实例、项目侧目录存在（实例侧目录可稍后由 junction 阶段创建）。
func (s *PrismService) AddDirLink(projectName, projectDir string) error {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return err
	}
	if pc.Instance == "" {
		return errs.New("err.link.not_found", projectName)
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return errs.New("err.sync.empty_dir")
	}
	if !isDir(filepath.Join(entry.Path, projectDir)) {
		return errs.New("err.sync.dir_not_exists", projectDir)
	}
	for i := range pc.DirLinks {
		if pc.DirLinks[i].ProjectDir == projectDir {
			pc.DirLinks[i].InstanceDir = projectDir // 默认同名（相对实例游戏目录）
			return appconfig.SaveProjectConfig(entry.Path, pc)
		}
	}
	pc.DirLinks = append(pc.DirLinks, appconfig.ProjectDirLink{
		ProjectDir:  projectDir,
		InstanceDir: projectDir,
	})
	return appconfig.SaveProjectConfig(entry.Path, pc)
}

// RemoveDirLink 移除目录关联对，并删除已建链接（仅链接本身，目标内容不动）
func (s *PrismService) RemoveDirLink(projectName, projectDir string) error {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return err
	}
	// 删除实例侧链接（若已建立且指向本项目）
	if inst, ok := s.scanInstancesSafe()[pc.Instance]; ok {
		for _, dl := range pc.DirLinks {
			if dl.ProjectDir == projectDir {
				_ = s.removeDirLinkTargets(inst, entry.Path, dl)
			}
		}
	}
	out := pc.DirLinks[:0]
	for _, dl := range pc.DirLinks {
		if dl.ProjectDir != projectDir {
			out = append(out, dl)
		}
	}
	pc.DirLinks = out
	return appconfig.SaveProjectConfig(entry.Path, pc)
}

// removeDirLinkTargets 删除某目录关联已建立的链接：
// files 模式删除文件硬链接并清理残留空目录；junction 模式删除目录链接（仅当指向项目侧）
func (s *PrismService) removeDirLinkTargets(inst prism.Instance, projectPath string, dl appconfig.ProjectDirLink) error {
	gameDir := inst.GameDir
	if dl.Mode == "files" {
		instDir := filepath.Join(gameDir, filepath.FromSlash(dl.InstanceDir))
		for _, f := range dl.Files {
			_ = os.Remove(filepath.Join(instDir, filepath.FromSlash(f)))
		}
		// 清理建链时创建的残留空目录结构（含用户内容的目录保留）
		removeEmptyDirs(instDir)
		return nil
	}
	// junction：仅删除指向项目侧的链接
	instSide := filepath.Join(gameDir, filepath.FromSlash(dl.InstanceDir))
	if isJ, _ := s.junctions.IsJunction(instSide); isJ {
		if target, err := s.junctions.TargetOf(instSide); err == nil && samePath(target, filepath.Join(projectPath, dl.ProjectDir)) {
			_ = s.junctions.Remove(instSide)
		}
	}
	return nil
}

// removeEmptyDirs 自底向上删除完全空的目录（含任何内容的目录保留不动）
func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // 非空目录删除失败，静默跳过
	}
}

// HasPGIgnore 检查项目是否已有 .pgignore 文件（一键关联前询问用）
func (s *PrismService) HasPGIgnore(projectName string) (bool, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return false, errs.New("err.proj.not_found", projectName)
	}
	_, err := os.Stat(filepath.Join(entry.Path, ".pgignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errs.NewDetail("err.toml.read", err.Error(), ".pgignore")
	}
	return true, nil
}

// EnsurePGIgnore 确保项目存在 .pgignore（已存在不覆盖），返回是否新建。
// 一键关联前前端据此询问用户是否生成默认忽略规则。
func (s *PrismService) EnsurePGIgnore(projectName string) (bool, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return false, errs.New("err.proj.not_found", projectName)
	}
	created, err := pgignore.Ensure(entry.Path)
	if err != nil {
		return false, errs.NewDetail("err.file.write", err.Error(), ".pgignore")
	}
	return created, nil
}

// CreateAllLinks 一键关联：将项目根下所有未被 .pgignore 忽略的顶层条目建链。
// 目录 → junction（实例游戏目录/<name> 指向 项目/<name>）；文件 → 硬链接。
// mods 目录始终排除（走 meta 推送机制）；实例侧已有真实内容时跳过并报告。
// 返回逐条目结果，单条目失败不中断其余条目。
func (s *PrismService) CreateAllLinks(projectName string) ([]prism.LinkResult, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil, errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return nil, err
	}
	if pc.Instance == "" {
		return nil, errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return nil, errs.New("err.prism.instance_not_found", pc.Instance)
	}
	entries, err := os.ReadDir(entry.Path)
	if err != nil {
		return nil, errs.NewDetail("err.proj.invalid_path", err.Error(), entry.Path)
	}
	// 实例游戏目录必须存在（新建实例可能尚无 minecraft/ 骨架）
	if err := os.MkdirAll(inst.GameDir, 0o755); err != nil {
		return nil, errs.NewDetail("err.file.mkdir", err.Error(), inst.GameDir)
	}
	matcher := pgignore.Load(entry.Path)

	results := make([]prism.LinkResult, 0, len(entries))
	newDirLinks := append([]appconfig.ProjectDirLink{}, pc.DirLinks...)
	newFileLinks := append([]string{}, pc.FileLinks...)

	for _, e := range entries {
		name := e.Name()
		if name == "mods" && e.IsDir() {
			continue // mods 始终排除：meta 推送机制
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
		if target, err := s.junctions.TargetOf(instSide); err == nil && samePath(target, projSide) {
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
	if _, err := os.Lstat(instSide); err == nil {
		if !isDir(instSide) {
			res.Status = "error"
			res.Detail = errs.New("err.sync.dir_conflict", name).Error()
			return res
		}
		entries, rerr := os.ReadDir(instSide)
		if rerr != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.toml.read", rerr.Error(), instSide).Error()
			return res
		}
		if len(entries) > 0 {
			res.Status = "manual"
			res.Detail = errs.New("err.sync.manual_required", name).Error()
			return res
		}
		// 空目录：直接删除后继续建链
		if err := os.Remove(instSide); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.sync.remove_failed", err.Error(), name).Error()
			return res
		}
	}
	if !isDir(projSide) {
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
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return res, errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return res, err
	}
	if pc.Instance == "" {
		return res, errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return res, errs.New("err.prism.instance_not_found", pc.Instance)
	}
	projSide := filepath.Join(entry.Path, dir)
	instSide := filepath.Join(inst.GameDir, filepath.FromSlash(dir))

	// 已是 junction：指向项目侧幂等返回，否则冲突
	if isJ, _ := s.junctions.IsJunction(instSide); isJ {
		if target, err := s.junctions.TargetOf(instSide); err == nil && samePath(target, projSide) {
			res.Status = "existing"
		} else {
			res.Status = "error"
			res.Detail = errs.New("err.junction.wrong_target", dir).Error()
			return res, nil
		}
		appendDirLink(&pc.DirLinks, dir, dir)
		return res, appconfig.SaveProjectConfig(entry.Path, pc)
	}

	// 实例侧已有内容：空目录直接删除；非空先复制到项目目录再删除
	if _, err := os.Lstat(instSide); err == nil {
		if !isDir(instSide) {
			res.Status = "error"
			res.Detail = errs.New("err.sync.dir_conflict", dir).Error()
			return res, nil
		}
		entries, rerr := os.ReadDir(instSide)
		if rerr != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.toml.read", rerr.Error(), instSide).Error()
			return res, nil
		}
		if len(entries) > 0 {
			if err := copyDirRecursive(instSide, projSide); err != nil {
				res.Status = "error"
				res.Detail = errs.NewDetail("err.sync.copy_failed", err.Error(), dir).Error()
				return res, nil
			}
		}
		if err := os.RemoveAll(instSide); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.sync.remove_failed", err.Error(), dir).Error()
			return res, nil
		}
	}

	if !isDir(projSide) {
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
	appendDirLink(&pc.DirLinks, dir, dir)
	return res, appconfig.SaveProjectConfig(entry.Path, pc)
}

// copyDirRecursive 递归复制 src 目录内容到 dst（dst 不存在时自动创建）。
// 同名文件跳过不覆盖——项目侧为权威，实例侧内容仅并入缺失部分。
func copyDirRecursive(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDirRecursive(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if _, err := os.Lstat(dstPath); err == nil {
			continue // 同名文件跳过，保留项目侧内容
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// linkFile 为单个顶层文件建硬链接（同卷，无需管理员权限）
func (s *PrismService) linkFile(instSide, projSide, name string, newFileLinks *[]string) prism.LinkResult {
	res := prism.LinkResult{Name: name, IsDir: false}
	if _, err := os.Lstat(instSide); err == nil {
		res.Status = "skipped"
		res.Detail = errs.New("err.link.file_exists", name).Error()
		return res
	}
	if err := os.Link(projSide, instSide); err != nil {
		res.Status = "error"
		res.Detail = errs.NewDetail("err.link.hardlink_failed", err.Error(), name).Error()
		return res
	}
	res.Status = "linked"
	*newFileLinks = append(*newFileLinks, name)
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
	var results []prism.LinkResult
	for _, f := range dl.Files {
		res := prism.LinkResult{Name: dl.ProjectDir + "/" + f, IsDir: false}
		projSide := filepath.Join(projectPath, dl.ProjectDir, filepath.FromSlash(f))
		instSide := filepath.Join(inst.GameDir, filepath.FromSlash(dl.InstanceDir), filepath.FromSlash(f))
		if !isFile(projSide) {
			res.Status = "skipped"
			res.Detail = errs.New("err.junction.target_missing", res.Name).Error()
			results = append(results, res)
			continue
		}
		if _, err := os.Lstat(instSide); err == nil {
			res.Status = "skipped"
			res.Detail = errs.New("err.link.file_exists", res.Name).Error()
			results = append(results, res)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(instSide), 0o755); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.file.mkdir", err.Error(), res.Name).Error()
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
		results = append(results, res)
	}
	return results
}

// isFile 判断路径是否为普通文件
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// SetDirLinkMode 切换目录关联的同步模式（""=整目录 junction / "files"=文件级）。
// 切换时自动重建链接：清理旧模式链接，按新模式建链；
// 切回 junction 且实例侧已有内容时返回错误提示先手动处理。
func (s *PrismService) SetDirLinkMode(projectName, dir, mode string) error {
	if mode != "" && mode != "files" {
		return errs.New("err.sync.invalid_mode", mode)
	}
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return err
	}
	if pc.Instance == "" {
		return errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return errs.New("err.prism.instance_not_found", pc.Instance)
	}
	idx := -1
	for i := range pc.DirLinks {
		if pc.DirLinks[i].ProjectDir == dir {
			idx = i
			break
		}
	}
	if idx == -1 {
		return errs.New("err.sync.dir_not_linked", dir)
	}
	if pc.DirLinks[idx].Mode == mode {
		return nil // 无变化
	}

	// 清理旧模式链接
	if err := s.removeDirLinkTargets(inst, entry.Path, pc.DirLinks[idx]); err != nil {
		return err
	}
	// 更新模式并重建链接
	pc.DirLinks[idx].Mode = mode
	if mode == "files" {
		s.linkDirFiles(inst, entry.Path, pc.DirLinks[idx]) // 对已有 Files 清单建链（结果由前端后续刷新）
	} else {
		instSide := filepath.Join(inst.GameDir, filepath.FromSlash(pc.DirLinks[idx].InstanceDir))
		projSide := filepath.Join(entry.Path, pc.DirLinks[idx].ProjectDir)
		r := s.linkDir(instSide, projSide, dir, &pc.DirLinks)
		if r.Status == "manual" {
			// 实例侧已有内容：切回 junction 需先手动处理
			return errs.New("err.sync.manual_required", dir)
		}
		if r.Status == "error" {
			return errs.NewDetail("err.junction.create", r.Detail, dir)
		}
	}
	return appconfig.SaveProjectConfig(entry.Path, pc)
}

// SetDirLinkFiles 设置文件级同步的清单（自动切换为 files 模式并重建链接）。
// 清单为空时视为退出文件级模式（回到 junction）。
func (s *PrismService) SetDirLinkFiles(projectName, dir string, files []string) error {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return err
	}
	if pc.Instance == "" {
		return errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return errs.New("err.prism.instance_not_found", pc.Instance)
	}
	idx := -1
	for i := range pc.DirLinks {
		if pc.DirLinks[i].ProjectDir == dir {
			idx = i
			break
		}
	}
	if idx == -1 {
		return errs.New("err.sync.dir_not_linked", dir)
	}

	// 清理旧链接（无论旧模式）
	if err := s.removeDirLinkTargets(inst, entry.Path, pc.DirLinks[idx]); err != nil {
		return err
	}

	// 去重排序清单
	seen := map[string]bool{}
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

	pc.DirLinks[idx].Files = clean
	if len(clean) == 0 {
		// 空清单：退出文件级模式，回到整目录 junction
		pc.DirLinks[idx].Mode = ""
		instSide := filepath.Join(inst.GameDir, filepath.FromSlash(pc.DirLinks[idx].InstanceDir))
		projSide := filepath.Join(entry.Path, pc.DirLinks[idx].ProjectDir)
		r := s.linkDir(instSide, projSide, dir, &pc.DirLinks)
		if r.Status == "manual" {
			return errs.New("err.sync.manual_required", dir)
		}
		if r.Status == "error" {
			return errs.NewDetail("err.junction.create", r.Detail, dir)
		}
	} else {
		pc.DirLinks[idx].Mode = "files"
		s.linkDirFiles(inst, entry.Path, pc.DirLinks[idx])
	}
	return appconfig.SaveProjectConfig(entry.Path, pc)
}

// ListDirFiles 递归列出项目目录下的全部文件（相对 projectDir，排除隐藏项），
// 供文件级同步的勾选界面使用
func (s *PrismService) ListDirFiles(projectName, dir string) ([]string, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil, errs.New("err.proj.not_found", projectName)
	}
	root := filepath.Join(entry.Path, dir)
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 单文件错误跳过
		}
		if path == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // 隐藏文件跳过
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, errs.NewDetail("err.proj.invalid_path", err.Error(), root)
	}
	sort.Strings(out)
	return out, nil
}

// ListInstanceDirFiles 递归列出实例侧游戏目录 <dir> 下的文件（相对 dir，排除隐藏项），
// 供文件级同步从目标侧选择要纳入同步的文件。
// 实例侧目录是 junction（整目录模式）时返回空列表——此时两侧为同一物理目录，无需选择。
func (s *PrismService) ListInstanceDirFiles(projectName, dir string) ([]string, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil, errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return nil, err
	}
	if pc.Instance == "" {
		return nil, errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return nil, errs.New("err.prism.instance_not_found", pc.Instance)
	}
	root := filepath.Join(inst.GameDir, filepath.FromSlash(dir))
	if isJ, _ := s.junctions.IsJunction(root); isJ {
		return nil, nil // 整目录模式：两侧同目录，无可选择文件
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, errs.NewDetail("err.proj.invalid_path", err.Error(), root)
	}
	sort.Strings(out)
	return out, nil
}

// SelectInstanceFiles 将实例侧选中的文件纳入文件级同步：
//  1. 从实例侧移动到项目目录（成为项目权威内容；项目侧已有同名文件时跳过不覆盖）
//  2. 移动后从项目目录建硬链接回实例侧（同步生效）
//  3. 记录到文件清单（mode=files）
//
// 实例侧目录为 junction（整目录模式）时拒绝——需先切换为文件级同步。
func (s *PrismService) SelectInstanceFiles(projectName, dir string, files []string) ([]prism.LinkResult, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil, errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return nil, err
	}
	if pc.Instance == "" {
		return nil, errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return nil, errs.New("err.prism.instance_not_found", pc.Instance)
	}

	// 找到该目录关联并切换 files 模式
	idx := -1
	for i := range pc.DirLinks {
		if pc.DirLinks[i].ProjectDir == dir {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, errs.New("err.sync.dir_not_linked", dir)
	}
	instDir := filepath.Join(inst.GameDir, filepath.FromSlash(dir))
	if isJ, _ := s.junctions.IsJunction(instDir); isJ {
		return nil, errs.New("err.sync.dir_is_junction", dir)
	}

	// 去重排序清单
	seen := map[string]bool{}
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

	// 逐个：移动 → 硬链接 → 记录清单
	results := make([]prism.LinkResult, 0, len(clean))
	newFiles := append([]string{}, pc.DirLinks[idx].Files...)
	for _, f := range clean {
		res := prism.LinkResult{Name: dir + "/" + f, IsDir: false}
		instSide := filepath.Join(instDir, filepath.FromSlash(f))
		projSide := filepath.Join(entry.Path, dir, filepath.FromSlash(f))
		if !isFile(instSide) {
			res.Status = "skipped"
			res.Detail = errs.New("err.junction.target_missing", res.Name).Error()
			results = append(results, res)
			continue
		}
		if isFile(projSide) {
			res.Status = "skipped"
			res.Detail = errs.New("err.sync.file_conflict", res.Name).Error()
			results = append(results, res)
			continue
		}
		// 移动到项目目录（同卷 Rename 原子移动；异卷失败时报错）
		if err := os.MkdirAll(filepath.Dir(projSide), 0o755); err != nil {
			res.Status = "error"
			res.Detail = errs.NewDetail("err.file.mkdir", err.Error(), res.Name).Error()
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
		newFiles = append(newFiles, f)
	}

	// 更新配置：files 模式 + 合并清单
	sort.Strings(newFiles)
	dedup := newFiles[:0]
	last := ""
	for _, f := range newFiles {
		if f != last {
			dedup = append(dedup, f)
			last = f
		}
	}
	pc.DirLinks[idx].Mode = "files"
	pc.DirLinks[idx].Files = dedup
	// 清理移动后残留的空目录结构（实例侧）
	removeEmptyDirs(instDir)
	if err := appconfig.SaveProjectConfig(entry.Path, pc); err != nil {
		return results, err
	}
	return results, nil
}

// ListDirLinks 返回某项目的目录关联对（含两侧目录实态）
func (s *PrismService) ListDirLinks(projectName string) []prism.DirLinkView {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return nil
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return nil
	}
	instances := s.scanInstancesSafe()
	var out []prism.DirLinkView
	for _, dl := range pc.DirLinks {
		view := prism.DirLinkView{
			Project:     entry.Name,
			Instance:    pc.Instance,
			ProjectDir:  dl.ProjectDir,
			InstanceDir: dl.InstanceDir,
			Mode:        dl.Mode,
			Files:       dl.Files,
		}
		view.ProjectExists = isDir(filepath.Join(entry.Path, dl.ProjectDir))
		if inst, ok := instances[pc.Instance]; ok {
			view.InstanceExists = isDir(filepath.Join(inst.GameDir, filepath.FromSlash(dl.InstanceDir)))
		}
		out = append(out, view)
	}
	return out
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// metaPushContext 组装单个 mod 推送所需的项目级信息
type metaPushContext struct {
	loaders    string
	mcVersions string
	cache      map[string]curseforge.CfFileCache
}

// PushMeta 将项目 mod 元数据推送到实例 mods/.index（Prism 兼容格式）：
// 每个 mod 的 pw.toml 在 side 条目后插入 x-prismlauncher-* 四个扩展字段
// （loaders/mc-versions/release-type/version-number），供 Prism 识别。
// modID 非空时仅推送该 mod（空串推送全部）。mods 目录本身不建 junction（meta 推送机制）。
func (s *PrismService) PushMeta(projectName, modID string) (int, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return 0, err
	}
	if proj.Error != "" {
		return 0, errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(proj.Path)
	if err != nil {
		return 0, err
	}
	if pc.Instance == "" {
		return 0, errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return 0, errs.New("err.prism.instance_not_found", pc.Instance)
	}
	indexDir := filepath.Join(inst.GameDir, "mods", ".index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return 0, errs.NewDetail("err.file.mkdir", err.Error(), indexDir)
	}

	// 项目级推送上下文（loader/mc 版本 + 版本缓存用于 release-type）
	ctx := metaPushContext{
		loaders:    loaderMeta(proj),
		mcVersions: proj.Minecraft,
	}
	if cache, err := curseforge.NewCfCacheStore(filepath.Join(proj.Path, ".cache")).Load(); err == nil {
		ctx.cache = cache
	}

	count := 0
	for _, mod := range proj.Mods {
		if modID != "" && mod.ID != modID {
			continue // 仅推送指定 mod
		}
		if mod.Path == "" {
			continue // 元数据文件缺失的条目（索引保留展示），跳过
		}
		content, err := os.ReadFile(mod.Path)
		if err != nil {
			continue
		}
		out, err := prism.ToPrismFormat(content, prism.PrismMeta{
			Loaders:     ctx.loaders,
			MCVersions:  ctx.mcVersions,
			ReleaseType: releaseTypeMeta(ctx, mod),
			Version:     mod.Version,
		})
		if err != nil {
			continue
		}
		target := filepath.Join(indexDir, mod.ID+".pw.toml")
		if err := os.WriteFile(target, out, 0o644); err != nil {
			return count, errs.NewDetail("err.file.write", err.Error(), target)
		}
		count++
	}
	return count, nil
}

// PullMeta 将实例 mods/.index 的元数据拉回项目 mods 目录（packwiz 格式）：
// 删除 x-prismlauncher-* 扩展字段与 [download] 表中的 url 条目。
// modID 非空时仅拉取该 mod（空串拉取全部）。拉回后需运行 packwiz refresh 收录。
func (s *PrismService) PullMeta(projectName, modID string) (int, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return 0, err
	}
	if proj.Error != "" {
		return 0, errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(proj.Path)
	if err != nil {
		return 0, err
	}
	if pc.Instance == "" {
		return 0, errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return 0, errs.New("err.prism.instance_not_found", pc.Instance)
	}
	indexDir := filepath.Join(inst.GameDir, "mods", ".index")
	entries, err := os.ReadDir(indexDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // 实例侧尚无 .index
		}
		return 0, errs.NewDetail("err.toml.read", err.Error(), indexDir)
	}
	modsDir := filepath.Join(proj.Path, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		return 0, errs.NewDetail("err.file.mkdir", err.Error(), modsDir)
	}

	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".pw.toml") {
			continue
		}
		if modID != "" && strings.TrimSuffix(e.Name(), ".pw.toml") != modID {
			continue // 仅拉取指定 mod
		}
		content, err := os.ReadFile(filepath.Join(indexDir, e.Name()))
		if err != nil {
			continue
		}
		out, err := prism.FromPrismFormat(content)
		if err != nil {
			continue
		}
		target := filepath.Join(modsDir, e.Name())
		if err := os.WriteFile(target, out, 0o644); err != nil {
			return count, errs.NewDetail("err.file.write", err.Error(), target)
		}
		count++
	}
	return count, nil
}

// MetaDiff 计算项目 ↔ 实例 mods 的元数据差异并刷新缓存：
// 项目侧以 index.toml 权威列表为准，实例侧扫描 mods/.index 的 pw.toml。
// 每次「查看差异」时调用（重新计算 + 写入 .cache/metadiff.cache），
// 不做实时监听，避免目录变化带来的性能开销。
func (s *PrismService) MetaDiff(projectName string) (prism.MetaDiff, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return prism.MetaDiff{}, err
	}
	if proj.Error != "" {
		return prism.MetaDiff{}, errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(proj.Path)
	if err != nil {
		return prism.MetaDiff{}, err
	}
	if pc.Instance == "" {
		return prism.MetaDiff{}, errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return prism.MetaDiff{}, errs.New("err.prism.instance_not_found", pc.Instance)
	}
	indexDir := filepath.Join(inst.GameDir, "mods", ".index")

	// 实例侧：mod id → 版本（.index/*.pw.toml）
	instanceMods := map[string]string{}
	if entries, err := os.ReadDir(indexDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".pw.toml") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".pw.toml")
			instanceMods[id] = parseMetaVersion(filepath.Join(indexDir, e.Name()))
		}
	}

	// 项目侧：index.toml 权威列表
	projectMods := map[string]string{}
	for _, m := range proj.Mods {
		projectMods[m.ID] = m.Version
	}

	diff := prism.MetaDiff{FetchedAt: time.Now().Format(time.RFC3339)}
	for id := range instanceMods {
		if _, ok := projectMods[id]; !ok {
			diff.InstanceOnly = append(diff.InstanceOnly, id)
		}
	}
	for id := range projectMods {
		if _, ok := instanceMods[id]; !ok {
			diff.ProjectOnly = append(diff.ProjectOnly, id)
		}
	}
	for id, pv := range projectMods {
		if iv, ok := instanceMods[id]; ok && pv != "" && pv != iv {
			diff.VersionDiff = append(diff.VersionDiff, prism.VersionDiffItem{
				ID: id, ProjectVersion: pv, InstanceVersion: iv,
			})
		}
	}
	sort.Strings(diff.InstanceOnly)
	sort.Strings(diff.ProjectOnly)
	sort.Slice(diff.VersionDiff, func(i, j int) bool { return diff.VersionDiff[i].ID < diff.VersionDiff[j].ID })

	// 刷新缓存（写失败不阻断展示——缓存仅用于持久化与离线读取）
	_ = appconfig.WriteTomlAtomic(filepath.Join(proj.Path, ".cache", "metadiff.cache"), diff)
	return diff, nil
}

// parseMetaVersion 轻量解析 pw.toml 的版本号：
// x-prismlauncher-version-number（Prism 侧）> 顶层 version > [update.*] 版本
func parseMetaVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var raw struct {
		Version      string                     `toml:"version"`
		PrismVersion string                     `toml:"x-prismlauncher-version-number"`
		Update       map[string]map[string]any `toml:"update"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return ""
	}
	if raw.PrismVersion != "" {
		return raw.PrismVersion
	}
	if raw.Version != "" {
		return raw.Version
	}
	for _, src := range []string{"modrinth", "fabric", "forge", "neoforge", "quilt", "liteloader", "curseforge"} {
		if v, ok := raw.Update[src]["version"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// loaderMeta 组装 Prism 的加载器字段（"forge:47.4.10"；无加载器为空串）
func loaderMeta(proj packwiz.PackProject) string {
	if proj.Modloader == "" {
		return ""
	}
	if proj.ModloaderVersion != "" {
		return proj.Modloader + ":" + proj.ModloaderVersion
	}
	return proj.Modloader
}

// releaseTypeMeta 从版本缓存取发布类型（1=正式 2=测试 3=Alpha），无缓存默认 release
func releaseTypeMeta(ctx metaPushContext, mod packwiz.ModInfo) string {
	if ctx.cache != nil && mod.CfProjectID > 0 && mod.CfFileID > 0 {
		if entry, ok := ctx.cache[curseforge.CacheKey(mod.CfProjectID, mod.CfFileID)]; ok {
			switch entry.ReleaseType {
			case 2:
				return "beta"
			case 3:
				return "alpha"
			}
		}
	}
	return "release"
}

// samePath 规范化比较两条路径是否指向同一位置（忽略大小写与分隔符差异）
func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}
