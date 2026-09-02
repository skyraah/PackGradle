package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"packgradle/internal/core/diff"
	"packgradle/internal/core/model"
)

// 票 #87：方向通道与 hunk JSON 证据共存（Conflict.Detail 复用，契约 07 §3.3）。
// bidirectional 时 detail 保持纯 hunk JSON；方向性规则时 direction 以兄弟键
// 并入，不覆盖块证据；detailDirection 兼容纯文本与 JSON 两种形态。

func TestWithDirectionDetailPreservesHunks(t *testing.T) {
	hunks := `{"hunks":[{"project":{"start":5,"lines":["a"]},"base":{"start":5,"lines":["b"]},"runtime":{"start":5,"lines":["c"]}}]}`

	t.Run("bidirectional 保留纯 hunk JSON", func(t *testing.T) {
		if got := withDirectionDetail(hunks, directionBidirectional); got != hunks {
			t.Fatalf("detail 被改写: %s", got)
		}
	})
	t.Run("方向性规则以兄弟键并入", func(t *testing.T) {
		got := withDirectionDetail(hunks, directionProjectToRuntime)
		var obj struct {
			Direction string `json:"direction"`
			Hunks     json.RawMessage
		}
		if err := json.Unmarshal([]byte(got), &obj); err != nil {
			t.Fatalf("detail 非 JSON: %v", err)
		}
		if obj.Direction != directionProjectToRuntime {
			t.Fatalf("direction = %q", obj.Direction)
		}
		if detailDirection(got) != directionProjectToRuntime {
			t.Fatalf("detailDirection 反解失败: %s", got)
		}
	})
	t.Run("空 detail 走既有纯文本形态", func(t *testing.T) {
		if got := withDirectionDetail("", directionRuntimeToProject); got != "direction=runtime_to_project" {
			t.Fatalf("detail = %q", got)
		}
		if detailDirection("direction=runtime_to_project") != directionRuntimeToProject {
			t.Fatal("纯文本形态反解失败")
		}
	})
	t.Run("非 JSON 形态原样保留", func(t *testing.T) {
		const legacy = "direction=project_to_runtime"
		if got := withDirectionDetail(legacy, directionRuntimeToProject); got != legacy {
			t.Fatalf("detail = %q", got)
		}
	})
}

// TestBuildDraftMergedCleanSummary 验证 merged_clean 行只进计数：无操作、
// 无冲突；Resolve 生成的新计划保留该计数。
func TestBuildDraftMergedCleanSummary(t *testing.T) {
	const id = "file:config/a.toml"
	// 互不重叠的两侧改动（真实 sha256 指纹 + 内存取数闭包 → merged_clean）。
	baseText := "[a]\nk1 = 0\nmid = 0\nk2 = 0\n"
	projText := "[a]\nk1 = 1\nmid = 0\nk2 = 0\n"
	rtText := "[a]\nk1 = 0\nmid = 0\nk2 = 2\n"
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	obs := func(d string) model.ResourceObservation {
		return model.ResourceObservation{
			ResourceID: model.ResourceID(id), Kind: model.ResourceTextFile,
			Representation: model.Representation{
				RelativePath: "config/a.toml", Format: "toml",
				Content: &model.ContentRef{Algorithm: "sha256", Digest: d, Size: int64(len(d))},
			},
		}
	}
	rep := obs(sum(baseText)).Representation // 基线指纹 = 基线全文实测 sha256（取数互验的前提）
	base := &model.SyncBaseline{
		SchemaVersion: model.CurrentSchemaVersion, BaselineID: "base_t", RelationID: "rel_t",
		Resources: map[model.ResourceID]model.BaselineResource{
			model.ResourceID(id): {State: "present", ProjectRepresentation: &rep, RuntimeRepresentation: &rep},
		},
	}
	snap := func(d string) model.ObservedSnapshot {
		return model.ObservedSnapshot{SchemaVersion: 1, Resources: map[model.ResourceID]model.ResourceObservation{
			model.ResourceID(id): obs(d),
		}}
	}
	mergeSrc := &diff.MergeSources{
		Base:    func(string) ([]byte, error) { return []byte(baseText), nil },
		Project: func(string) ([]byte, error) { return []byte(projText), nil },
		Runtime: func(string) ([]byte, error) { return []byte(rtText), nil },
	}
	in := BuildInput{
		RelationID: "rel_t", RelationRevision: 1,
		Policy: model.MappingPolicy{SchemaVersion: 1, PolicyID: "p", Revision: 1},
		Base:   base, BaseBaselineDigest: "sha256:base",
		Project: func() model.ObservedSnapshot {
			s := snap(sum(projText))
			s.Resources[model.ResourceID(id)] = obs(sum(projText))
			return s
		}(),
		Runtime: func() model.ObservedSnapshot {
			s := snap(sum(rtText))
			s.Resources[model.ResourceID(id)] = obs(sum(rtText))
			return s
		}(),
		Merge: mergeSrc,
	}
	// 指纹互验要求观察 Content 摘要与真实字节一致
	in.Project.Resources[model.ResourceID(id)] = obs(sum(projText))
	in.Runtime.Resources[model.ResourceID(id)] = obs(sum(rtText))

	draft, err := BuildDraft(in)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Summary.MergedCleanCount != 1 {
		t.Fatalf("merged_clean_count = %d，期望 1（summary=%+v）", draft.Summary.MergedCleanCount, draft.Summary)
	}
	if draft.Summary.ConflictCount != 0 || len(draft.Conflicts) != 0 || len(draft.Operations) != 0 {
		t.Fatalf("merged_clean 行不得有冲突/操作: %+v", draft.Summary)
	}
	if draft.Summary.ModifyCount != 0 {
		t.Fatalf("merged_clean 不并入 modify 计数: %+v", draft.Summary)
	}

	// Resolve：合并行无冲突需决议，零决议直接出 resolved 计划并保留计数
	resolved, err := Resolve(draft, in.Project, in.Runtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Summary.MergedCleanCount != 1 {
		t.Fatalf("Resolve 未保留 merged_clean_count: %+v", resolved.Summary)
	}
	// 分类词表锁词（GetChanges 筛选枚举由 application 层引用同一常量）
	if diff.ClassMergedClean != "merged_clean" {
		t.Fatalf("分类词表漂移: %s", diff.ClassMergedClean)
	}
}
