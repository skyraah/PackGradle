package sync_test

// 票 #60 单测：回滚执行链（ConfirmRestorePlan + restore 运行 + failed 终局 +
// 收口账目）。与 restore_t59_test.go（计划面）互补，覆盖：
//
//  1. exact 经 CAS 全链（真 apply 两轮历史）：ConfirmRestorePlan → 运行六阶段 →
//     committed kind=restore；逐字节复验、原历史不改写、计划投影 applied、
//     committed 后同 plan 不可重入、restore 提交是合法目标（测试 1）；
//  2. ConfirmRestorePlan 幂等口径：resolved/stale 校验、恢复所需门禁（与 apply
//     同门）、活跃运行幂等重入返回既有任务（测试 2）；
//  3. failed 终局可重入（假 CDN 注入，AC 硬门槛）：staging 重取 503 →
//     run=failed + task=failed + Problem=err.download.rate_limited，不进
//     recovery、零提交、关系健康不动；CDN 恢复后同 plan 重 Confirm 建新运行
//     committed exact（测试 3）；
//  4. partial 账目（红线④）：staged 字节消费经暂存锚 + 重取 + 删除行（带
//     deletion_warn 的手放 mod 照删不保全）+ unrecoverable skip → committed
//     kind=partial + relation 保持 dirty（测试 4）；
//  5. CAS miss 补全就绪面（红线②）：直删 CAS 对象 → user_object_required +
//     exact_infeasible 前置拒绝 → 错字节 hash_mismatch → 对字节 staged 翻转 →
//     exact committed（测试 5）。

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/download"
	"packgradle/internal/errs"

	syncapp "packgradle/internal/application/sync"
)

// ---- 通用辅助 ----

