package transport

import (
	"testing"

	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// TestPlanDiagnosticsDTO 验证计划诊断（含 mapping_collision 证据）进入 DTO，
// 且空诊断归一为空数组。
func TestPlanDiagnosticsDTO(t *testing.T) {
	v := view.SyncPlanView{
		SchemaVersion: model.CurrentSchemaVersion,
		PlanID:        "plan_1",
		Diagnostics: []model.Diagnostic{
			{
				Severity: "warning", Code: "diag.mapping.collision",
				Args: []string{"aaa", "zzz"}, RelativePath: "config/notes.txt",
				ResourceID: "file:config/notes.txt",
			},
		},
	}
	dto := planDTO(v)
	if len(dto.Diagnostics) != 1 {
		t.Fatalf("DTO 应携带 1 条诊断, got %d", len(dto.Diagnostics))
	}
	d := dto.Diagnostics[0]
	if d.Code != "diag.mapping.collision" || d.Severity != "warning" {
		t.Errorf("诊断 code/severity 错误: %+v", d)
	}
	if len(d.Args) != 2 || d.Args[0] != "aaa" || d.Args[1] != "zzz" {
		t.Errorf("诊断 args 错误: %v", d.Args)
	}
	if d.RelativePath != "config/notes.txt" || d.ResourceID != "file:config/notes.txt" {
		t.Errorf("诊断证据路径/资源 ID 错误: %+v", d)
	}

	empty := planDTO(view.SyncPlanView{SchemaVersion: model.CurrentSchemaVersion})
	if empty.Diagnostics == nil || len(empty.Diagnostics) != 0 {
		t.Errorf("空诊断应归一为空数组, got %+v", empty.Diagnostics)
	}
}
