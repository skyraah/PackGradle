package gc

// snapshot.go 实现孤儿快照判定的纯决策函数（ADR-0011 §4，票 #89）：
//
//	快照只是证据、不是回滚目标（回滚走提交/基线）——「该版本快照无法被回滚」
//	在机制上即孤儿快照：不被任何存活提交（verified_*_snapshot_id）引用、不被
//	任何计划（input_*_snapshot_id）引用、且不是该 relation 任一端最新一份
//	（最新快照是 diff/回滚/工作区视图的当前状态读取面）。
//
// 与 PlanPruning 同构：纯函数零 I/O、无时钟（判定只看引用图，时间零入参——
// 快照窗口 = 提交保留窗口，零新保留参数）。事实采集归 executor（GC 引擎
// 清扫阶段，与 .tmp-* 写中断残渣同位）；本函数只做集合运算。

// SnapshotFacts 是孤儿快照判定的引用图事实（调用方负责采集；id 一律原样比对）。
type SnapshotFacts struct {
	// All 是 observed_snapshots 全部快照 id（判定全集）。
	All []string
	// CommitVerified 是存活提交的验证快照集（verified_project_snapshot_id ∪
	// verified_runtime_snapshot_id；快照随其提交存亡，提交被修剪后自然失引）。
	CommitVerified []string
	// PlanInput 是现存计划行的输入快照集（input_project_snapshot_id ∪
	// input_runtime_snapshot_id；计划行本身按惰性通道清理，删除即释放引用）。
	PlanInput []string
	// Latest 是各 relation 每端最新一份快照（captured_at DESC, id DESC 取 1，
	// 与 SnapshotRepository.LatestByRelationSide 同序）。
	Latest []string
}

// OrphanSnapshots 返回孤儿快照 id（All 顺序与原文，确定性输出）：三通道皆无
// 引用的快照。判定是 ADR 定义的确定函数——逐快照真值仅取决于（提交引用 ∧
// 计划引用 ∧ 任一端最新）三元组的全否，单测表驱动全格覆盖。
//
// 引用比对大小写不敏感（digest/id 集合一律小写归一，与 Audit 同口径），输出
// 保留 All 原文（执行侧 DELETE 按库内原样 id 定位）；引用集内指向不存在快照
// 的悬空 id 不影响判定（集合差运算天然忽略）。
func OrphanSnapshots(facts SnapshotFacts) []string {
	verified := toSet(facts.CommitVerified)
	input := toSet(facts.PlanInput)
	latest := toSet(facts.Latest)

	var out []string
	for _, id := range facts.All {
		key := lower(id)
		if verified[key] || input[key] || latest[key] {
			continue
		}
		out = append(out, id)
	}
	return out
}