// r60CDNStack 装配带假 CDN 引擎的栈（Downloads + Probes 双引用，生产同款构造，
// 快退避让重试面不拖慢测试）。
func r60CDNStack(t *testing.T, dataRoot string, cdn *download.FakeCDN) (*syncapp.App, *sql.DB) {
	t.Helper()
	srv := httptest.NewServer(cdn.Handler())
	t.Cleanup(srv.Close)
	engine, err := download.New(download.Options{
		BaseURL: srv.URL + "/files",
		Backoff: func(int) time.Duration { return time.Millisecond },
		Sleep:   func(_ context.Context, _ time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, db := newStack(t, dataRoot, func(d *syncapp.AppDeps) {
		d.Downloads = engine
		d.Probes = engine
	})
	return app, db
}

// r60MustConfirmRestore 确认回滚计划并轮询至终态。
func r60MustConfirmRestore(t *testing.T, app syncapp.Application, planID string) view.TaskView {
	t.Helper()
	tv, err := app.ConfirmRestorePlan(context.Background(), view.ConfirmRestorePlanInput{PlanID: planID})
	if err != nil {
		t.Fatalf("ConfirmRestorePlan: %v", err)
	}
	if tv.Kind != model.TaskKindRestore {
		t.Fatalf("任务 kind = %s，期望 restore", tv.Kind)
	}
	return waitApplyTask(t, app, tv.TaskID)
}

// ---- 测试 1：exact 经 CAS 全链 + committed 不可重入 ----

func TestRestore60ExactRunViaCAS(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)
	gameDir := filepath.Join(instanceDir, "minecraft")

	// round1 initialize → commit1；round2（删 runtime b、改 project c）→ commit2；
	// 再加运行侧手放文件（回滚删除行主角，非 mod）。
	plan1 := mustResolveApplyPlan(t, app, rel, round1Choices)
	if waitApplyTask(t, app, mustConfirm(t, app, plan1.PlanID).TaskID).Status != model.TaskStatusSucceeded {
		t.Fatal("round1 应成功")
	}
	if err := os.Remove(filepath.Join(gameDir, "config", "b.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "config", "c.toml"), []byte(fxApplyC2), 0o644); err != nil {
		t.Fatal(err)
	}
	mustScanAndWait(t, app, rel.RelationID)
	plan2 := mustResolveApplyPlan(t, app, rel, nil)
	tv2 := mustConfirm(t, app, plan2.PlanID)
	commit2 := waitApplyTask(t, app, tv2.TaskID).CommitID
	writeFile(t, filepath.Join(gameDir, "config", "handmade.toml"), "handmade = true\n")
	mustScanAndWait(t, app, rel.RelationID)

	// 历史快照（原历史不改写断言的基线）。
	before, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	// 回滚到 commit1（round1 后状态）：b（create/cas）、c（modify/cas 双侧）、
	// handmade（delete 非 mod 不警示）。
	if _, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: "commit_x"}); err == nil ||
		errs.CodeOf(err) != "err.restore.commit_not_found" {
		t.Fatalf("不存在提交应 commit_not_found: %v", err)
	}
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: before.Items[1].CommitID})
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	if b := r59Item(t, draft, "file:config/b.toml"); b.Marker != model.MarkerRestorableFromCAS || b.ChangeKind != "create" {
		t.Fatalf("b 行: %+v", b)
	}
	if c := r59Item(t, draft, "file:config/c.toml"); c.Marker != model.MarkerRestorableFromCAS || c.ChangeKind != "modify" {
		t.Fatalf("c 行: %+v", c)
	}
	if h := r59Item(t, draft, "file:config/handmade.toml"); h.ChangeKind != "delete" || h.DeletionWarn {
		t.Fatalf("handmade 删除行（非 mod 走 before-preserve 不警示）: %+v", h)
	}
	if !draft.ExactFeasible {
		t.Fatal("全部行就绪，ExactFeasible 应为 true")
	}

	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft.PlanID, RequestedExactness: "exact"})
	if err != nil {
		t.Fatalf("ResolveRestorePlan(exact): %v", err)
	}

	// 门禁：draft 计划不可确认；sync 计划不可经 restore 通道确认（类别门禁对称）。
	if _, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: draft.PlanID}); err == nil ||
		errs.CodeOf(err) != "err.plan.stale" {
		t.Fatalf("ConfirmRestorePlan(draft) 应 stale: %v", err)
	}
	if _, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: plan2.PlanID}); err == nil ||
		errs.CodeOf(err) != "err.plan.not_found" {
		t.Fatalf("ConfirmRestorePlan(sync 计划) 应 not_found: %v", err)
	}

	task := r60MustConfirmRestore(t, app, resolved.PlanID)
	if task.Status != model.TaskStatusSucceeded || task.Outcome != model.TaskOutcomeExact {
		t.Fatalf("restore 任务终态 %s/%s（problem=%+v）", task.Status, task.Outcome, task.Problem)
	}
	if task.MessageKey != "msg.task.restore.succeeded" {
		t.Fatalf("进度短语键 = %s", task.MessageKey)
	}
	run, err := app.GetApplyRun(ctx, rel.RelationID)
	if err != nil || run.State != model.ApplyRunCommitted || run.CommitID != task.CommitID {
		t.Fatalf("运行头 state=%s commit=%s（err=%v）", run.State, run.CommitID, err)
	}

	// 账目：kind=restore、parent=head、exact/remaining=0。
	head, err := app.GetCommit(ctx, rel.RelationID, task.CommitID)
	if err != nil {
		t.Fatalf("GetCommit: %v", err)
	}
	if head.Summary.Kind != string(model.PlanRestore) || head.Summary.Completeness != model.TaskOutcomeExact || head.Summary.RemainingChangeCnt != 0 {
		t.Fatalf("提交头 kind=%s completeness=%s remaining=%d",
			head.Summary.Kind, head.Summary.Completeness, head.Summary.RemainingChangeCnt)
	}
	var parent string
	if err := db.QueryRow(`SELECT parent_id FROM sync_commits WHERE id=?`, task.CommitID).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != commit2 {
		t.Fatalf("restore 提交 parent = %s，期望 head %s", parent, commit2)
	}

	// 逐字节复验（验收红线①）：双端写回目标状态。
	for path, want := range map[string]string{
		filepath.Join(gameDir, "config", "b.toml"):     fxApplyB,
		filepath.Join(projectRoot, "config", "c.toml"): fxApplyC,
		filepath.Join(gameDir, "config", "c.toml"):     fxApplyC,
	} {
		got, rerr := os.ReadFile(path)
		if rerr != nil || string(got) != want {
			t.Fatalf("%s = %q（err=%v），期望 %q", path, string(got), rerr, want)
		}
	}
	if _, err := os.Stat(filepath.Join(gameDir, "config", "handmade.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("handmade.toml 应已删除（err=%v）", err)
	}

	// 历史不改写：新增 kind=restore 行在前，原两行原样保留（ADR-0006 §9）。
	after, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != len(before.Items)+1 {
		t.Fatalf("历史行数 %d，期望 %d+1", len(after.Items), len(before.Items))
	}
	if after.Items[0].Kind != string(model.PlanRestore) || after.Items[0].CommitID != task.CommitID {
		t.Fatalf("新头行 kind=%s id=%s", after.Items[0].Kind, after.Items[0].CommitID)
	}
	for i := range before.Items {
		want, got := before.Items[i], after.Items[i+1]
		if want.CommitID != got.CommitID || want.Kind != got.Kind || want.CreatedAt != got.CreatedAt {
			t.Fatalf("历史第 %d 行被改写: %+v → %+v", i, want, got)
		}
	}

	// 计划投影 applied；收口后 diff 归零；committed 后同 plan 不可重入。
	got, err := app.GetRestorePlan(ctx, resolved.PlanID)
	if err != nil || got.Status != "applied" {
		t.Fatalf("计划投影 status=%s（err=%v），期望 applied", got.Status, err)
	}
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.State.DiffState != "clean" {
		t.Fatalf("exact 回滚后 diff_state = %s，期望 clean", ws.State.DiffState)
	}
	if _, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolved.PlanID}); err == nil ||
		errs.CodeOf(err) != "err.plan.apply_not_reentrant" {
		t.Fatalf("committed 后重确认应 apply_not_reentrant: %v", err)
	}
	// restore 提交同样是合法回滚目标（历史 restore 提交=重做，ADR-0006 §1；
	// 执行面归 headless 场景④）。
	if _, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: task.CommitID}); err != nil {
		t.Fatalf("restore 提交作为目标应合法: %v", err)
	}
}

