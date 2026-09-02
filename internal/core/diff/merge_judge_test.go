package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"packgradle/internal/core/model"
)

// ---- 合并判定真值表（票 #87；验收规格 §3.1 七行全格）----
//
// 表驱动纯函数全格覆盖是外部契约的一部分（规格 Testing 节口径）：
// 三侧全文经注入的 MergeSources 提供（内存闭包，sha256 互验），
// converged/delete_modify 行同时断言「不走合并」。

// sumText 计算文本的 sha256（十六进制），供指纹互验的观察构造。
func sumText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// mergeFileObs 构造携带真实 sha256 内容指纹的文本资源观察。
func mergeFileObs(id model.ResourceID, relPath string, kind model.ResourceKind, text string) model.ResourceObservation {
	return model.ResourceObservation{
		ResourceID: id,
		Kind:       kind,
		Representation: model.Representation{
			RelativePath: relPath,
			Format:       "toml",
			Content:      &model.ContentRef{Algorithm: "sha256", Digest: sumText(text), Size: int64(len(text))},
		},
	}
}

// mergeBaseRep 构造基线表示（真实指纹）。
func mergeBaseRep(relPath string, text string) *model.Representation {
	return &model.Representation{
		RelativePath: relPath,
		Format:       "toml",
		Content:      &model.ContentRef{Algorithm: "sha256", Digest: sumText(text), Size: int64(len(text))},
	}
}

// mergeSourcesStub 以内存映射提供三侧全文，并记录取数调用（黑名单断言用）。
type mergeSourcesStub struct {
	base, project, runtime map[string]string
	called                 int
}

func (s *mergeSourcesStub) sources() *MergeSources {
	return &MergeSources{
		Base:    func(digest string) ([]byte, error) { s.called++; return []byte(s.base[digest]), nil },
		Project: func(rel string) ([]byte, error) { s.called++; return []byte(s.project[rel]), nil },
		Runtime: func(rel string) ([]byte, error) { s.called++; return []byte(s.runtime[rel]), nil },
	}
}

// 真值表夹具：同一 toml 的三侧全文。
const (
	mttBase    = "[graphics]\nfancy = false\n\n[audio]\nvolume = 0.5\n"
	mttProject = "[graphics]\nfancy = true\n\n[audio]\nvolume = 0.5\n"
	mttRuntime = "[graphics]\nfancy = false\n\n[audio]\nvolume = 1.0\n"
	// 真冲突：双侧同改同一行。
	mttConflictP = "[graphics]\nfancy = true\n\n[audio]\nvolume = 0.5\n"
	mttConflictR = "[graphics]\nfancy = \"ultra\"\n\n[audio]\nvolume = 0.5\n"
	// 校验失败：干净合并（零冲突块）但产物 toml 残缺。
	mttBrokenP = "[graphics]\nfancy = false\nbroken = [[[\n\n[audio]\nvolume = 0.5\n"
	mttBrokenR = "[graphics]\nfancy = false\n\n[audio]\nvolume = 0.5\nextra = 1\n"
)

