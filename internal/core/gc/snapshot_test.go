package gc

// snapshot_test.go 覆盖孤儿快照判定纯函数（ADR-0011 §4，票 #89）：
// 判定是 ADR 定义的确定函数——单快照真值仅取决于（提交引用, 计划引用,
// 任一端最新）三元组，表驱动 2³=8 格全格覆盖；外加多快照混合场景与
// 悬空引用的忽略语义。

import "testing"

// TestOrphanSnapshotsFullGrid 三元组全格：孤儿 ⇔ 三通道皆无引用。
// 任一通道命中即保留（提交验证快照随提交存亡、计划输入快照随计划存亡、
// 任一端最新一份是当前状态读取面）。
func TestOrphanSnapshotsFullGrid(t *testing.T) {
	cases := []struct {
		name           string
		commitVerified bool
		planInput      bool
		latest         bool
		wantOrphan     bool
	}{
		{"无任何引用", false, false, false, true},
		{"仅提交验证引用", true, false, false, false},
		{"仅计划输入引用", false, true, false, false},
		{"仅任一端最新", false, false, true, false},
		{"提交+计划引用", true, true, false, false},
		{"提交引用+最新", true, false, true, false},
		{"计划引用+最新", false, true, true, false},
		{"三通道皆命中", true, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := SnapshotFacts{All: []string{"snap_x"}}
			if tc.commitVerified {
				facts.CommitVerified = []string{"snap_x"}
			}
			if tc.planInput {
				facts.PlanInput = []string{"snap_x"}
			}
			if tc.latest {
				facts.Latest = []string{"snap_x"}
			}
			got := OrphanSnapshots(facts)
			if tc.wantOrphan && len(got) != 1 {
				t.Fatalf("判定 %v，期望孤儿 [snap_x]", got)
			}
			if !tc.wantOrphan && len(got) != 0 {
				t.Fatalf("判定 %v，期望保留（非孤儿）", got)
			}
		})
	}
}

// TestOrphanSnapshotsMixed 多快照混合场景（acceptance:gc 孤儿快照扩展的
// 单测投影）：提交验证快照保留、计划输入快照保留、两端最新保留、
// 无引用中间扫描快照判孤儿；输入顺序保持输出确定性。
func TestOrphanSnapshotsMixed(t *testing.T) {
	facts := SnapshotFacts{
		All: []string{
			"snap_scan_old_p", // 中间扫描快照（项目端）：无引用 → 孤儿
			"snap_scan_old_r", // 中间扫描快照（运行端）：无引用 → 孤儿
			"snap_scan_new_p", // 最新一份（项目端）→ 保留
			"snap_scan_new_r", // 最新一份（运行端）→ 保留
			"snap_verified_p", // 存活提交验证快照 → 保留
			"snap_input_p",    // 计划输入快照 → 保留
			"snap_pruned_ref", // 提交被修剪后的失引验证快照 → 孤儿（自然转孤儿一并删）
		},
		CommitVerified: []string{"snap_verified_p"},
		PlanInput:      []string{"snap_input_p", "snap_scan_new_p"},
		Latest:         []string{"snap_scan_new_p", "snap_scan_new_r"},
	}
	got := OrphanSnapshots(facts)
	want := []string{"snap_scan_old_p", "snap_scan_old_r", "snap_pruned_ref"}
	if len(got) != len(want) {
		t.Fatalf("孤儿集 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("孤儿集 %v，期望 %v（顺序即 All 序，确定性输出）", got, want)
		}
	}
}

// TestOrphanSnapshotsDanglingRefs 引用集指向不存在快照（悬空 id）不影响
// 判定；空集全 → 空孤儿集；引用比对大小写不敏感（ULID 原文输出保留）。
func TestOrphanSnapshotsDanglingRefs(t *testing.T) {
	facts := SnapshotFacts{
		All:            []string{"snap_a"},
		CommitVerified: []string{"snap_gone"},
	}
	got := OrphanSnapshots(facts)
	if len(got) != 1 || got[0] != "snap_a" {
		t.Fatalf("悬空引用应被忽略，got %v", got)
	}
	if out := OrphanSnapshots(SnapshotFacts{}); len(out) != 0 {
		t.Fatalf("空全集应返回空孤儿集，got %v", out)
	}
	// 大小写归一：大写 ULID 快照被小写引用集命中 → 保留且原文输出。
	mixed := OrphanSnapshots(SnapshotFacts{
		All:            []string{"snap_01M1J138HXB1Q06SW2BX9W3MZ0"},
		CommitVerified: []string{"snap_01m1j138hxb1q06sw2bx9w3mz0"},
	})
	if len(mixed) != 0 {
		t.Fatalf("大小写不敏感比对失败，got %v", mixed)
	}
}
