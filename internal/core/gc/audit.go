package gc

import "strings"

// audit.go 实现引用图不变式断言器（票 #64，验收规格 §6 五件套之③，架构篇
// P3 红线③的机器断言面）：
//
//	GC 后 CAS 存活对象集 = 存活提交可达闭包 ∪ 隔离区，超集为零（逐 digest 对账）。
//
// 纯函数零 I/O：调用方（headless 验收链 / 票 #66 验收基建 / 恢复处置后的
// 人工对账）负责采集四侧事实（可达闭包、隔离区、账目行、盘上文件），本函数
// 只做集合对账并产出逐 digest 违例清单。
//
// 对账四向：
//   - UndeadRows / UndeadFiles（超集违例）：账目行/盘上文件既不可达也不在隔离区
//     ——GC 漏收或账目损坏，违例必须为零；
//   - MissingFiles（红线违例）：可达 digest 在盘上无文件——活引用被误删，违例
//     必须为零（row-without-file 且被引用的对账豁免在调用方决定是否入参时处理：
//     该状态本身已是引用完整性受损面，断言器如实报告）；
//   - GhostRows（账目一致性）：ready 行在盘上无文件（Has() 已不可见的悬账）。
//     非硬违例（不阻断），产出供对账报告。

// AuditInput 是断言器输入的四侧事实（digest 一律小写；调用方负责归一）。
type AuditInput struct {
	// Reachable 是可达闭包：存活提交的 object_refs ∪ 存活基线 logical_digest
	// 命中 ∪ 活跃/未处置 run 的恢复引用（kind=cas）∪ 活跃计划屏障基线命中。
	Reachable []string
	// Quarantined 是隔离区（state='quarantined' 行 + trash 副本 digest 并集）
	// ——回收账目，不算超集。
	Quarantined []string
	// ReadyRows 是 objects 表 state='ready' 行 digest（账目侧存活集）。
	ReadyRows []string
	// OnDisk 是对象库盘上文件 digest（文件系统侧存活集）。
	OnDisk []string
}

// AuditFinding 是单 digest 违例（Kind ∈ undead_row / undead_file /
// missing_file / ghost_row）。
type AuditFinding struct {
	Kind   string
	Digest string
}

// 违例类别常量。
const (
	FindingUndeadRow   = "undead_row"   // ready 行不可达且未隔离（超集违例）
	FindingUndeadFile  = "undead_file"  // 盘上文件不可达且未隔离（超集违例）
	FindingMissingFile = "missing_file" // 可达 digest 盘上无文件（红线违例）
	FindingGhostRow    = "ghost_row"    // ready 行盘上无文件（悬账，非硬违例）
)

// Audit 逐 digest 对账四侧事实，返回全部违例（无违例返回空）。超集为零的
// 验收断言 = 结果中无 FindingUndeadRow/FindingUndeadFile；引用完整性红线 =
// 无 FindingMissingFile。
func Audit(in AuditInput) []AuditFinding {
	reach := toSet(in.Reachable)
	quarantined := toSet(in.Quarantined)
	onDisk := toSet(in.OnDisk)

	var out []AuditFinding
	for _, d := range in.ReadyRows {
		d = lower(d)
		if !reach[d] && !quarantined[d] {
			out = append(out, AuditFinding{Kind: FindingUndeadRow, Digest: d})
		}
		if !onDisk[d] {
			out = append(out, AuditFinding{Kind: FindingGhostRow, Digest: d})
		}
	}
	for _, d := range in.OnDisk {
		d = lower(d)
		if !reach[d] && !quarantined[d] {
			out = append(out, AuditFinding{Kind: FindingUndeadFile, Digest: d})
		}
	}
	for _, d := range in.Reachable {
		d = lower(d)
		if !onDisk[d] {
			out = append(out, AuditFinding{Kind: FindingMissingFile, Digest: d})
		}
	}
	return out
}

func toSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[lower(s)] = true
	}
	return out
}

func lower(s string) string { return strings.ToLower(s) }
