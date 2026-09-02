package sync

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// 错误码（契约 03 §3；票 #22）。文案由前端 locale 提供。
const (
	// CodeRebindPrepNotFound 是重绑预检不存在。
	CodeRebindPrepNotFound = "err.relation.rebind_prep_not_found"
	// CodeRebindPrepExpired 是重绑预检已过期（引导重新预检）。
	CodeRebindPrepExpired = "err.relation.rebind_prep_expired"
	// CodeRebindPrepConsumed 是重绑预检已被应用（ADR-0003 决议 4 拆码在重绑流的
	// 同款场景：双击/双窗口 → 引导刷新，工作区可能已重绑）。
	CodeRebindPrepConsumed = "err.relation.rebind_prep_consumed"
	// CodeRebindInvalidSide 是 side 非 project/runtime。
	CodeRebindInvalidSide = "err.relation.rebind_invalid_side"
)

// PrepareRebind 执行重绑预检（契约 03 §2.4；一次只重绑一侧）：新路径可达、
// 绑定指纹采集与新旧对比、路径包含关系、新端点占用、legacy 痕迹识别（识别不覆盖）
// 与重绑影响（将失效的计划数）。结果持久化，ApplyRebind 只接受其 ID。
func (a *App) PrepareRebind(ctx context.Context, input view.PrepareRebindInput) (view.RebindPreparationView, error) {
	side := model.Side(input.Side)
	if side != model.SideProject && side != model.SideRuntime {
		return view.RebindPreparationView{}, errs.New(CodeRebindInvalidSide, input.Side)
	}
	rel, err := a.deps.Relations.Get(ctx, input.RelationID)
	if err != nil {
		return view.RebindPreparationView{}, errs.New(CodeRelationNotFound, input.RelationID)
	}
	oldProj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		return view.RebindPreparationView{}, err
	}
	oldRt, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		return view.RebindPreparationView{}, err
	}

	blocking := func(code string, passed bool, detail string, args ...string) model.PreparationCheck {
		return model.PreparationCheck{Code: code, Passed: passed, Severity: "blocking", Detail: detail, Args: args}
	}
	checks := make([]model.PreparationCheck, 0, 4)

	// 端点路径规范化管线（与 PrepareRelation 同一强制入口）：project 侧输入为
	// pack.toml 所在目录；runtime 侧输入为 Prism 实例目录、游戏目录为其 minecraft/。
	var newRoot, newInstanceDir, newDisplay, newIdentity, newFp string
	var newProj *model.Project
	var newRt *model.Runtime
	readable := false
	readableDetail := ""

	if side == model.SideProject {
		real, nerr := a.deps.EndpointPaths.NormalizeEndpointPath(input.RootPath)
		switch {
		case nerr != nil:
			// R1（ADR-0011 §7）：不可达错误串内嵌绝对路径，检查 detail 新写即别名
			readableDetail = "项目源根目录不可达: " + model.AliasDetail(input.RootPath, model.AliasProject, nerr.Error())
		case !pathExists(filepath.Join(real, "pack.toml")):
			readableDetail = "pack.toml 不存在（不是 Packwiz 项目根目录）"
		default:
			newRoot = real
			readable = true
		}
		checks = append(checks, blocking("check.endpoint.readable", readable, readableDetail, input.RootPath))
		if readable {
			newDisplay = filepath.Base(newRoot)
			if newFp, err = a.deps.Fingerprinter.Fingerprint(newRoot); err != nil {
				return view.RebindPreparationView{}, errs.NewDetail(CodeRelationInvalidEndpoint,
					"计算项目端点指纹失败: "+model.AliasDetail(newRoot, model.AliasProject, err.Error()), newRoot)
			}
			// 绑定草稿携带旧端点 ID：ApplyRebind 原位更新该行（不新建端点）。
			newProj = &model.Project{
				SchemaVersion:      model.CurrentSchemaVersion,
				ProjectID:          oldProj.ProjectID,
				Adapter:            oldProj.Adapter,
				DisplayName:        newDisplay,
				RootPath:           newRoot,
				BindingFingerprint: newFp,
				CreatedAt:          oldProj.CreatedAt,
			}
		}
	} else {
		real, nerr := a.deps.EndpointPaths.NormalizeEndpointPath(input.RootPath)
		switch {
		case nerr != nil:
			readableDetail = "运行实例目录不可达: " + model.AliasDetail(input.RootPath, model.AliasRuntime, nerr.Error())
		default:
			realGame, gerr := a.deps.EndpointPaths.NormalizeEndpointPath(filepath.Join(real, "minecraft"))
			switch {
			case gerr != nil:
				readableDetail = "游戏目录 minecraft/ 不可达: " + model.AliasDetail(input.RootPath, model.AliasRuntime, gerr.Error())
			case !pathExists(filepath.Join(real, "instance.cfg")):
				readableDetail = "Prism 实例目录缺少 instance.cfg 或 minecraft/ 游戏目录"
			default:
				newInstanceDir = real
				newRoot = realGame
				readable = true
			}
		}
		checks = append(checks, blocking("check.endpoint.readable", readable, readableDetail, input.RootPath))
		if readable {
			newDisplay = filepath.Base(newInstanceDir)
			newIdentity = strings.ToLower(newDisplay)
			if newFp, err = a.deps.Fingerprinter.Fingerprint(newRoot); err != nil {
				return view.RebindPreparationView{}, errs.NewDetail(CodeRelationInvalidEndpoint,
					"计算运行时端点指纹失败: "+model.AliasDetail(newRoot, model.AliasRuntime, err.Error()), newInstanceDir)
			}
			newRt = &model.Runtime{
				SchemaVersion:      model.CurrentSchemaVersion,
				RuntimeID:          oldRt.RuntimeID,
				Adapter:            oldRt.Adapter,
				DisplayName:        newDisplay,
				RootPath:           newRoot,
				AdapterIdentity:    newIdentity,
				BindingFingerprint: newFp,
				CreatedAt:          oldRt.CreatedAt,
			}
		}
	}

	// 绑定指纹（与 PrepareRelation 同款检查行；采集失败已在上方硬错误）
	checks = append(checks, blocking("check.endpoint.binding", newFp != "", "端点绑定指纹采集失败"))

	fingerprintChanged := newFp != "" && newFp != oldFingerprintOf(side, oldProj, oldRt)

	// 路径包含关系：新端点根与对侧端点根互为祖先 → 拒绝（新端点不可达时跳过，
	// readable 检查已阻断）
	if readable {
		otherRoot := oldProj.RootPath
		if side == model.SideProject {
			otherRoot = oldRt.RootPath
		}
		newBase := newRoot
		if side == model.SideRuntime {
			newBase = newInstanceDir
		}
		containment := isAncestorOf(newBase, otherRoot) || isAncestorOf(otherRoot, newBase)
		checks = append(checks, blocking("check.path.containment", !containment,
			"新端点与对侧端点存在包含关系，无法建立隔离同步", newBase, otherRoot))
	}

	// 占用检查：新端点的绑定身份已被其他端点行持有 → 记失败检查项（端点行 UNIQUE
	// 约束使原位更新在撞行时物理不可行，且重复指纹/身份/路径会让幂等登记歧义；
	// 「重复 pair」是其中的真子集，统一以 err.relation.duplicate_pair 拒绝）。
	occupiedDetail := ""
	if readable {
		var err error
		if side == model.SideProject {
			occupiedDetail, err = projectOccupiedDetail(ctx, a.deps.Endpoints, newFp, newRoot, oldProj.ProjectID)
		} else {
			occupiedDetail, err = runtimeOccupiedDetail(ctx, a.deps.Endpoints, oldRt.Adapter, newIdentity, oldRt.RuntimeID)
		}
		if err != nil {
			return view.RebindPreparationView{}, err
		}
	} else {
		occupiedDetail = "新端点不可达，占用检查未执行"
	}
	checks = append(checks, blocking("check.pair.duplicate", occupiedDetail == "", occupiedDetail))

	// legacy 痕迹（警告级，识别不覆盖）：新位置存在旧架构 packgradle.toml 表示
	// 旧架构关联配置；仅作为迁移输入读取，不参与新同步语义
	legacyRoot := ""
	if side == model.SideProject {
		legacyRoot = newRoot
	} else {
		legacyRoot = newInstanceDir
	}
	legacyDetail := ""
	if legacyRoot != "" && pathExists(filepath.Join(legacyRoot, "packgradle.toml")) {
		legacyDetail = "检测到旧架构 packgradle.toml；将仅作为迁移输入读取，不参与新同步语义"
	}
	checks = append(checks, model.PreparationCheck{
		Code: "check.legacy.materialization", Passed: true, Severity: "warning", Detail: legacyDetail,
	})

	// 重绑影响：该关系下仍可推进（draft/resolved）的计划将在重绑后因绑定指纹
	// 失配投影为 stale（rebind 不递增 relation_revision，ADR-0002 决议 2）
	invalidated, err := a.deps.Plans.CountByRelation(ctx, rel.RelationID)
	if err != nil {
		return view.RebindPreparationView{}, err
	}

	now := a.deps.Now().UTC()
	prep := model.RebindPreparation{
		SchemaVersion:        model.CurrentSchemaVersion,
		PreparationID:        a.deps.IDs("prep_"),
		RelationID:           rel.RelationID,
		Side:                 side,
		CreatedAt:            now.Format(time.RFC3339),
		ExpiresAt:            now.Add(preparationTTL).Format(time.RFC3339),
		InputRootPath:        input.RootPath,
		NewProject:           newProj,
		NewRuntime:           newRt,
		Checks:               checks,
		FingerprintChanged:   fingerprintChanged,
		BaselineInheritance:  model.BaselineInheritanceReinitialize,
		InvalidatedPlanCount: invalidated,
	}
	if err := a.deps.Preparations.InsertRebind(ctx, prep); err != nil {
		return view.RebindPreparationView{}, err
	}
	return rebindPreparationView(prep, oldProj, oldRt), nil
}

