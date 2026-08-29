package sync

import (
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// P1 能力面固定值与 availability 推导（契约 03 §2.1；架构 §10.4）。
// feature=false 的动作不注册：availability 不含 apply_sync/prepare_restore/
// apply_restore 条目，前端不渲染入口。

// availability 动作名（与契约 03 §2.1 action 枚举一致）。
const (
	actionScan        = "scan"
	actionPrepareSync = "prepare_sync"
	actionRebind      = "rebind"
)

// p1Features 返回 P1 固定能力面（契约 03 §2.1 固定值表）。
func p1Features() view.WorkspaceFeaturesView {
	return view.WorkspaceFeaturesView{
		Scan:                 true,
		SyncPreview:          true,
		SyncApply:            false,
		ConflictInspection:   true,
		ConflictResolution:   "choose_side",
		HistoryView:          false,
		RestorePreview:       false,
		RestoreApply:         false,
		MaterializationModes: []string{},
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