// ---- 测试 2：ConfirmRestorePlan 幂等口径与恢复门禁 ----

func TestRestore60ConfirmIdempotencyAndGate(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	plan1 := mustResolveApplyPlan(t, app, rel, round1Choices)
	if waitApplyTask(t, app, mustConfirm(t, app, plan1.PlanID).TaskID).Status != model.TaskStatusSucceeded {
		t.Fatal("round1 应成功")
	}
	if err := os.Remove(filepath.Join(instanceDir, "minecraft", "config", "b.toml")); err != nil {
		t.Fatal(err)
	}
	mustScanAndWait(t, app, rel.RelationID)
	plan2 := mustResolveApplyPlan(t, app, rel, nil)
	if waitApplyTask(t, app, mustConfirm(t, app, plan2.PlanID).TaskID).Status != model.TaskStatusSucceeded {
		t.Fatal("round2 应成功")
	}

	commits, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commits.Items[1].CommitID})
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft.PlanID, RequestedExactness: "exact"})
	if err != nil {
		t.Fatalf("ResolveRestorePlan: %v", err)
	}

	// 恢复所需期间 confirm 与 prepare 同门禁（ADR-0006 §8，AC 硬门槛）。
	if _, err := db.Exec(`UPDATE relations SET health='recovery_required' WHERE id=?`, rel.RelationID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolved.PlanID}); err == nil ||
		errs.CodeOf(err) != "err.recovery.in_progress" {
		t.Fatalf("恢复期 confirm 应 in_progress: %v", err)
	}
	if _, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commits.Items[1].CommitID}); err == nil ||
		errs.CodeOf(err) != "err.recovery.in_progress" {
		t.Fatalf("恢复期 prepare_restore 应 in_progress: %v", err)
	}
	if _, err := db.Exec(`UPDATE relations SET health='healthy' WHERE id=?`, rel.RelationID); err != nil {
		t.Fatal(err)
	}

	// 首次确认 → 活跃运行窗口幂等重入返回既有任务（双击/双窗口安全）。
	tv, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolved.PlanID})
	if err != nil {
		t.Fatalf("ConfirmRestorePlan: %v", err)
	}
	if tv.PlanID != resolved.PlanID {
		t.Fatalf("任务 PlanID 回填 = %q", tv.PlanID)
	}
	re, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolved.PlanID})
	if err != nil {
		t.Fatalf("幂等重入: %v", err)
	}
	if re.TaskID != tv.TaskID {
		t.Fatalf("幂等重入返回新任务 %s，期望既有 %s", re.TaskID, tv.TaskID)
	}
	if final := waitApplyTask(t, app, tv.TaskID); final.Status != model.TaskStatusSucceeded {
		t.Fatalf("restore 运行应成功: %+v", final.Problem)
	}
}

// ---- 测试 3：failed 终局可重入（假 CDN 注入，AC 硬门槛）----