// TestThreeWayMergeTruthTable 双侧同改合并面真值表（验收规格 §3.1 七行全格）。
func TestThreeWayMergeTruthTable(t *testing.T) {
	const id = "file:config.toml"
	const relPath = "config/config.toml"
	cases := []struct {
		name          string
		baseText      string
		projectText   string // "" 表示该侧删除
		runtimeText   string
		kind          model.ResourceKind
		relPath       string
		want          Classification
		wantConflict  model.ConflictKind // "" 表示无冲突
		wantDetail    bool               // conflict_modify 行是否应带 hunk JSON 证据
		wantMergeCall bool               // 是否允许触发合并取数（converged/黑名单行为 false）
	}{
		{
			name:     "1 同改不重叠且类型校验通过 merged_clean",
			baseText: mttBase, projectText: mttProject, runtimeText: mttRuntime,
			kind: model.ResourceTextFile, relPath: relPath,
			want: ClassMergedClean, wantMergeCall: true,
		},
		{
			name:     "2 同改重叠真冲突 conflict_modify 且带块证据",
			baseText: mttBase, projectText: mttConflictP, runtimeText: mttConflictR,
			kind: model.ResourceTextFile, relPath: relPath,
			want: ClassConflictModify, wantConflict: model.ConflictModifyModify,
			wantDetail: true, wantMergeCall: true,
		},
		{
			name:     "3 合并结果类型校验失败降级 conflict_modify",
			baseText: mttBase, projectText: mttBrokenP, runtimeText: mttBrokenR,
			kind: model.ResourceTextFile, relPath: relPath,
			want: ClassConflictModify, wantConflict: model.ConflictModifyModify,
			wantMergeCall: true,
		},
		{
			name:     "4 双侧 digest 相等 converged 不走合并",
			baseText: mttBase, projectText: mttProject, runtimeText: mttProject,
			kind: model.ResourceTextFile, relPath: relPath,
			want: ClassConverged, wantMergeCall: false,
		},
		{
			name:     "5 一边删一边改 conflict_delete_modify 维持选侧",
			baseText: mttBase, projectText: "", runtimeText: mttRuntime,
			kind: model.ResourceTextFile, relPath: relPath,
			want: ClassConflictDeleteModify, wantConflict: model.ConflictDeleteModify,
			wantMergeCall: false,
		},
		{
			name:     "6 二进制资源永不合并（黑名单）",
			baseText: mttBase, projectText: mttProject, runtimeText: mttRuntime,
			kind: model.ResourceBinaryFile, relPath: "config/logo.dat",
			want: ClassConflictModify, wantConflict: model.ConflictModifyModify,
			wantMergeCall: false,
		},
		{
			name:     "7 .index 永不合并（黑名单+只读）",
			baseText: mttBase, projectText: mttProject, runtimeText: mttRuntime,
			kind: model.ResourceTextFile, relPath: "mods/.index/sodium.pw.toml",
			want: ClassConflictModify, wantConflict: model.ConflictModifyModify,
			wantMergeCall: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseRep := mergeBaseRep(tc.relPath, tc.baseText)
			base := baseline(baseEntry{id: id, project: baseRep, runtime: baseRep})
			var proj, rt []model.ResourceObservation
			if tc.projectText != "" {
				proj = append(proj, mergeFileObs(id, tc.relPath, tc.kind, tc.projectText))
			}
			if tc.runtimeText != "" {
				rt = append(rt, mergeFileObs(id, tc.relPath, tc.kind, tc.runtimeText))
			}
			stub := &mergeSourcesStub{
				base:    map[string]string{sumText(tc.baseText): tc.baseText},
				project: map[string]string{tc.relPath: tc.projectText},
				runtime: map[string]string{tc.relPath: tc.runtimeText},
			}
			res, err := ThreeWay(Input{
				RelationID: "rel_test",
				Base:       base,
				Project:    snapshot(model.SideProject, proj...),
				Runtime:    snapshot(model.SideRuntime, rt...),
				Merge:      stub.sources(),
			})
			if err != nil {
				t.Fatalf("ThreeWay 报错: %v", err)
			}
			if len(res.Diffs) != 1 {
				t.Fatalf("期望 1 条 diff，得到 %d", len(res.Diffs))
			}
			if got := res.Diffs[0].Classification; got != tc.want {
				t.Fatalf("分类 = %q，期望 %q", got, tc.want)
			}
			if tc.wantConflict == "" {
				if len(res.Conflicts) != 0 {
					t.Fatalf("期望无冲突，得到 %d 条", len(res.Conflicts))
				}
			} else if len(res.Conflicts) != 1 || res.Conflicts[0].Kind != tc.wantConflict {
				t.Fatalf("冲突不符合期望: %+v", res.Conflicts)
			}
			// 合并取数调用边界：converged 在 diff 层先行拦截、黑名单永不取数。
			if tc.wantMergeCall && stub.called == 0 {
				t.Fatal("期望触发合并取数，但取数闭包未被调用")
			}
			if !tc.wantMergeCall && stub.called != 0 {
				t.Fatalf("不应触发合并取数，实际调用 %d 次", stub.called)
			}
			// 冲突块证据断言（hunk JSON 定形，契约 07 §3.3）。
			if tc.wantDetail {
				detail := res.Conflicts[0].Detail
				if detail == "" {
					t.Fatal("conflict_modify 行应携带 hunk JSON 证据")
				}
				assertHunkDetail(t, detail, map[string]string{
					"project": tc.projectText, "base": tc.baseText, "runtime": tc.runtimeText,
				})
			}
		})
	}
}

// hunkSideShape 是 hunk JSON 单侧的定形（契约 07 §3.3：{start,lines}）。
type hunkSideShape struct {
	Start int      `json:"start"`
	Lines []string `json:"lines"`
}

