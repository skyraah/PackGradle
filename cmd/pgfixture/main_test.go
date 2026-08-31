package main

// T09（票 #46）验收面：pgfixture -eval 门槛评估（验收规格 §2.3 沿用 + P2 §3
// 增量）——扫描三项恒评；apply 记录在场时加评冷 apply ≤30s 与峰值内存增量
// <256MiB；任一超标退出码 1。schema 兼容 p1-perf-run/1 与 p2-perf-run/1。

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
	if code := evalRecords(cold, warm, nil); code != 0 {
		t.Fatalf("扫描三项全过应退出码 0，得 %d", code)
	}
}

// 五门槛全过（P2：3 记录模式）。
func TestEvalRecordsFiveGatesPass(t *testing.T) {
	cold := scanRecord("p2-perf-run/1", 8580, 0, 2700)
	warm := scanRecord("p2-perf-run/1", 536, 2700, 0)
	app := applyMetricsRec("initialize", 4200, 48*(1<<20))
	if code := evalRecords(cold, warm, app); code != 0 {
		t.Fatalf("五门槛全过应退出码 0，得 %d", code)
	}
}

// apply 两门槛逐项超标（冷 apply 超时 / 峰值内存增量 ≥256MiB）→ 退出码 1。
func TestEvalRecordsApplyGatesFail(t *testing.T) {
	cold := scanRecord("p2-perf-run/1", 8580, 0, 2700)
	warm := scanRecord("p2-perf-run/1", 536, 2700, 0)

	overTime := applyMetricsRec("initialize", applyBudgetMS+1, 1<<20)
	if code := evalRecords(cold, warm, overTime); code != 1 {
		t.Fatalf("冷 apply 超门槛应退出码 1，得 %d", code)
	}

	overMem := applyMetricsRec("initialize", 1000, applyPeakBytes)
	if code := evalRecords(cold, warm, overMem); code != 1 {
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
		if code := evalRecords(tc.cold, tc.warm, nil); code != 1 {
			t.Fatalf("%s（无 apply）应退出码 1，得 %d", tc.name, code)
		}
		if code := evalRecords(tc.cold, tc.warm, app); code != 1 {
			t.Fatalf("%s（含 apply）应退出码 1，得 %d", tc.name, code)
		}
	}
}

// readRecord：schema 双版本兼容 + p2 记录 apply 段反序列化 + 未知 schema 拒绝。
func TestReadRecordSchemaAndApplySection(t *testing.T) {
	p2 := map[string]any{
		"schema":         "p2-perf-run/1",
		"scan_total_ms":  900,
		"hash_cache":     map[string]any{"hits": 2700, "misses": 0, "hit_ratio": 1},
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
