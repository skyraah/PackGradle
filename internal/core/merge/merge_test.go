package merge

import (
	"encoding/json"
	"strings"
	"testing"

	"packgradle/internal/core/model"
)

// handmadeBase 是含手工注释、键序、空行与缩进样本的基线全文
// （ADR-0009 §2 验收口径的字节级保真夹具）。
const handmadeBase = `# 手工注释样本：玩家手写的配置头部。
# 第二行注释。

[graphics]
fancy_graphics = false
  render_distance = 12   # 行内注释 + 缩进键


[audio]
master_volume = 0.8

[anchors]
shared = "base"
`

// projectEdit / runtimeEdit 分别在 handmadeBase 的不同区域注入改动
// （互不重叠 → 干净合并；同区域 → 真冲突）。
var (
	projectEdit = strings.Replace(handmadeBase, `fancy_graphics = false`, "fancy_graphics = true", 1)
	runtimeEdit = strings.Replace(handmadeBase, `master_volume = 0.8`, "master_volume = 1.0", 1)
)

// TestTextsNonOverlappingMergeIsClean 验证互不重叠的双侧改动合并为零冲突块，
// 且合并产物为「基线 + 双侧改动」的确定字节串：未冲突区域（手工注释、键序、
// 空行、缩进）字节级不变。
func TestTextsNonOverlappingMergeIsClean(t *testing.T) {
	res := Texts([]byte(handmadeBase), []byte(projectEdit), []byte(runtimeEdit))
	if len(res.Hunks) != 0 {
		t.Fatalf("互不重叠改动不应有冲突块: %+v", res.Hunks)
	}
	want := strings.Replace(projectEdit, `master_volume = 0.8`, "master_volume = 1.0", 1)
	if string(res.Merged) != want {
		t.Fatalf("合并产物字节级不符:\n--- got ---\n%q\n--- want ---\n%q", res.Merged, want)
	}
	// 确定性：同输入重算同输出（ADR-0009 §8 暂存期重算的前提）。
	again := Texts([]byte(handmadeBase), []byte(projectEdit), []byte(runtimeEdit))
	if string(again.Merged) != string(res.Merged) {
		t.Fatal("同输入两次合并产物不同，确定性破坏")
	}
}

// TestTextsPreservesCRLF 验证 CRLF 文件合并后未冲突区域含 \r\n（字节级不变
// 不假设 LF）。两侧改动隔行注入（diff3 把相邻行改动按同一区域判冲突）。
func TestTextsPreservesCRLF(t *testing.T) {
	base := "[a]\r\nkey1 = 1\r\nmid = 0\r\nkey2 = 2\r\nkey3 = 3\r\n"
	proj := "[a]\r\nkey1 = 111\r\nmid = 0\r\nkey2 = 2\r\nkey3 = 3\r\n"
	rt := "[a]\r\nkey1 = 1\r\nmid = 0\r\nkey2 = 2\r\nkey3 = 333\r\n"
	res := Texts([]byte(base), []byte(proj), []byte(rt))
	if len(res.Hunks) != 0 {
		t.Fatalf("CRLF 互不重叠改动不应有冲突块: %+v", res.Hunks)
	}
	if want := "[a]\r\nkey1 = 111\r\nmid = 0\r\nkey2 = 2\r\nkey3 = 333\r\n"; string(res.Merged) != want {
		t.Fatalf("CRLF 合并产物不符: %q，期望 %q", res.Merged, want)
	}
}

// TestTextsTrueConflict 验证同区域双侧不同改动产出结构化冲突块：
// 三侧行片段与起始行号（1 起始）齐备。
func TestTextsTrueConflict(t *testing.T) {
	rtEdit := strings.Replace(handmadeBase, `fancy_graphics = false`, "fancy_graphics = \"ultra\"", 1)
	res := Texts([]byte(handmadeBase), []byte(projectEdit), []byte(rtEdit))
	if len(res.Hunks) != 1 {
		t.Fatalf("同区域真冲突应产出 1 个冲突块，得到 %d", len(res.Hunks))
	}
	if res.Merged != nil {
		t.Fatalf("含冲突块时不得产出合并产物: %q", res.Merged)
	}
	h := res.Hunks[0]
	if h.Project.Start != 5 || h.Base.Start != 5 || h.Runtime.Start != 5 {
		t.Fatalf("起始行号应为 5（1 起始）: %+v", h)
	}
	if want := []string{"fancy_graphics = true"}; !equalLines(h.Project.Lines, want) {
		t.Fatalf("project 侧行片段: %v，期望 %v", h.Project.Lines, want)
	}
	if want := []string{"fancy_graphics = false"}; !equalLines(h.Base.Lines, want) {
		t.Fatalf("基线侧行片段: %v，期望 %v", h.Base.Lines, want)
	}
	if want := []string{`fancy_graphics = "ultra"`}; !equalLines(h.Runtime.Lines, want) {
		t.Fatalf("runtime 侧行片段: %v，期望 %v", h.Runtime.Lines, want)
	}
}

