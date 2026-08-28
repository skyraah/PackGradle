package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/application/policy"
	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// 错误码（P1 新增；文案由前端 locale 提供）。
const (
	CodeRelationNotFound        = "err.relation.not_found"
	CodeRelationDuplicatePair   = "err.relation.duplicate_pair"
	CodeRelationInvalidEndpoint = "err.relation.invalid_endpoint"
	CodeRelationRebindRequired  = "err.relation.rebind_required"
	CodeRelationPrepNotFound    = "err.relation.preparation_not_found"
	CodeRelationPrepExpired     = "err.relation.preparation_expired"
	CodeMappingUnknownPolicy    = "err.mapping.unknown_policy"
	CodeRelationScanRunning     = "err.scan.already_running"
	CodeScanEndpointMissing     = "err.scan.endpoint_missing"
	CodeScanAdapterFailed       = "err.scan.adapter_failed"
	CodePlanNotFound            = "err.plan.not_found"
	CodePlanStale               = "err.plan.stale"
	CodePlanResolutionInvalid   = "err.plan.resolution_invalid"
	CodeSyncRevisionMismatch    = "err.sync.revision_mismatch"
	CodeSyncSnapshotNotFound    = "err.sync.snapshot_not_found"
	CodeTaskNotFound            = "err.scan.task_not_found"
	CodeTaskNotCancellable      = "err.scan.task_not_cancellable"
)

// preparationTTL 是预检有效期。
var preparationTTL = 30 * time.Minute