// ApplyRebind 消费重绑预检并原位更新端点绑定（契约 03 §2.4）。写入收进单个
// SQLite 事务（ADR-0003：消费预检 → 占用复核 → 更新端点 → 健康恢复 → 基线重置）：
// 中途失败整体回滚、零残留，同一 preparationID 可安全重试直至过期。
// 基线恒 reinitialize（清除 head_baseline_id，不继承）；修订号不动
// （ADR-0002 决议 2：唯一递增源是 policy 修改）。事件发布恒在事务提交之后。
func (a *App) ApplyRebind(ctx context.Context, preparationID string) (view.RelationView, error) {
	prep, err := a.deps.Preparations.GetRebind(ctx, preparationID)
	if err != nil {
		return view.RelationView{}, errs.New(CodeRebindPrepNotFound, preparationID)
	}

	var result view.RelationView
	err = a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		// 消费预检是事务内第一步：过期/已消费的存储层守卫在此（拆码由
		// MarkRebindConsumed 的哨兵区分），后续任何失败都回滚本次消费。
		if err := repos.Preparations.MarkRebindConsumed(ctx, preparationID); err != nil {
			switch {
			case errors.Is(err, ports.ErrPreparationExpired):
				return errs.New(CodeRebindPrepExpired, preparationID)
			case errors.Is(err, ports.ErrPreparationConsumed):
				return errs.New(CodeRebindPrepConsumed, preparationID)
			default:
				return err
			}
		}
		// 预检结果冻结于 prep；blocking 未通过 → 回滚消费（预检保持可重试）。
		if err := blockingCheckFailure(prep.Checks); err != nil {
			return err
		}
		rel, err := repos.Relations.Get(ctx, prep.RelationID)
		if err != nil {
			return errs.New(CodeRelationNotFound, prep.RelationID)
		}
		// 占用复核：预检到应用之间端点登记可能变化（占用 → 撞 UNIQUE 约束）
		if err := rebindOccupiedTx(ctx, repos, prep, rel); err != nil {
			return err
		}

		var proj model.Project
		var rt model.Runtime
		if prep.Side == model.SideProject {
			proj = *prep.NewProject
			if err := repos.Endpoints.UpdateProject(ctx, proj); err != nil {
				if errors.Is(err, ports.ErrDuplicate) {
					return errs.NewDetail(CodeRelationDuplicatePair, "新根路径已登记为其他项目端点", proj.RootPath)
				}
				return err
			}
			if rt, err = repos.Endpoints.GetRuntime(ctx, rel.RuntimeID); err != nil {
				return err
			}
		} else {
			rt = *prep.NewRuntime
			if err := repos.Endpoints.UpdateRuntime(ctx, rt); err != nil {
				if errors.Is(err, ports.ErrDuplicate) {
					return errs.NewDetail(CodeRelationDuplicatePair, "新实例目录或名称已登记为其他运行实例", rt.AdapterIdentity)
				}
				return err
			}
			if proj, err = repos.Endpoints.GetProject(ctx, rel.ProjectID); err != nil {
				return err
			}
		}

		// 健康恢复 + 基线重置（恒 reinitialize：不继承基线，重绑后工作区进入
		// 「需要初始化」直到完整扫描证明等价，契约 03 §2.4）；修订号不动。
		if err := repos.Relations.UpdateHealth(ctx, rel.RelationID, model.HealthHealthy); err != nil {
			return err
		}
		if err := repos.Relations.UpdateHeadBaseline(ctx, rel.RelationID, ""); err != nil {
			return err
		}
		rel.Health = model.HealthHealthy
		rel.HeadBaselineID = ""
		result = relationView(rel, proj, rt)
		return nil
	})
	if err != nil {
		return view.RelationView{}, err
	}
	// 事务已提交；发布 relation_invalidated（事件恒在提交之后，发布失败不影响提交）
	_ = a.pub.PublishRelationInvalidated(ctxWithoutCancel(ctx), prep.RelationID)
	return result, nil
}

