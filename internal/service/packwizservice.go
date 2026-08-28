package service

import (
	"log"
	"os"
	"path/filepath"

	"packgradle/internal/appconfig"
	"packgradle/internal/curseforge"
	"packgradle/internal/errs"
	"packgradle/internal/junction"
	"packgradle/internal/packwiz"
	"packgradle/internal/pgignore"
)

// PackwizService 负责 packwiz 项目的导入、解析与管理
type PackwizService struct {
	config    *appconfig.ConfigManager
	junctions junction.Manager
}

func NewPackwizService(config *appconfig.ConfigManager) *PackwizService {
	return &PackwizService{config: config, junctions: junction.NewWindowsManager()}
}

// ImportProject 导入一个 pack.toml 并返回解析结果（同名项目会覆盖路径）。
// 首次导入时在项目根目录创建 .pgignore（一键关联忽略规则，已存在不覆盖）。
func (s *PackwizService) ImportProject(packTomlPath string) (packwiz.PackProject, error) {
	abs, err := filepath.Abs(packTomlPath)
	if err != nil {
		return packwiz.PackProject{}, errs.NewDetail("err.proj.invalid_path", err.Error(), packTomlPath)
	}
	proj, err := packwiz.ParseProject(abs)
	if err != nil {
		return packwiz.PackProject{}, err
	}
	if _, err := pgignore.Ensure(proj.Path); err != nil {
		return packwiz.PackProject{}, errs.NewDetail("err.file.write", err.Error(), ".pgignore")
	}
	s.applyCfCache(&proj)
	if err := s.config.AddProject(appconfig.ProjectEntry{Name: proj.Name, Path: proj.Path}); err != nil {
		return packwiz.PackProject{}, err
	}
	return proj, nil
}

// ListProjects 返回所有已导入项目的解析结果
func (s *PackwizService) ListProjects() []packwiz.PackProject {
	entries := s.config.Get().Projects
	projects := make([]packwiz.PackProject, 0, len(entries))
	for _, e := range entries {
		proj, err := packwiz.ParseProject(filepath.Join(e.Path, "pack.toml"))
		if err != nil {
			projects = append(projects, packwiz.PackProject{
				Name:  e.Name,
				Path:  e.Path,
				Error: err.Error(),
			})
			continue
		}
		s.applyCfCache(&proj)
		projects = append(projects, proj)
	}
	return projects
}

// RemoveProject 按名称移除项目，返回剩余项目列表。
// 联动清理：若项目有关联 Prism 实例（项目级 packgradle.toml），
// 删除已建链接（junction/硬链接）并移除 packgradle.toml，
// 避免删除后 Prism 联动残留（重新导入时关联意外复活）。
// 项目目录内的用户文件（mods/config 等）不受影响。
func (s *PackwizService) RemoveProject(name string) ([]packwiz.PackProject, error) {
	if entry, ok := s.config.FindProject(name); ok {
		s.cleanupProjectLinks(entry.Path)
	}
	if err := s.config.RemoveProject(name); err != nil {
		return s.ListProjects(), err // 配置写盘失败不吞掉，前端可见
	}
	return s.ListProjects(), nil
}

// cleanupProjectLinks 清理项目的 Prism 联动配置：
// 删除已建链接（目录 junction + 文件硬链接，含 files 模式），随后移除 packgradle.toml。
// 实例目录无法定位或实例已被删除时跳过链接删除（残留链接由用户手动处理，不阻塞移除）。
func (s *PackwizService) cleanupProjectLinks(projectPath string) {
	_ = appconfig.WithProjectConfigLock(projectPath, func() error {
		pc, err := appconfig.LoadProjectConfig(projectPath)
		if err != nil || pc.Instance == "" {
			return nil // 未关联或配置不可读：直接移除 packgradle.toml 即可
		}
		if err := removeProjectLinkTargets(s.config, s.junctions, appconfig.ProjectEntry{Path: projectPath}, pc); err != nil {
			log.Printf("移除项目前清理链接失败（项目目录 %s）: %v", projectPath, err)
		}
		_ = os.Remove(appconfig.ProjectConfigPath(projectPath))
		return nil
	})
}

// RefreshProject 在项目目录执行 `packwiz refresh` 并返回输出
func (s *PackwizService) RefreshProject(name string) packwiz.RefreshResult {
	entry, ok := s.config.FindProject(name)
	if !ok {
		return packwiz.RefreshResult{OK: false, Output: errs.New("err.proj.not_found", name).Error()}
	}
	packwizPath, err := s.findPackwiz()
	if err != nil {
		return packwiz.RefreshResult{OK: false, Output: err.Error()}
	}
	return packwiz.RunRefresh(packwizPath, entry.Path)
}

// findPackwiz 返回 packwiz 可执行文件路径（查找链见 findPackwizExecutable）
func (s *PackwizService) findPackwiz() (string, error) {
	path, _, ok := findPackwizExecutable(s.config.Get())
	if !ok {
		return "", errs.New("err.tool.packwiz_not_found")
	}
	return path, nil
}

// findProject 按名称查找项目并解析
func (s *PackwizService) findProject(projectName string) (packwiz.PackProject, error) {
	return findProjectByName(s.config, projectName)
}

// findProjectMod 按名称查找项目并定位其中指定 ID 的 mod
func (s *PackwizService) findProjectMod(projectName, modID string) (packwiz.PackProject, packwiz.ModInfo, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return packwiz.PackProject{}, packwiz.ModInfo{}, err
	}
	for _, m := range proj.Mods {
		if m.ID == modID {
			return proj, m, nil
		}
	}
	return packwiz.PackProject{}, packwiz.ModInfo{}, errs.New("err.mod.not_found", modID)
}

// cfCacheStore 返回项目对应的缓存存储（<项目目录>/.cache）
func (s *PackwizService) cfCacheStore(proj packwiz.PackProject) *curseforge.CfCacheStore {
	return curseforge.NewCfCacheStore(filepath.Join(proj.Path, ".cache"))
}

// applyCfCache 将项目缓存的 CurseForge 文件信息回填到 mod 列表
func (s *PackwizService) applyCfCache(proj *packwiz.PackProject) {
	cache, err := s.cfCacheStore(*proj).Load()
	if err != nil {
		return // 缓存不可读时静默（仅影响版本显示）
	}
	for i := range proj.Mods {
		m := &proj.Mods[i]
		if m.CfProjectID == 0 || m.CfFileID == 0 {
			continue
		}
		entry, ok := cache[curseforge.CacheKey(m.CfProjectID, m.CfFileID)]
		if !ok {
			continue
		}
		m.CfVersion = entry.DisplayName
		m.CfFileDate = entry.FileDate
		m.CfReleaseType = entry.ReleaseType
	}
}
