package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
	"packgradle/internal/junction"
	"packgradle/internal/packwiz"
	"packgradle/internal/pgignore"
	"packgradle/internal/prism"
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
			instSide := filepath.Join(inst.GameDir, filepath.FromSlash(dl.InstanceDir))
			if isJ, _ := s.junctions.IsJunction(instSide); isJ {
				_ = s.junctions.Remove(instSide)
			}
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
		instSide := filepath.Join(inst.GameDir, filepath.FromSlash(projectDir))
		if isJ, _ := s.junctions.IsJunction(instSide); isJ {
			if target, err := s.junctions.TargetOf(instSide); err == nil && samePath(target, filepath.Join(entry.Path, projectDir)) {
				_ = s.junctions.Remove(instSide)
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
			results = append(results, s.linkDir(instSide, projSide, name, &newDirLinks))
		} else {
			results = append(results, s.linkFile(instSide, projSide, name, &newFileLinks))
		}
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
	// 实例侧已有真实内容：跳过（不自动合并，避免破坏游戏侧数据）
	if _, err := os.Lstat(instSide); err == nil {
		res.Status = "skipped"
		res.Detail = errs.New("err.junction.link_occupied", name).Error()
		return res
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

// samePath 规范化比较两条路径是否指向同一位置（忽略大小写与分隔符差异）
func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
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
