package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// 票 #87（契约 07 §3.3）：merged_clean_count 进 PlanSummaryDTO/ChangesSummaryDTO
// （DTO 只增不删；不并入 modify 计数），hunk JSON 证据随 ConflictDTO.Detail 直映。
func TestMergedCleanCountProjection(t *testing.T) {
	plan := planDTO(view.SyncPlanView{
		SchemaVersion: model.CurrentSchemaVersion, PlanID: "plan_1", RelationID: "rel_1",
		Summary: model.PlanSummary{
			ResourceTotal: 3, ModifyCount: 1, ConflictCount: 1, MergedCleanCount: 2,
		},
		Conflicts: []model.Conflict{{
			ResourceID: "file:config/a.toml", Kind: model.ConflictModifyModify,
			Detail: `{"hunks":[{"project":{"start":5,"lines":["x"]},"base":{"start":5,"lines":["y"]},"runtime":{"start":5,"lines":["z"]}}]}`,
		}},
	})
	if plan.Summary.MergedCleanCount != 2 {
		t.Fatalf("PlanSummaryDTO.merged_clean_count = %d，期望 2", plan.Summary.MergedCleanCount)
	}
	if plan.Summary.ModifyCount != 1 {
		t.Fatalf("modify 计数不得被合并行污染: %+v", plan.Summary)
	}
	if plan.Conflicts[0].Detail == "" {
		t.Fatal("ConflictDTO.Detail 未直映 hunk JSON")
	}
	// wire 形状锁定：字段名与 JSON 序列化稳定（前端消费面）。
	b, _ := json.Marshal(plan.Summary)
	if !strings.Contains(string(b), `"merged_clean_count":2`) {
		t.Fatalf("wire 字段名漂移: %s", b)
	}

	changes := changesDTO(view.ChangesPage{
		SchemaVersion: model.CurrentSchemaVersion,
		Items:         []view.ChangeView{},
		Summary: view.ChangesSummary{
			Total: 2, MergedCleanCount: 1,
		},
	})
	if changes.Summary.MergedCleanCount != 1 {
		t.Fatalf("ChangesSummaryDTO.merged_clean_count = %d，期望 1", changes.Summary.MergedCleanCount)
	}
	b, _ = json.Marshal(changes.Summary)
	if !strings.Contains(string(b), `"merged_clean_count":1`) {
		t.Fatalf("wire 字段名漂移: %s", b)
	}
}

// 票 #93（契约 07 §3.3）：write_merged 操作与 take_merged 决议经 OperationDTO/
// ResolutionDTO 直映（Kind 即内容源分派；reversible=true；枚举词表锁词）。
func TestWriteMergedOperationProjection(t *testing.T) {
	plan := planDTO(view.SyncPlanView{
		SchemaVersion: model.CurrentSchemaVersion, PlanID: "plan_1", RelationID: "rel_1",
		Operations: []model.PlannedOperation{{
			ID: "op_0001", Kind: model.OpWriteMerged, ResourceID: "file:config/a.toml",
			Reversible: true,
			Preconditions: []model.Precondition{
				{ResourceID: "file:config/a.toml", Side: "project", Existence: "present"},
				{ResourceID: "file:config/a.toml", Side: "runtime", Existence: "present"},
			},
		}},
		Resolutions: []model.Resolution{{ResourceID: "file:config/a.toml", Choice: model.ChoiceTakeMerged}},
	})
	if len(plan.Operations) != 1 {
		t.Fatalf("operations = %d", len(plan.Operations))
	}
	op := plan.Operations[0]
	if op.Kind != "write_merged" || !op.Reversible || op.ID != "op_0001" {
		t.Fatalf("write_merged 投影不符: %+v", op)
	}
	if len(op.Preconditions) != 2 {
		t.Fatalf("双端前置条件投影: %+v", op.Preconditions)
	}
	if len(plan.Resolutions) != 1 || plan.Resolutions[0].Choice != "take_merged" {
		t.Fatalf("take_merged 决议投影: %+v", plan.Resolutions)
	}
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"write_merged"`) || !strings.Contains(string(b), `"take_merged"`) {
		t.Fatalf("wire 词表漂移: %s", b)
	}
	// 词表锁词（GetChanges 筛选/前端徽标引用同一字面）。
	if string(model.OpWriteMerged) != "write_merged" || string(model.ChoiceTakeMerged) != "take_merged" {
		t.Fatal("枚举词表漂移")
	}
}
