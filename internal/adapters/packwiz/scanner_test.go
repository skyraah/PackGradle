package packwiz

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

const (
	tomlModrinth = `
name = "Sodium"
filename = "sodium-0.6.5.jar"
side = "client"

[download]
url = "https://cdn.modrinth.com/data/AANobbMI/versions/x/sodium-0.6.5.jar"
hash-format = "sha256"
hash = "aaabbbcccddd"

[update.modrinth]
mod-id = "AANobbMI"
version-id = "99887766"
`
	tomlCurseforge = `
name = "JEI"
filename = "jei-19.5.jar"
side = "both"
version = "19.5.0.3"

[download]
url = "https://mediafilez.example/jei.jar"
hash-format = "murmur2"
hash = "11223344"

[update.curseforge]
project-id = 228525
file-id = 5566778
`
	tomlNoSource = `
name = "本地小玩意"
filename = "local-thing-1.0.jar"
`
	indexTOML = `
index = { file = "index.toml", hash-format = "sha256", hash = "0" }

[[files]]
file = "mods/sodium.pw.toml"
hash = "1"
metafile = true

[[files]]
file = "mods/jei.pw.toml"
hash = "2"
metafile = true

[[files]]
file = "mods/local.pw.toml"
hash = "3"
metafile = true

[[files]]
file = "mods/../evil.pw.toml"
hash = "4"
metafile = true

[[files]]
file = "config/notes.txt"
hash = "5"
`
)

func makeProject(t *testing.T) string {
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pack.toml", "name = \"Collapse\"\nauthor = \"tester\"\nversion = \"1.0\"\n")
	write("index.toml", indexTOML)
	write("mods/sodium.pw.toml", tomlModrinth)
	write("mods/jei.pw.toml", tomlCurseforge)
	write("mods/local.pw.toml", tomlNoSource)
	return dir
}

func modsOnlyPolicy() model.MappingPolicy {
	return model.MappingPolicy{
		SchemaVersion: 1,
		PolicyID:      "default-v1",
		Revision:      1,
		Rules: []model.MappingRule{{
			ID: "mods", ResourceKind: "mod",
			ProjectPrefix: "mods", RuntimePrefix: "mods",
			Direction: "bidirectional", Materialization: "copy", MergePolicy: "packwiz",
			RuntimeLocalPolicy: "exclude",
		}},
	}
}

func TestScanModIdentities(t *testing.T) {
	dir := makeProject(t)
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[model.ResourceID]model.ResourceObservation{}
	for _, o := range report.Observations {
		byID[o.ResourceID] = o
	}
	sodium, ok := byID["mod:modrinth:AANobbMI"]
	if !ok {
		t.Fatalf("缺少 modrinth 身份: %v", keys(byID))
	}
	if sodium.Identity.Confidence != model.ConfidenceHigh {
		t.Fatal("modrinth 应高置信度")
	}
	// 顶层 version 为空 → 回退 update.modrinth version-id
	if sodium.Representation.Metadata[model.MetaVersion] != "99887766" {
		t.Fatalf("sodium version: %q", sodium.Representation.Metadata[model.MetaVersion])
	}
	if sodium.Representation.Metadata[model.MetaFilename] != "sodium-0.6.5.jar" {
		t.Fatal("filename 元数据缺失")
	}
	if sodium.Representation.Metadata[model.MetaDeclaredHashAlgo] != "sha256" ||
		sodium.Representation.Metadata[model.MetaDeclaredHashValue] != "aaabbbcccddd" {
		t.Fatal("声明 hash 提取失败")
	}

	jei, ok := byID["mod:curseforge:228525"]
	if !ok {
		t.Fatalf("缺少 curseforge 身份: %v", keys(byID))
	}
	if jei.Representation.Metadata[model.MetaVersion] != "19.5.0.3" {
		t.Fatalf("顶层 version 应优先: %q", jei.Representation.Metadata[model.MetaVersion])
	}
	if jei.Representation.Metadata[model.MetaDeclaredHashAlgo] != "murmur2" {
		t.Fatal("murmur2 声明 hash 未提取")
	}

	local, ok := byID["mod:path:mods/local.pw.toml"]
	if !ok {
		t.Fatalf("无源 mod 应回退路径身份: %v", keys(byID))
	}
	if local.Identity.Confidence != model.ConfidenceLow || local.Identity.Provider != "path" {
		t.Fatalf("无源身份: %+v", local.Identity)
	}

	// 路径逃逸条目 → 诊断 + 不产出观察
	for _, o := range report.Observations {
		if strings.Contains(string(o.ResourceID), "evil") {
			t.Fatal("逃逸条目不应产出观察")
		}
	}
	found := false
	for _, d := range report.Diagnostics {
		if d.Code == "diag.scan.path_escape" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺少 path_escape 诊断: %+v", report.Diagnostics)
	}

	// 排序断言
	for i := 1; i < len(report.Observations); i++ {
		if report.Observations[i-1].ResourceID >= report.Observations[i].ResourceID {
			t.Fatal("observations 未按 ResourceID 排序")
		}
	}
}

