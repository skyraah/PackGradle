package service

import (
	"os"
	"path/filepath"

	"packgradle/internal/appconfig"
	"packgradle/internal/curseforge"
	"packgradle/internal/envutil"
	"packgradle/internal/errs"
	"packgradle/internal/packwiz"
	"packgradle/internal/pgignore"
)

// PackwizService 负责 packwiz 项目的导入、解析与管理
type PackwizService struct {
	config *appconfig.ConfigManager
}

func NewPackwizService(config *appconfig.ConfigManager) *PackwizService {
	return &PackwizService{config: config}
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

// RemoveProject 按名称移除项目，返回剩余项目列表
func (s *PackwizService) RemoveProject(name string) []packwiz.PackProject {
	_ = s.config.RemoveProject(name)
	return s.ListProjects()
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

// findPackwiz 返回 packwiz 可执行文件路径。
// 统一查找链：config → PACKWIZ 环境变量 → PATH → %USERPROFILE%\go\bin
func (s *PackwizService) findPackwiz() (string, error) {
	cfg := s.config.Get()
	goBin := filepath.Join(os.Getenv("USERPROFILE"), "go", "bin")
	path, _, ok := envutil.FindExecutable(cfg.PackwizPath, "packwiz", "PACKWIZ", goBin)
	if !ok {
		return "", errs.New("err.tool.packwiz_not_found")
	}
	return path, nil
}

// findProject 按名称查找项目并解析
func (s *PackwizService) findProject(projectName string) (packwiz.PackProject, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return packwiz.PackProject{}, errs.New("err.proj.not_found", projectName)
	}
	return packwiz.ParseProject(filepath.Join(entry.Path, "pack.toml"))
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
