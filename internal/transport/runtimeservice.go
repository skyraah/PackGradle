package transport

import (
	"context"

	"packgradle/internal/application/runtime"
	"packgradle/internal/application/view"
)

// RuntimeService 是运行实例端点用例的 Wails 出口（契约 03 §1/§2.5；/runtimes 页）。
type RuntimeService struct {
	app runtime.Application
}

// NewRuntimeService 构造服务。
func NewRuntimeService(app runtime.Application) *RuntimeService { return &RuntimeService{app: app} }

// ServiceName 返回服务注册名（Wails v3 生命周期可选接口）。
func (s *RuntimeService) ServiceName() string { return "packgradle.core.RuntimeService" }

// DiscoverRuntimes 从 Prism 实例目录枚举运行实例候选。
func (s *RuntimeService) DiscoverRuntimes() ([]RuntimeCandidateDTO, error) {
	cands, err := s.app.DiscoverRuntimes(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]RuntimeCandidateDTO, 0, len(cands))
	for _, c := range cands {
		out = append(out, runtimeCandidateDTO(c))
	}
	return out, nil
}

// RegisterRuntime 幂等登记运行实例端点（adapter identity 身份，稳定 opaque ID）。
func (s *RuntimeService) RegisterRuntime(input RegisterEndpointDTO) (EndpointDTO, error) {
	v, err := s.app.RegisterRuntime(context.Background(), view.RegisterEndpointInput{RootPath: input.RootPath})
	if err != nil {
		return EndpointDTO{}, err
	}
	return endpointDTO(v), nil
}

// GetRuntimeHealth 返回运行实例端点健康（只读）。
func (s *RuntimeService) GetRuntimeHealth(endpointID string) (EndpointHealthDTO, error) {
	v, err := s.app.GetRuntimeHealth(context.Background(), endpointID)
	if err != nil {
		return EndpointHealthDTO{}, err
	}
	return endpointHealthDTO(v), nil
}

// ListRuntimes 返回全部已登记运行实例。
func (s *RuntimeService) ListRuntimes() ([]EndpointDTO, error) {
	items, err := s.app.ListRuntimes(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]EndpointDTO, 0, len(items))
	for _, e := range items {
		out = append(out, endpointDTO(e))
	}
	return out, nil
}
