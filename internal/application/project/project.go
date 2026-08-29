// Package project 实现项目源端点用例（契约 03 §1/§2.5）：发现（有限深度查找
// pack.toml）、幂等登记（binding fingerprint 决定身份，重复登记返回既有端点）
// 与只读健康检查。与 application/sync 的 PrepareRelation 并列：本包服务
// /sources 端点管理页，不创建 Relation。
package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"packgradle/internal/application/endpoint"
	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// Application 是项目源端点用例接口（transport 依赖此接口）。
type Application interface {
	DiscoverProjects(ctx context.Context, parentDir string) ([]view.ProjectCandidateView, error)
	RegisterProject(ctx context.Context, input view.RegisterEndpointInput) (view.EndpointView, error)
	GetProjectHealth(ctx context.Context, endpointID string) (view.EndpointHealthView, error)
	// ListProjects 返回全部已登记项目（契约 03 §1 之外的增补查询：
	// /sources 页展示已登记端点列表所需）。
	ListProjects(ctx context.Context) ([]view.EndpointView, error)
}

var _ Application = (*App)(nil)

// Deps 是应用依赖。
type Deps struct {
	Endpoints     ports.EndpointRepository
	Paths         ports.EndpointNormalizer
	Fingerprinter ports.BindingFingerprinter
	Discovery     ports.ProjectDiscovery
	IDs           func(prefix string) string
	Now           func() time.Time
}

// App 是项目源端点用例的 P1 实现。
type App struct {
	deps Deps
}

// New 构造应用；依赖缺失返回错误。
func New(deps Deps) (*App, error) {
	required := []struct {
		name string
		ok   bool
	}{
		{"Endpoints", deps.Endpoints != nil},
		{"Paths", deps.Paths != nil},
		{"Fingerprinter", deps.Fingerprinter != nil},
		{"Discovery", deps.Discovery != nil},
		{"IDs", deps.IDs != nil},
		{"Now", deps.Now != nil},
	}
	for _, r := range required {
		if !r.ok {
			return nil, fmt.Errorf("project: 缺少依赖 %s", r.name)
		}
	}
	return &App{deps: deps}, nil
}

// DiscoverProjects 在 parentDir 内有限深度发现 Packwiz 项目源，
// 并按 binding fingerprint 判定各候选的登记状态。
func (a *App) DiscoverProjects(ctx context.Context, parentDir string) ([]view.ProjectCandidateView, error) {
	root, err := a.deps.Paths.NormalizeEndpointPath(parentDir)
	if err != nil {
		return nil, errs.New(endpoint.CodeDiscoveryFailed, parentDir)
	}
	cands, err := a.deps.Discovery.DiscoverProjects(ctx, root)
	if err != nil {
		return nil, errs.NewDetail(endpoint.CodeDiscoveryFailed, err.Error(), parentDir)
	}
	out := make([]view.ProjectCandidateView, 0, len(cands))
	for _, c := range cands {
		v := view.ProjectCandidateView{
			DisplayName:  c.DisplayName,
			RootPath:     c.RootPath,
			PackTomlPath: c.PackTomlPath,
			Minecraft:    c.Minecraft,
			Modloader:    c.Modloader,
		}
		if id, ok := a.findByRoot(ctx, c.RootPath); ok {
			v.Registered = true
			v.EndpointID = id
		}
		out = append(out, v)
	}
	return out, nil
}

