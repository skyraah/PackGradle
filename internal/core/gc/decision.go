// Package gc 实现保留策略的纯决策函数（ADR-0007，票 #64）：
// 锚点 N/D/C 选择、连续前缀修剪、级联删除计划、K=3 硬保底、trash 老化判定。
//
// 本包是纯域逻辑：无 I/O、无时钟（时间一律由调用方以 Now 注入，单测用 fake
// clock 表驱动）、不 import store/application 层。执行面（任务化/安全窗口/
// 删除协议/孤儿清扫）归 internal/application/sync 的 GC 引擎。
//
// 两层模型（ADR-0007 §1）：保留策略作用在同步提交上（修剪），GC 只回收
// 零存活引用对象（回收决策始终全局，锚点按 relation 记账）。
package gc

import (
	"time"

	"packgradle/internal/core/model"
)

// HardFloorKeep 是 K=3 硬保底（ADR-0007 §2）：最近 3 个提交任何情况下不裁
//（head 天然在内）。固定值，不可调，不设设置键。
const HardFloorKeep = 3

// CommitNode 是决策输入的提交节点（sync_commits 行的最小投影；链序 oldest-first）。
type CommitNode struct {
	CommitID         string
	ParentID         string // 首提交为空
	ResultBaselineID string // 级联删除的 result baseline
	CreatedAt        time.Time
}

// ObjectRef 是决策输入的对象引用行（object_refs，owner=存活提交）。
type ObjectRef struct {
	CommitID string
	Digest   string
	Size     int64
}

// PruneInput 是 PlanPruning 的输入。
type PruneInput struct {
	// Now 是决策时点（fake clock 注入点；容量/时间锚点全部相对它计算）。
	Now time.Time
	// Retention 是保留策略五键（K 不在其中——HardFloorKeep 常量）。
	Retention model.RetentionSettings
	// Commits 是该 relation 的全部存活提交，oldest-first 连续链（executor 从
	// sync_commits 按 id 升序读出；id 为 ULID，创建序即链序）。
	Commits []CommitNode
	// Refs 是该 relation 全部存活提交的 object_refs 行（容量记账与释放模拟的数据源）。
	Refs []ObjectRef
	// ProtectedBaselines 是「屏障」基线集合：结果基线命中的提交一律不裁
	//（relations.head_baseline_id ∪ 活跃 draft/resolved 计划的 base_baseline_id，
	// ADR-0007 §4 保护根集的计划引用通道）。
	ProtectedBaselines map[string]bool
}

// PruneDecision 是一次修剪决策的完整产出：executor 据此在单事务内执行级联删除。
type PruneDecision struct {
	// Pruned 是被裁提交（oldest-first 连续前缀）。
	Pruned []string
	// Survivors 是存活提交（oldest-first；链长 ≥ K 时恒 ≥ HardFloorKeep）。
	Survivors []string
	// DroppedBaselines 是随裁删除的 result baseline（= Pruned 的结果基线去重集）。
	DroppedBaselines []string
	// ReconnectCommitID 是被裁链首的首个存活提交：其 parent_id 与
	// previous_baseline_id 置空（仅元数据重连，内容不改；ADR-0007 §1 只点名
	// previous_baseline_id，parent_id 因 sync_commits.parent_id 外键指向被裁行
	// 必须同批置空——「parent 链不断链」的落地口径是存活子链自洽连续）。
	// 空 = 无裁剪。
	ReconnectCommitID string
	// ReconnectBaselineID 是首个存活提交的结果基线：其 parent_id 置空（外键指向
	// 被裁的末位 baseline，SQLite 立即外键要求先重连后删行；同一「元数据重连」口径）。
	ReconnectBaselineID string
	// CapacityExceeded 为 true 表示追加修剪用尽可裁区后关系占用仍超 C——
	// 容量宁超不违保底（K=3），后续提交增长后重查。
	CapacityExceeded bool
	// CapacityDriven 为 true 表示本次裁剪由容量锚点追加驱动（超出 N/D 之外）。
	CapacityDriven bool
}