// r60DLFixture 造「项目端声明 CF mod、运行端缺 jar」的夹具（票 #63 场景造数法）：
// 初始化 initialize_from_project 产生 write_runtime(download) 操作。
func r60DLFixture(t *testing.T) (projectRoot, instanceDir, dataRoot string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instance")
	meta := "name = \"Chrono\"\nfilename = \"chrono-1.0.jar\"\nside = \"both\"\n\n" +
		"[download]\nhash-format = \"sha256\"\nhash = \"" + r59sha256(r60JarV0) + "\"\n\n" +
		"[update.curseforge]\nproject-id = 369812\nfile-id = 7654321\n"
	writeFile(t, filepath.Join(projectRoot, "pack.toml"), fxR59PackToml)
	writeFile(t, filepath.Join(projectRoot, "index.toml"), r59IndexToml("chrono.pw.toml"))
	writeFile(t, filepath.Join(projectRoot, "mods", "chrono.pw.toml"), meta)
	writeFile(t, filepath.Join(projectRoot, "config", "extras.toml"), r60ExtrasV0)
	writeFile(t, filepath.Join(instanceDir, "instance.cfg"), r59InstanceCfg)
	writeFile(t, filepath.Join(instanceDir, "minecraft", ".keep"), "")
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "extras.toml"), r60ExtrasV0)
	dataRoot = filepath.Join(base, "userdata")
	return
}

const (
	r60JarV0 = "fake chrono jar v0"
	r60JarV2 = "fake chrono jar v2"
	// config 双侧文件（round2 的 copy 成功行，保证 sync commit2 = partial 而非
	// 全败 failed——restore 侧 failed 终局断言需要独立的对照面）。
	r60ExtrasV0 = "extras = \"v1\"\n"
	r60ExtrasV2 = "extras = \"v2\"\n"
	// 与引擎同口径的假 CDN 路径：/files/{fileID/1000}/{fileID%1000}/{filename}。
	r60CDNPath = "/files/7654/321/chrono-1.0.jar"
	r60JarPath = "mods/chrono-1.0.jar"
)