// oldFingerprintOf 返回重绑侧的当前存储指纹。
func oldFingerprintOf(side model.Side, proj model.Project, rt model.Runtime) string {
	if side == model.SideProject {
		return proj.BindingFingerprint
	}
	return rt.BindingFingerprint
}

// projectOccupiedDetail 返回项目侧重绑目标的占用证据（空串 = 未占用）：
// 新指纹或新根路径被其他项目行持有——前者让幂等登记（FindProjectByRoot）歧义，
// 后者撞 UNIQUE(adapter, root_path) 使原位更新物理不可行；预检就给出证据。
func projectOccupiedDetail(ctx context.Context, endpoints ports.EndpointRepository, newFp, newRoot, selfProjectID string) (string, error) {
	projects, err := endpoints.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.ProjectID == selfProjectID {
			continue
		}
		if p.BindingFingerprint == newFp {
			return "新路径的绑定指纹已登记为其他项目端点: " + p.RootPath, nil
		}
		if strings.EqualFold(filepath.Clean(p.RootPath), filepath.Clean(newRoot)) {
			return "新根路径已登记为其他项目端点: " + p.RootPath, nil
		}
	}
	return "", nil
}

// runtimeOccupiedDetail 返回运行实例侧重绑目标的占用证据（空串 = 未占用）：
// 新 adapter identity（= 实例目录名）被其他运行实例行持有——撞
// UNIQUE(adapter, adapter_identity) 使原位更新物理不可行，且重复身份会让
// 幂等登记（FindRuntimeByIdentity）歧义。
func runtimeOccupiedDetail(ctx context.Context, endpoints ports.EndpointRepository, runtimeAdapter, newIdentity, selfRuntimeID string) (string, error) {
	existing, found, err := endpoints.FindRuntimeByIdentity(ctx, runtimeAdapter, newIdentity)
	if err != nil {
		return "", err
	}
	if found && existing.RuntimeID != selfRuntimeID {
		// R1（ADR-0011 §7）：已登记端点根为绝对路径，占用证据别名化
		return "同名实例目录已登记为其他运行实例: " + model.AliasPath(existing.RootPath, model.AliasRuntime, existing.RootPath), nil
	}
	return "", nil
}

