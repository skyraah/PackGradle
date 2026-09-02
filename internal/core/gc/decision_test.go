package gc

import (
	"testing"
	"time"

	"packgradle/internal/core/model"
)

// 决策函数单测（验收规格 §6.1：纯函数 + fake clock 表驱动）。
// 锚点选择（N/D/C）、连续前缀修剪、级联产出、K=3 硬保底（宁超不违保底）、
// 屏障（活跃计划/头基线引用）、trash 老化全部在此覆盖；执行面（隔离/复活/
// 安全窗口续排）归 sync 引擎测试。

var baseTime = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func day(n int) time.Time { return baseTime.Add(-time.Duration(n) * 24 * time.Hour) }

// chain 构造 n 个提交的 oldest-first 链：commit_i，基线 base_i，createdAt 逐个更新。
func chain(n int, createdAt func(i int) time.Time) []CommitNode {
	nodes := make([]CommitNode, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, CommitNode{
			CommitID:         commitIDOf(i),
			ParentID:         parentOf(i),
			ResultBaselineID: baselineIDOf(i),
			CreatedAt:        createdAt(i),
		})
	}
	return nodes
}

func commitIDOf(i int) string { return "commit_" + string(rune('a'+i)) }
func baselineIDOf(i int) string { return "base_" + string(rune('a'+i)) }
func parentOf(i int) string {
	if i == 0 {
		return ""
	}
	return commitIDOf(i - 1)
}

// evenRefs 给每个提交一个独占对象（digest 内嵌序号，size 100），便于容量投影断言。
func evenRefs(n int) []ObjectRef {
	refs := make([]ObjectRef, 0, n)
	for i := 0; i < n; i++ {
		refs = append(refs, ObjectRef{CommitID: commitIDOf(i), Digest: digestOf(i), Size: 100})
	}
	return refs
}

func digestOf(i int) string { return "d" + string(rune('a'+i)) }

func retention(keepCommits, keepDays int, capacity int64) model.RetentionSettings {
	return model.RetentionSettings{
		KeepCommits:           keepCommits,
		KeepDays:              keepDays,
		RelationCapacityBytes: capacity,
		PreserveMaxBytes:      model.PreserveMaxDefault,
		TrashDays:             model.TrashDaysDefault,
	}
}

// TestPlanPruningAnchors 表驱动：N/D 锚点选择与连续前缀。
func TestPlanPruningAnchors(t *testing.T) {
	// 24 提交、逐个 1 天前（D=90 天全不命中）。
	newest := func(i int) time.Time { return day(24 - i) }
	tests := []struct {
		name      string
		commits   []CommitNode
		retention model.RetentionSettings
		wantPrune int // 期望前缀长度
	}{
		{"N 内不裁", chain(20, newest), retention(20, 90, 0), 0},
		{"N 超出裁最旧", chain(24, newest), retention(20, 90, 0), 4},
		{"K 保底：链长=K 全活", chain(3, newest), retention(5, 90, 0), 0},
		{"N 大于链长不裁", chain(6, newest), retention(20, 90, 0), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanPruning(PruneInput{
				Now: baseTime, Retention: tc.retention, Commits: tc.commits, Refs: evenRefs(len(tc.commits)),
			})
			if len(got.Pruned) != tc.wantPrune {
				t.Fatalf("裁剪数 = %d，期望 %d（%v）", len(got.Pruned), tc.wantPrune, got.Pruned)
			}
			// 连续前缀：被裁的必须恰是链首 got.Pruned 个。
			for i, id := range got.Pruned {
				if id != tc.commits[i].CommitID {
					t.Fatalf("被裁[%d] = %s，期望链首 %s", i, id, tc.commits[i].CommitID)
				}
			}
		})
	}
}

