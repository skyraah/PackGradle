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
