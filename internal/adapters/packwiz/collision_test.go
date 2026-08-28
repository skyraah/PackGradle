package packwiz

import (
	"context"
	"os"
	"path/filepath"
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
	dir := makeProject(t)
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	pol := modsOnlyPolicy()
	pol.Rules = append(pol.Rules, collisionRule("aaa", "config"), collisionRule("zzz", "config"))
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: pol})
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
	if collision.ResourceID != "file:config/notes.txt" {
		t.Errorf("碰撞证据资源 ID = %q", collision.ResourceID)
	}
}

func TestScanNestedPrefixMostSpecificWins(t *testing.T) {
	// config 与 config/deep 嵌套前缀：最深（最具体）规则胜出，不产生碰撞
	dir := makeProject(t)
	if err := os.MkdirAll(filepath.Join(dir, "config", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "deep", "x.ini"), []byte("k=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	pol := modsOnlyPolicy()
	pol.Rules = append(pol.Rules, collisionRule("config", "config"), collisionRule("deep", "config/deep"))
	fakeHash := func(ctx context.Context, abs string) (model.ContentRef, ports.FileFacts, error) {
		return model.ContentRef{Algorithm: "sha256", Digest: "d1", Size: 5}, ports.FileFacts{SizeBytes: 5}, nil
	}
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: pol, HashFile: fakeHash})
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

func TestScanIncludeExcludeDisjointPrefixes(t *testing.T) {
	// 同前缀但 include/exclude 互斥：各自唯一决议，无碰撞
	dir := makeProject(t)
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "a.toml"), []byte("k=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "b.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tomlOnly := collisionRule("toml-only", "config")
	tomlOnly.Include = []string{"config/*.toml"}
	rest := collisionRule("rest", "config")
	rest.Exclude = []string{"config/*.toml"}
	pol := modsOnlyPolicy()
	pol.Rules = append(pol.Rules, tomlOnly, rest)
	fakeHash := func(ctx context.Context, abs string) (model.ContentRef, ports.FileFacts, error) {
		return model.ContentRef{Algorithm: "sha256", Digest: "d1", Size: 3}, ports.FileFacts{SizeBytes: 3}, nil
	}
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: pol, HashFile: fakeHash})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[model.ResourceID]model.ResourceObservation{}
	for _, o := range report.Observations {
		byID[o.ResourceID] = o
	}
	if o, ok := byID["file:config/a.toml"]; !ok || o.PolicyID != "toml-only" {
		t.Fatalf("a.toml 应归 toml-only 规则: %+v", o)
	}
	if o, ok := byID["file:config/b.json"]; !ok || o.PolicyID != "rest" {
		t.Fatalf("b.json 应归 rest 规则: %+v", o)
	}
}
