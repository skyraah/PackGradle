package transport

import (
	"context"

	projectapp "packgradle/internal/application/project"
	"packgradle/internal/application/view"
)

// ProjectService 是项目源端点用例的 Wails 出口（契约 03 §1/§2.5；/sources 页）。
type ProjectService struct {
	app projectapp.Application
}

// NewProjectService 构造服务。
func NewProjectService(app projectapp.Application) *ProjectService { return &ProjectService{app: app} }

// ServiceName 返回服务注册名（Wails v3 生命周期可选接口）。
func (s *ProjectService) ServiceName() string { return "packgradle.core.ProjectService" }

// DiscoverProjects 在 parentDir 内有限深度发现 Packwiz 项目源。
func (s *ProjectService) DiscoverProjects(parentDir string) ([]ProjectCandidateDTO, error) {
	cands, err := s.app.DiscoverProjects(context.Background(), parentDir)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectCandidateDTO, 0, len(cands))
	for _, c := range cands {
		out = append(out, projectCandidateDTO(c))
	}
	return out, nil
}

// RegisterProject 幂等登记项目源端点（fingerprint 身份，稳定 opaque ID）。
func (s *ProjectService) RegisterProject(input RegisterEndpointDTO) (EndpointDTO, error) {
	v, err := s.app.RegisterProject(context.Background(), view.RegisterEndpointInput{RootPath: input.RootPath})
	if err != nil {
		return EndpointDTO{}, err
	}
	return endpointDTO(v), nil
}

// GetProjectHealth 返回项目源端点健康（只读）。
func (s *ProjectService) GetProjectHealth(endpointID string) (EndpointHealthDTO, error) {
	v, err := s.app.GetProjectHealth(context.Background(), endpointID)
	if err != nil {
		return EndpointHealthDTO{}, err
	}
	return endpointHealthDTO(v), nil
}

// ListProjects 返回全部已登记项目源。
func (s *ProjectService) ListProjects() ([]EndpointDTO, error) {
	items, err := s.app.ListProjects(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]EndpointDTO, 0, len(items))
	for _, e := range items {
		out = append(out, endpointDTO(e))
	}
	return out, nil
}