func TestRestore60FailedTerminalAndReentry(t *testing.T) {
	projectRoot, instanceDir, dataRoot := r60ModFixture(t)
	cdn := download.NewFakeCDN()
	// 503 全程（prepare 探测 → unknown 保持乐观标记；执行重取 → 重试耗尽
	// rate_limited）；SetFile 供清除脚本后的恢复面。
	cdn.SetFile(r60CDNPath, []byte(r60JarV0))
	cdn.Script(r60CDNPath, download.FakeStep{Status: 503})
	app, db := r60CDNStack(t, dataRoot, cdn)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)
	gameDir := filepath.Join(instanceDir, "minecraft")

	// 注入回滚目标（v0）与漂移头（v2 占位提交——failed 终局「零提交」断言的对照面）。
	recs := map[model.ResourceID]model.Recoverability{
		"mod:curseforge:369812":       model.RecoverabilityRedownload,
		"mod:path:mods/local.pw.toml": model.RecoverabilityRedownload,
		"mod:jar:runtimeonly-1.0.jar": model.RecoverabilityUnrecoverable,
	}
	commitTarget := mustInjectRestoreTarget(t, app, db, rel, model.RecoverabilityCAS, recs)

	// v2 漂移：chrono jar v2 + .index 声明 v2（运行侧语义漂移 → redownload 行）；
	// local/runtimeonly jar 漂移；新增手放 loose jar（删除行）。
	if err := os.WriteFile(filepath.Join(gameDir, "mods", "chrono-1.0.jar"), []byte(r60JarV2), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gameDir, "mods", ".index", "chrono-1.0.jar.pw.toml"),
		r59IndexMeta("Chrono", "chrono-1.0.jar", "sha256", r59sha256(r60JarV2)))
	if err := os.WriteFile(filepath.Join(gameDir, "mods", "local-thing-1.0.jar"), []byte("fake local jar v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, "mods", "runtimeonly-1.0.jar"), []byte("fake runtimeonly jar v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gameDir, "mods", "loose-1.0.jar"), "fake loose jar")
	mustScanAndWait(t, app, rel.RelationID)
	commitHead := mustInjectRestoreTarget(t, app, db, rel, model.RecoverabilityCAS, recs)

	// 回滚计划：chrono（redownload）、runtimeonly（unrecoverable → skip）、
	// local（user_object 未补全 → skip）、loose（删除行执行）。
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commitTarget})
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	chrono := r59Item(t, draft, "mod:curseforge:369812")
	r59AssertMarker(t, chrono, model.MarkerRedownloadRequired, "")
	if chrono.Availability != "unknown" {
		t.Fatalf("503 探测应保持乐观标记 availability=unknown，got %q", chrono.Availability)
	}
	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft.PlanID, RequestedExactness: "allow_partial",
		SkipResourceIDs: []string{"mod:jar:runtimeonly-1.0.jar", "mod:path:mods/local.pw.toml"},
	})
	if err != nil {
		t.Fatalf("ResolveRestorePlan(allow_partial): %v", err)
	}

	// 确认 → 运行：CDN 503 → 重试耗尽 → failed 终局（不进 recovery，零提交）。
	rt, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolved.PlanID})
	if err != nil {
		t.Fatalf("ConfirmRestorePlan: %v", err)
	}
	final := waitApplyTask(t, app, rt.TaskID)
	if final.Status != model.TaskStatusFailed {
		t.Fatalf("任务终态 = %s，期望 failed（problem=%+v）", final.Status, final.Problem)
	}
	if final.Problem == nil || final.Problem.Code != "err.download.rate_limited" {
		t.Fatalf("Problem = %+v，期望 err.download.rate_limited", final.Problem)
	}
	if final.MessageKey != "msg.task.restore.failed" {
		t.Fatalf("进度短语键 = %s", final.MessageKey)
	}
	run, err := app.GetApplyRun(ctx, rel.RelationID)
	if err != nil || run.State != model.ApplyRunFailed {
		t.Fatalf("运行头 state=%s（err=%v），期望 failed", run.State, err)
	}
	if h := appGetHealth(t, db, rel.RelationID); h != "healthy" {
		t.Fatalf("failed 终局后关系健康 = %s，期望 healthy（网络失败 ≠ 恢复面）", h)
	}
	head, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if head.Items[0].CommitID != commitHead {
		t.Fatalf("failed 终局不得产生提交：head=%s，期望 %s（零部分提交）", head.Items[0].CommitID, commitHead)
	}
	// 计划投影保持 resolved（failed ≠ committed），可重新确认。
	if got, err := app.GetRestorePlan(ctx, resolved.PlanID); err != nil || got.Status != "resolved" {
		t.Fatalf("failed 后计划投影 = %s（err=%v），期望 resolved", got.Status, err)
	}

	// CDN 恢复 → 同 plan 重新确认建新运行（failed 可重入，Q8）→ committed。
	cdn.Script(r60CDNPath) // 清除脚本，回落 SetFile 的 v0 字节
	re, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolved.PlanID})
	if err != nil {
		t.Fatalf("failed 后重确认: %v", err)
	}
	if re.TaskID == rt.TaskID {
		t.Fatal("failed 重确认应建新任务")
	}
	finalRe := waitApplyTask(t, app, re.TaskID)
	if finalRe.Status != model.TaskStatusSucceeded || finalRe.Outcome != model.TaskOutcomePartial {
		t.Fatalf("重试运行应 committed（partial：runtimeonly/local skip）: %s/%s %+v",
			finalRe.Status, finalRe.Outcome, finalRe.Problem)
	}
	// chrono jar 经 CDN 重取回 v0；skip 行保持 v2 现状；手放 loose 照删。
	if got, _ := os.ReadFile(filepath.Join(gameDir, "mods", "chrono-1.0.jar")); string(got) != r60JarV0 {
		t.Fatalf("重试后 chrono jar 应重取回 v0")
	}
	if got, _ := os.ReadFile(filepath.Join(gameDir, "mods", "runtimeonly-1.0.jar")); string(got) != "fake runtimeonly jar v2" {
		t.Fatalf("skip 行应保持 v2 现状")
	}
	if _, err := os.Stat(filepath.Join(gameDir, "mods", "loose-1.0.jar")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("手放 loose jar 应照删（err=%v）", err)
	}
	head, err = app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if head.Items[0].Kind != string(model.PlanRestore) || head.Items[0].CommitID != finalRe.CommitID {
		t.Fatalf("重试提交头 kind=%s id=%s", head.Items[0].Kind, head.Items[0].CommitID)
	}
}
// final1CommitID 读关系头提交（GetApplyRun.AttachCommit 后的头提交即最近提交）。
func final1CommitID(t *testing.T, app syncapp.Application, relationID string) string {
	t.Helper()
	page, err := app.ListCommits(context.Background(), relationID, ports.PageRequest{Limit: 1})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("ListCommits: %v", err)
	}
	return page.Items[0].CommitID
}

