package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// TestBuildDraftMergedCleanSummary 验证 merged_clean 行的计划面（票 #93 起
// 含操作面）：draft 以 write_merged 操作承载默认推荐（无冲突、不并入 modify
// 计数、reversible、双端前置条件），Resolve 零决议保留计数与操作；
// take_merged 决议矩阵（契约 07 §3.3：只对 merged 行合法）。
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
	if draft.Summary.ConflictCount != 0 || len(draft.Conflicts) != 0 {
		t.Fatalf("merged_clean 行不得有冲突: %+v", draft.Summary)
	}
	if draft.Summary.ModifyCount != 0 {
		t.Fatalf("merged_clean 不并入 modify 计数: %+v", draft.Summary)
	}
	// 操作面（票 #93）：draft 即以 write_merged 承载默认推荐——一资源一操作、
	// reversible=true、双端前置条件（present + 期望摘要）。
	if len(draft.Operations) != 1 {
		t.Fatalf("merged_clean 行应有 1 条 write_merged 操作: %+v", draft.Operations)
	}
	op := draft.Operations[0]
	if op.Kind != model.OpWriteMerged || op.ResourceID != model.ResourceID(id) || !op.Reversible {
		t.Fatalf("write_merged 操作面不符: %+v", op)
	}
	sides := map[string]model.Precondition{}
	for _, pc := range op.Preconditions {
		sides[pc.Side] = pc
	}
	for _, side := range []string{"project", "runtime"} {
		pc, ok := sides[side]
		if !ok || pc.Existence != "present" || pc.Expected == nil || pc.Expected.Digest == "" {
			t.Fatalf("write_merged 双端前置条件不符（%s）: %+v", side, op.Preconditions)
		}
	}
	wantProjectDigest := sum(projText)
	if sides["project"].Expected.Digest != wantProjectDigest {
		t.Fatalf("project 侧期望摘要与计划快照不符: %s", sides["project"].Expected.Digest)
	}

	// Resolve：合并行无冲突需决议，零决议（默认推荐）直接出 resolved 计划，
	// 保留计数与操作；确认要求为空（非冲突操作免确认，授权模式批量执行）。
	resolved, err := Resolve(draft, in.Project, in.Runtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Summary.MergedCleanCount != 1 {
		t.Fatalf("Resolve 未保留 merged_clean_count: %+v", resolved.Summary)
	}
	if len(resolved.Operations) != 1 || resolved.Operations[0].Kind != model.OpWriteMerged {
		t.Fatalf("Resolve 后 write_merged 操作不符: %+v", resolved.Operations)
	}
	if len(resolved.ConfirmationRequirements) != 0 {
		t.Fatalf("write_merged 不得产生确认要求: %+v", resolved.ConfirmationRequirements)
	}
	// 显式 take_merged：合法、记录进决议、操作面不变。
	resolvedExplicit, err := Resolve(draft, in.Project, in.Runtime, []model.Resolution{
		{ResourceID: model.ResourceID(id), Choice: model.ChoiceTakeMerged},
	})
	if err != nil {
		t.Fatalf("take_merged 于 merged 行被拒: %v", err)
	}
	if len(resolvedExplicit.Resolutions) != 1 || resolvedExplicit.Resolutions[0].Choice != model.ChoiceTakeMerged {
		t.Fatalf("take_merged 决议未记录: %+v", resolvedExplicit.Resolutions)
	}
	if len(resolvedExplicit.Operations) != 1 || resolvedExplicit.Operations[0].Kind != model.OpWriteMerged {
		t.Fatalf("take_merged 不得改变操作面: %+v", resolvedExplicit.Operations)
	}
	// take_merged 作用于非 merged 行（未知资源）→ ErrResolutionInvalidChoice
	//（应用层透传 err.plan.resolution_invalid）。
	if _, err := Resolve(draft, in.Project, in.Runtime, []model.Resolution{
		{ResourceID: "file:config/other.toml", Choice: model.ChoiceTakeMerged},
	}); err == nil || !errors.Is(err, ErrResolutionInvalidChoice) {
		t.Fatalf("take_merged 于非 merged 行应拒绝: %v", err)
	}
	// 其余选择作用于 merged 行（非冲突资源）→ ErrResolutionUnknown 同口径拒绝。
	if _, err := Resolve(draft, in.Project, in.Runtime, []model.Resolution{
		{ResourceID: model.ResourceID(id), Choice: model.ChoiceTakeProject},
	}); err == nil {
		t.Fatal("take_project 于 merged 行应拒绝")
	}
	// 分类词表锁词（GetChanges 筛选枚举由 application 层引用同一常量）
	if diff.ClassMergedClean != "merged_clean" {
		t.Fatalf("分类词表漂移: %s", diff.ClassMergedClean)
	}
	if string(model.OpWriteMerged) != "write_merged" || string(model.ChoiceTakeMerged) != "take_merged" {
		t.Fatal("操作/决议词表漂移")
	}
}
