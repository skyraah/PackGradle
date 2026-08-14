package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
	"packgradle/internal/fsutil"
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

// findInstancesDir 定位实例根目录（定位链见 resolveInstancesDir）。
// 自动定位成功后回写 config.toml 的 prism_instances_dir（持久化供查看/修改，
// 值未变化时跳过写入）。
func (s *PrismService) findInstancesDir() (string, error) {
	instDir, fromAuto, err := resolveInstancesDir(s.config.Get())
	if err != nil {
		return "", err
	}
	if fromAuto {
		// 回写持久化（失败仅影响审计展示，不影响本次定位）
		_ = s.config.SetPrismInstancesDir(instDir)
	}
	return instDir, nil
}

// SetInstancesPath 保存用户手动指定的实例根目录（空串清除，恢复自动定位）。
// 自动定位失败时前端据此引导用户手动输入路径。
func (s *PrismService) SetInstancesPath(path string) error {
	path = strings.TrimSpace(path)
	if path != "" && !fsutil.IsDir(path) {
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
	return findProjectByName(s.config, projectName)
}

// scanInstancesSafe 扫描实例（定位失败返回 nil），供 GetLinks 等实时组装使用
func (s *PrismService) scanInstancesSafe() map[string]prism.Instance {
	instDir, err := s.findInstancesDir()
	if err != nil {
		return nil
	}
	return indexInstances(prism.ScanInstances(instDir))
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
	entry, pc, err := s.projectConfig(projectName)
	if err != nil {
		return err
	}
	if err := s.ensureInstanceExists(instanceID); err != nil {
		return err
	}
	pc.Instance = instanceID
	return appconfig.SaveProjectConfig(entry.Path, pc)
}

// UnlinkProject 解除项目关联（连同其目录关联与已建链接）
func (s *PrismService) UnlinkProject(projectName string) error {
	entry, pc, err := s.projectConfig(projectName)
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
		removeHardlinkFiles(inst, pc.FileLinks)
	}
	pc.Instance = ""
	pc.DirLinks = nil
	pc.FileLinks = nil
	return appconfig.SaveProjectConfig(entry.Path, pc)
}

// GetLinks 返回全部项目 ↔ 实例关联的组装视图（读取各项目 packgradle.toml，
// 实时扫描实例，实例被删时标记失效）
func (s *PrismService) GetLinks() []prism.LinkView {
	return s.buildLinks(s.scanInstancesSafe())
}

// buildLinks 用已扫描的实例索引组装关联视图（Overview 复用同一扫描结果，避免重复扫描）
func (s *PrismService) buildLinks(instances map[string]prism.Instance) []prism.LinkView {
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

// PrismOverview 是 Prism 页面一次性装载所需的全部数据（实例目录 + 实例列表 + 关联视图）。
// 前端页面装载/刷新时一次调用即可，避免多次往返与重复扫描实例目录。
type PrismOverview struct {
	InstancesDir string           `json:"instances_dir"` // 当前定位到的实例根目录（定位失败时为空）
	LocateError  string           `json:"locate_error"`  // 定位失败的错误码 JSON 文本（空串 = 定位成功）
	Instances    []prism.Instance `json:"instances"`     // 实例列表（定位失败时为空）
	Links        []prism.LinkView `json:"links"`         // 项目 ↔ 实例关联视图（定位失败时实例侧为失效态）
}

// Overview 一次性返回 Prism 页所需的全部数据。
// 定位失败不中断：错误落入 LocateError（前端解析错误码渲染引导），关联视图仍可用。
func (s *PrismService) Overview() PrismOverview {
	overview := PrismOverview{}
	instDir, err := s.findInstancesDir()
	if err != nil {
		overview.LocateError = err.Error()
	} else {
		overview.InstancesDir = instDir
		overview.Instances = prism.ScanInstances(instDir)
	}
	overview.Links = s.buildLinks(indexInstances(overview.Instances))
	return overview
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
	entry, pc, err := s.projectConfig(projectName)
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
	if !fsutil.IsDir(filepath.Join(entry.Path, projectDir)) {
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
	entry, pc, err := s.projectConfig(projectName)
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
		view.ProjectExists = fsutil.IsDir(filepath.Join(entry.Path, dl.ProjectDir))
		if inst, ok := instances[pc.Instance]; ok {
			view.InstanceExists = fsutil.IsDir(filepath.Join(inst.GameDir, filepath.FromSlash(dl.InstanceDir)))
		}
		out = append(out, view)
	}
	return out
}