// PrepareRelation 执行端点登记预检：路径可达、packwiz/prism 结构存在、
// 重复 pair、路径包含关系、legacy 痕迹与绑定指纹；结果持久化，CreateRelation 只接受其 ID。
func (a *App) PrepareRelation(ctx context.Context, input model.PrepareRelationInput) (view.RelationPreparationView, error) {
	policySet := input.PolicySet
	if policySet == "" {
		policySet = policy.DefaultPolicySet
	}
	pol, err := policy.Template(policySet)
	if err != nil {
		return view.RelationPreparationView{}, errs.New(CodeMappingUnknownPolicy, policySet)
	}

	checks := make([]model.PreparationCheck, 0, 6)
	blocking := func(code string, passed bool, detail string, args ...string) model.PreparationCheck {
		return model.PreparationCheck{Code: code, Passed: passed, Severity: "blocking", Detail: detail, Args: args}
	}

	// 端点路径规范化管线（P0-4 强制入口）：相对输入绝对化 → realpath →
	// 目录校验；登记与指纹一律使用 canonical 路径。不可达落到对应 readable 检查。
	projectRoot := input.ProjectRoot
	projectReadable := false
	projectDetail := "pack.toml 不存在（不是 Packwiz 项目根目录）"
	if real, nerr := a.deps.EndpointPaths.NormalizeEndpointPath(input.ProjectRoot); nerr != nil {
		projectDetail = "项目源根目录不可达: " + nerr.Error()
	} else {
		projectRoot = real
		projectReadable = pathExists(filepath.Join(real, "pack.toml"))
	}

	instanceDir := input.RuntimeInstanceDir
	gameDir := ""
	runtimeReadable := false
	runtimeDetail := "Prism 实例目录缺少 instance.cfg 或 minecraft/ 游戏目录"
	if real, nerr := a.deps.EndpointPaths.NormalizeEndpointPath(input.RuntimeInstanceDir); nerr != nil {
		runtimeDetail = "运行实例目录不可达: " + nerr.Error()
	} else if realGame, gerr := a.deps.EndpointPaths.NormalizeEndpointPath(filepath.Join(real, "minecraft")); gerr != nil {
		runtimeDetail = "游戏目录 minecraft/ 不可达: " + gerr.Error()
	} else {
		instanceDir = real
		gameDir = realGame
		runtimeReadable = pathExists(filepath.Join(real, "instance.cfg"))
	}

	// 1. Project 端点可达且有 pack.toml
	checks = append(checks, blocking("check.endpoint.readable", projectReadable,
		projectDetail, projectRoot))

	// 2. Runtime 端点可达（实例目录 + instance.cfg + 游戏目录）
	checks = append(checks, blocking("check.endpoint.readable", runtimeReadable,
		runtimeDetail, instanceDir))

	var fpProject, fpRuntime string
	if projectReadable {
		if fpProject, err = a.deps.Fingerprinter.Fingerprint(projectRoot); err != nil {
			return view.RelationPreparationView{}, errs.NewDetail(CodeRelationInvalidEndpoint, "计算项目端点指纹失败: "+err.Error(), projectRoot)
		}
	}
	if runtimeReadable {
		if fpRuntime, err = a.deps.Fingerprinter.Fingerprint(gameDir); err != nil {
			return view.RelationPreparationView{}, errs.NewDetail(CodeRelationInvalidEndpoint, "计算运行时端点指纹失败: "+err.Error(), instanceDir)
		}
	}

	// 3. 路径包含关系（project root 与 game dir 互为祖先 → 拒绝）
	containment := isAncestorOf(projectRoot, gameDir) || isAncestorOf(gameDir, projectRoot)
	checks = append(checks, blocking("check.path.containment", !containment,
		"项目根目录与实例游戏目录存在包含关系，无法建立隔离同步", projectRoot, gameDir))

	// 4. 重复 pair（同 fingerprint 项目 + 同 identity 运行时）
	adapterIdentity := strings.ToLower(filepath.Base(instanceDir))
	duplicate := false
	if projectReadable && runtimeReadable {
		existingProj, projFound, err := a.deps.Endpoints.FindProjectByRoot(ctx, fpProject)
		if err != nil {
			return view.RelationPreparationView{}, err
		}
		existingRt, rtFound, err := a.deps.Endpoints.FindRuntimeByIdentity(ctx, "prism", adapterIdentity)
		if err != nil {
			return view.RelationPreparationView{}, err
		}
		if projFound && rtFound {
			duplicate, err = a.deps.Relations.PairExists(ctx, existingProj.ProjectID, existingRt.RuntimeID)
			if err != nil {
				return view.RelationPreparationView{}, err
			}
		}
	}
	checks = append(checks, blocking("check.pair.duplicate", !duplicate,
		"该 Project/Runtime 组合已存在 Relation", projectRoot, instanceDir))

	// 5. legacy 痕迹（警告级：项目内 packgradle.toml 表示旧架构关联配置）
	hasLegacy := pathExists(filepath.Join(projectRoot, "packgradle.toml"))
	legacyDetail := ""
	if hasLegacy {
		legacyDetail = "检测到旧架构 packgradle.toml；将仅作为迁移输入读取，不参与新同步语义"
	}
	checks = append(checks, model.PreparationCheck{
		Code: "check.legacy.materialization", Passed: true, Severity: "warning", Detail: legacyDetail,
	})

	// 6. 绑定指纹
	checks = append(checks, blocking("check.endpoint.binding", fpProject != "" && fpRuntime != "",
		"端点绑定指纹采集失败"))

	now := a.deps.Now().UTC()
	prep := model.RelationPreparation{
		SchemaVersion: model.CurrentSchemaVersion,
		PreparationID: a.deps.IDs("prep_"),
		CreatedAt:     now.Format(time.RFC3339),
		ExpiresAt:     now.Add(preparationTTL).Format(time.RFC3339),
		Input:         input,
		Policy:        pol,
		Checks:        checks,
	}
	if projectReadable {
		prep.Project = &model.Project{
			SchemaVersion:      model.CurrentSchemaVersion,
			ProjectID:          a.deps.IDs("prj_"),
			Adapter:            "packwiz",
			DisplayName:        filepath.Base(projectRoot),
			RootPath:           projectRoot,
			BindingFingerprint: fpProject,
			CreatedAt:          now.Format(time.RFC3339),
		}
	}
	if runtimeReadable {
		prep.Runtime = &model.Runtime{
			SchemaVersion:      model.CurrentSchemaVersion,
			RuntimeID:          a.deps.IDs("run_"),
			Adapter:            "prism",
			DisplayName:        filepath.Base(instanceDir),
			RootPath:           gameDir,
			AdapterIdentity:    adapterIdentity,
			BindingFingerprint: fpRuntime,
			CreatedAt:          now.Format(time.RFC3339),
		}
	}
	if err := a.deps.Preparations.Insert(ctx, prep); err != nil {
		return view.RelationPreparationView{}, err
	}
	return preparationView(prep), nil
}

