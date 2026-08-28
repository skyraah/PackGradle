package prism

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

const (
	// sodium 的 .index 条目：x-prismlauncher-version-number 应优先于顶层 version
	// 与 [update.modrinth] version-id。
	sodiumIndexTOML = `
name = "Sodium"
side = "client"
x-prismlauncher-loaders = "fabric:0.16.0"
x-prismlauncher-version-number = "0.6.5"
version = "ignored-top-level"

[download]
url = "https://cdn.modrinth.com/data/AANobbMI/versions/x/sodium-0.6.5.jar"
hash-format = "sha256"
hash = "f00dcafe"

[update.modrinth]
mod-id = "AANobbMI"
version-id = 99887766
`
	// jei 的 .index 条目：故意写坏，验证降级为诊断而非丢资源。
	brokenIndexTOML = "this is [ not toml"
)

// mustWrite 在 root 下写文件（自动建父目录）。
func mustWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeGameDir 构建 <tmp>/instances/Collapse/minecraft fixture，返回游戏目录。
func makeGameDir(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "instances", "Collapse", "minecraft")
	mustWrite(t, root, "mods/sodium-0.6.5.jar", "sodium jar bytes")
	mustWrite(t, root, "mods/jei-19.5.jar", "jei jar bytes")
	mustWrite(t, root, "mods/runtimeonly-1.0.jar", "runtimeonly jar bytes")
	mustWrite(t, root, "mods/.index/sodium-0.6.5.jar.pw.toml", sodiumIndexTOML)
	mustWrite(t, root, "mods/.index/jei-19.5.jar.pw.toml", brokenIndexTOML)
	return root
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

// fakeHash 返回按文件名派生的确定性 ContentRef，便于断言 Content 确实来自 HashFile。
func fakeHash() func(context.Context, string) (model.ContentRef, ports.FileFacts, error) {
	return func(ctx context.Context, abs string) (model.ContentRef, ports.FileFacts, error) {
		base := strings.ToLower(filepath.Base(abs))
		return model.ContentRef{Algorithm: "sha256", Digest: "digest-" + base, Size: int64(len(base))},
			ports.FileFacts{SizeBytes: int64(len(base))}, nil
	}
}

func baseHint() map[string]string {
	return map[string]string{
		"sodium-0.6.5.jar": "mod:modrinth:AANobbMI",
		"jei-19.5.jar":     "mod:curseforge:228525",
	}
}

func byID(report model.ScanReport) map[model.ResourceID]model.ResourceObservation {
	out := make(map[model.ResourceID]model.ResourceObservation, len(report.Observations))
	for _, o := range report.Observations {
		out[o.ResourceID] = o
	}
	return out
}

func obsKeys(m map[model.ResourceID]model.ResourceObservation) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	return out
}

func countDiag(report model.ScanReport, code string) int {
	n := 0
	for _, d := range report.Diagnostics {
		if d.Code == code {
			n++
		}
	}
	return n
}