// appGetHealth 读关系健康（DB 直查；failed 终局断言「网络失败 ≠ 恢复面」）。
func appGetHealth(t *testing.T, db *sql.DB, relationID string) string {
	t.Helper()
	var health string
	if err := db.QueryRow(`SELECT health FROM relations WHERE id=?`, relationID).Scan(&health); err != nil {
		t.Fatal(err)
	}
	return health
}

// ---- 测试 4：partial 账目（红线④）+ staged 消费 + 重取 + 删除行执行 ----

// r60ModFixture 搭 mod 回滚夹具（v0）：chrono（CF sha256，重取主角）、local
//（手放 metafile 无 update 段 → user_object）、runtimeonly（unrecoverable）。
// 返回后由测试推进 v2（jar 漂移 / 新增手放 jar）。
func r60ModFixture(t *testing.T) (projectRoot, instanceDir, dataRoot string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instance")
	gameDir := filepath.Join(instanceDir, "minecraft")

	chronoMeta := r59CFMeta("Chrono", "chrono-1.0.jar", 369812, 7654321, "sha256", r59sha256(r60JarV0))
	writeFile(t, filepath.Join(projectRoot, "pack.toml"), fxR59PackToml)
	writeFile(t, filepath.Join(projectRoot, "index.toml"), r59IndexToml("chrono.pw.toml", "local.pw.toml"))
	writeFile(t, filepath.Join(projectRoot, "mods", "chrono.pw.toml"), chronoMeta)
	writeFile(t, filepath.Join(projectRoot, "mods", "local.pw.toml"), fxR59LocalMeta)
	writeFile(t, filepath.Join(instanceDir, "instance.cfg"), r59InstanceCfg)
	writeFile(t, filepath.Join(gameDir, "mods", "chrono-1.0.jar"), r60JarV0)
	writeFile(t, filepath.Join(gameDir, "mods", "local-thing-1.0.jar"), r60LocalV0)
	writeFile(t, filepath.Join(gameDir, "mods", "runtimeonly-1.0.jar"), r60RTOnlyV0)
	writeFile(t, filepath.Join(gameDir, "mods", ".index", "chrono-1.0.jar.pw.toml"),
		r59IndexMeta("Chrono", "chrono-1.0.jar", "sha256", r59sha256(r60JarV0)))
	dataRoot = filepath.Join(base, "userdata")
	return
}

const (
	r60LocalV0  = "fake local jar v0"
	r60RTOnlyV0 = "fake runtimeonly jar v0"
)

