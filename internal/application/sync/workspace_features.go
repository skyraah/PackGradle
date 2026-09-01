package sync

import (
	"context"

	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// 能力面固定值与 availability 推导（P1 契约 03 §2.1 + P2 契约 05 §1；架构 §10.4）。
// feature=false 的动作不注册：availability 不含 prepare_restore/apply_restore
// 条目，前端不渲染入口（Phase 3 能力，硬约束 2）。

// availability 动作名（与契约 03 §2.1 action 枚举一致）。
const (
	actionScan        = "scan"
	actionPrepareSync = "prepare_sync"
	actionApplySync   = "apply_sync"
	actionRebind      = "rebind"
)

// workspaceFeatures 返回能力面固定值（契约 05 §1：Phase 2 起为 P2 固定值表）。
func workspaceFeatures() view.WorkspaceFeaturesView {
	return view.WorkspaceFeaturesView{
		Scan:                 true,
		SyncPreview:          true,
		SyncApply:            true, // ConfirmPlan/Apply 全链路点亮（T03）
		ConflictInspection:   true,
		ConflictResolution:   "choose_side",
		HistoryView:          true, // ListCommits/GetCommit（T03 契约面，消费票落地）
		RestorePreview:       false,
		RestoreApply:         false,
		// MaterializationModes（契约 06 §3.7，票 #63）：download 物化点亮（CF
		// 免钥匙直链，ADR-0008）；v1 模式选用为后端推导，无用户选择面。
		MaterializationModes: []string{"copy", "download"},
	}
}

// deriveAvailability 按契约 03 §2.1 推导表计算各动作可用性。
//   - scan/rebind：被恢复占用（health=recovery_required，即恢复任务占用）或
//     活跃任务阻塞；原因优先恢复占用。rebind 不受 rebind_required 等端点
//     健康态影响——路径迁移是合法的主动操作（契约表行注）。
//   - prepare_sync：被活跃任务阻塞（此时 scan_state 必非 ready，already_running
//     比 incomplete 更准确）；否则 scan_state 非 ready → incomplete。
func deriveAvailability(health, scanState string, hasActiveTask bool) []view.ActionAvailabilityView {
	recovery := health == string(model.HealthRecoveryRequired)

	taskBlockedCode := ""
	switch {
	case recovery:
		taskBlockedCode = CodeRecoveryInProgress
	case hasActiveTask:
		taskBlockedCode = CodeRelationScanRunning
	}
	prepareCode := ""
	switch {
	case hasActiveTask:
		prepareCode = CodeRelationScanRunning
	case scanState != "ready":
		prepareCode = CodeScanIncomplete
	}

	mk := func(action string, available bool, reasonCode string) view.ActionAvailabilityView {
		a := view.ActionAvailabilityView{Action: action, Available: available}
		if reasonCode != "" {
			a.ReasonCode = reasonCode
		}
		return a
	}
	return []view.ActionAvailabilityView{
		mk(actionScan, !hasActiveTask && !recovery, taskBlockedCode),
		mk(actionPrepareSync, scanState == "ready" && !hasActiveTask, prepareCode),
		mk(actionRebind, !hasActiveTask && !recovery, taskBlockedCode),
	}
}

// planReadiness 是 apply_sync availability 的计划面输入（契约 05 §1：存在可应用
// 计划 = status=resolved、非 stale、非 expired）。
type planReadiness struct {
	hasResolved   bool // 存在 resolved 计划行
	hasApplicable bool // 存在可应用计划（resolved 且未过期且修订/绑定一致）
	latestExpired bool // 最新 resolved 计划已过期（原因码判定先过期后 stale，同 planViewWithStatus）
}

// deriveApplySyncAvailability 按契约 05 §1 apply_sync 推导行计算：无活跃任务，
// 且 relation_health 非 recovery_required，且 scan_state=ready，且该关系存在可
// 应用的计划。原因码优先级沿推导表列序：already_running → in_progress →
// incomplete → expired/stale（既有）→ none_ready（新增）。
func deriveApplySyncAvailability(health, scanState string, hasActiveTask bool, plans planReadiness) view.ActionAvailabilityView {
	a := view.ActionAvailabilityView{Action: actionApplySync, Available: false}
	switch {
	case hasActiveTask:
		a.ReasonCode = CodeRelationScanRunning
	case health == string(model.HealthRecoveryRequired):
		a.ReasonCode = CodeRecoveryInProgress
	case scanState != "ready":
		a.ReasonCode = CodeScanIncomplete
	case !plans.hasResolved:
		a.ReasonCode = CodePlanNoneReady
	case plans.hasApplicable:
		a.Available = true
	case plans.latestExpired:
		a.ReasonCode = CodePlanExpired
	default:
		a.ReasonCode = CodePlanStale
	}
	return a
}

// planReadinessForRelation 汇总关系计划面的 apply_sync 推导输入：逐计划判定
// resolved + 可应用（未过期、修订一致、两端绑定指纹一致），任一满足即可应用；
// expired/stale 原因取最新 resolved 计划的缺陷。计划读取失败向上传播
// （availability 是投影的一部分，不静默降级）。
func (a *App) planReadinessForRelation(ctx context.Context, rel model.Relation, proj model.Project, rt model.Runtime) (planReadiness, error) {
	plans, err := a.deps.Plans.ListByRelation(ctx, rel.RelationID)
	if err != nil {
		return planReadiness{}, err
	}
	var rd planReadiness
	now := a.deps.Now().UTC()
	for i := range plans {
		p := plans[i]
		if p.Status != model.PlanResolved {
			continue
		}
		rd.hasResolved = true
		rd.latestExpired = expired(p.ExpiresAt, now)
		if !rd.latestExpired &&
			p.RelationRevision == rel.Revision &&
			p.ExpectedBindings.Project == proj.BindingFingerprint &&
			p.ExpectedBindings.Runtime == rt.BindingFingerprint {
			rd.hasApplicable = true
		}
	}
	return rd, nil
}