func TestScanModsWithHintAndIndexMeta(t *testing.T) {
	root := makeGameDir(t)
	report, err := New().Scan(context.Background(), root, ports.ScanOptions{
		Policy:   modsOnlyPolicy(),
		Hint:     ports.ScanHint{FilenameToResourceID: baseHint()},
		HashFile: fakeHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	obs := byID(report)

	// sodium：hint 高置信身份 + .index 元数据 + 内容指纹
	sodium, ok := obs["mod:modrinth:AANobbMI"]
	if !ok {
		t.Fatalf("缺少 hint 命中的 modrinth 身份: %v", obsKeys(obs))
	}
	wantIdentity := model.Identity{Provider: "modrinth", Key: "AANobbMI", Confidence: model.ConfidenceHigh}
	if sodium.Identity != wantIdentity {
		t.Fatalf("sodium identity: %+v", sodium.Identity)
	}
	md := sodium.Representation.Metadata
	if md[model.MetaVersion] != "0.6.5" {
		t.Fatalf("x-prismlauncher-version-number 应优先: %q", md[model.MetaVersion])
	}
	if md[model.MetaSide] != "client" {
		t.Fatalf("sodium side: %q", md[model.MetaSide])
	}
	if md[model.MetaDeclaredHashAlgo] != "sha256" || md[model.MetaDeclaredHashValue] != "f00dcafe" {
		t.Fatalf("声明 hash 提取失败: %v", md)
	}
	if md[model.MetaDisplayName] != "Sodium" {
		t.Fatalf("展示名缺失: %q", md[model.MetaDisplayName])
	}
	if sodium.Representation.RelativePath != "mods/sodium-0.6.5.jar" || sodium.Representation.Format != "jar" {
		t.Fatalf("sodium 表示: %+v", sodium.Representation)
	}
	if sodium.Representation.Content == nil || sodium.Representation.Content.Digest != "digest-sodium-0.6.5.jar" {
		t.Fatalf("Content 应来自 HashFile: %+v", sodium.Representation.Content)
	}
	if sodium.PolicyID != "mods" {
		t.Fatalf("PolicyID: %q", sodium.PolicyID)
	}

	// jei：hint 身份保留，坏 .index 降级为诊断，资源仍在
	jei, ok := obs["mod:curseforge:228525"]
	if !ok {
		t.Fatalf("缺少 curseforge 身份: %v", obsKeys(obs))
	}
	if jei.Representation.Metadata != nil {
		t.Fatal("坏 .index 不应产出 metadata")
	}
	if jei.Representation.Content == nil {
		t.Fatal("坏 .index 不影响内容指纹")
	}
	if countDiag(report, "diag.scan.index_meta_unreadable") != 1 {
		t.Fatalf("缺少 index_meta_unreadable 诊断: %+v", report.Diagnostics)
	}

	// runtimeonly：无 hint 回退 jar 低置信身份，无 metadata
	rt, ok := obs["mod:jar:runtimeonly-1.0.jar"]
	if !ok {
		t.Fatalf("缺少本地 jar 身份: %v", obsKeys(obs))
	}
	wantLocal := model.Identity{Provider: "jar", Key: "runtimeonly-1.0.jar", Confidence: model.ConfidenceLow}
	if rt.Identity != wantLocal {
		t.Fatalf("runtimeonly identity: %+v", rt.Identity)
	}
	if rt.Representation.Metadata != nil {
		t.Fatalf("无 .index 不应有 metadata: %v", rt.Representation.Metadata)
	}
	if rt.Representation.Content == nil {
		t.Fatal("本地 jar 仍应有内容指纹")
	}

	// .index 目录本身不产出任何观察
	if len(report.Observations) != 3 {
		t.Fatalf("应恰有 3 个观察: %+v", report.Observations)
	}
	// 排序断言（字节序严格递增）
	for i := 1; i < len(report.Observations); i++ {
		if report.Observations[i-1].ResourceID >= report.Observations[i].ResourceID {
			t.Fatal("observations 未按 ResourceID 字节序排序")
		}
	}
}

func TestScanHintCaseInsensitive(t *testing.T) {
	root := filepath.Join(t.TempDir(), "instances", "Mixed", "minecraft")
	mustWrite(t, root, "mods/Sodium-0.6.5.jar", "sodium bytes")
	mustWrite(t, root, "mods/JEI-19.5.jar", "jei bytes")
	// hint 键故意用非规范大写：jar 文件名小写后查表（键也归一小写）应命中
	hint := map[string]string{"SODIUM-0.6.5.jar": "mod:modrinth:AANobbMI"}

	report, err := New().Scan(context.Background(), root, ports.ScanOptions{
		Policy:   modsOnlyPolicy(),
		Hint:     ports.ScanHint{FilenameToResourceID: hint},
		HashFile: fakeHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	obs := byID(report)
	sodium, ok := obs["mod:modrinth:AANobbMI"]
	if !ok {
		t.Fatalf("大小写不敏感的 hint 未命中: %v", obsKeys(obs))
	}
	if sodium.Representation.RelativePath != "mods/Sodium-0.6.5.jar" {
		t.Fatalf("展示路径应保留原大小写: %q", sodium.Representation.RelativePath)
	}
	// 未命中 hint 的混合大小写 jar 回退到小写 jar 身份
	if _, ok := obs["mod:jar:jei-19.5.jar"]; !ok {
		t.Fatalf("回退身份应使用小写文件名: %v", obsKeys(obs))
	}
	if len(report.Observations) != 2 {
		t.Fatalf("应恰有 2 个观察: %+v", report.Observations)
	}
}

func TestScanHasherMissing(t *testing.T) {
	root := makeGameDir(t)
	report, err := New().Scan(context.Background(), root, ports.ScanOptions{
		Policy: modsOnlyPolicy(),
		Hint:   ports.ScanHint{FilenameToResourceID: baseHint()},
		// HashFile 为 nil：全部 jar 跳过
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Observations) != 0 {
		t.Fatalf("无哈希函数时 jar 应全部跳过: %+v", report.Observations)
	}
	if n := countDiag(report, "diag.scan.hasher_missing"); n != 3 {
		t.Fatalf("hasher_missing 诊断数 = %d, 期望 3: %+v", n, report.Diagnostics)
	}
}

func TestScanManagedFileRules(t *testing.T) {
	root := makeGameDir(t)
	mustWrite(t, root, "config/settings.toml", "key = 1\n")
	mustWrite(t, root, "config/secret.txt", "hush\n")

	// default-v1 只有 mod 规则：config 下文件不产出
	report, err := New().Scan(context.Background(), root, ports.ScanOptions{
		Policy:   modsOnlyPolicy(),
		Hint:     ports.ScanHint{FilenameToResourceID: baseHint()},
		HashFile: fakeHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range report.Observations {
		if o.Kind != model.ResourceMod {
			t.Fatalf("default-v1 不应产出文件观察: %+v", o)
		}
	}

	// 加 config 规则后产出且 Content 填充，Exclude 生效
	policy := modsOnlyPolicy()
	policy.Rules = append(policy.Rules, model.MappingRule{
		ID: "config", ResourceKind: "text_file",
		ProjectPrefix: "config", RuntimePrefix: "config",
		Direction: "bidirectional", Materialization: "copy", MergePolicy: "manual", RuntimeLocalPolicy: "exclude",
		Exclude: []string{"config/secret*"},
	})
	report, err = New().Scan(context.Background(), root, ports.ScanOptions{
		Policy:   policy,
		Hint:     ports.ScanHint{FilenameToResourceID: baseHint()},
		HashFile: fakeHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	obs := byID(report)
	file, ok := obs["file:config/settings.toml"]
	if !ok {
		t.Fatalf("config 规则未产出文件观察: %v", obsKeys(obs))
	}
	if file.Kind != model.ResourceTextFile || file.PolicyID != "config" {
		t.Fatalf("文件观察类别/规则: %+v", file)
	}
	if file.Representation.Content == nil || file.Representation.Content.Digest != "digest-settings.toml" {
		t.Fatalf("文件 Content 缺失: %+v", file.Representation.Content)
	}
	if file.Representation.Format != "toml" {
		t.Fatalf("格式推断: %q", file.Representation.Format)
	}
	if _, has := obs["file:config/secret.txt"]; has {
		t.Fatal("Exclude 未生效")
	}
}

func TestScanMissingModsDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "instances", "Empty", "minecraft")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := New().Scan(context.Background(), root, ports.ScanOptions{
		Policy:   modsOnlyPolicy(),
		HashFile: fakeHash(),
	})
	if err != nil {
		t.Fatalf("mods 目录不存在不应报错: %v", err)
	}
	if len(report.Observations) != 0 || len(report.Diagnostics) != 0 {
		t.Fatalf("应为空 report: %+v", report)
	}
}

func TestScanDirDisguisedAsJar(t *testing.T) {
	root := makeGameDir(t)
	if err := os.MkdirAll(filepath.Join(root, "mods", "fake.jar"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := New().Scan(context.Background(), root, ports.ScanOptions{
		Policy:   modsOnlyPolicy(),
		Hint:     ports.ScanHint{FilenameToResourceID: baseHint()},
		HashFile: fakeHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if countDiag(report, "diag.scan.not_regular_file") != 1 {
		t.Fatalf("缺少 not_regular_file 诊断: %+v", report.Diagnostics)
	}
	if obs := byID(report); len(obs) != 3 {
		t.Fatalf("伪 jar 目录不应产出观察: %v", obsKeys(obs))
	}
}

func TestScanDuplicateIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "instances", "Dupes", "minecraft")
	mustWrite(t, root, "mods/a.jar", "a bytes")
	mustWrite(t, root, "mods/b.jar", "b bytes")
	// 两个不同 jar 经 hint 映射到同一 ResourceID（Windows 大小写重名的等价场景）
	hint := map[string]string{
		"a.jar": "mod:modrinth:DUPE",
		"b.jar": "mod:modrinth:DUPE",
	}
	report, err := New().Scan(context.Background(), root, ports.ScanOptions{
		Policy:   modsOnlyPolicy(),
		Hint:     ports.ScanHint{FilenameToResourceID: hint},
		HashFile: fakeHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if countDiag(report, "diag.scan.duplicate_identity") != 1 {
		t.Fatalf("缺少 duplicate_identity 诊断: %+v", report.Diagnostics)
	}
	if len(report.Observations) != 1 {
		t.Fatalf("重复身份的后者应跳过: %+v", report.Observations)
	}
	if obs := byID(report); len(obs) != 1 {
		t.Fatalf("首个观察应保留: %v", obsKeys(obs))
	}
}

func TestIndexVersionPriority(t *testing.T) {
	cases := []struct {
		name string
		m    indexMeta
		want string
	}{
		{"x-prismlauncher 优先", indexMeta{
			XPLVersion: "0.6.5", Version: "v1",
			Update: map[string]map[string]any{"modrinth": {"version-id": int64(1)}},
		}, "0.6.5"},
		{"顶层 version 次之", indexMeta{
			Version: "v1",
			Update:  map[string]map[string]any{"modrinth": {"version-id": int64(1)}},
		}, "v1"},
		{"modrinth version-id 数字转字符串", indexMeta{
			Update: map[string]map[string]any{"modrinth": {"version-id": int64(99887766)}},
		}, "99887766"},
		{"curseforge file-id 兜底", indexMeta{
			Update: map[string]map[string]any{"curseforge": {"file-id": 5566778}},
		}, "5566778"},
		{"全空", indexMeta{}, ""},
	}
	for _, c := range cases {
		if got := indexVersion(c.m); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestDiscoverInstances(t *testing.T) {
	instances := t.TempDir()
	// 含 BOM/CRLF/注释/节头 的 instance.cfg
	mustWrite(t, instances, "Collapse/instance.cfg",
		"\ufeff# Prism Launcher comment\r\n[General]\r\nname = Collapse Display\r\n")
	mustWrite(t, instances, "NoName/instance.cfg", "; only a comment\niconKey=default\n")
	mustWrite(t, instances, "NotInstance/somefile.txt", "not an instance\n")
	mustWrite(t, instances, "notes.txt", "top level file\n")

	got, err := DiscoverInstances(instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("实例数 = %d, 期望 2: %+v", len(got), got)
	}
	// 按名称排序：Collapse Display < NoName
	if got[0].Name != "Collapse Display" {
		t.Fatalf("cfg name 解析失败: %+v", got[0])
	}
	if got[0].Dir != filepath.Join(instances, "Collapse") {
		t.Fatalf("实例目录: %q", got[0].Dir)
	}
	if got[0].GameDir != filepath.Join(instances, "Collapse", "minecraft") {
		t.Fatalf("游戏目录: %q", got[0].GameDir)
	}
	if got[1].Name != "NoName" {
		t.Fatalf("name 缺失应回退目录名: %+v", got[1])
	}
	if got[1].GameDir != filepath.Join(instances, "NoName", "minecraft") {
		t.Fatalf("游戏目录: %q", got[1].GameDir)
	}
}

func TestDiscoverInstancesMissingDir(t *testing.T) {
	if _, err := DiscoverInstances(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("实例根目录不存在应返回错误")
	}
}

func TestReadINIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.cfg")
	if err := os.WriteFile(path, []byte("\ufeff# 注释行\r\n[General]\r\nname = 崩坏幸存者\r\n; 分号注释\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, ok, err := readINIKey(path, "name")
	if err != nil || !ok || v != "崩坏幸存者" {
		t.Fatalf("got (%q, %v, %v)", v, ok, err)
	}
	if _, ok, _ := readINIKey(path, "missing"); ok {
		t.Fatal("未定义键不应命中")
	}
	if _, ok, err := readINIKey(filepath.Join(dir, "nope.cfg"), "name"); ok || err != nil {
		t.Fatalf("文件不存在应返回 (false, nil): got (%v, %v)", ok, err)
	}
}