func TestRestore60PartialAccountAndDirty(t *testing.T) {
	projectRoot, instanceDir, dataRoot := r60ModFixture(t)
	cdn := download.NewFakeCDN()
	cdn.SetFile(r60CDNPath, []byte(r60JarV0))
	app, db := r60CDNStack(t, dataRoot, cdn)
	_ = db // CDN 注入栈的 db 句柄（mustInjectRestoreTarget 需要）
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)
	gameDir := filepath.Join(instanceDir, "minecraft")

	// 注入回滚目标（v0 快照照抄为基线；恢复途径按行指定）。
	recs := map[model.ResourceID]model.Recoverability{
		"mod:curseforge:369812":       model.RecoverabilityRedownload,
		"mod:path:mods/local.pw.toml": model.RecoverabilityRedownload,
		"mod:jar:runtimeonly-1.0.jar": model.RecoverabilityUnrecoverable,
	}
	commitID := mustInjectRestoreTarget(t, app, db, rel, model.RecoverabilityCAS, recs)

	// v2：chrono/local jar 漂移（仅 runtime 侧）；新增手放 loose jar（删除行
	// deletion_warn 主角）；runtimeonly jar 漂移（unrecoverable 行主角）。
	if err := os.WriteFile(filepath.Join(gameDir, "mods", "chrono-1.0.jar"), []byte(r60JarV2), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gameDir, "mods", ".index", "chrono-1.0.jar.pw.toml"),
		r59IndexMeta("Chrono", "chrono-1.0.jar", "sha256", r59sha256(r60JarV2)))
	if err := os.WriteFile(filepath.Join(gameDir, "mods", "local-thing-1.0.jar"), []byte("fake local jar v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, "mods", "runtimeonly-1.0.jar"), []byte("fake runtimeonly jar v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gameDir, "mods", "loose-1.0.jar"), "fake loose jar")
	mustScanAndWait(t, app, rel.RelationID)

	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commitID})
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	chrono := r59Item(t, draft, "mod:curseforge:369812")
	r59AssertMarker(t, chrono, model.MarkerRedownloadRequired, "")
	local := r59Item(t, draft, "mod:path:mods/local.pw.toml")
	r59AssertMarker(t, local, model.MarkerUserObjectRequired, model.MarkerReasonNoRedownloadInfo)
	rtOnly := r59Item(t, draft, "mod:jar:runtimeonly-1.0.jar")
	r59AssertMarker(t, rtOnly, model.MarkerUnrecoverable, "")
	loose := r59Item(t, draft, "mod:jar:loose-1.0.jar")
	if loose.ChangeKind != "delete" || !loose.DeletionWarn {
		t.Fatalf("loose 删除行应带不可重取警示: %+v", loose)
	}
	// skip 对删除行不合法（决议面只收 user_object/unrecoverable）。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft.PlanID, RequestedExactness: "allow_partial", SkipResourceIDs: []string{"mod:jar:loose-1.0.jar"},
	}); err == nil || errs.CodeOf(err) != "err.restore.skip_invalid" {
		t.Fatalf("skip(删除行) 应拒绝: %v", err)
	}

	// 补全 local（对字节进暂存锚）；runtimeonly 留待 skip。
	staged, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft.PlanID, ResourceID: "mod:path:mods/local.pw.toml",
		SourcePath: r59WriteTempFile(t, r60LocalV0),
	})
	if err != nil {
		t.Fatalf("StageUserObject: %v", err)
	}
	if !r59Item(t, staged, "mod:path:mods/local.pw.toml").Staged || staged.ExactFeasible {
		t.Fatalf("local staged=%v feasible=%v，期望 true/false（runtimeonly 未跳过）",
			r59Item(t, staged, "mod:path:mods/local.pw.toml").Staged, staged.ExactFeasible)
	}

	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft.PlanID, RequestedExactness: "allow_partial",
		SkipResourceIDs: []string{"mod:jar:runtimeonly-1.0.jar"},
	})
	if err != nil {
		t.Fatalf("ResolveRestorePlan(allow_partial): %v", err)
	}

	task := r60MustConfirmRestore(t, app, resolved.PlanID)
	if task.Status != model.TaskStatusSucceeded || task.Outcome != model.TaskOutcomePartial {
		t.Fatalf("partial 回滚任务 %s/%s（problem=%+v）", task.Status, task.Outcome, task.Problem)
	}
	head, err := app.GetCommit(ctx, rel.RelationID, task.CommitID)
	if err != nil {
		t.Fatal(err)
	}
	if head.Summary.Kind != string(model.PlanRestore) || head.Summary.Completeness != model.TaskOutcomePartial || head.Summary.RemainingChangeCnt != 1 {
		t.Fatalf("提交头 kind=%s completeness=%s remaining=%d，期望 restore/partial/1",
			head.Summary.Kind, head.Summary.Completeness, head.Summary.RemainingChangeCnt)
	}
	// 红线④：partial 后 relation 保持 dirty（缺失资源不谎报为成功恢复）。
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.State.DiffState != "dirty" {
		t.Fatalf("partial 后 diff_state = %s，期望 dirty", ws.State.DiffState)
	}
	// 执行面断言：重取/补全/删除行落盘，skip 行保持现状。
	if got, _ := os.ReadFile(filepath.Join(gameDir, "mods", "chrono-1.0.jar")); string(got) != r60JarV0 {
		t.Fatalf("chrono jar 应经 CDN 重取回 v0")
	}
	if got, _ := os.ReadFile(filepath.Join(gameDir, "mods", "local-thing-1.0.jar")); string(got) != r60LocalV0 {
		t.Fatalf("local jar 应经暂存锚补全回 v0")
	}
	if got, _ := os.ReadFile(filepath.Join(gameDir, "mods", "runtimeonly-1.0.jar")); string(got) != "fake runtimeonly jar v2" {
		t.Fatalf("skip 行应保持 v2 现状")
	}
	if _, err := os.Stat(filepath.Join(gameDir, "mods", "loose-1.0.jar")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("手放 loose jar 应照删（err=%v）", err)
	}
}

// ---- 测试 5：CAS miss → 补全就绪面 → exact（红线②）----

