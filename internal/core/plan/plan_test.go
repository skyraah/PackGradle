package plan

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// content 构造固定 sha256 内容指纹。
func content(digest string) *model.ContentRef {
	return &model.ContentRef{Algorithm: "sha256", Digest: digest, Size: int64(len(digest))}
}

// fileObs 构造带 PolicyID 的 text_file 资源观察。
func fileObs(id, digest, policyID string) model.ResourceObservation {
	return model.ResourceObservation{
		ResourceID: model.ResourceID(id),
		Kind:       model.ResourceTextFile,
		Representation: model.Representation{
			RelativePath: "config/" + strings.TrimPrefix(id, "file:") + ".ini",
			Format:       "ini",
			Content:      content(digest),
		},
		PolicyID: policyID,
	}
}

// modObs 构造带 PolicyID 的 mod 资源观察（mod:jar: 前缀为低置信度身份）。
func modObs(id, version, declaredHash, policyID string) model.ResourceObservation {
	ident := normalize.IdentityFromResourceID(model.ResourceID(id))
	return model.ResourceObservation{
		ResourceID: model.ResourceID(id),
		Kind:       model.ResourceMod,
		Identity:   ident,
		Representation: model.Representation{
			RelativePath: "mods/" + ident.Key,
			Format:       "jar",
			Content:      content("actual-" + declaredHash),
			Metadata: map[string]string{
				model.MetaVersion:           version,
				model.MetaSide:              "both",
				model.MetaDeclaredHashAlgo:  "sha256",
				model.MetaDeclaredHashValue: declaredHash,
			},
		},
		PolicyID: policyID,
	}
}

// snapshot 构造最小 ObservedSnapshot（乱序插入 map，验证输出确定性）。
func snapshot(side model.Side, obs ...model.ResourceObservation) model.ObservedSnapshot {
	resources := make(map[model.ResourceID]model.ResourceObservation, len(obs))
	for _, o := range obs {
		resources[o.ResourceID] = o
	}
	return model.ObservedSnapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		SnapshotID:    "snap_" + string(side),
		RelationID:    "rel_test",
		Side:          side,
		Resources:     resources,
	}
}

// baseEntry 描述基线单资源的双侧表示。
type baseEntry struct {
	id               string
	project, runtime *model.Representation
}

// bothBase 构造双侧 present 且同指纹的基线条目。
func bothBase(id, digest string) baseEntry {
	rep := model.Representation{
		RelativePath: "config/" + strings.TrimPrefix(id, "file:") + ".ini",
		Format:       "ini",
		Content:      content(digest),
	}
	return baseEntry{id: id, project: &rep, runtime: &rep}
}

// baseline 构造最小 SyncBaseline。
func baseline(entries ...baseEntry) *model.SyncBaseline {
	resources := make(map[model.ResourceID]model.BaselineResource, len(entries))
	for _, e := range entries {
		state := "absent"
		if e.project != nil || e.runtime != nil {
			state = "present"
		}
		resources[model.ResourceID(e.id)] = model.BaselineResource{
			State:                 state,
			ProjectRepresentation: e.project,
			RuntimeRepresentation: e.runtime,
			Recoverability:        model.RecoverabilityNone,
		}
	}
	return &model.SyncBaseline{
		SchemaVersion: model.CurrentSchemaVersion,
		BaselineID:    "base_test",
		RelationID:    "rel_test",
		Resources:     resources,
	}
}

// buildInput 构造最小 BuildInput；base 为 nil 时 Kind 取 initialize。
func buildInput(base *model.SyncBaseline, project, runtime model.ObservedSnapshot, rules ...model.MappingRule) BuildInput {
	kind := model.PlanSync
	baseDigest := "sha256:base"
	if base == nil {
		kind = model.PlanInitialize
		baseDigest = ""
	}
	return BuildInput{
		RelationID:         "rel_test",
		RelationRevision:   3,
		Policy:             model.MappingPolicy{SchemaVersion: model.CurrentSchemaVersion, PolicyID: "default-v1", Revision: 1, Rules: rules},
		PolicyDigest:       "sha256:policy",
		Kind:               kind,
		Base:               base,
		BaseBaselineDigest: baseDigest,
		Project:            project,
		Runtime:            runtime,
		ExpectedBindings:   model.ExpectedBindings{Project: "fp-project", Runtime: "fp-runtime"},
		ExpiresAt:          time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
	}
}

