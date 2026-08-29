// Package runtime 实现运行实例端点用例（契约 03 §1/§2.5）：发现（定位 Prism
// 实例目录并枚举）、幂等登记（adapter identity 决定身份）与只读健康检查。
// 与 application/sync 的 PrepareRelation 并列：本包服务 /runtimes 端点管理页，
// 不创建 Relation。
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/application/endpoint"
	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// Application 是运行实例端点用例接口（transport 依赖此接口）。
type Application interface {
	DiscoverRuntimes(ctx context.Context) ([]view.RuntimeCandidateView, error)
	RegisterRuntime(ctx context.Context, input view.RegisterEndpointInput) (view.EndpointView, error)
	GetRuntimeHealth(ctx context.Context, endpointID string) (view.EndpointHealthView, error)
	// ListRuntimes 返回全部已登记运行实例（契约 03 §1 之外的增补查询：
	// /runtimes 页展示已登记端点列表所需）。
	ListRuntimes(ctx context.Context) ([]view.EndpointView, error)
}

var _ Application = (*App)(nil)

// runtimeAdapter 是 P1 唯一的运行实例适配器身份。
const runtimeAdapter = "prism"

// Deps 是应用依赖。
type Deps struct {
	Endpoints     ports.EndpointRepository
	Paths         ports.EndpointNormalizer
	Fingerprinter ports.BindingFingerprinter
	Discovery     ports.RuntimeDiscovery
	IDs           func(prefix string) string
	Now           func() time.Time
}

// App 是运行实例端点用例的 P1 实现。
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
			return nil, fmt.Errorf("runtime: 缺少依赖 %s", r.name)
		}
	}
	return &App{deps: deps}, nil
}

// DiscoverRuntimes 从 Prism 实例目录枚举运行实例候选，并按 adapter identity
// 判定各候选的登记状态。
func (a *App) DiscoverRuntimes(ctx context.Context) ([]view.RuntimeCandidateView, error) {
	cands, err := a.deps.Discovery.DiscoverRuntimes(ctx)
	if err != nil {
		// 发现失败统一映射 instances_dir_not_found（args {0}=尝试的数据目录，
		// 由 *ports.InstancesDirError 携带）
		var ide *ports.InstancesDirError
		if errors.As(err, &ide) {
			return nil, errs.NewDetail(endpoint.CodeInstancesDirNotFound, err.Error(), ide.DataDir)
		}
		return nil, errs.NewDetail(endpoint.CodeInstancesDirNotFound, err.Error(), "")
	}
	out := make([]view.RuntimeCandidateView, 0, len(cands))
	for _, c := range cands {
		v := view.RuntimeCandidateView{
			InstanceID:  c.InstanceID,
			InstanceDir: c.InstanceDir,
			DisplayName: c.DisplayName,
			GameDir:     c.GameDir,
			Minecraft:   c.Minecraft,
			Modloader:   c.Modloader,
		}
		if id, ok := a.findByIdentity(ctx, c.InstanceID); ok {
			v.Registered = true
			v.EndpointID = id
		}
		out = append(out, v)
	}
	return out, nil
}