// CreateRelation 消费预检：登记端点（幂等复用）、创建 Relation 与初始 policy。
func (a *App) CreateRelation(ctx context.Context, preparationID string) (view.RelationView, error) {
	prep, err := a.deps.Preparations.Get(ctx, preparationID)
	if err != nil {
		return view.RelationView{}, errs.New(CodeRelationPrepNotFound, preparationID)
	}
	now := a.deps.Now().UTC().Format(time.RFC3339)
	if expiresAt, perr := time.Parse(time.RFC3339, prep.ExpiresAt); perr != nil || now > prep.ExpiresAt || !expiresAt.After(a.deps.Now().UTC()) {
		return view.RelationView{}, errs.New(CodeRelationPrepExpired, preparationID)
	}
	for _, c := range prep.Checks {
		if c.Severity == "blocking" && !c.Passed {
			args := make([]any, len(c.Args))
			for i, a := range c.Args {
				args[i] = a
			}
			return view.RelationView{}, errs.NewDetail(CodeRelationInvalidEndpoint, "预检未通过: "+c.Code, args...)
		}
	}
	if err := a.deps.Preparations.MarkConsumed(ctx, preparationID); err != nil {
		return view.RelationView{}, errs.New(CodeRelationPrepExpired, preparationID)
	}

	project := *prep.Project
	if existing, found, err := a.deps.Endpoints.FindProjectByRoot(ctx, project.BindingFingerprint); err != nil {
		return view.RelationView{}, err
	} else if found {
		project = existing
	} else if err := a.deps.Endpoints.CreateProject(ctx, project); err != nil {
		if errors.Is(err, ports.ErrDuplicate) {
			if existing, found, _ := a.deps.Endpoints.FindProjectByRoot(ctx, project.BindingFingerprint); found {
				project = existing
			}
		} else {
			return view.RelationView{}, err
		}
	}

	runtime := *prep.Runtime
	if existing, found, err := a.deps.Endpoints.FindRuntimeByIdentity(ctx, runtime.Adapter, runtime.AdapterIdentity); err != nil {
		return view.RelationView{}, err
	} else if found {
		// 同名实例目录（UNIQUE(adapter, adapter_identity) 命中）必须指向同一路径，
		// 否则会把新 Relation 静默绑到另一个启动器安装的同名实例上（违反 §5.2 端点身份原则）
		if !strings.EqualFold(filepath.Clean(existing.RootPath), filepath.Clean(runtime.RootPath)) {
			return view.RelationView{}, errs.NewDetail(CodeRelationInvalidEndpoint,
				"同名实例目录已登记为不同路径: "+existing.RootPath, runtime.AdapterIdentity, runtime.RootPath)
		}
		runtime = existing
	} else if err := a.deps.Endpoints.CreateRuntime(ctx, runtime); err != nil {
		if errors.Is(err, ports.ErrDuplicate) {
			if existing, found, _ := a.deps.Endpoints.FindRuntimeByIdentity(ctx, runtime.Adapter, runtime.AdapterIdentity); found {
				if !strings.EqualFold(filepath.Clean(existing.RootPath), filepath.Clean(runtime.RootPath)) {
					return view.RelationView{}, errs.NewDetail(CodeRelationInvalidEndpoint,
						"同名实例目录已登记为不同路径: "+existing.RootPath, runtime.AdapterIdentity, runtime.RootPath)
				}
				runtime = existing
			}
		} else {
			return view.RelationView{}, err
		}
	}

	dup, err := a.deps.Relations.PairExists(ctx, project.ProjectID, runtime.RuntimeID)
	if err != nil {
		return view.RelationView{}, err
	}
	if dup {
		return view.RelationView{}, errs.New(CodeRelationDuplicatePair, project.RootPath, runtime.AdapterIdentity)
	}

	rel := model.Relation{
		SchemaVersion: model.CurrentSchemaVersion,
		RelationID:    a.deps.IDs("rel_"),
		ProjectID:     project.ProjectID,
		RuntimeID:     runtime.RuntimeID,
		PolicySet:     prep.Policy.PolicyID,
		Revision:      1,
		Health:        model.HealthHealthy,
		CreatedAt:     now,
	}
	if err := a.deps.Relations.Create(ctx, rel); err != nil {
		return view.RelationView{}, err
	}
	if err := a.deps.Mappings.SavePolicy(ctx, rel.RelationID, prep.Policy); err != nil {
		return view.RelationView{}, err
	}
	// SavePolicy 联动递增 relation revision，回读权威值
	rel, err = a.deps.Relations.Get(ctx, rel.RelationID)
	if err != nil {
		return view.RelationView{}, err
	}
	return relationView(rel, project, runtime), nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// isAncestorOf 判断 a（清理后）是否为 b 的祖先或相等（大小写不敏感）。
func isAncestorOf(a, b string) bool {
	la := strings.ToLower(filepath.ToSlash(filepath.Clean(a)))
	lb := strings.ToLower(filepath.ToSlash(filepath.Clean(b)))
	la = strings.TrimSuffix(la, "/")
	lb = strings.TrimSuffix(lb, "/")
	return la == lb || strings.HasPrefix(lb, la+"/")
}