// assertHunkDetail 断言 detail 为定形 hunk JSON：顶层仅 hunks 键、块内域词汇
// project/base/runtime、起始行号 1 起始、行片段与对应侧输入逐字节一致。
func assertHunkDetail(t *testing.T, detail string, sideTexts map[string]string) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(detail), &top); err != nil {
		t.Fatalf("detail 非定形 hunk JSON: %v\n%s", err, detail)
	}
	if len(top) != 1 || top["hunks"] == nil {
		t.Fatalf("顶层应仅有 hunks 键: %s", detail)
	}
	var hunks []map[string]hunkSideShape
	if err := json.Unmarshal(top["hunks"], &hunks); err != nil {
		t.Fatalf("hunks 形状不符: %v\n%s", err, detail)
	}
	if len(hunks) == 0 {
		t.Fatalf("hunks 数组为空: %s", detail)
	}
	for _, h := range hunks {
		for _, side := range []string{"project", "base", "runtime"} {
			hs, ok := h[side]
			if !ok {
				t.Fatalf("冲突块缺 %s 侧: %s", side, detail)
			}
			if hs.Start < 1 {
				t.Fatalf("%s 侧起始行号应为 1 起始正数: %d", side, hs.Start)
			}
			if len(hs.Lines) == 0 {
				t.Fatalf("%s 侧行片段为空", side)
			}
			// 行片段必须与对应侧全文在 start 处逐字节一致（证据可信）。
			text := sideTexts[side]
			lines := strings.Split(text, "\n")
			end := hs.Start - 1 + len(hs.Lines)
			if end > len(lines) {
				t.Fatalf("%s 侧片段越界: start=%d lines=%d", side, hs.Start, len(hs.Lines))
			}
			for i, l := range hs.Lines {
				if l != lines[hs.Start-1+i] {
					t.Fatalf("%s 侧片段与输入不符（第 %d 行）: %q vs %q", side, hs.Start+i, l, lines[hs.Start-1+i])
				}
			}
		}
	}
}

// TestThreeWayMergeUnavailableKeepsConflict 验证合并面不可用（未注入）时
// 双侧同改维持 conflict_modify 现状（零行为回退的底线）。
func TestThreeWayMergeUnavailableKeepsConflict(t *testing.T) {
	const id = "file:config.toml"
	baseRep := mergeBaseRep("config/config.toml", mttBase)
	res, err := ThreeWay(Input{
		Base:    baseline(baseEntry{id: id, project: baseRep, runtime: baseRep}),
		Project: snapshot(model.SideProject, mergeFileObs(id, "config/config.toml", model.ResourceTextFile, mttProject)),
		Runtime: snapshot(model.SideRuntime, mergeFileObs(id, "config/config.toml", model.ResourceTextFile, mttRuntime)),
	})
	if err != nil {
		t.Fatalf("ThreeWay 报错: %v", err)
	}
	if res.Diffs[0].Classification != ClassConflictModify {
		t.Fatalf("分类 = %q，期望 conflict_modify（合并面未注入）", res.Diffs[0].Classification)
	}
}

// TestThreeWayMergeDigestMismatchSkips 验证取数字节与快照指纹不符（扫描后
// 外部写者竞态）时逐资源跳过合并，维持 conflict_modify。
func TestThreeWayMergeDigestMismatchSkips(t *testing.T) {
	const id = "file:config.toml"
	baseRep := mergeBaseRep("config/config.toml", mttBase)
	stub := &mergeSourcesStub{
		base:    map[string]string{sumText(mttBase): mttBase},
		project: map[string]string{"config/config.toml": "外部写者改过的内容\n"},
		runtime: map[string]string{"config/config.toml": mttRuntime},
	}
	res, err := ThreeWay(Input{
		Base:    baseline(baseEntry{id: id, project: baseRep, runtime: baseRep}),
		Project: snapshot(model.SideProject, mergeFileObs(id, "config/config.toml", model.ResourceTextFile, mttProject)),
		Runtime: snapshot(model.SideRuntime, mergeFileObs(id, "config/config.toml", model.ResourceTextFile, mttRuntime)),
		Merge:   stub.sources(),
	})
	if err != nil {
		t.Fatalf("ThreeWay 报错: %v", err)
	}
	if res.Diffs[0].Classification != ClassConflictModify {
		t.Fatalf("分类 = %q，期望 conflict_modify（指纹失配不合并）", res.Diffs[0].Classification)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].Detail != "" {
		t.Fatalf("指纹失配降级不应带块证据: %+v", res.Conflicts)
	}
}