// rebindOccupiedTx 是 ApplyRebind 事务内的占用复核（预检到应用之间登记可能变化）；
// 撞行映射为 err.relation.duplicate_pair（存储层 UNIQUE 兜底同样落在该映射）。
func rebindOccupiedTx(ctx context.Context, repos ports.Repos, prep model.RebindPreparation, rel model.Relation) error {
	var (
		detail string
		arg    string
		err    error
	)
	if prep.Side == model.SideProject {
		detail, err = projectOccupiedDetail(ctx, repos.Endpoints,
			prep.NewProject.BindingFingerprint, prep.NewProject.RootPath, rel.ProjectID)
		arg = prep.NewProject.RootPath
	} else {
		detail, err = runtimeOccupiedDetail(ctx, repos.Endpoints,
			prep.NewRuntime.Adapter, prep.NewRuntime.AdapterIdentity, rel.RuntimeID)
		arg = prep.NewRuntime.AdapterIdentity
	}
	if err != nil {
		return err
	}
	if detail != "" {
		return errs.NewDetail(CodeRelationDuplicatePair, detail, arg)
	}
	return nil
}

// rebindPreparationView 组装重绑预检投影。旧端点取当前登记；新端点草稿不可得
// （新路径不可达）时以原始输入填充候选证据，端点 ID 恒为旧端点（原位更新语义）。
func rebindPreparationView(prep model.RebindPreparation, oldProj model.Project, oldRt model.Runtime) view.RebindPreparationView {
	var oldView view.EndpointView
	var newView view.EndpointView
	if prep.Side == model.SideProject {
		oldView = projectEndpointView(oldProj)
		newView = view.EndpointView{
			ID: oldProj.ProjectID, Adapter: oldProj.Adapter,
			DisplayName: filepath.Base(prep.InputRootPath), RootPath: prep.InputRootPath,
		}
		if prep.NewProject != nil {
			newView = projectEndpointView(*prep.NewProject)
		}
	} else {
		oldView = runtimeEndpointView(oldRt)
		newView = view.EndpointView{
			ID: oldRt.RuntimeID, Adapter: oldRt.Adapter,
			DisplayName: filepath.Base(prep.InputRootPath), RootPath: prep.InputRootPath,
		}
		if prep.NewRuntime != nil {
			newView = runtimeEndpointView(*prep.NewRuntime)
		}
	}

	checks := make([]view.PreparationCheckView, 0, len(prep.Checks))
	for _, c := range prep.Checks {
		args := c.Args
		if args == nil {
			args = []string{}
		}
		checks = append(checks, view.PreparationCheckView{
			Code: c.Code, Passed: c.Passed, Severity: c.Severity, Args: args, Detail: c.Detail,
		})
	}
	return view.RebindPreparationView{
		SchemaVersion:        model.CurrentSchemaVersion,
		PreparationID:        prep.PreparationID,
		CreatedAt:            prep.CreatedAt,
		ExpiresAt:            prep.ExpiresAt,
		Side:                 string(prep.Side),
		Checks:               checks,
		OldEndpoint:          oldView,
		NewEndpoint:          newView,
		FingerprintChanged:   prep.FingerprintChanged,
		BaselineInheritance:  prep.BaselineInheritance,
		InvalidatedPlanCount: prep.InvalidatedPlanCount,
	}
}