func TestRestore60CASMissStagedCompletion(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	// round1 → commit1；round2 删 runtime b（before 保全进 CAS）→ commit2。
	plan1 := mustResolveApplyPlan(t, app, rel, round1Choices)
	if waitApplyTask(t, app, mustConfirm(t, app, plan1.PlanID).TaskID).Status != model.TaskStatusSucceeded {
		t.Fatal("round1 应成功")
	}
	if err := os.Remove(filepath.Join(instanceDir, "minecraft", "config", "b.toml")); err != nil {
		t.Fatal(err)
	}
	mustScanAndWait(t, app, rel.RelationID)
	plan2 := mustResolveApplyPlan(t, app, rel, nil)
	if waitApplyTask(t, app, mustConfirm(t, app, plan2.PlanID).TaskID).Status != model.TaskStatusSucceeded {
		t.Fatal("round2 应成功")
	}
	commits, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}

	// 直删 CAS 对象（夹具提供的 CAS miss 构造路径）：b 的 v0 字节消失。
	digest := r59sha256(fxApplyB)
	objectPath := filepath.Join(dataRoot, "objects", "sha256", digest[:2], digest)
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("删除 CAS 对象: %v", err)
	}

	// prepare：CAS miss → user_object_required + no_redownload_info，
	// exact_infeasible + blocked_by（draft 时点静态评估）。
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commits.Items[1].CommitID})
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	b := r59Item(t, draft, "file:config/b.toml")
	r59AssertMarker(t, b, model.MarkerUserObjectRequired, model.MarkerReasonNoRedownloadInfo)
	if b.ExpectedDigest != digest {
		t.Fatalf("验收摘要 = %q，期望 %q", b.ExpectedDigest, digest)
	}
	if draft.ExactFeasible || len(draft.BlockedBy) != 1 || draft.BlockedBy[0].ResourceID != "file:config/b.toml" {
		t.Fatalf("就绪面/阻塞清单: feasible=%v blocked=%+v", draft.ExactFeasible, draft.BlockedBy)
	}
	// exact 决议遇未就绪面前置拒绝（ADR-0006 §4，不在 Confirm 才拦截）。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft.PlanID, RequestedExactness: "exact"}); err == nil ||
		errs.CodeOf(err) != "err.restore.exact_infeasible" {
		t.Fatalf("exact 应前置拒绝: %v", err)
	}
	// 错字节 hash_mismatch（{0}=期望摘要，可重试）→ 对字节 staged 就绪面翻转。
	if _, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft.PlanID, ResourceID: "file:config/b.toml", SourcePath: r59WriteTempFile(t, "corrupted"),
	}); err == nil || errs.CodeOf(err) != "err.userobject.hash_mismatch" {
		t.Fatalf("错字节应 hash_mismatch: %v", err)
	}
	staged, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft.PlanID, ResourceID: "file:config/b.toml", SourcePath: r59WriteTempFile(t, fxApplyB),
	})
	if err != nil {
		t.Fatalf("对字节补全: %v", err)
	}
	if !r59Item(t, staged, "file:config/b.toml").Staged || !staged.ExactFeasible {
		t.Fatal("对字节后 staged/ExactFeasible 应翻转 true")
	}
	// 字节进 staging 不回填 CAS（对象文件保持缺失）。
	if _, err := os.Stat(objectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("补全字节不得进 CAS（err=%v）", err)
	}

	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft.PlanID, RequestedExactness: "exact"})
	if err != nil {
		t.Fatalf("ResolveRestorePlan(exact): %v", err)
	}
	task := r60MustConfirmRestore(t, app, resolved.PlanID)
	if task.Status != model.TaskStatusSucceeded || task.Outcome != model.TaskOutcomeExact {
		t.Fatalf("补全后 exact 回滚应成功: %s/%s %+v", task.Status, task.Outcome, task.Problem)
	}
	got, rerr := os.ReadFile(filepath.Join(instanceDir, "minecraft", "config", "b.toml"))
	if rerr != nil || string(got) != fxApplyB {
		t.Fatalf("b.toml = %q（err=%v），期望 %q", string(got), rerr, fxApplyB)
	}
	// 写回后 CAS 对象由提交收口期基线内容摄取重建（票 #93 泛化：项目侧全部
	// 表示统一入 CAS；staging 补全路径本身仍零 CAS 污染，ADR-0005 §7）——
	// 后续回滚到本提交时 b 行自动 restorable_from_cas，不再依赖补全。
	if _, err := os.Stat(objectPath); err != nil {
		t.Fatalf("写回后 CAS 对象应由基线内容摄取重建: %v", err)
	}
}
