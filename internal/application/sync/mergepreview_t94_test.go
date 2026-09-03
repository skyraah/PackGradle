package sync_test

// 合并预览用例集成（票 #94，契约 07 §3.4；ADR-0009 §8）：GetMergedPreview 对
// merged_clean 行实时计算两段全文（不落库），非 merged_clean 行 →
// err.merge.not_mergeable，stale/expired 计划仍可预览，取数失败透传零新码。
// 复用 #87 的初始化链夹具（基线字节进 CAS 的时序与验收链一致）。

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/merge"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// t94MergedPlan 造出含一条 merged_clean 行（handmade.toml 双侧互不重叠改动）
// 的 draft 计划并返回。tune 透传 newStack（expired 场景注入假时钟）。
func t94MergedPlan(t *testing.T, projectRoot, instanceDir, dataRoot string, tune ...func(*syncapp.AppDeps)) (syncapp.Application, view.SyncPlanView) {
	t.Helper()
	t87WriteRuntimeSide(t, instanceDir, t87Handmade)
	app, _ := newStack(t, dataRoot, tune...)
	t87SeedCAS(t, dataRoot, t87Handmade)

	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	t87EnableConfigAndInitialSync(t, app, &rel)

	t87ReplaceIn(t, filepath.Join(projectRoot, "config", "handmade.toml"),
		`project_marker = "untouched"`, `project_marker = "edited-by-project"`)
	t87ReplaceIn(t, filepath.Join(instanceDir, "minecraft", "config", "handmade.toml"),
		`runtime_marker = "untouched"`, `runtime_marker = "edited-by-runtime"`)
	scanAndWait(t, app, rel.RelationID)
	return app, t87PrepareSync(t, app, rel)
}