// rule 构造指定方向的映射规则。
func rule(id, direction string) model.MappingRule {
	return model.MappingRule{ID: id, Direction: direction}
}

// findOp 按 ResourceID 查找操作。
func findOp(ops []model.PlannedOperation, id string) (model.PlannedOperation, bool) {
	for _, op := range ops {
		if string(op.ResourceID) == id {
			return op, true
		}
	}
	return model.PlannedOperation{}, false
}

// assertPrecondition 断言单条前置条件；expectedDigest 为 "" 表示期望无指纹。
func assertPrecondition(t *testing.T, pc model.Precondition, side, existence, expectedDigest string) {
	t.Helper()
	if pc.Side != side {
		t.Errorf("前置条件 side = %q，期望 %q", pc.Side, side)
	}
	if pc.Existence != existence {
		t.Errorf("前置条件(%s) existence = %q，期望 %q", side, pc.Existence, existence)
	}
	if expectedDigest == "" {
		if pc.Expected != nil {
			t.Errorf("前置条件(%s) 不应携带期望指纹: %+v", side, pc.Expected)
		}
		return
	}
	if pc.Expected == nil || pc.Expected.Digest != expectedDigest {
		t.Errorf("前置条件(%s) 期望指纹 = %+v，期望 digest %q", side, pc.Expected, expectedDigest)
	}
}

