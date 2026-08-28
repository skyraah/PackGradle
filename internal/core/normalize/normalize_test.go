package normalize

import (
	"strings"
	"testing"

	"packgradle/internal/core/model"
)

func TestNormalizeRelativePath(t *testing.T) {
	cases := []struct {
		in    string
		lower bool
		want  string
	}{
		{`a\b.toml`, false, "a/b.toml"},
		{`./a/../b`, false, ""}, // .. 拒绝
		{`a//b/./c`, false, "a/b/c"},
		{`Config/Foo.TOML`, true, "config/foo.toml"},
		{`Config/Foo.TOML`, false, "Config/Foo.TOML"},
	}
	for _, c := range cases {
		got, err := NormalizeRelativePath(c.in, c.lower)
		if c.want == "" {
			if err == nil {
				t.Fatalf("%q 应当被拒绝，得到 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q 意外错误: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q => %q, want %q", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "/abs/path", `C:\x`, "c:/x", "..", "a/../..", "a/../../b", "mod:modrinth:x"} {
		if _, err := NormalizeRelativePath(bad, false); err == nil {
			t.Fatalf("%q 应当被拒绝", bad)
		}
	}
}

func TestCanonicalJSONDeterministic(t *testing.T) {
	// 乱序插入的 map 必须产生相同字节
	a, _ := CanonicalJSON(map[string]any{
		"zeta": 1, "alpha": map[string]any{"b": []any{1, 2, 3}, "a": "x"}, "mid": true,
	})
	b, _ := CanonicalJSON(map[string]any{
		"mid": true, "alpha": map[string]any{"a": "x", "b": []any{1, 2, 3}}, "zeta": 1,
	})
	if string(a) != string(b) {
		t.Fatalf("map 插入顺序影响编码:\n%s\n%s", a, b)
	}
	if !strings.Contains(string(a), `"alpha":{"a":"x","b":[1,2,3]},"mid":true,"zeta":1`) {
		t.Fatalf("key 排序不符: %s", a)
	}
	// 不做 HTML 转义
	c, _ := CanonicalJSON(map[string]any{"u": "<script>&</script>"})
	if strings.Contains(string(c), `\u003c`) {
		t.Fatalf("不应做 HTML 转义: %s", c)
	}
	// 浮点禁止
	if _, err := CanonicalJSON(map[string]any{"f": 1.5}); err == nil {
		t.Fatal("浮点应当被拒绝")
	}
	// nil 与 []any{}、null
	if d, _ := CanonicalJSON(map[string]any{"n": nil, "e": []any{}}); string(d) != `{"e":[],"n":null}` {
		t.Fatalf("nil/空数组编码: %s", d)
	}
}

func makeSnapshot() model.ObservedSnapshot {
	return model.ObservedSnapshot{
		SchemaVersion:        1,
		SnapshotID:           "snap_TEST",
		RelationID:           "rel_TEST",
		Side:                 model.SideProject,
		CapturedAt:           "2026-08-22T00:00:00Z",
		BindingFingerprint:   "sha256:binding",
		SnapshotDigest:       "",
		NormalizationVersion: 1,
		PolicyDigest:         "sha256:policy",
		Scanner:              model.ScannerInfo{Name: "packwiz", Version: "1.0.0"},
		Resources: map[model.ResourceID]model.ResourceObservation{
			"mod:modrinth:AANobbMI": {
				ResourceID: "mod:modrinth:AANobbMI",
				Kind:       model.ResourceMod,
				Identity:   model.Identity{Provider: "modrinth", Key: "AANobbMI", Confidence: "high"},
				Representation: model.Representation{
					RelativePath: "mods/Sodium.pw.toml",
					Format:       "packwiz-mod-toml",
					Metadata: map[string]string{
						model.MetaVersion:           "0.6.5",
						model.MetaSide:              "client",
						model.MetaDeclaredHashAlgo:  "sha256",
						model.MetaDeclaredHashValue: "abc123",
						model.MetaDisplayName:       "Sodium 显示名（不入 digest）",
					},
				},
				PolicyID: "mods",
			},
			"file:config/jei/jei-client.ini": {
				ResourceID: "file:config/jei/jei-client.ini",
				Kind:       model.ResourceTextFile,
				Identity:   model.Identity{},
				Representation: model.Representation{
					RelativePath: "config/JEI/jei-client.ini",
					Format:       "ini",
					Content:      &model.ContentRef{Algorithm: "sha256", Digest: "def456", Size: 120},
				},
				PolicyID: "config",
			},
		},
		Diagnostics: []model.Diagnostic{{Severity: "warning", Code: "diag.scan.x"}},
	}
}

func TestSnapshotDigestExcludesVolatile(t *testing.T) {
	base := makeSnapshot()
	d1, err := SnapshotDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	// 变更所有排除字段：digest 不变
	mutated := makeSnapshot()
	mutated.SnapshotID = "snap_OTHER"
	mutated.RelationID = "rel_OTHER"
	mutated.CapturedAt = "2027-01-01T00:00:00Z"
	mutated.BindingFingerprint = "sha256:other"
	mutated.Scanner = model.ScannerInfo{Name: "other", Version: "9"}
	mutated.Diagnostics = append(mutated.Diagnostics, model.Diagnostic{Severity: "info", Code: "diag.scan.more"})
	d2, err := SnapshotDigest(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("排除字段影响 digest: %s vs %s", d1, d2)
	}
	// 变更受管内容：digest 必变
	changed := makeSnapshot()
	obs := changed.Resources["file:config/jei/jei-client.ini"]
	obs.Representation.Content.Digest = "changed"
	changed.Resources["file:config/jei/jei-client.ini"] = obs
	d3, _ := SnapshotDigest(changed)
	if d3 == d1 {
		t.Fatal("内容变化未影响 digest")
	}
	// 变更策略 digest：必变
	changedPolicy := makeSnapshot()
	changedPolicy.PolicyDigest = "sha256:policy2"
	d4, _ := SnapshotDigest(changedPolicy)
	if d4 == d1 {
		t.Fatal("policy_digest 变化未影响 digest")
	}
	// mod 显示名（Metadata 中的 display_name）不影响 digest
	changedName := makeSnapshot()
	obs2 := changedName.Resources["mod:modrinth:AANobbMI"]
	obs2.Representation.Metadata[model.MetaDisplayName] = "完全不同的显示名"
	changedName.Resources["mod:modrinth:AANobbMI"] = obs2
	d5, _ := SnapshotDigest(changedName)
	if d5 != d1 {
		t.Fatal("显示名不应影响 digest")
	}
	// mod 版本变化影响 digest
	changedVer := makeSnapshot()
	obs3 := changedVer.Resources["mod:modrinth:AANobbMI"]
	obs3.Representation.Metadata[model.MetaVersion] = "0.6.6"
	changedVer.Resources["mod:modrinth:AANobbMI"] = obs3
	d6, _ := SnapshotDigest(changedVer)
	if d6 == d1 {
		t.Fatal("版本变化未影响 digest")
	}
}

func TestSemanticDigestRules(t *testing.T) {
	// 文件缺内容指纹报错
	if _, err := SemanticDigest(model.ResourceTextFile, model.Representation{RelativePath: "x"}, model.Identity{}); err == nil {
		t.Fatal("文件缺少内容指纹应当报错")
	}
	// 高置信度 mod：side 大小写归一化、declared hash 算法小写归一化
	repA := model.Representation{
		RelativePath: "mods/a.pw.toml",
		Metadata: map[string]string{
			model.MetaVersion: "1.0", model.MetaSide: "Client",
			model.MetaDeclaredHashAlgo: "SHA256", model.MetaDeclaredHashValue: "h1",
		},
	}
	semUpper, err := SemanticDigest(model.ResourceMod, repA, model.Identity{Provider: "modrinth", Key: "K", Confidence: "high"})
	if err != nil {
		t.Fatal(err)
	}
	repB := repA
	repB.Metadata = map[string]string{
		model.MetaVersion: "1.0", model.MetaSide: "client",
		model.MetaDeclaredHashAlgo: "sha256", model.MetaDeclaredHashValue: "h1",
	}
	semLower, _ := SemanticDigest(model.ResourceMod, repB, model.Identity{Provider: "modrinth", Key: "K", Confidence: "high"})
	if semUpper != semLower {
		t.Fatal("side/算法大小写归一化失败")
	}
	// 低置信度 mod：文件名小写参与
	semLow, err := SemanticDigest(model.ResourceMod, model.Representation{
		RelativePath: "Mods/Sodium-0.6.5.Jar",
		Content:      &model.ContentRef{Algorithm: "sha256", Digest: "x", Size: 1},
	}, model.Identity{Provider: "jar", Key: "sodium", Confidence: "low"})
	if err != nil {
		t.Fatal(err)
	}
	semLow2, _ := SemanticDigest(model.ResourceMod, model.Representation{
		RelativePath: "mods/sodium-0.6.5.jar",
		Content:      &model.ContentRef{Algorithm: "sha256", Digest: "x", Size: 1},
	}, model.Identity{Provider: "jar", Key: "whatever", Confidence: "low"})
	if semLow != semLow2 {
		t.Fatal("低置信度 digest 应基于小写文件名而非 identity key")
	}
}

func TestAbsentTombstoneStable(t *testing.T) {
	if AbsentTombstone() != AbsentTombstone() {
		t.Fatal("tombstone 必须是固定值")
	}
	if AbsentTombstone() == "" || !strings.HasPrefix(AbsentTombstone(), "sha256:") {
		t.Fatalf("tombstone 格式: %s", AbsentTombstone())
	}
}

func TestLogicalDigest(t *testing.T) {
	a := LogicalDigest("semA", "semB")
	b := LogicalDigest("semA", "semB")
	if a != b {
		t.Fatal("相同输入 digest 不同")
	}
	if LogicalDigest("semA", "") == a {
		t.Fatal("单侧缺失应改变 digest")
	}
	if LogicalDigest("", "") == LogicalDigest("", "x") {
		t.Fatal("runtime 侧语义应参与 digest")
	}
}

func TestIdentityFromResourceID(t *testing.T) {
	cases := map[model.ResourceID][3]string{
		"mod:modrinth:AANobbMI":    {"modrinth", "AANobbMI", "high"},
		"mod:curseforge:12345":     {"curseforge", "12345", "high"},
		"mod:jar:sodium-0.6.5.jar": {"jar", "sodium-0.6.5.jar", "low"},
		"mod:path:mods/x.pw.toml":  {"path", "mods/x.pw.toml", "low"},
		"file:config/a.toml":       {"", "", ""},
	}
	for id, want := range cases {
		got := IdentityFromResourceID(id)
		if got.Provider != want[0] || got.Key != want[1] || got.Confidence != want[2] {
			t.Fatalf("%s => %+v, want %v", id, got, want)
		}
	}
}

func TestPlanDigestExcludesVolatile(t *testing.T) {
	base := model.SyncPlan{
		SchemaVersion:              1,
		PlanID:                     "plan_A",
		RelationID:                 "rel_A",
		Kind:                       model.PlanInitialize,
		RelationRevision:           3,
		PolicyDigest:               "sha256:p",
		InputProjectSnapshotDigest: "sha256:ps",
		InputRuntimeSnapshotDigest: "sha256:rs",
		ExpectedBindings:           model.ExpectedBindings{Project: "sha256:pb", Runtime: "sha256:rb"},
		Status:                     model.PlanDraft,
		ExpiresAt:                  "2026-08-22T00:00:00Z",
		Operations: []model.PlannedOperation{
			{ID: "op_0001", Kind: model.OpWriteRuntime, ResourceID: "file:config/a.toml",
				Preconditions: []model.Precondition{{ResourceID: "file:config/a.toml", Side: "runtime", Existence: "absent"}},
				Reversible:    true},
		},
		Conflicts: []model.Conflict{{ResourceID: "mod:jar:x.jar", Kind: model.ConflictInitialize, Detail: "说明文字"}},
	}
	d1, err := PlanDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	mutated := base
	mutated.PlanID = "plan_B"
	mutated.RelationID = "rel_B"
	mutated.Status = model.PlanResolved
	mutated.ExpiresAt = "2027-01-01T00:00:00Z"
	d2, _ := PlanDigest(mutated)
	if d1 != d2 {
		t.Fatal("排除字段（id/status/expires）影响了 plan digest")
	}
	// resolutions 参与 digest
	resolved := base
	resolved.ResolvedFromPlanID = "plan_A"
	resolved.Resolutions = []model.Resolution{{ResourceID: "mod:jar:x.jar", Choice: model.ChoiceSkip}}
	d3, _ := PlanDigest(resolved)
	if d3 == d1 {
		t.Fatal("resolutions 应参与 digest")
	}
	// 操作排序变化（确定性编号输入）影响 digest
	reordered := base
	reordered.Operations = []model.PlannedOperation{
		{ID: "op_0001", Kind: model.OpWriteProject, ResourceID: "file:config/a.toml", Reversible: true},
	}
	d4, _ := PlanDigest(reordered)
	if d4 == d1 {
		t.Fatal("操作 kind 变化应影响 digest")
	}
}
