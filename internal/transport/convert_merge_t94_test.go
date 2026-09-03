package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// 票 #94（契约 07 §3.4）：GetMergedPreview 的缝① convert 单测——
// MergedPreviewDTO 投影两段全文直映 + schema_version 由投影补齐，
// wire 字段名与 JSON 序列化形状锁定（前端预览抽屉消费面）。
func TestMergedPreviewProjection(t *testing.T) {
	dto := mergedPreviewDTO(view.MergedPreviewView{
		PlanID:       "plan_1",
		ResourceID:   "file:config/handmade.toml",
		RelativePath: "config/handmade.toml",
		Content:      "merged\ntext\n",
		BaseContent:  "base\ntext\n",
	})
	if dto.SchemaVersion != model.CurrentSchemaVersion {
		t.Fatalf("schema_version = %d，期望 %d（投影补齐）", dto.SchemaVersion, model.CurrentSchemaVersion)
	}
	if dto.PlanID != "plan_1" || dto.ResourceID != "file:config/handmade.toml" {
		t.Fatalf("标识直映不符: %+v", dto)
	}
	if dto.RelativePath != "config/handmade.toml" {
		t.Fatalf("relative_path 直映不符: %+v", dto)
	}
	if dto.Content != "merged\ntext\n" || dto.BaseContent != "base\ntext\n" {
		t.Fatalf("两段全文直映不符: content=%q base=%q", dto.Content, dto.BaseContent)
	}
	// wire 形状锁定：字段名稳定（契约 07 §3.4 的 JSON 键逐一在场）。
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"schema_version":`, `"plan_id":`, `"resource_id":`,
		`"relative_path":`, `"content":`, `"base_content":`,
	} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("wire 字段漂移，缺 %s: %s", key, b)
		}
	}
}