// TestMergedPreviewDeterministicContent 干净合并行预览：content 与 core/merge.Texts
// 对同一三侧输入的重算产物逐字节一致（所见即所写），base_content = 基线全文，
// 双侧改动同时保留、未冲突区域字节级不变。
func TestMergedPreviewDeterministicContent(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, plan := t94MergedPlan(t, projectRoot, instanceDir, dataRoot)

	projBytes, err := os.ReadFile(filepath.Join(projectRoot, "config", "handmade.toml"))
	if err != nil {
		t.Fatal(err)
	}
	rtBytes, err := os.ReadFile(filepath.Join(instanceDir, "minecraft", "config", "handmade.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := merge.Texts([]byte(t87Handmade), projBytes, rtBytes)

	got, err := app.GetMergedPreview(context.Background(), view.GetMergedPreviewInput{
		PlanID: plan.PlanID, ResourceID: "file:config/handmade.toml",
	})
	if err != nil {
		t.Fatalf("GetMergedPreview: %v", err)
	}
	if got.PlanID != plan.PlanID || got.ResourceID != "file:config/handmade.toml" {
		t.Fatalf("标识不符: %+v", got)
	}
	if got.RelativePath != "config/handmade.toml" {
		t.Fatalf("relative_path = %q，期望 config/handmade.toml", got.RelativePath)
	}
	if got.Content != string(want.Merged) {
		t.Fatalf("content 与 merge.Texts 重算不一致（所见即所写破约）:\n got=%q\nwant=%q", got.Content, want.Merged)
	}
	if got.BaseContent != t87Handmade {
		t.Fatalf("base_content 应为基线全文（标注锚点）: %q", got.BaseContent)
	}
	// 双侧改动同时保留（合并语义），且判定时锚点段未漂移。
	if !strings.Contains(got.Content, `project_marker = "edited-by-project"`) ||
		!strings.Contains(got.Content, `runtime_marker = "edited-by-runtime"`) {
		t.Fatalf("合并结果未保留双侧改动: %q", got.Content)
	}
	if !strings.Contains(got.Content, "# 手工注释：测试样本头部。") {
		t.Fatalf("未冲突区域字节级不变破约（手工注释丢失）: %q", got.Content)
	}
}

// TestMergedPreviewStaleAndExpiredStillPreviewable stale/expired 计划仍可预览
// （只读）：停用（修订前移）与过期（假时钟越过 TTL）都不拦截预览。
func TestMergedPreviewStaleAndExpiredStillPreviewable(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		projectRoot, instanceDir, dataRoot := makeFixtures(t)
		app, plan := t94MergedPlan(t, projectRoot, instanceDir, dataRoot)

		// 修订前移 → 计划读取时投影 stale（更新策略即可，同规则重复保存）。
		pol, err := app.GetMappingPolicy(context.Background(), plan.RelationID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.UpdateMappingPolicy(context.Background(), view.UpdateMappingPolicyInput{
			RelationID: plan.RelationID, ExpectedRevision: pol.RelationRevision, Rules: pol.Rules,
		}); err != nil {
			t.Fatalf("UpdateMappingPolicy(同规则保存推修订): %v", err)
		}
		got, err := app.GetPlan(context.Background(), plan.PlanID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != string(model.PlanStale) {
			t.Fatalf("前置失败：计划 status = %s，期望 stale", got.Status)
		}
		preview, err := app.GetMergedPreview(context.Background(), view.GetMergedPreviewInput{
			PlanID: plan.PlanID, ResourceID: "file:config/handmade.toml",
		})
		if err != nil {
			t.Fatalf("stale 计划仍可预览: %v", err)
		}
		if preview.Content == "" || preview.BaseContent == "" {
			t.Fatalf("stale 预览两段全文不得为空: %+v", preview)
		}
	})

	t.Run("expired", func(t *testing.T) {
		projectRoot, instanceDir, dataRoot := makeFixtures(t)
		now := time.Now()
		app, plan := t94MergedPlan(t, projectRoot, instanceDir, dataRoot,
			func(d *syncapp.AppDeps) { d.Now = func() time.Time { return now } })

		// 越过计划 TTL（planTTL=15m）→ 读取时投影 expired；预览照常。
		now = now.Add(16 * time.Minute)
		got, err := app.GetPlan(context.Background(), plan.PlanID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != string(model.PlanExpired) {
			t.Fatalf("前置失败：计划 status = %s，期望 expired", got.Status)
		}
		preview, err := app.GetMergedPreview(context.Background(), view.GetMergedPreviewInput{
			PlanID: plan.PlanID, ResourceID: "file:config/handmade.toml",
		})
		if err != nil {
			t.Fatalf("expired 计划仍可预览: %v", err)
		}
		if preview.Content == "" || preview.BaseContent == "" {
			t.Fatalf("expired 预览两段全文不得为空: %+v", preview)
		}
	})
}

// TestMergedPreviewNotMergeableRows 非 merged_clean 行（冲突行 / noop 行 /
// 不存在行 / restore 类计划）→ err.merge.not_mergeable（{0}=resource_id）。
func TestMergedPreviewNotMergeableRows(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	t87WriteRuntimeSide(t, instanceDir, t87Handmade)
	app, _ := newStack(t, dataRoot)
	t87SeedCAS(t, dataRoot, t87Handmade)
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	t87EnableConfigAndInitialSync(t, app, &rel)

	// 同一锚点双侧各改各的 → 真冲突行（判定时 conflict_modify + hunk 证据）。
	t87ReplaceIn(t, filepath.Join(projectRoot, "config", "handmade.toml"),
		`project_marker = "untouched"`, `project_marker = "conflict-project"`)
	t87ReplaceIn(t, filepath.Join(instanceDir, "minecraft", "config", "handmade.toml"),
		`project_marker = "untouched"`, `project_marker = "conflict-runtime"`)
	scanAndWait(t, app, rel.RelationID)
	plan := t87PrepareSync(t, app, rel)
	if plan.Summary.ConflictCount != 1 {
		t.Fatalf("前置失败：期望 1 条冲突行: %+v", plan.Summary)
	}

	cases := []struct {
		name       string
		planID     string
		resourceID string
	}{
		{"冲突行", plan.PlanID, "file:config/handmade.toml"},
		{"noop 行", plan.PlanID, "file:pack.toml"},
		{"不存在行", plan.PlanID, "file:config/absent.toml"},
	}
	for _, tc := range cases {
		_, err := app.GetMergedPreview(context.Background(), view.GetMergedPreviewInput{
			PlanID: tc.planID, ResourceID: tc.resourceID,
		})
		if errs.CodeOf(err) != "err.merge.not_mergeable" {
			t.Fatalf("%s: 期望 err.merge.not_mergeable，got %v", tc.name, err)
		}
		if args := errs.ArgsOf(err); len(args) != 1 || args[0] != tc.resourceID {
			t.Fatalf("%s: args {0}=resource_id 不符: %v", tc.name, args)
		}
	}
	// 未知计划 → 既有 err.plan.not_found（零新码）。
	if _, err := app.GetMergedPreview(context.Background(), view.GetMergedPreviewInput{
		PlanID: "plan_absent", ResourceID: "file:config/handmade.toml",
	}); errs.CodeOf(err) != "err.plan.not_found" {
		t.Fatalf("未知计划期望 err.plan.not_found，got %v", err)
	}
}

// TestMergedPreviewFetchFailurePropagates 预览时基线对象缺失（外部竞态）：
// 错误透传零新码——既不静默编内容，也不谎报 not_mergeable。
func TestMergedPreviewFetchFailurePropagates(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	t87WriteRuntimeSide(t, instanceDir, t87Handmade)
	// #93 摄取面泛化后基线字节随收口入 CAS，「重取失败形态」须直删对象构造
	// （pgheadless -restore 场景③同款手术）：判定面只认 CAS 实存。
	app, _ := newStack(t, dataRoot)

	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	t87EnableConfigAndInitialSync(t, app, &rel)
	t87ReplaceIn(t, filepath.Join(projectRoot, "config", "handmade.toml"),
		`project_marker = "untouched"`, `project_marker = "edited-by-project"`)
	t87ReplaceIn(t, filepath.Join(instanceDir, "minecraft", "config", "handmade.toml"),
		`runtime_marker = "untouched"`, `runtime_marker = "edited-by-runtime"`)
	scanAndWait(t, app, rel.RelationID)
	plan := t87PrepareSync(t, app, rel)

	baselineDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(t87Handmade)))
	objectPath := filepath.Join(dataRoot, "objects", "sha256", baselineDigest[:2], baselineDigest)
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("直删基线 CAS 对象: %v", err)
	}

	_, err := app.GetMergedPreview(context.Background(), view.GetMergedPreviewInput{
		PlanID: plan.PlanID, ResourceID: "file:config/handmade.toml",
	})
	if err == nil {
		t.Fatal("基线对象缺失必须报错（不静默编内容）")
	}
	if code := errs.CodeOf(err); code == "err.merge.not_mergeable" {
		t.Fatal("重取失败不得谎报为非合并行")
	}
}