// TestBuildDraftDeterminism 验证相同输入两次构建产生相同 digest 与操作序列，
// 且与 map 插入顺序无关。
func TestBuildDraftDeterminism(t *testing.T) {
	const modID = "mod:modrinth:AANobbMI"
	base := baseline(bothBase("file:a", "h1"), bothBase("file:c", "h1"), bothBase("file:d", "h1"))
	proj := snapshot(model.SideProject,
		fileObs("file:a", "h2", ""),    // P 变 => write_runtime（modify）
		fileObs("file:b", "hb", ""),    // P 新增 => write_runtime（create）
		fileObs("file:c", "h1", ""),    // 未变
		fileObs("file:d", "h1", ""),    // 未变
		modObs(modID, "1.0", "hm", ""), // P 新增 mod => write_runtime（create）
	)
	rt := snapshot(model.SideRuntime,
		fileObs("file:a", "h1", ""), // 未变
		fileObs("file:c", "h2", ""), // R 变 => write_project（modify）
		// file:d 缺失 => R 删 => remove_project
	)

	p1, err := BuildDraft(buildInput(base, proj, rt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	p2, err := BuildDraft(buildInput(base, proj, rt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}

	if p1.PlanDigest == "" {
		t.Fatal("PlanDigest 不应为空")
	}
	if p1.PlanDigest != p2.PlanDigest {
		t.Errorf("相同输入的 PlanDigest 必须相等: %s vs %s", p1.PlanDigest, p2.PlanDigest)
	}
	if !reflect.DeepEqual(p1.Operations, p2.Operations) {
		t.Errorf("相同输入的操作序列必须完全一致:\n%+v\n%+v", p1.Operations, p2.Operations)
	}

	// 期望的操作排序与编号
	wantOps := []struct {
		id       string
		kind     model.OperationKind
		resource string
	}{
		{"op_0001", model.OpWriteRuntime, "file:a"},
		{"op_0002", model.OpWriteRuntime, "file:b"},
		{"op_0003", model.OpWriteRuntime, modID},
		{"op_0004", model.OpWriteProject, "file:c"},
		{"op_0005", model.OpRemoveProject, "file:d"},
	}
	if len(p1.Operations) != len(wantOps) {
		t.Fatalf("期望 %d 个操作，得到 %d", len(wantOps), len(p1.Operations))
	}
	for i, want := range wantOps {
		got := p1.Operations[i]
		if got.ID != want.id || got.Kind != want.kind || string(got.ResourceID) != want.resource {
			t.Errorf("操作[%d] = %s/%s/%s，期望 %s/%s/%s",
				i, got.ID, got.Kind, got.ResourceID, want.id, want.kind, want.resource)
		}
		if !got.Reversible {
			t.Errorf("操作 %s 必须 Reversible", got.ID)
		}
	}

	// 摘要
	wantSummary := model.PlanSummary{ResourceTotal: 5, CreateCount: 2, ModifyCount: 2, DeleteCount: 1}
	if p1.Summary != wantSummary {
		t.Errorf("Summary = %+v，期望 %+v", p1.Summary, wantSummary)
	}

	// 乱序（反向）插入构造 snapshot.Resources/baseline 后 digest 不变
	reversedProj := snapshot(model.SideProject,
		modObs(modID, "1.0", "hm", ""),
		fileObs("file:d", "h1", ""),
		fileObs("file:c", "h1", ""),
		fileObs("file:b", "hb", ""),
		fileObs("file:a", "h2", ""),
	)
	reversedRt := snapshot(model.SideRuntime,
		fileObs("file:c", "h2", ""),
		fileObs("file:a", "h1", ""),
	)
	reversedBase := baseline(bothBase("file:d", "h1"), bothBase("file:c", "h1"), bothBase("file:a", "h1"))
	p3, err := BuildDraft(buildInput(reversedBase, reversedProj, reversedRt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	if p3.PlanDigest != p1.PlanDigest {
		t.Errorf("map 插入顺序不应影响 PlanDigest: %s vs %s", p3.PlanDigest, p1.PlanDigest)
	}
	if !reflect.DeepEqual(p3.Operations, p1.Operations) {
		t.Errorf("map 插入顺序不应影响操作序列")
	}
}

// TestBuildDraftDirectionFilter 验证方向过滤：ignore 资源完全不进计划，
// 单向规则剔除逆向候选操作。
func TestBuildDraftDirectionFilter(t *testing.T) {
	const ignored = "file:ignored.ini"
	const oneWay = "file:oneway.ini"
	const normal = "file:normal.ini"
	base := baseline(bothBase(ignored, "h1"), bothBase(oneWay, "h1"), bothBase(normal, "h1"))
	rules := []model.MappingRule{rule("r_ig", "ignore"), rule("r_one", "project_to_runtime")}
	proj := snapshot(model.SideProject,
		fileObs(ignored, "h2", "r_ig"), // 双端修改 => 本应 conflict_modify
		fileObs(oneWay, "h1", "r_one"),
		fileObs(normal, "h1", ""),
	)
	rt := snapshot(model.SideRuntime,
		fileObs(ignored, "h3", "r_ig"),
		fileObs(oneWay, "h2", "r_one"), // R 变 => 本应 write_project
		fileObs(normal, "h2", ""),
	)

	plan, err := BuildDraft(buildInput(base, proj, rt, rules...))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}

	// ignore 资源不进 operations/conflicts，也不计入摘要
	if op, ok := findOp(plan.Operations, ignored); ok {
		t.Errorf("ignore 资源不应产生操作: %+v", op)
	}
	for _, c := range plan.Conflicts {
		if string(c.ResourceID) == ignored {
			t.Errorf("ignore 资源不应产生冲突: %+v", c)
		}
	}
	if plan.Summary.ResourceTotal != 2 {
		t.Errorf("ResourceTotal = %d，期望 2（ignore 资源不计入）", plan.Summary.ResourceTotal)
	}
	if plan.Summary.ConflictCount != 0 {
		t.Errorf("ConflictCount = %d，期望 0", plan.Summary.ConflictCount)
	}

	// project_to_runtime 规则剔除 runtime→project 候选
	if op, ok := findOp(plan.Operations, oneWay); ok {
		t.Errorf("单向规则应剔除逆向候选操作: %+v", op)
	}
	// 无规则资源视为 bidirectional，候选保留
	op, ok := findOp(plan.Operations, normal)
	if !ok || op.Kind != model.OpWriteProject {
		t.Errorf("bidirectional 资源应保留候选: %+v ok=%v", op, ok)
	}
}

// TestBuildDraftInitialize 验证初始化计划：单侧存在只产生 init_choice 冲突、
// 无自动跨端操作；adopt_equal 无操作但计入摘要。
func TestBuildDraftInitialize(t *testing.T) {
	const onlyProj = "file:only-project.ini"
	const adoptMod = "mod:modrinth:AANobbMI"
	proj := snapshot(model.SideProject,
		fileObs(onlyProj, "hp", ""),
		modObs(adoptMod, "1.0", "dh", ""),
	)
	rt := snapshot(model.SideRuntime,
		modObs(adoptMod, "1.0", "dh", ""), // 双端相同高置信度 mod
	)

	plan, err := BuildDraft(buildInput(nil, proj, rt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	if plan.Kind != model.PlanInitialize {
		t.Errorf("Kind = %q，期望 initialize", plan.Kind)
	}
	if plan.Status != model.PlanDraft {
		t.Errorf("Status = %q，期望 draft", plan.Status)
	}
	if plan.BaseBaselineID != "" || plan.BaseBaselineDigest != "" {
		t.Errorf("初始化计划不应携带基线引用: %s/%s", plan.BaseBaselineID, plan.BaseBaselineDigest)
	}
	if len(plan.Operations) != 0 {
		t.Errorf("初始化计划不得生成自动跨端操作: %+v", plan.Operations)
	}
	if len(plan.Conflicts) != 1 {
		t.Fatalf("期望 1 条 init_choice 冲突，得到 %d", len(plan.Conflicts))
	}
	c := plan.Conflicts[0]
	if c.ResourceID != model.ResourceID(onlyProj) || c.Kind != model.ConflictInitialize {
		t.Errorf("冲突不符合期望: %+v", c)
	}
	wantSummary := model.PlanSummary{ResourceTotal: 2, AdoptEqualCount: 1, ConflictCount: 1}
	if plan.Summary != wantSummary {
		t.Errorf("Summary = %+v，期望 %+v", plan.Summary, wantSummary)
	}
}

// TestBuildDraftOperationsAndPreconditions 验证 sync 计划的操作、
// 前置条件与摘要计数。
func TestBuildDraftOperationsAndPreconditions(t *testing.T) {
	const modified = "file:modified.ini"
	const created = "file:created.ini"
	const removed = "file:removed.ini"
	base := baseline(bothBase(modified, "h1"), bothBase(removed, "h1"))
	proj := snapshot(model.SideProject,
		fileObs(modified, "h2", ""),
		fileObs(created, "hc", ""),
		// removed 被删除：不在 project 快照中
	)
	rt := snapshot(model.SideRuntime,
		fileObs(modified, "h1", ""),
		fileObs(removed, "h1", ""),
	)

	plan, err := BuildDraft(buildInput(base, proj, rt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	if plan.Kind != model.PlanSync {
		t.Errorf("Kind = %q，期望 sync", plan.Kind)
	}
	if plan.BaseBaselineID != "base_test" || plan.BaseBaselineDigest != "sha256:base" {
		t.Errorf("sync 计划应携带基线引用: %s/%s", plan.BaseBaselineID, plan.BaseBaselineDigest)
	}
	if len(plan.Operations) != 3 {
		t.Fatalf("期望 3 个操作，得到 %d: %+v", len(plan.Operations), plan.Operations)
	}

	// op_0001 write_runtime(created)：源侧 present+hc，目标侧 absent（字节序在前）
	op1 := plan.Operations[0]
	if op1.ID != "op_0001" || op1.Kind != model.OpWriteRuntime || string(op1.ResourceID) != created {
		t.Fatalf("操作[0] 不符合期望: %+v", op1)
	}
	if len(op1.Preconditions) != 2 {
		t.Fatalf("write 操作应有两个前置条件: %+v", op1.Preconditions)
	}
	assertPrecondition(t, op1.Preconditions[0], "project", "present", "hc")
	assertPrecondition(t, op1.Preconditions[1], "runtime", "absent", "")

	// op_0002 write_runtime(modified)：源侧 present+h2，目标侧 present+h1
	op2 := plan.Operations[1]
	if op2.ID != "op_0002" || op2.Kind != model.OpWriteRuntime || string(op2.ResourceID) != modified {
		t.Fatalf("操作[1] 不符合期望: %+v", op2)
	}
	assertPrecondition(t, op2.Preconditions[0], "project", "present", "h2")
	assertPrecondition(t, op2.Preconditions[1], "runtime", "present", "h1")

	// op_0003 remove_runtime(removed)：被删侧 present+h1
	op3 := plan.Operations[2]
	if op3.Kind != model.OpRemoveRuntime || string(op3.ResourceID) != removed {
		t.Fatalf("操作[2] 不符合期望: %+v", op3)
	}
	if len(op3.Preconditions) != 1 {
		t.Fatalf("remove 操作应有一个前置条件: %+v", op3.Preconditions)
	}
	assertPrecondition(t, op3.Preconditions[0], "runtime", "present", "h1")

	wantSummary := model.PlanSummary{ResourceTotal: 3, CreateCount: 1, ModifyCount: 1, DeleteCount: 1}
	if plan.Summary != wantSummary {
		t.Errorf("Summary = %+v，期望 %+v", plan.Summary, wantSummary)
	}
}

// TestResolveValidation 覆盖 resolution 校验的四种失败。
func TestResolveValidation(t *testing.T) {
	const p1 = "file:only-project.ini"
	const p2 = "file:only-runtime.ini"
	proj := snapshot(model.SideProject, fileObs(p1, "hp", ""))
	rt := snapshot(model.SideRuntime, fileObs(p2, "hr", ""))
	draft, err := BuildDraft(buildInput(nil, proj, rt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	if len(draft.Conflicts) != 2 {
		t.Fatalf("期望 2 条冲突，得到 %d", len(draft.Conflicts))
	}

	t.Run("漏一个冲突", func(t *testing.T) {
		_, err := Resolve(draft, proj, rt, []model.Resolution{
			{ResourceID: p1, Choice: model.ChoiceInitializeFromProject},
		})
		if !errors.Is(err, ErrResolutionIncomplete) {
			t.Errorf("期望 ErrResolutionIncomplete，得到 %v", err)
		}
	})
	t.Run("多余 resolution", func(t *testing.T) {
		_, err := Resolve(draft, proj, rt, []model.Resolution{
			{ResourceID: p1, Choice: model.ChoiceSkip},
			{ResourceID: p2, Choice: model.ChoiceSkip},
			{ResourceID: "file:ghost.ini", Choice: model.ChoiceSkip},
		})
		if !errors.Is(err, ErrResolutionUnknown) {
			t.Errorf("期望 ErrResolutionUnknown，得到 %v", err)
		}
	})
	t.Run("重复 resolution", func(t *testing.T) {
		_, err := Resolve(draft, proj, rt, []model.Resolution{
			{ResourceID: p1, Choice: model.ChoiceSkip},
			{ResourceID: p1, Choice: model.ChoiceSkip},
		})
		if !errors.Is(err, ErrResolutionIncomplete) {
			t.Errorf("期望 ErrResolutionIncomplete，得到 %v", err)
		}
	})
	t.Run("非法 choice", func(t *testing.T) {
		_, err := Resolve(draft, proj, rt, []model.Resolution{
			{ResourceID: p1, Choice: model.ChoiceTakeProject}, // initialize 冲突不接受 take_*
			{ResourceID: p2, Choice: model.ChoiceSkip},
		})
		if !errors.Is(err, ErrResolutionInvalidChoice) {
			t.Errorf("期望 ErrResolutionInvalidChoice，得到 %v", err)
		}
	})
}

// TestResolveSuccess 验证恰好覆盖成功时的 resolved plan 形态与确定性。
func TestResolveSuccess(t *testing.T) {
	const onlyProj = "file:only-project.ini"
	const onlyRt = "file:only-runtime.ini"
	proj := snapshot(model.SideProject, fileObs(onlyProj, "hp", ""))
	rt := snapshot(model.SideRuntime, fileObs(onlyRt, "hr", ""))
	draft, err := BuildDraft(buildInput(nil, proj, rt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	draft.PlanID = "plan_draft1" // 模拟 application 分配的 ID（不参与 digest）

	resolved, err := Resolve(draft, proj, rt, []model.Resolution{
		{ResourceID: onlyRt, Choice: model.ChoiceSkip},
		{ResourceID: onlyProj, Choice: model.ChoiceInitializeFromProject},
	})
	if err != nil {
		t.Fatalf("Resolve 报错: %v", err)
	}

	if resolved.Status != model.PlanResolved {
		t.Errorf("Status = %q，期望 resolved", resolved.Status)
	}
	if resolved.PlanID != "" {
		t.Errorf("PlanID 应留空由 application 分配，得到 %q", resolved.PlanID)
	}
	if resolved.ResolvedFromPlanID != "plan_draft1" {
		t.Errorf("ResolvedFromPlanID = %q", resolved.ResolvedFromPlanID)
	}
	if resolved.PlanDigest == draft.PlanDigest {
		t.Errorf("resolved digest 必须与 draft 不同: %s", resolved.PlanDigest)
	}
	if len(resolved.Conflicts) != 2 {
		t.Errorf("冲突应保留作证据，得到 %d 条", len(resolved.Conflicts))
	}
	// resolutions 排序后保存
	if len(resolved.Resolutions) != 2 || string(resolved.Resolutions[0].ResourceID) != onlyProj {
		t.Errorf("Resolutions 应按 ResourceID 排序: %+v", resolved.Resolutions)
	}

	// skip 不生成操作；initialize_from_project 生成 write_runtime 并重新编号
	if len(resolved.Operations) != 1 {
		t.Fatalf("期望 1 个操作，得到 %d: %+v", len(resolved.Operations), resolved.Operations)
	}
	op := resolved.Operations[0]
	if op.ID != "op_0001" || op.Kind != model.OpWriteRuntime || string(op.ResourceID) != onlyProj {
		t.Errorf("操作不符合期望: %+v", op)
	}
	assertPrecondition(t, op.Preconditions[0], "project", "present", "hp")
	assertPrecondition(t, op.Preconditions[1], "runtime", "absent", "")

	// resolution 顺序不同也不影响结果（确定性）
	again, err := Resolve(draft, proj, rt, []model.Resolution{
		{ResourceID: onlyProj, Choice: model.ChoiceInitializeFromProject},
		{ResourceID: onlyRt, Choice: model.ChoiceSkip},
	})
	if err != nil {
		t.Fatalf("Resolve 报错: %v", err)
	}
	if again.PlanDigest != resolved.PlanDigest || !reflect.DeepEqual(again.Operations, resolved.Operations) {
		t.Errorf("resolution 输入顺序不应影响 resolved plan")
	}

	wantSummary := model.PlanSummary{ResourceTotal: 2, CreateCount: 1, ConflictCount: 2}
	if resolved.Summary != wantSummary {
		t.Errorf("Summary = %+v，期望 %+v", resolved.Summary, wantSummary)
	}
	if len(resolved.ConfirmationRequirements) != 0 {
		t.Errorf("无 overwrite/delete/write_project/unrecoverable 时确认要求应为空: %+v",
			resolved.ConfirmationRequirements)
	}
}

// TestResolveConflictChoices 验证 take_project/take_runtime 的操作生成、
// 方向过滤与重新编号排序。
func TestResolveConflictChoices(t *testing.T) {
	const dm = "file:dm.ini"
	const mm = "file:mm.ini"
	base := baseline(bothBase(dm, "h1"), bothBase(mm, "h1"))
	proj := snapshot(model.SideProject, fileObs(mm, "h2", "")) // dm 被删除；mm 修改
	rt := snapshot(model.SideRuntime, fileObs(dm, "h2", ""), fileObs(mm, "h3", ""))

	draft, err := BuildDraft(buildInput(base, proj, rt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	if len(draft.Conflicts) != 2 || len(draft.Operations) != 0 {
		t.Fatalf("冲突资源不应有自动操作: %+v %+v", draft.Conflicts, draft.Operations)
	}

	// take_project 在 project-absent 冲突上生成 remove_runtime；
	// take_runtime 在双 present 冲突上生成 write_project
	resolved, err := Resolve(draft, proj, rt, []model.Resolution{
		{ResourceID: dm, Choice: model.ChoiceTakeProject},
		{ResourceID: mm, Choice: model.ChoiceTakeRuntime},
	})
	if err != nil {
		t.Fatalf("Resolve 报错: %v", err)
	}
	if len(resolved.Operations) != 2 {
		t.Fatalf("期望 2 个操作，得到 %d: %+v", len(resolved.Operations), resolved.Operations)
	}
	// write_project(rank 1) 排在 remove_runtime(rank 2) 之前，且重新编号
	if resolved.Operations[0].ID != "op_0001" || resolved.Operations[0].Kind != model.OpWriteProject {
		t.Errorf("操作[0] 不符合期望: %+v", resolved.Operations[0])
	}
	if resolved.Operations[1].ID != "op_0002" || resolved.Operations[1].Kind != model.OpRemoveRuntime {
		t.Errorf("操作[1] 不符合期望: %+v", resolved.Operations[1])
	}
	dmOp, _ := findOp(resolved.Operations, dm)
	if dmOp.Kind != model.OpRemoveRuntime {
		t.Errorf("take_project 在 project-absent 冲突上应生成 remove_runtime: %+v", dmOp)
	}
	assertPrecondition(t, dmOp.Preconditions[0], "runtime", "present", "h2")
	mmOp, _ := findOp(resolved.Operations, mm)
	if mmOp.Kind != model.OpWriteProject {
		t.Errorf("take_runtime 应生成 write_project: %+v", mmOp)
	}
	assertPrecondition(t, mmOp.Preconditions[0], "runtime", "present", "h3")
	assertPrecondition(t, mmOp.Preconditions[1], "project", "present", "h2")

	// 确认要求：overwrite=1(info)、delete=1、write_project=1
	wantReqs := []model.ConfirmationRequirement{
		{Code: "overwrite", Severity: "info", ResourceCount: 1},
		{Code: "delete", Severity: "warning", ResourceCount: 1},
		{Code: "write_project", Severity: "warning", ResourceCount: 1},
	}
	if !reflect.DeepEqual(resolved.ConfirmationRequirements, wantReqs) {
		t.Errorf("ConfirmationRequirements = %+v，期望 %+v", resolved.ConfirmationRequirements, wantReqs)
	}
	if resolved.PlanDigest == draft.PlanDigest {
		t.Errorf("resolved digest 必须与 draft 不同")
	}
}

// TestResolveDirectionFilter 验证方向过滤同样应用于 resolution 生成的操作。
func TestResolveDirectionFilter(t *testing.T) {
	const oneWay = "file:oneway.ini"
	rules := []model.MappingRule{rule("r_one", "project_to_runtime")}
	proj := snapshot(model.SideProject) // 资源不存在
	rt := snapshot(model.SideRuntime, fileObs(oneWay, "h1", "r_one"))

	draft, err := BuildDraft(buildInput(nil, proj, rt, rules...))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	if len(draft.Conflicts) != 1 {
		t.Fatalf("非 ignore 单向资源应保留冲突，得到 %d 条", len(draft.Conflicts))
	}

	// initialize_from_runtime => write_project，被 project_to_runtime 方向禁止
	resolved, err := Resolve(draft, proj, rt, []model.Resolution{
		{ResourceID: oneWay, Choice: model.ChoiceInitializeFromRuntime},
	})
	if err != nil {
		t.Fatalf("Resolve 报错: %v", err)
	}
	if op, ok := findOp(resolved.Operations, oneWay); ok {
		t.Errorf("方向禁止的 resolution 操作不应生成: %+v", op)
	}
	// 合法选择本身仍被接受（资源保持未同步）
	if len(resolved.Resolutions) != 1 {
		t.Errorf("resolution 应保留: %+v", resolved.Resolutions)
	}
}

// TestResolveConfirmationRequirements 构造 present/absent 目标与低置信度 mod，
// 验证确认要求计数。
func TestResolveConfirmationRequirements(t *testing.T) {
	const overwriteRT = "file:a.ini"   // write_runtime，目标 present => overwrite
	const createRT = "file:b.ini"      // write_runtime，目标 absent => create
	const delRT = "file:c.ini"         // remove_runtime => delete
	const wp = "file:d.ini"            // write_project，目标 present => overwrite+write_project
	const lowMod = "mod:jar:weird.jar" // 低置信度 mod write_runtime => unrecoverable
	base := baseline(bothBase(overwriteRT, "h1"), bothBase(delRT, "h1"), bothBase(wp, "h1"))
	proj := snapshot(model.SideProject,
		fileObs(overwriteRT, "h2", ""),
		fileObs(createRT, "hb", ""),
		fileObs(wp, "h1", ""),
		modObs(lowMod, "1.0", "hl", ""),
	)
	rt := snapshot(model.SideRuntime,
		fileObs(overwriteRT, "h1", ""),
		fileObs(delRT, "h1", ""),
		fileObs(wp, "h2", ""),
	)

	draft, err := BuildDraft(buildInput(base, proj, rt))
	if err != nil {
		t.Fatalf("BuildDraft 报错: %v", err)
	}
	if len(draft.Conflicts) != 0 || len(draft.Operations) != 5 {
		t.Fatalf("前置失败：期望 0 冲突 5 操作，得到 %d 冲突 %d 操作", len(draft.Conflicts), len(draft.Operations))
	}
	// draft 不计算确认要求（由 resolved plan 的最终操作推导）
	if len(draft.ConfirmationRequirements) != 0 {
		t.Errorf("draft 不应携带确认要求: %+v", draft.ConfirmationRequirements)
	}

	// 无冲突计划允许空 resolutions（恰好覆盖）；
	// 模拟 application 已分配 PlanID（resolved 标志经 ResolvedFromPlanID 参与 digest）
	draft.PlanID = "plan_draft2"
	resolved, err := Resolve(draft, proj, rt, nil)
	if err != nil {
		t.Fatalf("Resolve 报错: %v", err)
	}

	wantReqs := []model.ConfirmationRequirement{
		{Code: "overwrite", Severity: "info", ResourceCount: 2},        // a 的 runtime、d 的 project
		{Code: "delete", Severity: "warning", ResourceCount: 1},        // c
		{Code: "write_project", Severity: "warning", ResourceCount: 1}, // d
		{Code: "unrecoverable", Severity: "warning", ResourceCount: 1}, // 低置信度 mod e
	}
	if !reflect.DeepEqual(resolved.ConfirmationRequirements, wantReqs) {
		t.Errorf("ConfirmationRequirements = %+v，期望 %+v", resolved.ConfirmationRequirements, wantReqs)
	}

	wantSummary := model.PlanSummary{ResourceTotal: 5, CreateCount: 2, ModifyCount: 2, DeleteCount: 1}
	if resolved.Summary != wantSummary {
		t.Errorf("Summary = %+v，期望 %+v", resolved.Summary, wantSummary)
	}
	if resolved.PlanDigest == draft.PlanDigest {
		t.Errorf("resolved digest 必须与 draft 不同（resolved 标志经 ResolvedFromPlanID 参与 digest）")
	}
}

// TestResolveInitializeFromAbsentSideRejected 回归：不能「从不存在的一侧初始化」，
// 否则会生成源侧 absent、永不可执行的写操作。
func TestResolveInitializeFromAbsentSideRejected(t *testing.T) {
	projSnap := snapshot(model.SideProject) // 项目侧为空
	rtSnap := snapshot(model.SideRuntime, fileObs("file:config/new.ini", "d1", ""))
	draft, err := BuildDraft(buildInput(nil, projSnap, rtSnap))
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Conflicts) != 1 {
		t.Fatalf("conflicts: %d", len(draft.Conflicts))
	}
	c := draft.Conflicts[0]
	if c.Project != nil || c.Runtime == nil {
		t.Fatalf("冲突证据不符（应为仅 runtime 侧）: %+v", c)
	}

	_, err = Resolve(draft, projSnap, rtSnap, []model.Resolution{
		{ResourceID: c.ResourceID, Choice: model.ChoiceInitializeFromProject},
	})
	if !errors.Is(err, ErrResolutionInvalidChoice) {
		t.Fatalf("应拒绝从 project 侧初始化: %v", err)
	}

	resolved, err := Resolve(draft, projSnap, rtSnap, []model.Resolution{
		{ResourceID: c.ResourceID, Choice: model.ChoiceInitializeFromRuntime},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Operations) != 1 || resolved.Operations[0].Kind != model.OpWriteProject {
		t.Fatalf("应生成单条 write_project: %+v", resolved.Operations)
	}
}