// TestPlanPruningDaysAnchor 时间锚点：早于 D 的旧提交裁；前缀内混入新提交则截断。
func TestPlanPruningDaysAnchor(t *testing.T) {
	// 10 提交：前 4 个 100 天前（>D=90），第 5 个 10 天前。
	aged := func(i int) time.Time {
		if i < 4 {
			return day(100)
		}
		return day(10)
	}
	got := PlanPruning(PruneInput{
		Now: baseTime, Retention: retention(20, 90, 0), Commits: chain(10, aged), Refs: evenRefs(10),
	})
	if len(got.Pruned) != 4 {
		t.Fatalf("D 锚点应裁 4 个旧提交，实际 %d", len(got.Pruned))
	}

	// 连续前缀截断：第 2 个提交是新的（10 天），其余全旧——前缀只能到第 2 个前。
	mixed := func(i int) time.Time {
		if i == 1 {
			return day(1)
		}
		return day(200)
	}
	got = PlanPruning(PruneInput{
		Now: baseTime, Retention: retention(20, 90, 0), Commits: chain(10, mixed), Refs: evenRefs(10),
	})
	if len(got.Pruned) != 1 {
		t.Fatalf("连续前缀应在新提交处截断（裁 1 个），实际 %d", len(got.Pruned))
	}
}

// TestPlanPruningCapacityAnchor 容量锚点：超 C 追加修剪，仍受 K 与屏障约束。
func TestPlanPruningCapacityAnchor(t *testing.T) {
	// 10 提交全部 1 天前（N=20/D=90 不触发）；每提交独占 100 字节。
	fresh := func(int) time.Time { return day(1) }
	// C=550：需把占用从 1000 降到 ≤550 → 至少裁 5 个（裁后 500）。
	got := PlanPruning(PruneInput{
		Now: baseTime, Retention: retention(20, 90, 550), Commits: chain(10, fresh), Refs: evenRefs(10),
	})
	if len(got.Pruned) != 5 {
		t.Fatalf("容量锚点应追加裁 5 个，实际 %d", len(got.Pruned))
	}
	if !got.CapacityDriven {
		t.Fatal("应标记 CapacityDriven")
	}
	if got.CapacityExceeded {
		t.Fatal("裁后占用 500 ≤ 550，不应 CapacityExceeded")
	}

	// 共享对象不重复记账：全部提交引用同一 digest（100 字节）——占用恒 100，
	// 裁剪不释放字节，不触发追加。
	shared := []ObjectRef{}
	for i := 0; i < 10; i++ {
		shared = append(shared, ObjectRef{CommitID: commitIDOf(i), Digest: "shared", Size: 100})
	}
	got = PlanPruning(PruneInput{
		Now: baseTime, Retention: retention(20, 90, 550), Commits: chain(10, fresh), Refs: shared,
	})
	if len(got.Pruned) != 0 {
		t.Fatalf("共享对象占用 100 ≤ 550，不应裁剪，实际裁 %d", len(got.Pruned))
	}
}

// TestPlanPruningHardFloor K=3 硬保底：容量宁超不违保底。
func TestPlanPruningHardFloor(t *testing.T) {
	// 5 提交、每提交独占 1000 字节、C=1：可裁区 [0,2)，全裁仍超——
	// CapacityExceeded=true 且存活 ≥3。
	big := evenRefs(5)
	for i := range big {
		big[i].Size = 1000
	}
	fresh := func(int) time.Time { return day(1) }
	got := PlanPruning(PruneInput{
		Now: baseTime, Retention: retention(5, 90, 1), Commits: chain(5, fresh), Refs: big,
	})
	if len(got.Pruned) != 2 {
		t.Fatalf("可裁区应全裁（2 个），实际 %d", len(got.Pruned))
	}
	if !got.CapacityExceeded {
		t.Fatal("占用仍超 C 应标记 CapacityExceeded（宁超不违保底）")
	}
	if len(got.Survivors) != HardFloorKeep {
		t.Fatalf("K=3 保底被违反：存活 %d", len(got.Survivors))
	}
}