// PlanPruning 计算一次连续前缀修剪决策（ADR-0007 §1/§2）。
//
// 算法：
//  1. 可裁区 = 链首到 K 硬保底边界（最后 HardFloorKeep 个提交任何情况下不裁）；
//  2. 修剪条件＝超出 N ∨ 早于 D（时间锚点相对 fake Now）；
//  3. 前缀推进：从链首连续取满足修剪条件的提交——屏障（ProtectedBaselines
//     命中的结果基线）与首个不满足条件的提交都终止前缀，保证存活 parent 链
//     不断链（ADR-0007 §1 连续前缀修剪）；
//  4. 容量锚点 C：前缀止后关系占用仍超 C 时，在可裁区内逐个追加裁剪
//    （同样尊重屏障），直到投影占用 ≤ C 或可裁区用尽；用尽仍超 →
//    CapacityExceeded（宁超不违保底）。
//
// 占用口径（ADR-0007 §2）：SUM(objects.size) over 该 relation 存活提交引用的
// 去重 digest；被裁前缀释放的字节只计「不再被任何存活提交引用」的对象——
// CAS 跨 relation 去重，本函数按 relation 记账，回收判定归 executor 全局执行。
func PlanPruning(in PruneInput) PruneDecision {
	var decision PruneDecision
	n := len(in.Commits)
	if n == 0 {
		return decision
	}

	// 可裁区上界（不含）：K 硬保底。链长 ≤ K 时无可裁区。
	prunableCount := n - HardFloorKeep
	if prunableCount <= 0 {
		decision.Survivors = commitIDs(in.Commits)
		return decision
	}

	// 引用索引：digest → 引用提交下标集合（占用投影用）。
	owners := map[string][]int{}
	for _, r := range in.Refs {
		idx := commitIndex(in.Commits, r.CommitID)
		if idx < 0 {
			continue
		}
		owners[r.Digest] = append(owners[r.Digest], idx)
	}
	usageAfter := func(prefix int) int64 {
		var total int64
		for digest, idxs := range owners {
			for _, idx := range idxs {
				if idx >= prefix {
					total += digestSize(in.Refs, digest)
					break
				}
			}
		}
		return total
	}

	// 步骤 3：N/D 锚点的连续前缀推进。
	prefixEnd := 0 // [0, prefixEnd) 为当前前缀
	for i := 0; i < prunableCount; i++ {
		c := in.Commits[i]
		if in.ProtectedBaselines[c.ResultBaselineID] {
			break // 屏障：计划/头引用的基线所在提交不裁，前缀到此为止
		}
		beyondN := n-i > in.Retention.KeepCommits
		olderThanD := in.Retention.KeepDays > 0 &&
			in.Now.Sub(c.CreatedAt) > time.Duration(in.Retention.KeepDays)*24*time.Hour
		if !beyondN && !olderThanD {
			break // 首个不满足修剪条件的提交终止前缀（连续前缀不变式）
		}
		prefixEnd = i + 1
	}

	// 步骤 4：容量锚点追加修剪（仍在可裁区内，仍尊重屏障）。
	projected := usageAfter(prefixEnd)
	if in.Retention.RelationCapacityBytes > 0 && projected > in.Retention.RelationCapacityBytes {
		for i := prefixEnd; i < prunableCount; i++ {
			c := in.Commits[i]
			if in.ProtectedBaselines[c.ResultBaselineID] {
				break
			}
			prefixEnd = i + 1
			decision.CapacityDriven = true
			projected = usageAfter(prefixEnd)
			if projected <= in.Retention.RelationCapacityBytes {
				break
			}
		}
		decision.CapacityExceeded = projected > in.Retention.RelationCapacityBytes
	}

	for i := 0; i < prefixEnd; i++ {
		decision.Pruned = append(decision.Pruned, in.Commits[i].CommitID)
		decision.DroppedBaselines = appendUnique(decision.DroppedBaselines, in.Commits[i].ResultBaselineID)
	}
	for i := prefixEnd; i < n; i++ {
		decision.Survivors = append(decision.Survivors, in.Commits[i].CommitID)
	}
	if prefixEnd > 0 {
		decision.ReconnectCommitID = in.Commits[prefixEnd].CommitID
		decision.ReconnectBaselineID = in.Commits[prefixEnd].ResultBaselineID
	}
	return decision
}

// commitIndex 返回提交在链中的下标（不存在 -1）。
func commitIndex(nodes []CommitNode, id string) int {
	for i := range nodes {
		if nodes[i].CommitID == id {
			return i
		}
	}
	return -1
}

// digestSize 返回 digest 的对象 size（无引用行时 0）。
func digestSize(refs []ObjectRef, digest string) int64 {
	for _, r := range refs {
		if r.Digest == digest {
			return r.Size
		}
	}
	return 0
}

func appendUnique(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}

func commitIDs(nodes []CommitNode) []string {
	out := make([]string, 0, len(nodes))
	for _, c := range nodes {
		out = append(out, c.CommitID)
	}
	return out
}

// TrashEntry 是回收站账目视图（文件系统侧：digest + mtime 时钟起点）。
type TrashEntry struct {
	Digest     string
	ModifiedAt time.Time
}

// ExpiredTrash 返回超 trash_days 的回收站条目（物理清除对象，ADR-0007 §5 步骤 3）：
// 文件 mtime 即时钟起点。
func ExpiredTrash(now time.Time, entries []TrashEntry, trashDays int) []TrashEntry {
	var out []TrashEntry
	for _, e := range entries {
		if now.Sub(e.ModifiedAt) > time.Duration(trashDays)*24*time.Hour {
			out = append(out, e)
		}
	}
	return out
}