// TestTextsIdenticalChangesNotConflict 验证双侧相同改动（假冲突）按已决议处理。
func TestTextsIdenticalChangesNotConflict(t *testing.T) {
	res := Texts([]byte(handmadeBase), []byte(projectEdit), []byte(projectEdit))
	if len(res.Hunks) != 0 {
		t.Fatalf("双侧相同改动不应有冲突块: %+v", res.Hunks)
	}
	if string(res.Merged) != projectEdit {
		t.Fatalf("合并产物应为单侧改动全文: %q", res.Merged)
	}
}

// TestDetailJSONShape 验证 hunk JSON 定形（契约 07 §3.3）：
// 顶层仅 hunks 键，块内域词汇 project/base/runtime，各侧 {start,lines}。
func TestDetailJSONShape(t *testing.T) {
	rtEdit := strings.Replace(handmadeBase, `fancy_graphics = false`, "fancy_graphics = \"ultra\"", 1)
	res := Texts([]byte(handmadeBase), []byte(projectEdit), []byte(rtEdit))
	if len(res.Hunks) != 1 {
		t.Fatalf("前置失败：应产出 1 个冲突块，得到 %d", len(res.Hunks))
	}
	detail, err := DetailJSON(res.Hunks)
	if err != nil {
		t.Fatalf("DetailJSON: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(detail), &top); err != nil {
		t.Fatalf("detail 非 JSON: %v", err)
	}
	if len(top) != 1 || top["hunks"] == nil {
		t.Fatalf("顶层应仅有 hunks 键: %v", detail)
	}
	var hunks []map[string]json.RawMessage
	if err := json.Unmarshal(top["hunks"], &hunks); err != nil {
		t.Fatalf("hunks 形状不符: %v", err)
	}
	if len(hunks) != 1 {
		t.Fatalf("期望 1 块打包成数组，得到 %d", len(hunks))
	}
	for _, side := range []string{"project", "base", "runtime"} {
		raw, ok := hunks[0][side]
		if !ok {
			t.Fatalf("冲突块缺 %s 侧: %s", side, detail)
		}
		var hs HunkSide
		if err := json.Unmarshal(raw, &hs); err != nil {
			t.Fatalf("%s 侧应为 {start,lines}: %v", side, err)
		}
		if hs.Start != 5 || len(hs.Lines) != 1 {
			t.Fatalf("%s 侧证据不符: %+v", side, hs)
		}
	}
}

// TestValidateMergedDispatch 验证类型校验分派（ADR-0009 §5）：
// toml→BurntSushi、json→标准库、其余纯文本不校验。
func TestValidateMergedDispatch(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
		wantErr bool
	}{
		{"合法 toml", "config/a.toml", "x = 1\n", false},
		{"残缺 toml", "config/a.toml", "x = [[[", true},
		{"pw.toml 同口径", "mods/sodium.pw.toml", "name = [[[", true},
		{"合法 json", "config/a.json", `{"x": 1}`, false},
		{"残缺 json", "config/a.json", `{"x":`, true},
		{"其余文本不校验", "config/b.ini", "完全不是 toml [[[", false},
		{"js 也不校验", "kubejs/s.js", "not json {{{", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMerged(tc.path, []byte(tc.content))
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateMerged(%q) err=%v，期望 wantErr=%v", tc.path, err, tc.wantErr)
			}
		})
	}
}

// TestMergeableBlacklist 验证永不合并黑名单（ADR-0009 §5）：
// 二进制资源与 .index 元数据永不合并，其余文本默认可合并。
func TestMergeableBlacklist(t *testing.T) {
	cases := []struct {
		kind model.ResourceKind
		path string
		want bool
	}{
		{model.ResourceBinaryFile, "config/logo.dat", false},
		{model.ResourceTextFile, "mods/.index/sodium-0.6.5.jar.pw.toml", false},
		{model.ResourceTextFile, "mods/.index", false},
		{model.ResourceTextFile, "config/deep/.index/a.toml", false},
		{model.ResourceTextFile, "pack.toml", true},
		{model.ResourceMod, "mods/sodium.pw.toml", true},
		{model.ResourceTextFile, "config/options.ini", true},
	}
	for _, tc := range cases {
		if got := Mergeable(tc.kind, tc.path); got != tc.want {
			t.Fatalf("Mergeable(%s, %q) = %v，期望 %v", tc.kind, tc.path, got, tc.want)
		}
	}
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