// RegisterProject 幂等登记项目源端点：路径规范化 → pack.toml 存在性 →
// 指纹身份 → 已登记则返回既有端点（稳定 opaque ID），否则新建。
func (a *App) RegisterProject(ctx context.Context, input view.RegisterEndpointInput) (view.EndpointView, error) {
	root, err := a.deps.Paths.NormalizeEndpointPath(input.RootPath)
	if err != nil {
		return view.EndpointView{}, errs.NewDetail(endpoint.CodeInvalidPath, err.Error(), input.RootPath)
	}
	if _, err := os.Stat(filepath.Join(root, "pack.toml")); err != nil {
		return view.EndpointView{}, errs.New(endpoint.CodeInvalidPath, root)
	}
	fp, err := a.deps.Fingerprinter.Fingerprint(root)
	if err != nil {
		return view.EndpointView{}, errs.NewDetail(endpoint.CodeInvalidPath, "计算绑定指纹失败: "+err.Error(), root)
	}
	if existing, found, err := a.deps.Endpoints.FindProjectByRoot(ctx, fp); err != nil {
		return view.EndpointView{}, err
	} else if found {
		return projectView(existing), nil
	}

	p := model.Project{
		SchemaVersion:      model.CurrentSchemaVersion,
		ProjectID:          a.deps.IDs("prj_"),
		Adapter:            "packwiz",
		DisplayName:        packDisplayName(root),
		RootPath:           root,
		BindingFingerprint: fp,
		CreatedAt:          a.deps.Now().UTC().Format(time.RFC3339),
	}
	if err := a.deps.Endpoints.CreateProject(ctx, p); err != nil {
		// 并发登记同一根目录：唯一约束命中后回读既有端点（幂等）
		if errors.Is(err, ports.ErrDuplicate) {
			if existing, found, ferr := a.deps.Endpoints.FindProjectByRoot(ctx, fp); ferr == nil && found {
				return projectView(existing), nil
			}
		}
		return view.EndpointView{}, err
	}
	return projectView(p), nil
}

// GetProjectHealth 只读健康检查：路径存在性 + 绑定指纹匹配。
func (a *App) GetProjectHealth(ctx context.Context, endpointID string) (view.EndpointHealthView, error) {
	p, err := a.deps.Endpoints.GetProject(ctx, endpointID)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return view.EndpointHealthView{}, errs.New(endpoint.CodeNotFound, endpointID)
		}
		return view.EndpointHealthView{}, err
	}
	return endpoint.Evaluate(a.healthDeps(), endpointID, p.RootPath, p.BindingFingerprint), nil
}

// ListProjects 返回全部已登记项目。
func (a *App) ListProjects(ctx context.Context) ([]view.EndpointView, error) {
	items, err := a.deps.Endpoints.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]view.EndpointView, 0, len(items))
	for _, p := range items {
		out = append(out, projectView(p))
	}
	return out, nil
}

// findByRoot 判定候选根目录是否已登记（返回端点 ID）。候选不可达/指纹不可算
// 一律按未登记处理（发现结果是探索性数据，不为它报错）。
func (a *App) findByRoot(ctx context.Context, rootPath string) (string, bool) {
	root, err := a.deps.Paths.NormalizeEndpointPath(rootPath)
	if err != nil {
		return "", false
	}
	fp, err := a.deps.Fingerprinter.Fingerprint(root)
	if err != nil {
		return "", false
	}
	p, found, err := a.deps.Endpoints.FindProjectByRoot(ctx, fp)
	if err != nil || !found {
		return "", false
	}
	return p.ProjectID, true
}

func (a *App) healthDeps() endpoint.HealthDeps {
	return endpoint.HealthDeps{Paths: a.deps.Paths, Fingerprinter: a.deps.Fingerprinter, Now: a.deps.Now}
}

// packDisplayName 从 pack.toml 读取展示名（name 字段），缺失或解析失败回退目录名。
// 只取展示元数据，不参与身份判定（身份是 binding fingerprint）。
func packDisplayName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "pack.toml"))
	if err == nil {
		var meta struct {
			Name string `toml:"name"`
		}
		if toml.Unmarshal(data, &meta) == nil && meta.Name != "" {
			return meta.Name
		}
	}
	return filepath.Base(root)
}

func projectView(p model.Project) view.EndpointView {
	return view.EndpointView{
		ID:                 p.ProjectID,
		Adapter:            p.Adapter,
		DisplayName:        p.DisplayName,
		RootPath:           p.RootPath,
		BindingFingerprint: p.BindingFingerprint,
	}
}
