package main

// T09（票 #46）验收面：pgfixture -eval 门槛评估（验收规格 §2.3 沿用 + P2 §3
// 增量 + P3 §7 新增，票 #66）——扫描三项恒评；apply 记录在场时加评冷 apply
// ≤30s 与峰值内存增量 <256MiB；restore/gc 记录在场时加评 restore 冷 ≤30s、
// restore 峰值内存 <256MiB、GC ≤30s；任一超标退出码 1。schema 兼容
// p1/p2/p3-perf-run/1。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func scanRecord(schema string, scanMS int64, hits, misses int64) *perfRecord {
	return &perfRecord{
		Schema:      schema,
		ScanTotalMS: scanMS,
		HashCache: struct {
			Hits     int64   `json:"hits"`
			Misses   int64   `json:"misses"`
			HitRatio float64 `json:"hit_ratio"`
		}{Hits: hits, Misses: misses, HitRatio: 0},
	}
}

func applyMetricsRec(kind string, totalMS int64, deltaBytes uint64) *applyMetrics {
	a := &applyMetrics{Kind: kind, OperationCount: 2700, ApplyTotalMS: totalMS}
	a.PeakMemory.Metric = "go_runtime_memstats_sys_peak_delta"
	a.PeakMemory.DeltaBytes = deltaBytes
	a.PeakMemory.DeltaMiB = float64(deltaBytes) / (1 << 20)
	return a
}

// 三扫描门槛全过（P1 口径回归：2 记录模式退出码 0）。
func TestEvalRecordsScanOnlyPass(t *testing.T) {
	cold := scanRecord("p2-perf-run/1", 8580, 0, 2700)
	warm := scanRecord("p2-perf-run/1", 536, 2700, 0)
	if code := evalRecords(cold, warm, nil, nil, nil); code != 0 {
		t.Fatalf("扫描三项全过应退出码 0，得 %d", code)
	}
}

// 五门槛全过（P2：3 记录模式）。
func TestEvalRecordsFiveGatesPass(t *testing.T) {
	cold := scanRecord("p2-perf-run/1", 8580, 0, 2700)
	warm := scanRecord("p2-perf-run/1", 536, 2700, 0)
	app := applyMetricsRec("initialize", 4200, 48*(1<<20))
	if code := evalRecords(cold, warm, app, nil, nil); code != 0 {
		t.Fatalf("五门槛全过应退出码 0，得 %d", code)
	}
}

// apply 两门槛逐项超标（冷 apply 超时 / 峰值内存增量 ≥256MiB）→ 退出码 1。
func TestEvalRecordsApplyGatesFail(t *testing.T) {
	cold := scanRecord("p2-perf-run/1", 8580, 0, 2700)
	warm := scanRecord("p2-perf-run/1", 536, 2700, 0)

	overTime := applyMetricsRec("initialize", applyBudgetMS+1, 1<<20)
	if code := evalRecords(cold, warm, overTime, nil, nil); code != 1 {
		t.Fatalf("冷 apply 超门槛应退出码 1，得 %d", code)
	}

	overMem := applyMetricsRec("initialize", 1000, applyPeakBytes)
	if code := evalRecords(cold, warm, overMem, nil, nil); code != 1 {
		t.Fatalf("峰值内存增量达 256MiB 应退出码 1，得 %d", code)
	}
}

// 扫描三项任一超标 → 退出码 1（apply 记录在场与否均评）。
func TestEvalRecordsScanGatesFail(t *testing.T) {
	app := applyMetricsRec("initialize", 1000, 1<<20)
	cases := []struct {
		name       string
		cold, warm *perfRecord
	}{
		{"cold over", scanRecord("p2-perf-run/1", coldBudgetMS+1, 0, 2700), scanRecord("p2-perf-run/1", 536, 2700, 0)},
		{"warm over", scanRecord("p2-perf-run/1", 1000, 0, 2700), scanRecord("p2-perf-run/1", warmBudgetMS+1, 2700, 0)},
		{"ratio low", scanRecord("p2-perf-run/1", 1000, 0, 2700), scanRecord("p2-perf-run/1", 536, 90, 10)},
	}
	for _, tc := range cases {
		if code := evalRecords(tc.cold, tc.warm, nil, nil, nil); code != 1 {
			t.Fatalf("%s（无 apply）应退出码 1，得 %d", tc.name, code)
		}
		if code := evalRecords(tc.cold, tc.warm, app, nil, nil); code != 1 {
			t.Fatalf("%s（含 apply）应退出码 1，得 %d", tc.name, code)
		}
	}
}

// readRecord：schema 双版本兼容 + p2 记录 apply 段反序列化 + 未知 schema 拒绝。
func TestReadRecordSchemaAndApplySection(t *testing.T) {
	p2 := map[string]any{
		"schema":        "p2-perf-run/1",
		"scan_total_ms": 900,
		"hash_cache":    map[string]any{"hits": 2700, "misses": 0, "hit_ratio": 1},
		"apply": map[string]any{
			"kind":            "initialize",
			"operation_count": 2700,
			"apply_total_ms":  4200,
			"peak_memory_delta": map[string]any{
				"metric":      "go_runtime_memstats_sys_peak_delta",
				"delta_bytes": 50331648,
				"delta_mib":   48.0,
			},
		},
	}
	write := func(t *testing.T, v any) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "rec.json")
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("序列化: %v", err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("写文件: %v", err)
		}
		return p
	}

	p := write(t, p2)
	rec, err := readRecord(p)
	if err != nil {
		t.Fatalf("p2-perf-run/1 应可读: %v", err)
	}
	if rec.Apply == nil || rec.Apply.Kind != "initialize" || rec.Apply.ApplyTotalMS != 4200 {
		t.Fatalf("apply 段读回不符: %+v", rec.Apply)
	}
	if rec.Apply.PeakMemory.DeltaBytes != 50331648 || rec.Apply.PeakMemory.Metric != "go_runtime_memstats_sys_peak_delta" {
		t.Fatalf("峰值内存段读回不符: %+v", rec.Apply.PeakMemory)
	}

	p1 := map[string]any{"schema": "p1-perf-run/1", "scan_total_ms": 900,
		"hash_cache": map[string]any{"hits": 0, "misses": 2700}}
	if _, err := readRecord(write(t, p1)); err != nil {
		t.Fatalf("p1-perf-run/1 应兼容: %v", err)
	}

	bad := map[string]any{"schema": "p3-perf-run/9", "scan_total_ms": 900}
	if _, err := readRecord(write(t, bad)); err == nil {
		t.Fatalf("未知 schema 应拒绝")
	}
}

