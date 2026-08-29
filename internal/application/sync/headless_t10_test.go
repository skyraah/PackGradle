package sync_test

// T10（票 #20）验收面：映射策略读写（契约 03 §2.3）：
// ① 读：CreateRelation 后 GetMappingPolicy 可读，关系修订精确 == 1（ADR-0002 决议 1/4）；
// ② 写：编译校验 + 乐观锁 + SavePolicy 同事务递增修订（保存后 == 前值+1），
//    策略集身份（PolicyID/模板 Revision）不变；失败路径修订号不前进；
// ③ 乐观锁：expected_revision 与权威值不等 → err.mapping.stale_revision；
// ④ 编译失败：非法规则 → err.mapping.compile_failed，修订号不动；
// ⑤ collision 证据：并列规则重扫后 diag.mapping.collision（并列规则 ID + 命中路径）
//    随快照持久化并经 GetSnapshotDiagnostics 可查；碰撞路径从观察剔除。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// modRuleOf 取 policy 中的 mod 语义规则（fixture 初始模板恒有且仅有一条）。
func modRuleOf(rules []model.MappingRule) model.MappingRule {
	for _, r := range rules {
		if r.ResourceKind == string(model.ResourceMod) {
			return r
		}
	}
	return model.MappingRule{}
}

// mustGetMappingPolicy 调 GetMappingPolicy 并断言成功。
func mustGetMappingPolicy(t *testing.T, app syncapp.Application, relationID string) view.PolicyView {
	t.Helper()
	p, err := app.GetMappingPolicy(context.Background(), relationID)
	if err != nil {
		t.Fatalf("GetMappingPolicy: %v", err)
	}
	if p.Rules == nil {
		t.Fatal("rules 应归一为空切片而非 nil")
	}
	return p
}

// TestHeadlessMappingPolicyReadWrite 读/写往返：初始代次 1、保存后 +1、
// 策略集身份不变；未知关系 → err.relation.not_found。
func TestHeadlessMappingPolicyReadWrite(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	// 读：创建即第 1 代且已带初始 policy（ADR-0002 决议 1/4：精确 == 1）
	p0 := mustGetMappingPolicy(t, app, rel.RelationID)
	if p0.PolicyID != "default-v1" || p0.PolicyRevision != 1 || p0.RelationRevision != 1 {
		t.Fatalf("初始策略不符: policy_id=%s policy_rev=%d relation_rev=%d", p0.PolicyID, p0.PolicyRevision, p0.RelationRevision)
	}
	if len(p0.Rules) != 1 || p0.Rules[0].ID != "mods" {
		t.Fatalf("初始模板应仅 mods 语义规则: %+v", p0.Rules)
	}

	// 未知关系（读/写两路）→ err.relation.not_found
	if _, err := app.GetMappingPolicy(ctx, "rel_missing"); errCode(t, err) != "err.relation.not_found" {
		t.Fatalf("未知关系读应 relation.not_found，得到 %v", err)
	}
	_, err := app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{RelationID: "rel_missing", Rules: p0.Rules})
	if errCode(t, err) != "err.relation.not_found" {
		t.Fatalf("未知关系写应 relation.not_found，得到 %v", err)
	}

	// 写：改 mods 方向 + 并入 config 建议规则，expected_revision=1 → 成功且代次 +1
	mods := modRuleOf(p0.Rules)
	mods.Direction = "runtime_to_project"
	config := model.MappingRule{
		ID: "config", ResourceKind: "text_file",
		ProjectPrefix: "config", RuntimePrefix: "config",
		Direction: "bidirectional", Materialization: "copy",
		MergePolicy: "manual", RuntimeLocalPolicy: "exclude",
	}
	p1, err := app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: rel.RelationID, ExpectedRevision: 1,
		Rules: []model.MappingRule{mods, config},
	})
	if err != nil {
		t.Fatalf("UpdateMappingPolicy: %v", err)
	}
	if p1.RelationRevision != 2 {
		t.Fatalf("保存后关系代次应 == 前值+1（ADR-0002 决议 4），得到 %d", p1.RelationRevision)
	}
	// 策略集身份不变（ADR-0002 决议 5：模板版本与关系代次互不驱动）
	if p1.PolicyID != p0.PolicyID || p1.PolicyRevision != p0.PolicyRevision {
		t.Fatalf("策略集身份应保持不变: %+v vs %+v", p1, p0)
	}
	// 读回一致
	p1r := mustGetMappingPolicy(t, app, rel.RelationID)
	if p1r.RelationRevision != 2 || len(p1r.Rules) != 2 {
		t.Fatalf("读回不符: rev=%d rules=%d", p1r.RelationRevision, len(p1r.Rules))
	}
	got := modRuleOf(p1r.Rules)
	if got.Direction != "runtime_to_project" {
		t.Fatalf("mods 方向应已保存: %+v", got)
	}

	// 乐观锁：以过期 revision=1 再写 → err.mapping.stale_revision（args {0}=1 {1}=2）
	_, err = app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: rel.RelationID, ExpectedRevision: 1, Rules: p1r.Rules,
	})
	if errCode(t, err) != "err.mapping.stale_revision" {
		t.Fatalf("过期 revision 应 stale_revision，得到 %v", err)
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || len(appErr.Args) != 2 || appErr.Args[0] != "1" || appErr.Args[1] != "2" {
		t.Fatalf("stale_revision 应回传 expected/actual: %+v", err)
	}
	// 失败后修订号不前进
	if p := mustGetMappingPolicy(t, app, rel.RelationID); p.RelationRevision != 2 {
		t.Fatalf("失败保存不应递增修订: %d", p.RelationRevision)
	}
}

