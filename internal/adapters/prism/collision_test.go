package prism

import (
	"context"
	"testing"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// collisionRule 构造指定 ID 的同前缀文件规则。
func collisionRule(id, prefix string) model.MappingRule {
	return model.MappingRule{
		ID: id, ResourceKind: "text_file",
		ProjectPrefix: prefix, RuntimePrefix: prefix,
		Direction: "bidirectional", Materialization: "copy", MergePolicy: "manual",
		RuntimeLocalPolicy: "exclude",
	}
}

func TestScanMappingCollisionDropsPathWithEvidence(t *testing.T) {
	// 两条规则同前缀且 include/exclude 无法互斥 → 同一路径无法唯一决议：
	// 路径从观察剔除，诊断保留证据（并列规则 ID + 命中路径）。
	root := makeGameDir(t)
	mustWrite(t, root, "config/notes.txt", "hello")
	pol := modsOnlyPolicy()
	pol.Rules = append(pol.Rules, collisionRule("aaa", "config"), collisionRule("zzz", "config"))
	report, err := New().Scan(context.Background(), root, ports.ScanOptions{Policy: pol})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range report.Observations {
		if o.Kind == model.ResourceTextFile {
			t.Fatalf("碰撞路径不应产出观察: %+v", o)
		}
	}
	var collision *model.Diagnostic
	for i := range report.Diagnostics {
		if report.Diagnostics[i].Code == "diag.mapping.collision" {
			collision = &report.Diagnostics[i]
		}
	}
	if collision == nil {
		t.Fatalf("缺少 diag.mapping.collision 诊断: %+v", report.Diagnostics)
	}
	if collision.Args[0] != "aaa" || collision.Args[1] != "zzz" {
		t.Errorf("碰撞证据规则 ID = %v, want [aaa zzz]（字节序）", collision.Args)
	}
	if collision.RelativePath != "config/notes.txt" {
		t.Errorf("碰撞证据路径 = %q, want config/notes.txt", collision.RelativePath)
	}
}

func TestScanNestedPrefixMostSpecificWins(t *testing.T) {
	// config 与 config/deep 嵌套前缀：最深（最具体）规则胜出，不产生碰撞
	root := makeGameDir(t)
	mustWrite(t, root, "config/notes.txt", "hello")
	mustWrite(t, root, "config/deep/x.ini", "k=1")
	pol := modsOnlyPolicy()
	pol.Rules = append(pol.Rules, collisionRule("config", "config"), collisionRule("deep", "config/deep"))
	report, err := New().Scan(context.Background(), root, ports.ScanOptions{
		Policy: pol, HashFile: fakeHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[model.ResourceID]model.ResourceObservation{}
	for _, o := range report.Observations {
		byID[o.ResourceID] = o
	}
	if o, ok := byID["file:config/notes.txt"]; !ok || o.PolicyID != "config" {
		t.Fatalf("config/notes.txt 应归 config 规则: %+v", o)
	}
	if o, ok := byID["file:config/deep/x.ini"]; !ok || o.PolicyID != "deep" {
		t.Fatalf("config/deep/x.ini 应归更具体的 deep 规则: %+v", o)
	}
	for _, d := range report.Diagnostics {
		if d.Code == "diag.mapping.collision" {
			t.Fatalf("嵌套前缀不应产生碰撞诊断: %+v", report.Diagnostics)
		}
	}
}