// TestPlanPruningProtectedBaseline 屏障：头基线/活跃计划引用的基线所在提交不裁，
// 前缀在其前截断（ADR-0007 §4 计划引用通道）。
func TestPlanPruningProtectedBaseline(t *testing.T) {
	fresh := func(int) time.Time { return day(1) }
	commits := chain(10, fresh)
	got := PlanPruning(PruneInput{
		Now: baseTime, Retention: retention(5, 90, 0), Commits: commits, Refs: evenRefs(10),
		ProtectedBaselines: map[string]bool{baselineIDOf(4): true}, // 提交 e 被活跃计划引用
	})
	// N=5 → 无屏障本可裁 5 个；屏障在 index 4 → 前缀止于 4。
	if len(got.Pruned) != 4 {
		t.Fatalf("屏障前应裁 4 个，实际 %d（%v）", len(got.Pruned), got.Pruned)
	}
	for _, id := range got.Pruned {
		if id == commits[4].CommitID {
			t.Fatal("被屏障提交被裁")
		}
	}
}

// TestPlanPruningCascadeOutput 级联产出：dropped baselines、重连点、存活链。
func TestPlanPruningCascadeOutput(t *testing.T) {
	newest := func(i int) time.Time { return day(24 - i) }
	commits := chain(24, newest)
	got := PlanPruning(PruneInput{
		Now: baseTime, Retention: retention(20, 90, 0), Commits: commits, Refs: evenRefs(24),
	})
	if len(got.Pruned) != 4 || len(got.DroppedBaselines) != 4 {
		t.Fatalf("级联应裁 4 提交 4 基线，实际 %d/%d", len(got.Pruned), len(got.DroppedBaselines))
	}
	if got.ReconnectCommitID != commits[4].CommitID {
		t.Fatalf("重连提交 = %s，期望 %s", got.ReconnectCommitID, commits[4].CommitID)
	}
	if got.ReconnectBaselineID != commits[4].ResultBaselineID {
		t.Fatalf("重连基线 = %s，期望 %s", got.ReconnectBaselineID, commits[4].ResultBaselineID)
	}
	// 被裁链首存活提交的 previous_baseline_id 指向末位被裁基线（executor 置空的数据依据）。
	if commits[4].ParentID != got.Pruned[len(got.Pruned)-1] {
		t.Fatal("重连提交的 parent 应是末位被裁提交")
	}
	if len(got.Survivors) != 20 {
		t.Fatalf("存活 %d，期望 20", len(got.Survivors))
	}
}

// TestPlanPruningIdempotentOnShortChain 短链幂等：≤K 不产出任何裁剪。
func TestPlanPruningIdempotentOnShortChain(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		got := PlanPruning(PruneInput{
			Now: baseTime, Retention: retention(5, 7, 1), Commits: chain(n, func(int) time.Time { return day(400) }),
			Refs: evenRefs(n),
		})
		if len(got.Pruned) != 0 || got.ReconnectCommitID != "" {
			t.Fatalf("n=%d：短链不裁（K 保底），got %+v", n, got)
		}
	}
}

// TestExpiredTrash trash 老化表驱动（fake clock）。
func TestExpiredTrash(t *testing.T) {
	entries := []TrashEntry{
		{Digest: "old", ModifiedAt: day(8)},
		{Digest: "edge", ModifiedAt: day(7)},  // 恰 7 天：未超，不清
		{Digest: "new", ModifiedAt: day(1)},
	}
	got := ExpiredTrash(baseTime, entries, 7)
	if len(got) != 1 || got[0].Digest != "old" {
		t.Fatalf("超期清除应只含 old，got %+v", got)
	}
	if got2 := ExpiredTrash(baseTime, entries, 90); len(got2) != 0 {
		t.Fatalf("trash_days=90 不应有条目超期，got %+v", got2)
	}
}