func keys(m map[model.ResourceID]model.ResourceObservation) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	return out
}

func TestScanBrokenModMetaKept(t *testing.T) {
	dir := makeProject(t)
	if err := os.WriteFile(filepath.Join(dir, "mods", "local.pw.toml"), []byte("this is [ not toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	var hasLow, hasDiag bool
	for _, o := range report.Observations {
		if o.ResourceID == "mod:path:mods/local.pw.toml" {
			hasLow = true
		}
	}
	for _, d := range report.Diagnostics {
		if d.Code == "diag.scan.modmeta_unreadable" {
			hasDiag = true
		}
	}
	if !hasLow || !hasDiag {
		t.Fatalf("损坏 metafile 应保留低置信条目+诊断: obs=%v diag=%v", hasLow, hasDiag)
	}
}

func TestScanMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: modsOnlyPolicy()}); !errors.Is(err, ErrNotPackwizProject) {
		t.Fatalf("缺 pack.toml: err=%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.toml"), []byte("name=\"x\""), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: modsOnlyPolicy()}); !errors.Is(err, ErrIndexMissing) {
		t.Fatalf("缺 index.toml: err=%v", err)
	}
}

func TestScanManagedFilesPolicy(t *testing.T) {
	dir := makeProject(t)
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	hashed := 0
	fakeHash := func(ctx context.Context, abs string) (model.ContentRef, ports.FileFacts, error) {
		hashed++
		return model.ContentRef{Algorithm: "sha256", Digest: fmt.Sprintf("digest-%d", hashed), Size: 5}, ports.FileFacts{SizeBytes: 5}, nil
	}

	// 仅 mods 规则：config 不产出观察
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: modsOnlyPolicy(), HashFile: fakeHash})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range report.Observations {
		if o.Kind != model.ResourceMod {
			t.Fatalf("default-v1 不应产出文件观察: %+v", o)
		}
	}

	// 加 config 规则后产出观察并哈希
	policy := modsOnlyPolicy()
	policy.Rules = append(policy.Rules, model.MappingRule{
		ID: "config", ResourceKind: "text_file",
		ProjectPrefix: "config", RuntimePrefix: "config",
		Direction: "bidirectional", Materialization: "copy", MergePolicy: "manual", RuntimeLocalPolicy: "exclude",
		Exclude: []string{"config/secret*"},
	})
	report, err = New().Scan(context.Background(), dir, ports.ScanOptions{Policy: policy, HashFile: fakeHash})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range report.Observations {
		if o.ResourceID == "file:config/notes.txt" {
			found = true
			if o.Representation.Content == nil || o.PolicyID != "config" {
				t.Fatalf("文件观察内容缺失: %+v", o)
			}
		}
	}
	if !found {
		t.Fatal("config 规则未产出文件观察")
	}

	// HashFile 缺失 → 诊断而非崩溃
	report, err = New().Scan(context.Background(), dir, ports.ScanOptions{Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	var warned bool
	for _, d := range report.Diagnostics {
		if d.Code == "diag.scan.hasher_missing" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("缺少 hasher_missing 诊断")
	}
}

// index.toml 中非 mods/ metafile 条目（包内追踪但不在受管范围）标记 ignored。
func TestScanIgnoresNonMetafileEntries(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pack.toml", "name = \"Collapse\"\n")
	write("index.toml", `index = { file = "index.toml", hash-format = "sha256", hash = "0" }

[[files]]
file = "mods/sodium.pw.toml"
hash = "1"
metafile = true

[[files]]
file = "config/jei.ini"
hash = "2"

[[files]]
file = "README.md"
hash = "3"
metafile = false
`)
	write("mods/sodium.pw.toml", tomlModrinth)
	write("config/jei.ini", "key=value\n")
	write("README.md", "readme\n")

	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range report.Diagnostics {
		if d.Code == "diag.scan.ignored" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("ignored 诊断应恰好 2 条，得到 %d: %v", n, report.Diagnostics)
	}
	paths := map[string]bool{}
	for _, d := range report.Diagnostics {
		if d.Code == "diag.scan.ignored" {
			paths[d.RelativePath] = true
		}
	}
	if !paths["config/jei.ini"] || !paths["README.md"] {
		t.Errorf("ignored 证据应含两个非受管条目: %v", paths)
	}
	// 非受管条目不产出观察
	for _, o := range report.Observations {
		if o.Representation.RelativePath == "config/jei.ini" || o.Representation.RelativePath == "README.md" {
			t.Errorf("ignored 条目不应产出观察: %+v", o)
		}
	}
}
