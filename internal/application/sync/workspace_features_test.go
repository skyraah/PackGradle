package sync

import (
	"testing"

	"packgradle/internal/core/model"
)

// workspaceFeatures 固定值（契约 03 §2.1 + 契约 05 §1 P2 固定值表）。
func TestWorkspaceFeaturesFixedValues(t *testing.T) {
	f := workspaceFeatures()
	if !f.Scan || !f.SyncPreview || !f.ConflictInspection {
		t.Errorf("能力面 scan/sync_preview/conflict_inspection 应为 true: %+v", f)
	}
	// P2 点亮（契约 05 §1）：sync_apply（ConfirmPlan/Apply 全链路）+ history_view
	if !f.SyncApply || !f.HistoryView {
		t.Errorf("P2 应点亮 sync_apply/history_view: %+v", f)
	}
	// restore 全家维持 false（Phase 3，硬约束 2）
	if f.RestorePreview || f.RestoreApply {
		t.Errorf("restore 能力应维持 false: %+v", f)
	}
	if f.ConflictResolution != "choose_side" {
		t.Errorf("conflict_resolution 应为 choose_side，得到 %s", f.ConflictResolution)
	}
	if len(f.MaterializationModes) != 1 || f.MaterializationModes[0] != "copy" {
		t.Errorf("materialization_modes 应为 [\"copy\"]: %v", f.MaterializationModes)
	}
}

// deriveApplySyncAvailability 按契约 05 §1 apply_sync 推导行逐支断言
// （原因码优先级：already_running → in_progress → incomplete → expired/stale →
// none_ready；票 #36）。
func TestDeriveApplySyncAvailability(t *testing.T) {
	recovery := string(model.HealthRecoveryRequired)

	ready := planReadiness{hasResolved: true, hasApplicable: true}
	expiredPlan := planReadiness{hasResolved: true, latestExpired: true}
	stalePlan := planReadiness{hasResolved: true}
	noPlan := planReadiness{}

	cases := []struct {
		name          string
		health        string
		scanState     string
		hasActiveTask bool
		plans         planReadiness
		available     bool
		reason        string
	}{
		{
			name: "全条件满足且存在可应用计划 → available",
			health: string(model.HealthHealthy), scanState: "ready", plans: ready,
			available: true,
		},
		{
			name: "活跃任务优先（推导表列序第一）",
			health: string(model.HealthHealthy), scanState: "ready", hasActiveTask: true, plans: ready,
			reason: "err.scan.already_running",
		},
		{
			name: "恢复占用 → in_progress",
			health: recovery, scanState: "ready", plans: ready,
			reason: "err.recovery.in_progress",
		},
		{
			name: "扫描未就绪 → incomplete",
			health: string(model.HealthHealthy), scanState: "never_scanned", plans: ready,
			reason: "err.scan.incomplete",
		},
		{
			name: "无任何 resolved 计划 → none_ready（新码）",
			health: string(model.HealthHealthy), scanState: "ready", plans: noPlan,
			reason: "err.plan.none_ready",
		},
		{
			name: "最新 resolved 计划已过期 → expired（既有码）",
			health: string(model.HealthHealthy), scanState: "ready", plans: expiredPlan,
			reason: "err.plan.expired",
		},
		{
			name: "最新 resolved 计划 stale → stale（既有码）",
			health: string(model.HealthHealthy), scanState: "ready", plans: stalePlan,
			reason: "err.plan.stale",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveApplySyncAvailability(tc.health, tc.scanState, tc.hasActiveTask, tc.plans)
			if got.Action != "apply_sync" {
				t.Fatalf("action = %s，期望 apply_sync", got.Action)
			}
			if got.Available != tc.available || got.ReasonCode != tc.reason {
				t.Fatalf("available=%v reason=%q，期望 available=%v reason=%q",
					got.Available, got.ReasonCode, tc.available, tc.reason)
			}
		})
	}
}

