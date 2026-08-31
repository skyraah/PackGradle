package main

// T09（票 #46）验收面：apply 段峰值内存采样器（runtime.ReadMemStats 口径）——
// 基线即首个样本、峰值单调不减、增量 = 峰值 − 基线、nil 采样器安全。

import (
	"testing"
)

func TestMemPeakSamplerBaselineAndDelta(t *testing.T) {
	s := beginMemPeakSample()
	if s.samples != 1 {
		t.Fatalf("首个样本应即基线（samples=1），得 %d", s.samples)
	}
	if s.baselineSys != s.peakSys || s.baselineHeapInuse != s.peakHeapInuse {
		t.Fatalf("基线样本应同时记入峰值: %+v", s)
	}
	before := s.peakSys
	s.sample()
	s.sample()
	if s.samples != 3 {
		t.Fatalf("samples 应为 3，得 %d", s.samples)
	}
	if s.peakSys < before || s.peakHeapInuse < s.baselineHeapInuse {
		t.Fatalf("峰值应单调不减: before=%d peak=%d", before, s.peakSys)
	}

	r := s.result()
	if r.Metric != memMetricSysPeakDelta {
		t.Fatalf("口径标识不符: %s", r.Metric)
	}
	if r.BaselineBytes != s.baselineSys || r.PeakBytes != s.peakSys {
		t.Fatalf("基线/峰值字节不符: %+v", r)
	}
	if r.DeltaBytes != s.peakSys-s.baselineSys {
		t.Fatalf("增量 = 峰值 − 基线: %+v", r)
	}
	if r.DeltaMiB <= 0 && r.DeltaBytes > 0 || r.DeltaMiB != float64(r.DeltaBytes)/(1<<20) {
		t.Fatalf("MiB 换算不符: %+v", r)
	}
	if r.Samples != 3 || r.Note == "" {
		t.Fatalf("样本数/口径说明缺失: %+v", r)
	}
}

func TestMemPeakSamplerNilSafe(t *testing.T) {
	var s *memPeakSampler
	s.sample() // 不应 panic
}