// TestHeadlessMappingPolicyCompileGate 编译失败不落库：非法方向、缺 mod 语义
// 规则均 → err.mapping.compile_failed（args {0}=rule_id），修订号与规则集不变。
func TestHeadlessMappingPolicyCompileGate(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	p0 := mustGetMappingPolicy(t, app, rel.RelationID)

	cases := []struct {
		name   string
		rules  []model.MappingRule
		ruleID string
	}{
		{"非法方向", []model.MappingRule{model.MappingRule{
			ID: "mods", ResourceKind: "mod", ProjectPrefix: "mods", RuntimePrefix: "mods",
			Direction: "sideways", Materialization: "copy", MergePolicy: "packwiz", RuntimeLocalPolicy: "exclude",
		}}, "mods"},
		{"缺 mod 语义规则", nil, ""},
		{"文件规则进入 mods 前缀", []model.MappingRule{
			modRuleOf(p0.Rules),
			{ID: "modsfiles", ResourceKind: "text_file", ProjectPrefix: "mods/extra", RuntimePrefix: "mods/extra",
				Direction: "bidirectional", Materialization: "copy", MergePolicy: "manual", RuntimeLocalPolicy: "exclude"},
		}, "modsfiles"},
	}
	for _, tc := range cases {
		_, err := app.UpdateMappingPolicy(context.Background(), view.UpdateMappingPolicyInput{
			RelationID: rel.RelationID, ExpectedRevision: p0.RelationRevision, Rules: tc.rules,
		})
		if errCode(t, err) != "err.mapping.compile_failed" {
			t.Fatalf("%s 应 compile_failed，得到 %v", tc.name, err)
		}
		if tc.ruleID != "" {
			var appErr *errs.AppError
			if !errors.As(err, &appErr) || len(appErr.Args) != 1 || appErr.Args[0] != tc.ruleID {
				t.Fatalf("%s 应定位违规规则 %q: %+v", tc.name, tc.ruleID, err)
			}
		}
	}
	// 全部失败后修订号与规则集不变
	p1 := mustGetMappingPolicy(t, app, rel.RelationID)
	if p1.RelationRevision != p0.RelationRevision || len(p1.Rules) != 1 {
		t.Fatalf("编译失败不应改变状态: rev=%d rules=%d", p1.RelationRevision, len(p1.Rules))
	}
}

// TestHeadlessMappingPolicyCollisionEvidence 并列规则重扫：diag.mapping.collision
// （并列规则 ID 字节序 + 命中路径）随快照持久化、GetSnapshotDiagnostics 可查，
// 碰撞路径从观察剔除（GetChanges 无该资源行）。
func TestHeadlessMappingPolicyCollisionEvidence(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	p0 := mustGetMappingPolicy(t, app, rel.RelationID)
	// 初扫无映射冲突诊断（模板仅 mods 语义规则，无文件规则）
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := app.GetSnapshotDiagnostics(ctx, rel.RelationID, ws.LatestProjectSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range clean {
		if d.Code == "diag.mapping.collision" {
			t.Fatalf("初扫不应有映射冲突诊断: %+v", d)
		}
	}

	// 并入两条并列 config 规则（同侧前缀等长）→ config/ 下路径无法唯一决议
	policyRules := append([]model.MappingRule{}, p0.Rules...)
	for _, id := range []string{"config_a", "config_b"} {
		policyRules = append(policyRules, model.MappingRule{
			ID: id, ResourceKind: "text_file",
			ProjectPrefix: "config", RuntimePrefix: "config",
			Direction: "bidirectional", Materialization: "copy",
			MergePolicy: "manual", RuntimeLocalPolicy: "exclude",
		})
	}
	if _, err := app.UpdateMappingPolicy(ctx, view.UpdateMappingPolicyInput{
		RelationID: rel.RelationID, ExpectedRevision: p0.RelationRevision, Rules: policyRules,
	}); err != nil {
		t.Fatalf("UpdateMappingPolicy: %v", err)
	}

	// 端点写入 config 文件并重扫：碰撞路径进诊断、离开观察
	writeFile(t, filepath.Join(projectRoot, "config", "options.txt"), "key=value\n")
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "options.txt"), "key=value\n")
	scanAndWait(t, app, rel.RelationID)

	ws2, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	diags, err := app.GetSnapshotDiagnostics(ctx, rel.RelationID, ws2.LatestProjectSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Code != "diag.mapping.collision" {
			continue
		}
		found = true
		if len(d.Args) != 2 || d.Args[0] != "config_a" || d.Args[1] != "config_b" {
			t.Fatalf("碰撞证据应含并列规则 ID 字节序: %+v", d)
		}
		if d.RelativePath != "config/options.txt" {
			t.Fatalf("碰撞证据应携带命中路径: %+v", d)
		}
	}
	if !found {
		t.Fatalf("重扫后应产出 diag.mapping.collision: %+v", diags)
	}

	// 碰撞路径从观察剔除：GetChanges 无该资源行
	page := mustGetChanges(t, app, view.GetChangesInput{RelationID: rel.RelationID})
	for _, it := range page.Items {
		if it.RelativePath == "config/options.txt" {
			t.Fatalf("碰撞路径应从观察剔除: %+v", it)
		}
	}
}