// deriveQuickUpdateAvailability 按契约 06 §1 quick_update 推导行逐支断言
// （票 #62：授权开关未开启 → err.auth_mode.disabled 优先；其余三门禁与
// prepare_restore 同款，原因码沿 already_running → in_progress → incomplete）。
func TestDeriveQuickUpdateAvailability(t *testing.T) {
	recovery := string(model.HealthRecoveryRequired)

	cases := []struct {
		name          string
		health        string
		scanState     string
		hasActiveTask bool
		authorized    bool
		available     bool
		reason        string
	}{
		{
			name: "开关开启且无活跃任务且非恢复且扫描就绪 → available",
			health: string(model.HealthHealthy), scanState: "ready", authorized: true,
			available: true,
		},
		{
			name: "开关未开启 → auth_mode.disabled（连举条件序第一，优先于其余门禁）",
			health: string(model.HealthHealthy), scanState: "ready", authorized: false,
			reason: "err.auth_mode.disabled",
		},
		{
			name: "开关未开启且扫描未就绪 → 仍报 auth_mode.disabled（引导先开开关）",
			health: string(model.HealthHealthy), scanState: "never_scanned", authorized: false,
			reason: "err.auth_mode.disabled",
		},
		{
			name: "活跃任务占用 → already_running",
			health: string(model.HealthHealthy), scanState: "ready", hasActiveTask: true, authorized: true,
			reason: "err.scan.already_running",
		},
		{
			name: "恢复占用 → in_progress（开关值保留，门禁单独挡）",
			health: recovery, scanState: "ready", authorized: true,
			reason: "err.recovery.in_progress",
		},
		{
			name: "扫描未就绪 → incomplete",
			health: string(model.HealthHealthy), scanState: "failed", authorized: true,
			reason: "err.scan.incomplete",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveQuickUpdateAvailability(tc.health, tc.scanState, tc.hasActiveTask, tc.authorized)
			if got.Action != "quick_update" {
				t.Fatalf("action = %s，期望 quick_update", got.Action)
			}
			if got.Available != tc.available || got.ReasonCode != tc.reason {
				t.Fatalf("available=%v reason=%q，期望 available=%v reason=%q",
					got.Available, got.ReasonCode, tc.available, tc.reason)
			}
		})
	}
}

// deriveAvailability 按契约 03 §2.1 推导表逐行断言。
func TestDeriveAvailability(t *testing.T) {
	recovery := string(model.HealthRecoveryRequired)

	type expect struct {
		available bool
		reason    string
	}
	want := func(available bool, reason string) expect { return expect{available, reason} }

	cases := []struct {
		name          string
		health        string
		scanState     string
		hasActiveTask bool
		scan          expect
		prepareSync   expect
		rebind        expect
	}{
		{
			name: "健康且无活跃任务，未扫描",
			scanState: "never_scanned",
			scan:      want(true, ""),
			// scan_state 非 ready → incomplete
			prepareSync: want(false, "err.scan.incomplete"),
			rebind:      want(true, ""),
		},
		{
			name: "健康且无活跃任务，已就绪",
			scanState: "ready",
			scan:        want(true, ""),
			prepareSync: want(true, ""),
			rebind:      want(true, ""),
		},
		{
			name: "活跃任务占用（已就绪也不可同步）",
			scanState: "ready",
			hasActiveTask: true,
			scan:        want(false, "err.scan.already_running"),
			prepareSync: want(false, "err.scan.already_running"),
			rebind:      want(false, "err.scan.already_running"),
		},
		{
			name: "扫描中（scan_state=scanning 且有活跃任务）",
			scanState: "scanning",
			hasActiveTask: true,
			scan:        want(false, "err.scan.already_running"),
			prepareSync: want(false, "err.scan.already_running"),
			rebind:      want(false, "err.scan.already_running"),
		},
		{
			name: "恢复占用优先于活跃任务（scan/rebind）",
			health: recovery,
			scanState: "ready",
			hasActiveTask: true,
			scan:        want(false, "err.recovery.in_progress"),
			prepareSync: want(false, "err.scan.already_running"),
			rebind:      want(false, "err.recovery.in_progress"),
		},
		{
			name: "恢复占用（无活跃任务）",
			health: recovery,
			scanState: "ready",
			scan:        want(false, "err.recovery.in_progress"),
			// 契约 prepare_sync 条件只看 ready 与活跃任务
			prepareSync: want(true, ""),
			rebind:      want(false, "err.recovery.in_progress"),
		},
		{
			name: "rebind_required 健康态不阻塞 rebind（路径迁移是合法操作）",
			health: string(model.HealthRebindRequired),
			scanState: "ready",
			scan:        want(true, ""),
			prepareSync: want(true, ""),
			rebind:      want(true, ""),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveAvailability(tc.health, tc.scanState, tc.hasActiveTask)
			if len(got) != 3 {
				t.Fatalf("应恰好 3 个动作条目（feature=false 不注册），得到 %d", len(got))
			}
			byAction := map[string]expect{}
			for _, a := range got {
				byAction[a.Action] = expect{a.Available, a.ReasonCode}
			}
			for action, w := range map[string]expect{
				"scan": tc.scan, "prepare_sync": tc.prepareSync, "rebind": tc.rebind,
			} {
				g := byAction[action]
				if g != w {
					t.Errorf("%s = %+v，期望 %+v", action, g, w)
				}
			}
		})
	}
}
