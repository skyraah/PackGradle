package service

import (
	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
	"packgradle/internal/fsutil"
	"packgradle/internal/packwiz"
	"packgradle/internal/prism"
)

// linkedProject 是一次「项目 ↔ 实例」关联操作所需的全部上下文：
// 项目条目 + 项目级配置（packgradle.toml）+ 实时扫描到的关联实例，
// 可选携带已解析的 pack.toml（meta 推送/拉取/差异用）。
type linkedProject struct {
	Entry appconfig.ProjectEntry
	Pack  packwiz.PackProject // PackToml 为空表示未解析
	PC    appconfig.ProjectConfig
	Inst  prism.Instance
}

// projectConfig 查找项目条目并加载项目级配置（packgradle.toml）
func (s *PrismService) projectConfig(projectName string) (appconfig.ProjectEntry, appconfig.ProjectConfig, error) {
	entry, ok := s.config.FindProject(projectName)
	if !ok {
		return appconfig.ProjectEntry{}, appconfig.ProjectConfig{}, errs.New("err.proj.not_found", projectName)
	}
	pc, err := appconfig.LoadProjectConfig(entry.Path)
	if err != nil {
		return appconfig.ProjectEntry{}, appconfig.ProjectConfig{}, err
	}
	return entry, pc, nil
}

// linkedInstance 校验项目已关联实例，并返回实时扫描到的实例
func (s *PrismService) linkedInstance(projectName string, pc appconfig.ProjectConfig) (prism.Instance, error) {
	if pc.Instance == "" {
		return prism.Instance{}, errs.New("err.link.not_found", projectName)
	}
	inst, ok := s.scanInstancesSafe()[pc.Instance]
	if !ok {
		return prism.Instance{}, errs.New("err.prism.instance_not_found", pc.Instance)
	}
	return inst, nil
}

// loadLinkedProject 加载关联操作上下文（项目条目 + 项目配置 + 关联实例）。
// 未关联 / 实例不存在时返回对应错误码。
func (s *PrismService) loadLinkedProject(projectName string) (*linkedProject, error) {
	entry, pc, err := s.projectConfig(projectName)
	if err != nil {
		return nil, err
	}
	inst, err := s.linkedInstance(projectName, pc)
	if err != nil {
		return nil, err
	}
	return &linkedProject{Entry: entry, PC: pc, Inst: inst}, nil
}

// loadLinkedProjectPack 同 loadLinkedProject，并解析 pack.toml
// （meta 推送/拉取/差异用）。检查顺序与原实现一致：先解析项目、再取关联。
func (s *PrismService) loadLinkedProjectPack(projectName string) (*linkedProject, error) {
	pack, err := findProjectByName(s.config, projectName)
	if err != nil {
		return nil, err
	}
	if pack.Error != "" {
		return nil, errs.New("err.proj.not_found", projectName)
	}
	lp, err := s.loadLinkedProject(projectName)
	if err != nil {
		return nil, err
	}
	lp.Pack = pack
	return lp, nil
}

// junctionTargets 判断 instSide 是否为指向 projSide 的 junction
func (s *PrismService) junctionTargets(instSide, projSide string) bool {
	isJ, err := s.junctions.IsJunction(instSide)
	if err != nil || !isJ {
		return false
	}
	target, err := s.junctions.TargetOf(instSide)
	return err == nil && fsutil.SamePath(target, projSide)
}
