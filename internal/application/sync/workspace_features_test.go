package sync

import (
	"testing"

	"packgradle/internal/core/model"
)

// p1Features 固定值（契约 03 §2.1 固定值表）。
func TestP1FeaturesFixedValues(t *testing.T) {
	f := p1Features()
	if !f.Scan || !f.SyncPreview || !f.ConflictInspection {
		t.Errorf("P1 能力面 scan/sync_preview/conflict_inspection 应为 true: %+v", f)
	}
	if f.SyncApply || f.HistoryView || f.RestorePreview || f.RestoreApply {
		t.Errorf("P1 未实现能力应为 false: %+v", f)
	}
	if f.ConflictResolution != "choose_side" {
		t.Errorf("conflict_resolution 应为 choose_side，得到 %s", f.ConflictResolution)
	}
	if len(f.MaterializationModes) != 0 {
		t.Errorf("materialization_modes 应为空数组（非 nil 语义由 transport 归一）: %v", f.MaterializationModes)
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
