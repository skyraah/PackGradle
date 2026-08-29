package transport

import (
	"encoding/json"
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

// TestEndpointDTOConversion 验证端点候选/健康 DTO 转换：字段直映、
// 空结果 slice 归一为 []（不得 marshal 成 null）。
func TestEndpointDTOConversion(t *testing.T) {
	cand := projectCandidateDTO(view.ProjectCandidateView{
		DisplayName: "Collapse Pack", RootPath: "C:/packs/Collapse",
		PackTomlPath: "C:/packs/Collapse/pack.toml",
		Minecraft:    "1.20.1", Modloader: "fabric",
		Registered: true, EndpointID: "prj_1",
	})
	if cand.DisplayName != "Collapse Pack" || cand.EndpointID != "prj_1" || !cand.Registered {
		t.Fatalf("候选字段映射: %+v", cand)
	}
	rc := runtimeCandidateDTO(view.RuntimeCandidateView{
		InstanceID: "Collapse", DisplayName: "Collapse", GameDir: "C:/inst/Collapse/minecraft",
	})
	if rc.InstanceID != "Collapse" || rc.GameDir == "" || rc.Registered {
		t.Fatalf("runtime 候选字段映射: %+v", rc)
	}
	h := endpointHealthDTO(view.EndpointHealthView{
		EndpointID: "prj_1", Status: "ok", PathExists: true, FingerprintMatches: true, CheckedAt: "2026-01-01T00:00:00Z",
	})
	if h.Status != "ok" || !h.PathExists || !h.FingerprintMatches {
		t.Fatalf("健康字段映射: %+v", h)
	}

	// 空发现结果必须序列化为 [] 而非 null
	emptyProjects, _ := json.Marshal([]ProjectCandidateDTO{})
	if string(emptyProjects) != "[]" {
		t.Fatalf("空候选应序列化为 []: %s", emptyProjects)
	}
}

// TestWorkspaceFeaturesAvailabilityDTO 验证 features/availability 内嵌 WorkspaceDTO
//（契约 03 §2.1）：availability 归一为空数组、reason_args 归一、materialization_modes 归一。
func TestWorkspaceFeaturesAvailabilityDTO(t *testing.T) {
	v := view.WorkspaceView{
		SchemaVersion: model.CurrentSchemaVersion,
		Features: view.WorkspaceFeaturesView{
			Scan: true, SyncPreview: true, ConflictInspection: true,
			ConflictResolution: "choose_side", MaterializationModes: []string{},
		},
		Availability: []view.ActionAvailabilityView{
			{Action: "scan", Available: false, ReasonCode: "err.scan.already_running"},
			{Action: "prepare_sync", Available: true},
		},
	}
	dto := workspaceDTO(v)
	if !dto.Features.Scan || !dto.Features.SyncPreview || !dto.Features.ConflictInspection {
		t.Errorf("features 布尔位错误: %+v", dto.Features)
	}
	if dto.Features.ConflictResolution != "choose_side" || len(dto.Features.MaterializationModes) != 0 {
		t.Errorf("features 固定值错误: %+v", dto.Features)
	}
	if len(dto.Availability) != 2 {
		t.Fatalf("availability 应 2 条: %+v", dto.Availability)
	}
	if dto.Availability[0].ReasonCode != "err.scan.already_running" || len(dto.Availability[0].ReasonArgs) != 0 {
		t.Errorf("availability 原因码/args 归一错误: %+v", dto.Availability[0])
	}
	if dto.Availability[1].Available != true || dto.Availability[1].ReasonCode != "" {
		t.Errorf("可用动作不应带原因码: %+v", dto.Availability[1])
	}

	// 空availability 归一为 []（非 null）
	empty := workspaceDTO(view.WorkspaceView{SchemaVersion: model.CurrentSchemaVersion})
	if empty.Availability == nil || len(empty.Availability) != 0 {
		t.Errorf("availability 应归一为空数组: %v", empty.Availability)
	}
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || !json.Valid(b) {
		t.Fatalf("序列化失败: %s", b)
	}
}

// TestPolicyViewDTO 验证映射策略读写投影（票 #20）：RelationRevision 随读写
// 视图透出（乐观锁取值来源）、预检投影 policyDTO 不携带该字段、规则 DTO 与
// model 往返一致。
func TestPolicyViewDTO(t *testing.T) {
	v := view.PolicyView{
		SchemaVersion:  model.CurrentSchemaVersion,
		PolicyID:       "default-v1",
		PolicyRevision: 1,
		Rules: []model.MappingRule{
			{ID: "mods", ResourceKind: "mod", ProjectPrefix: "mods", RuntimePrefix: "mods",
				Direction: "bidirectional", Materialization: "copy", MergePolicy: "packwiz", RuntimeLocalPolicy: "exclude"},
		},
		RelationRevision: 3,
	}
	dto := policyViewDTO(v)
	if dto.RelationRevision != 3 {
		t.Errorf("读写投影应携带 relation_revision: %+v", dto)
	}
	if len(dto.Rules) != 1 || dto.Rules[0].ID != "mods" || dto.PolicyID != "default-v1" || dto.Revision != 1 {
		t.Errorf("策略本体投影错误: %+v", dto)
	}
	if dto.SchemaVersion != model.CurrentSchemaVersion {
		t.Errorf("schema_version 错误: %d", dto.SchemaVersion)
	}

	// 预检投影（policyDTO）不带 relation_revision（omitempty 恒 0）
	prep := policyDTO(model.MappingPolicy{SchemaVersion: 1, PolicyID: "default-v1", Revision: 1})
	if prep.RelationRevision != 0 {
		t.Errorf("预检投影 relation_revision 应为 0: %+v", prep)
	}

	// 规则 DTO → model 往返
	rm := mappingRuleModel(MappingRuleDTO{
		ID: "config", ResourceKind: "text_file", ProjectPrefix: "config", RuntimePrefix: "config",
		Include: []string{}, Exclude: []string{"*.log"},
		Direction: "project_to_runtime", Materialization: "copy",
		MergePolicy: "manual", RuntimeLocalPolicy: "report",
	})
	if rm.ID != "config" || rm.Include == nil || len(rm.Exclude) != 1 || rm.Exclude[0] != "*.log" ||
		rm.Direction != "project_to_runtime" || rm.RuntimeLocalPolicy != "report" {
		t.Errorf("规则还原错误: %+v", rm)
	}
}
