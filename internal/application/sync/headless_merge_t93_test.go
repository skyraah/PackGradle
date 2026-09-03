package sync_test

// P4 合并执行面集成（票 #93；ADR-0009 §4/§8/§9；契约 07 §3.3；验收规格 §3.2
// 场景 1/2 的应用层断言）：merged_clean 行的 write_merged 全链——
// resolve take_merged → confirm → committed → 双端落盘字节=确定性重算产物 →
// 产物入 CAS（提交收口期摄取通道，含非 mod 文本与 mod metafile 泛化面）→
// 回滚到合并前提交 merged 行 restorable_from_cas 零网络收口。
//
// 与 t87 判定面测试的关键差异：不预置 CAS 对象——初次同步的基线内容摄取
// （票 #93 泛化面）自然供给合并 Base 侧，无预置直达 merged_clean。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/merge"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
	"packgradle/internal/store"
	"packgradle/internal/store/objectstore"
	"packgradle/internal/store/sqlite"
)

// t93OpenCAS 打开数据目录的 CAS 断言句柄（t87SeedCAS 的只读形态）。
func t93OpenCAS(t *testing.T, dataRoot string) *objectstore.CAS {
	t.Helper()
	layout, err := store.EnsureLayout(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(layout.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cas, err := objectstore.Open(layout.ObjectsDir, db)
	if err != nil {
		t.Fatal(err)
	}
	return cas
}

func t93Sha256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestMergedApplyCommitAndRollback 场景①+② 的应用层断言链。
func TestMergedApplyCommitAndRollback(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	t87WriteRuntimeSide(t, instanceDir, t87Handmade)
	app, _ := newStack(t, dataRoot)
	cas := t93OpenCAS(t, dataRoot)
	ctx := context.Background()

	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	// 初次同步（无预置 CAS 对象）：基线内容摄取使合并 Base 侧可得（票 #93）。
	t87EnableConfigAndInitialSync(t, app, &rel)
	commits, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(commits.Items) == 0 {
		t.Fatal("初次同步应产生提交")
	}
	c0 := commits.Items[0].CommitID

	projFile := filepath.Join(projectRoot, "config", "handmade.toml")
	rtFile := filepath.Join(instanceDir, "minecraft", "config", "handmade.toml")
	t87ReplaceIn(t, projFile, `project_marker = "untouched"`, `project_marker = "edited-by-project"`)
	t87ReplaceIn(t, rtFile, `runtime_marker = "untouched"`, `runtime_marker = "edited-by-runtime"`)
	scanAndWait(t, app, rel.RelationID)

	plan := t87PrepareSync(t, app, rel)
	if plan.Summary.MergedCleanCount != 1 || len(plan.Operations) != 1 || plan.Operations[0].Kind != "write_merged" {
		t.Fatalf("merged 计划面不符: summary=%+v ops=%+v", plan.Summary, plan.Operations)
	}
	mergedResource := model.ResourceID(plan.Operations[0].ResourceID)
	// take_merged 误用于非 merged 行 → 既有 resolution_invalid。
	if _, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: plan.PlanID, Resolutions: []model.Resolution{
		{ResourceID: "file:config/other.toml", Choice: model.ChoiceTakeMerged},
	}}); err == nil || errs.CodeOf(err) != "err.plan.resolution_invalid" {
		t.Fatalf("take_merged 于非 merged 行应 resolution_invalid: %v", err)
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: plan.PlanID, Resolutions: []model.Resolution{
		{ResourceID: mergedResource, Choice: model.ChoiceTakeMerged},
	}})
	if err != nil {
		t.Fatalf("resolve take_merged: %v", err)
	}
	if len(resolved.ConfirmationRequirements) != 0 {
		t.Fatalf("merged_clean 属非冲突操作，确认要求应为空: %+v", resolved.ConfirmationRequirements)
	}
	tv := mustConfirm(t, app, resolved.PlanID)
	if final := waitTask(t, app, tv.TaskID); final.Status != model.TaskStatusSucceeded {
		t.Fatalf("合并 apply 任务终态 %s（%+v）", final.Status, final.Problem)
	}

	// 双端落盘字节 = 确定性重算产物（同算法同输入同输出，harness 复算）。
	projGot, err := os.ReadFile(projFile)
	if err != nil {
		t.Fatal(err)
	}
	rtGot, err := os.ReadFile(rtFile)
	if err != nil {
		t.Fatal(err)
	}
	// 重算输入：基线=原始样本，双端=注入后的落盘前内容（t87ReplaceIn 语义可逆）。
	wantProj := strings.Replace(t87Handmade, `project_marker = "untouched"`, `project_marker = "edited-by-project"`, 1)
	wantRt := strings.Replace(t87Handmade, `runtime_marker = "untouched"`, `runtime_marker = "edited-by-runtime"`, 1)
	want := merge.Texts([]byte(t87Handmade), []byte(wantProj), []byte(wantRt))
	if len(want.Hunks) != 0 {
		t.Fatal("夹具自检：样本应干净合并")
	}
	if string(projGot) != string(want.Merged) || string(rtGot) != string(want.Merged) {
		t.Fatalf("落盘字节≠确定性重算产物:\nproj=%q\nrt=%q\nwant=%q", projGot, rtGot, want.Merged)
	}
	// 未冲突区域字节级不变（红线③，对落盘文件直接断）。
	for _, marker := range []string{"# 手工注释：测试样本头部。", "  render_distance = 12   # 行内注释 + 缩进键序", "\n\n[audio]"} {
		if !strings.Contains(string(projGot), marker) {
			t.Fatalf("未冲突区域被改写: %q", marker)
		}
	}
	// 合并产物入 CAS（红线②）。
	if ok, err := cas.Has(ctx, t93Sha256(want.Merged)); err != nil || !ok {
		t.Fatalf("合并产物应入 CAS: ok=%v err=%v", ok, err)
	}

	// ---- 场景②：回滚到 c0 —— merged 行 restorable_from_cas 零网络收口 ----
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c0})
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	for i := range draft.Items {
		if draft.Items[i].ResourceID == mergedResource {
			if draft.Items[i].Marker != model.MarkerRestorableFromCAS {
				t.Fatalf("merged 行 marker=%s（reason=%s），期望 restorable_from_cas", draft.Items[i].Marker, draft.Items[i].MarkerReason)
			}
		}
	}
	if !draft.ExactFeasible {
		t.Fatalf("回滚计划应全部就绪: blocked=%+v", draft.BlockedBy)
	}
	resolvedR, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft.PlanID, RequestedExactness: "exact"})
	if err != nil {
		t.Fatalf("resolve restore: %v", err)
	}
	tvR, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolvedR.PlanID})
	if err != nil {
		t.Fatalf("ConfirmRestorePlan: %v", err)
	}
	if final := waitTask(t, app, tvR.TaskID); final.Status != model.TaskStatusSucceeded {
		t.Fatalf("restore 任务终态 %s（%+v）", final.Status, final.Problem)
	}
	// 双端回到合并前字节（CAS 对象流写回，零网络零用户介入）。
	if got, err := os.ReadFile(projFile); err != nil || string(got) != t87Handmade {
		t.Fatalf("回滚后 project 侧字节不符: %q（err=%v）", got, err)
	}
	if got, err := os.ReadFile(rtFile); err != nil || string(got) != t87Handmade {
		t.Fatalf("回滚后 runtime 侧字节不符: %q（err=%v）", got, err)
	}
}
