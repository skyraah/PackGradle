package main

// R2 机器名脱敏断言（ADR-0011 §7 R2；P4 验收规格 §5.2 场景 3）：`-metrics`
// 输出不再采集 os.Hostname——machineInfo 序列化无 host 键，OS/Arch/GoVersion/
// CPUs 四键保留。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetricsMachineInfoNoHost(t *testing.T) {
	b, err := json.Marshal(machineInfo{OS: "windows", Arch: "amd64", GoVersion: "go1.25.0", CPUs: 8})
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, forbidden := range []string{`"host"`, `"hostname"`} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("metrics 机器信息不得含机器名键: %s", out)
		}
	}
	for _, want := range []string{`"os"`, `"arch"`, `"go_version"`, `"cpus"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics 机器信息缺少保留键 %s: %s", want, out)
		}
	}
}

func TestMetricsRecordMachineShape(t *testing.T) {
	// 全链路记录形态：machine 段随 metricsRecord 序列化后同样无 host 键
	b, err := json.Marshal(metricsRecord{Schema: "p3-perf-run/1", Machine: newMachineInfo()})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	machine, ok := decoded["machine"].(map[string]any)
	if !ok {
		t.Fatalf("machine 段缺失: %s", b)
	}
	if _, present := machine["host"]; present {
		t.Fatalf("metrics 输出不得含 host 键（R2）: %s", b)
	}
	for _, want := range []string{"os", "arch", "go_version", "cpus"} {
		if _, present := machine[want]; !present {
			t.Fatalf("metrics 机器信息缺少保留键 %s: %s", want, b)
		}
	}
}