// RegisterRuntime 幂等登记运行实例端点：实例目录规范化 → instance.cfg 与
// minecraft/ 游戏目录存在性 → adapter identity 身份 → 已登记则返回既有端点，
// 同名实例目录已被登记为不同路径时拒绝（端点身份原则，与 PrepareRelation 一致）。
func (a *App) RegisterRuntime(ctx context.Context, input view.RegisterEndpointInput) (view.EndpointView, error) {
	instanceDir, err := a.deps.Paths.NormalizeEndpointPath(input.RootPath)
	if err != nil {
		return view.EndpointView{}, errs.NewDetail(endpoint.CodeInvalidPath, err.Error(), input.RootPath)
	}
	if _, err := os.Stat(filepath.Join(instanceDir, "instance.cfg")); err != nil {
		return view.EndpointView{}, errs.New(endpoint.CodeInvalidPath, instanceDir)
	}
	gameDir, err := a.deps.Paths.NormalizeEndpointPath(filepath.Join(instanceDir, "minecraft"))
	if err != nil {
		return view.EndpointView{}, errs.NewDetail(endpoint.CodeInvalidPath,
			"游戏目录 minecraft/ 不可达: "+err.Error(), instanceDir)
	}
	fp, err := a.deps.Fingerprinter.Fingerprint(gameDir)
	if err != nil {
		return view.EndpointView{}, errs.NewDetail(endpoint.CodeInvalidPath, "计算绑定指纹失败: "+err.Error(), gameDir)
	}

	identity := strings.ToLower(filepath.Base(instanceDir))
	existing, found, err := a.deps.Endpoints.FindRuntimeByIdentity(ctx, runtimeAdapter, identity)
	if err != nil {
		return view.EndpointView{}, err
	}
	if found {
		if err := requireSamePath(existing, gameDir); err != nil {
			return view.EndpointView{}, err
		}
		return runtimeView(existing), nil
	}

	rt := model.Runtime{
		SchemaVersion:      model.CurrentSchemaVersion,
		RuntimeID:          a.deps.IDs("run_"),
		Adapter:            runtimeAdapter,
		DisplayName:        filepath.Base(instanceDir),
		RootPath:           gameDir,
		AdapterIdentity:    identity,
		BindingFingerprint: fp,
		CreatedAt:          a.deps.Now().UTC().Format(time.RFC3339),
	}
	if err := a.deps.Endpoints.CreateRuntime(ctx, rt); err != nil {
		// 并发登记同一实例：唯一约束命中后回读（幂等；路径不同仍拒绝）
		if errors.Is(err, ports.ErrDuplicate) {
			if existing, found, ferr := a.deps.Endpoints.FindRuntimeByIdentity(ctx, runtimeAdapter, identity); ferr == nil && found {
				if err := requireSamePath(existing, gameDir); err != nil {
					return view.EndpointView{}, err
				}
				return runtimeView(existing), nil
			}
		}
		return view.EndpointView{}, err
	}
	return runtimeView(rt), nil
}

// requireSamePath 强制同名实例目录指向同一路径（端点身份原则）：
// 否则会把新登记静默绑到另一个启动器安装的同名实例上。
func requireSamePath(existing model.Runtime, gameDir string) error {
	if !strings.EqualFold(filepath.Clean(existing.RootPath), filepath.Clean(gameDir)) {
		return errs.NewDetail(endpoint.CodeIdentityMismatch,
			"同名实例目录已登记为不同路径: "+existing.RootPath, existing.RuntimeID)
	}
	return nil
}

// GetRuntimeHealth 只读健康检查：游戏目录存在性 + 绑定指纹匹配。
func (a *App) GetRuntimeHealth(ctx context.Context, endpointID string) (view.EndpointHealthView, error) {
	rt, err := a.deps.Endpoints.GetRuntime(ctx, endpointID)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return view.EndpointHealthView{}, errs.New(endpoint.CodeNotFound, endpointID)
		}
		return view.EndpointHealthView{}, err
	}
	return endpoint.Evaluate(a.healthDeps(), endpointID, rt.RootPath, rt.BindingFingerprint), nil
}

// ListRuntimes 返回全部已登记运行实例。
func (a *App) ListRuntimes(ctx context.Context) ([]view.EndpointView, error) {
	items, err := a.deps.Endpoints.ListRuntimes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]view.EndpointView, 0, len(items))
	for _, rt := range items {
		out = append(out, runtimeView(rt))
	}
	return out, nil
}

// findByIdentity 判定候选实例是否已登记（返回端点 ID）。
func (a *App) findByIdentity(ctx context.Context, instanceID string) (string, bool) {
	rt, found, err := a.deps.Endpoints.FindRuntimeByIdentity(ctx, runtimeAdapter, strings.ToLower(instanceID))
	if err != nil || !found {
		return "", false
	}
	return rt.RuntimeID, true
}

func (a *App) healthDeps() endpoint.HealthDeps {
	return endpoint.HealthDeps{Paths: a.deps.Paths, Fingerprinter: a.deps.Fingerprinter, Now: a.deps.Now}
}

func runtimeView(rt model.Runtime) view.EndpointView {
	return view.EndpointView{
		ID:                 rt.RuntimeID,
		Adapter:            rt.Adapter,
		DisplayName:        rt.DisplayName,
		RootPath:           rt.RootPath,
		AdapterIdentity:    rt.AdapterIdentity,
		BindingFingerprint: rt.BindingFingerprint,
	}
}