// ---- P3 新门槛（票 #66；验收规格 §7）----

func restoreMetricsRec(totalMS int64, deltaBytes uint64) *restoreMetrics {
	r := &restoreMetrics{Kind: "restore", OperationCount: 2400, PrepareMS: 900, RestoreTotalMS: totalMS}
	r.PeakMemory.Metric = "go_runtime_memstats_sys_peak_delta"
	r.PeakMemory.DeltaBytes = deltaBytes
	r.PeakMemory.DeltaMiB = float64(deltaBytes) / (1 << 20)
	return r
}

func gcMetricsRec(totalMS int64, violations int) *gcMetrics {
	return &gcMetrics{Kind: "gc", GCTotalMS: totalMS, Tombstones: 5, AliveCommits: 20,
		AuditViolations: violations, OldestVerifiedObjects: 12}
}

// 七门槛全过（P3：5 记录模式，restore/GC 段在场）。
func TestEvalRecordsSevenGatesPass(t *testing.T) {
	cold := scanRecord("p3-perf-run/1", 8580, 0, 2700)
	warm := scanRecord("p3-perf-run/1", 536, 2700, 0)
	app := applyMetricsRec("initialize", 4200, 48*(1<<20))
	rst := restoreMetricsRec(18_000, 96*(1<<20))
	gc := gcMetricsRec(8_000, 0)
	if code := evalRecords(cold, warm, app, rst, gc); code != 0 {
		t.Fatalf("七门槛全过应退出码 0，得 %d", code)
	}
}

// restore 两门槛逐项超标（restore 超时 / 峰值内存 ≥256MiB）→ 退出码 1。
func TestEvalRecordsRestoreGatesFail(t *testing.T) {
	cold := scanRecord("p3-perf-run/1", 8580, 0, 2700)
	warm := scanRecord("p3-perf-run/1", 536, 2700, 0)

	overTime := restoreMetricsRec(restoreBudgetMS+1, 1<<20)
	if code := evalRecords(cold, warm, nil, overTime, nil); code != 1 {
		t.Fatalf("restore 超门槛应退出码 1，得 %d", code)
	}
	overMem := restoreMetricsRec(1000, restorePeakBytes)
	if code := evalRecords(cold, warm, nil, overMem, nil); code != 1 {
		t.Fatalf("restore 峰值内存达 256MiB 应退出码 1，得 %d", code)
	}
}

// GC 门槛（超时 / 引用图对账违例非零）→ 退出码 1。
func TestEvalRecordsGCGatesFail(t *testing.T) {
	cold := scanRecord("p3-perf-run/1", 8580, 0, 2700)
	warm := scanRecord("p3-perf-run/1", 536, 2700, 0)

	if code := evalRecords(cold, warm, nil, nil, gcMetricsRec(gcBudgetMS+1, 0)); code != 1 {
		t.Fatalf("GC 超门槛应退出码 1，得 %d", code)
	}
	if code := evalRecords(cold, warm, nil, nil, gcMetricsRec(1000, 1)); code != 1 {
		t.Fatalf("GC 对账违例非零应退出码 1，得 %d", code)
	}
}

// readRecord：p3-perf-run/1 兼容 + restore/gc 段反序列化。
func TestReadRecordP3Sections(t *testing.T) {
	p3 := map[string]any{
		"schema":        "p3-perf-run/1",
		"scan_total_ms": 900,
		"hash_cache":    map[string]any{"hits": 2700, "misses": 0},
		"restore": map[string]any{
			"kind": "restore", "operation_count": 2400, "prepare_ms": 900,
			"phases_ms":         map[string]any{"staging": 5000, "applying": 6000, "verifying": 2000},
			"restore_total_ms":  18000,
			"peak_memory_delta": map[string]any{"metric": "x", "delta_bytes": 100, "delta_mib": 0.1},
		},
		"gc": map[string]any{"kind": "gc", "gc_total_ms": 8000, "tombstones": 5},
	}
	write := func(t *testing.T, v any) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "rec.json")
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("序列化: %v", err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("写文件: %v", err)
		}
		return p
	}
	rec, err := readRecord(write(t, p3))
	if err != nil {
		t.Fatalf("p3-perf-run/1 应可读: %v", err)
	}
	if rec.Restore == nil || rec.Restore.RestoreTotalMS != 18000 || rec.Restore.PhasesMS.Applying != 6000 {
		t.Fatalf("restore 段读回不符: %+v", rec.Restore)
	}
	if rec.GC == nil || rec.GC.GCTotalMS != 8000 || rec.GC.Tombstones != 5 {
		t.Fatalf("gc 段读回不符: %+v", rec.GC)
	}
}
