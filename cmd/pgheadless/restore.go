package main

// pgheadless -restore（P3 票 #60；验收规格 §3）：A 口径回滚四场景断言链——
// 离线面（零 CDN，回滚行全部 restorable_from_cas / user_object / 删除行），
// 在同一数据目录顺序执行（③ 先于 ②：partial 的 skip 残留按 ADR-0006 §9 保持
// dirty，不干扰后续 exact 场景的就绪面）：
//
//	场景① exact 经 CAS：D1 漂移（改项目侧 pg-a + 删运行端 pg-b）→ c2 →
//	      回滚 c1 → 逐字节复验 + 历史不改写（ListCommits 新增 kind=restore
//	      行且原记录原样）；
//	场景③ 补全就绪面（红线②）：D2 删运行端 pg-c（v0 保全进 CAS）→ c3 →
//	      直删 CAS 对象构造 miss → user_object_required + exact_infeasible
//	      前置拒绝 → 错字节 hash_mismatch → 对字节 staged 翻转 → exact
//	      committed（写回零 CAS 回填）；
//	场景② partial（红线④）：D3 纯外部漂移（运行端 pg-e 改写 → CAS miss →
//	      user_object_required 行 + 手放 jar 的 deletion_warn 删除行）→ 回滚
//	      c1：skip + 删除行执行 → committed kind=partial + relation 保持
//	      dirty；
//	场景④ 重做语义（ADR-0006 §1）：回滚目标 = head（restore 提交，API 合法）
//	      → 空差异计划合法收口 committed。
//
// 夹具：pgfixture -plain-mods N 生成「无 CF 声明 mod」变体（user_object 行
// 来源）；受管 config 面用 pg-*.toml 种子文件（config 规则由关系建议携带，
// 须在首次扫描前落位）。全链任一断言不符即返回 error（main 非零退出）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/errs"
)

// restore 轮询参数（沿用 -apply 节奏）。
const (
	restorePollInterval = 100 * time.Millisecond
	restorePollTimeout  = 2 * time.Minute
)

// 夹具种子文件内容（pg-*.toml，config 规则受管）。
const (
	rstPgA  = "pg_a = \"v1\"\n"
	rstPgA2 = "pg_a = \"v2\"\n"
	rstPgB  = "pg_b = \"v1\"\n"
	rstPgC  = "pg_c = \"v1\"\n"
	rstPgE  = "pg_e = \"v1\"\n"
	rstPgE2 = "pg_e = \"v2\"\n"
)

func rsha256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// rstSeedFiles 写入受管 config 种子：pg-a 项目侧单侧、pg-b 运行侧单侧、
// pg-c 双侧（initialize 后三者在基线中双侧 present，rec=cas）。
func rstSeedFiles(projectRoot, gameDir string) error {
	for path, content := range map[string]string{
		filepath.Join(projectRoot, "config", "pg-a.toml"): rstPgA,
		filepath.Join(gameDir, "config", "pg-b.toml"):     rstPgB,
		filepath.Join(projectRoot, "config", "pg-c.toml"): rstPgC,
		filepath.Join(gameDir, "config", "pg-c.toml"):     rstPgC,
		filepath.Join(gameDir, "config", "pg-e.toml"):     rstPgE,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runRestoreChain 执行 -restore 四场景断言链。rel 为已登记 Relation（主链路
// 已完成首轮扫描——种子文件须在首次扫描前落位）。
func runRestoreChain(ctx context.Context, app syncapp.Application, rel view.RelationView,
	projectRoot, instanceDir, dataRoot string) error {

	projCfg := filepath.Join(projectRoot, "config")
	gameDir := filepath.Join(instanceDir, "minecraft")
	gameCfg := filepath.Join(gameDir, "config")

	fmt.Println("== -restore == 场景链开始（离线面：cas / user_object / 删除行）")

	// ---- R0：initialize → c1（种子文件入基线，rec=cas）----
	// mod 冲突一律 skip（P2 -apply 划线先例：离线面不物化 mod——skip 只是不
	// 建写操作，基线随复扫收录现实，后续 restore 判定面不受影响）。
	c1, err := rstApplyRound(ctx, app, rel)
	if err != nil {
		return fmt.Errorf("R0 initialize: %w", err)
	}

	// ---- 场景①：exact 经 CAS ----
	// D1：改项目侧 pg-a（sync 传播运行端）+ 删运行端 pg-b（传播删项目侧）。
	if err := os.WriteFile(filepath.Join(projCfg, "pg-a.toml"), []byte(rstPgA2), 0o644); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(gameCfg, "pg-b.toml")); err != nil {
		return err
	}
	c2, err := rstApplyRound(ctx, app, rel)
	if err != nil {
		return fmt.Errorf("D1 apply: %w", err)
	}
	draft1, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景① PrepareRestore: %w", err)
	}
	if err := rstAssertMarker(draft1, "file:config/pg-a.toml", model.MarkerRestorableFromCAS); err != nil {
		return fmt.Errorf("场景①: %w", err)
	}
	if err := rstAssertMarker(draft1, "file:config/pg-b.toml", model.MarkerRestorableFromCAS); err != nil {
		return fmt.Errorf("场景①: %w", err)
	}
	if !draft1.ExactFeasible {
		return fmt.Errorf("场景① 计划应全部就绪（exact feasible）")
	}
	resolved1, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft1.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return fmt.Errorf("场景① resolve exact: %w", err)
	}
	cR1, err := rstConfirmAndRestore(ctx, app, rel, resolved1.PlanID, model.TaskOutcomeExact)
	if err != nil {
		return fmt.Errorf("场景① restore: %w", err)
	}
	// 逐字节复验（验收红线①）：双端写回 c1 状态。
	for path, want := range map[string]string{
		filepath.Join(projCfg, "pg-a.toml"): rstPgA,
		filepath.Join(gameCfg, "pg-a.toml"): rstPgA,
		filepath.Join(projCfg, "pg-b.toml"): rstPgB,
		filepath.Join(gameCfg, "pg-b.toml"): rstPgB,
	} {
		got, rerr := os.ReadFile(path)
		if rerr != nil || string(got) != want {
			return fmt.Errorf("场景① 逐字节复验 %s = %q（err=%v），期望 %q", path, string(got), rerr, want)
		}
	}
	// 历史不改写：新增 kind=restore 行在前，原记录原样。
	if err := rstAssertHistory(ctx, app, rel.RelationID, cR1, c2, c1); err != nil {
		return err
	}
	if err := rstAssertClean(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("场景①: %w", err)
	}
	fmt.Println("== 场景① exact 经 CAS == 通过（逐字节+digest 复验 + 历史不改写）")

	// ---- 场景③：补全就绪面（红线②）----
	// D2：删运行端 pg-c（v0 保全进 CAS）→ c3 → 回滚 c1。
	if err := os.Remove(filepath.Join(gameCfg, "pg-c.toml")); err != nil {
		return err
	}
	if _, err := rstApplyRound(ctx, app, rel); err != nil {
		return fmt.Errorf("D2 apply: %w", err)
	}
	// prepare#1：CAS 实存 → restorable_from_cas。
	draft3a, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景③ PrepareRestore: %w", err)
	}
	pgC := rstFindItem(draft3a, "file:config/pg-c.toml")
	if pgC == nil || pgC.Marker != model.MarkerRestorableFromCAS {
		return fmt.Errorf("场景③ pg-c 行应 cas: %+v", pgC)
	}
	// 直删 CAS 对象（验收规格 §3「CAS miss 构造」路径）→ prepare#2 降标
	// user_object_required + no_redownload_info，exact_infeasible + blocked_by。
	digest := rsha256(rstPgC)
	objectPath := filepath.Join(dataRoot, "objects", "sha256", digest[:2], digest)
	if err := os.Remove(objectPath); err != nil {
		return fmt.Errorf("场景③ 直删 CAS 对象: %w", err)
	}
	draft3, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景③ PrepareRestore(miss): %w", err)
	}
	pgC = rstFindItem(draft3, "file:config/pg-c.toml")
	if pgC == nil || pgC.Marker != model.MarkerUserObjectRequired || pgC.MarkerReason != model.MarkerReasonNoRedownloadInfo {
		return fmt.Errorf("场景③ CAS miss 应降标 user_object_required: %+v", pgC)
	}
	if draft3.ExactFeasible || len(draft3.BlockedBy) == 0 {
		return fmt.Errorf("场景③ 应 exact_infeasible + blocked_by 非空")
	}
	// exact 决议遇未就绪面前置拒绝（ADR-0006 §4）。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft3.PlanID, RequestedExactness: "exact"}); err == nil ||
		errs.CodeOf(err) != "err.restore.exact_infeasible" {
		return fmt.Errorf("场景③ exact 应前置拒绝: %v", err)
	}
	// 错字节 → hash_mismatch；对字节 → staged + ExactFeasible 翻转。
	wrong := filepath.Join(dataRoot, "rst-wrong.bin")
	right := filepath.Join(dataRoot, "rst-right.bin")
	if err := os.WriteFile(wrong, []byte("corrupted bytes"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(right, []byte(rstPgC), 0o644); err != nil {
		return err
	}
	if _, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft3.PlanID, ResourceID: string(pgC.ResourceID), SourcePath: wrong,
	}); err == nil || errs.CodeOf(err) != "err.userobject.hash_mismatch" {
		return fmt.Errorf("场景③ 错字节应 hash_mismatch: %v", err)
	}
	staged, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft3.PlanID, ResourceID: string(pgC.ResourceID), SourcePath: right,
	})
	if err != nil {
		return fmt.Errorf("场景③ 对字节补全: %w", err)
	}
	if item := rstFindItem(staged, "file:config/pg-c.toml"); item == nil || !item.Staged || !staged.ExactFeasible {
		return fmt.Errorf("场景③ staged/ExactFeasible 应翻转")
	}
	// staged 字节零 CAS 污染（对象文件保持缺失）。
	if _, err := os.Stat(objectPath); !os.IsNotExist(err) {
		return fmt.Errorf("场景③ 补全字节不得进 CAS（err=%v）", err)
	}
	resolved3, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft3.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return fmt.Errorf("场景③ resolve exact: %w", err)
	}
	cR2, err := rstConfirmAndRestore(ctx, app, rel, resolved3.PlanID, model.TaskOutcomeExact)
	if err != nil {
		return fmt.Errorf("场景③ restore: %w", err)
	}
	if got, rerr := os.ReadFile(filepath.Join(gameCfg, "pg-c.toml")); rerr != nil || string(got) != rstPgC {
		return fmt.Errorf("场景③ pg-c 复验 = %q（err=%v）", string(got), rerr)
	}
	// staging 面消费后 CAS 对象文件仍缺失（写回零 CAS 回填）。
	if _, err := os.Stat(objectPath); !os.IsNotExist(err) {
		return fmt.Errorf("场景③ 写回后 CAS 对象应仍缺失（err=%v）", err)
	}
	fmt.Println("== 场景③ 补全就绪面 == 通过（CAS miss → hash_mismatch → staged 翻转 → exact committed）")

	// ---- 场景④：重做语义（ADR-0006 §1；先于场景②执行——partial 的 skip 残留按
// ADR-0006 §9 保持 dirty，head 为目标的空差异断言要求基线与现实一致）----
	// 回滚目标 = head（cR2，restore 提交，API 合法）→ 空差异计划合法收口。
	draft4, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: cR2})
	if err != nil {
		return fmt.Errorf("场景④ PrepareRestore: %w", err)
	}
	if len(draft4.Items) != 0 {
		return fmt.Errorf("场景④ head 为目标应为空差异计划: %d 行", len(draft4.Items))
	}
	resolved4, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft4.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return fmt.Errorf("场景④ resolve: %w", err)
	}
	cR3, err := rstConfirmAndRestore(ctx, app, rel, resolved4.PlanID, model.TaskOutcomeExact)
	if err != nil {
		return fmt.Errorf("场景④ restore: %w", err)
	}
	// 历史追加不改写：cR2 → cR3 相邻且 kind=restore。
	page, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 10})
	if err != nil {
		return err
	}
	if len(page.Items) < 2 || page.Items[0].CommitID != cR3 || page.Items[1].CommitID != cR2 ||
		page.Items[0].Kind != string(model.PlanRestore) {
		return fmt.Errorf("场景④ 历史头两行应 cR3/cR2（kind=restore）")
	}
	if err := rstAssertClean(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("场景④: %w", err)
	}
	fmt.Printf("== 场景④ 重做语义 == 通过（head 为目标空差异合法收口 cR3=%s）\n", cR3)

	// ---- 场景②：partial（红线④）----
	// D3（纯外部漂移，不经 sync——restore 判定面直接观察）：运行端 pg-e 改写
	//（v0 从未保全 → CAS miss → user_object_required）+ 手放 jar（deletion_warn
	// 删除行来源；无 metafile 无 .index 的裸 jar 不进 sync 计划面）。手放 jar
	// 与 pg-e 漂移同批进入 restore 输入快照。
	if err := os.WriteFile(filepath.Join(gameCfg, "pg-e.toml"), []byte(rstPgE2), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gameDir, "mods", "hand-placed-1.0.jar"), []byte("hand placed jar"), 0o644); err != nil {
		return err
	}
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return err
	}
	// 回滚 c1：pg-e 行 modify（运行侧漂移、CAS miss → user_object_required，
	// 未补全 → 合法 skip）；手放 jar 为删除行（目标 absent 当前 present、无重
	// 取信息 → deletion_warn，不可 skip，照删）。
	draft2, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景② PrepareRestore: %w", err)
	}
	var pgERow, handRow *view.RestorePlanItemView
	for i := range draft2.Items {
		it := &draft2.Items[i]
		switch {
		case it.ResourceID == "file:config/pg-e.toml":
			pgERow = it
		case it.ChangeKind == "delete" && it.DeletionWarn:
			handRow = it // 手放 jar：运行端-only、无重取信息 → 不可重取警示
		}
	}
	if pgERow == nil || pgERow.Marker != model.MarkerUserObjectRequired {
		return fmt.Errorf("场景② pg-e 行应 user_object_required: %+v", pgERow)
	}
	if pgERow.ExpectedDigest == "" {
		return fmt.Errorf("场景② 补全行应透出验收摘要")
	}
	if handRow == nil {
		return fmt.Errorf("场景② 计划缺手放 jar 的 deletion_warn 删除行: %+v", draft2.Items)
	}
	resolved2, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft2.PlanID, RequestedExactness: "allow_partial",
		SkipResourceIDs: []string{string(pgERow.ResourceID)},
	})
	if err != nil {
		return fmt.Errorf("场景② resolve allow_partial: %w", err)
	}
	cR4, err := rstConfirmAndRestore(ctx, app, rel, resolved2.PlanID, model.TaskOutcomePartial)
	if err != nil {
		return fmt.Errorf("场景② restore: %w", err)
	}
	// 红线④：partial 后 relation 保持 dirty（skip 行单侧差异对基线保持可见，
	// 不被复扫快照「收编」为 converged clean）。
	if err := rstAssertDirty(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("场景②: %w", err)
	}
	// skip 行保持漂移现状（v2）；手放 jar 照删。
	if got, rerr := os.ReadFile(filepath.Join(gameCfg, "pg-e.toml")); rerr != nil || string(got) != rstPgE2 {
		return fmt.Errorf("场景② skip 行应保持 v2 漂移现状: %q（err=%v）", string(got), rerr)
	}
	if _, err := os.Stat(filepath.Join(gameDir, "mods", "hand-placed-1.0.jar")); !os.IsNotExist(err) {
		return fmt.Errorf("场景② 手放 jar 应照删（err=%v）", err)
	}
	fmt.Printf("== 场景② partial + dirty == 通过（kind=partial + relation dirty + deletion_warn 删除行执行，cR4=%s）\n", cR4)
	fmt.Println("== -restore == 四场景全部通过（链末保持 dirty：场景② skip 残留的诚实投影）")
	return nil
}

// ---- 链路辅助 ----

// rstApplyRound 执行一轮同步（扫描 → 计划 → 决议 → 确认 → Apply → committed），
// 返回新提交 id。initialize 轮按「mod skip、其余 project 优先/运行时兜底」决议
// （P2 -apply 划线先例：离线面不物化 mod）；sync 轮无冲突直接决议。
func rstApplyRound(ctx context.Context, app syncapp.Application, rel view.RelationView) (string, error) {
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return "", err
	}
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return "", err
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		return "", fmt.Errorf("PrepareSync: %w", err)
	}
	resolutions := make([]model.Resolution, 0, len(draft.Conflicts))
	for _, c := range draft.Conflicts {
		choice := model.ChoiceInitializeFromRuntime
		if c.Project != nil {
			if normalize.KindOfResourceID(c.ResourceID) == model.ResourceMod {
				choice = model.ChoiceSkip // P2 划线：离线面不物化 mod
			} else {
				choice = model.ChoiceInitializeFromProject
			}
		}
		resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: choice})
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		return "", fmt.Errorf("ResolvePlan: %w", err)
	}
	for _, op := range resolved.Operations {
		fmt.Println("[DBG-RST]", op.ID, op.Kind, op.Materialization, op.ResourceID, op.Preconditions)
	}
	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	if err != nil {
		return "", fmt.Errorf("ConfirmPlan: %w", err)
	}
	final, err := rstWaitTask(ctx, app, tv.TaskID)
	if err != nil {
		return "", err
	}
	if final.Status != model.TaskStatusSucceeded {
		return "", fmt.Errorf("apply 任务终态 %s（problem=%+v）", final.Status, final.Problem)
	}
	head, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 1})
	if err != nil || len(head.Items) == 0 {
		return "", fmt.Errorf("ListCommits: %w", err)
	}
	return head.Items[0].CommitID, nil
}

// rstConfirmAndRestore 确认回滚计划并轮询至终态，断言任务 kind 与提交头账目
// （kind=restore、完整性），返回新提交 id。
func rstConfirmAndRestore(ctx context.Context, app syncapp.Application, rel view.RelationView,
	planID string, wantOutcome string) (string, error) {

	tv, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: planID})
	if err != nil {
		return "", fmt.Errorf("ConfirmRestorePlan: %w", err)
	}
	if tv.Kind != model.TaskKindRestore {
		return "", fmt.Errorf("任务 kind = %s，期望 restore", tv.Kind)
	}
	final, err := rstWaitTask(ctx, app, tv.TaskID)
	if err != nil {
		return "", err
	}
	if final.Status != model.TaskStatusSucceeded || final.Outcome != wantOutcome {
		return "", fmt.Errorf("restore 任务终态 %s/%s（problem=%+v），期望 succeeded/%s",
			final.Status, final.Outcome, final.Problem, wantOutcome)
	}
	head, err := app.GetCommit(ctx, rel.RelationID, final.CommitID)
	if err != nil {
		return "", err
	}
	if head.Summary.Kind != string(model.PlanRestore) || head.Summary.Completeness != wantOutcome {
		return "", fmt.Errorf("提交头 kind=%s completeness=%s", head.Summary.Kind, head.Summary.Completeness)
	}
	return final.CommitID, nil
}

// rstAssertHistory 断言历史追加不改写：新增 kind=restore 行在前，原两行原样。
func rstAssertHistory(ctx context.Context, app syncapp.Application, relationID, newID, first, second string) error {
	page, err := app.ListCommits(ctx, relationID, ports.PageRequest{Limit: 10})
	if err != nil {
		return err
	}
	if len(page.Items) < 3 {
		return fmt.Errorf("历史应 ≥3 行，got %d", len(page.Items))
	}
	if page.Items[0].CommitID != newID || page.Items[0].Kind != string(model.PlanRestore) {
		return fmt.Errorf("新头行应 kind=restore 的 %s: %+v", newID, page.Items[0])
	}
	if page.Items[1].CommitID != first || page.Items[2].CommitID != second {
		return fmt.Errorf("原历史被改写: %+v", page.Items[1:3])
	}
	return nil
}

// rstAssertClean / rstAssertDirty 断言关系 diff 面（exact 收口归零 / partial 保持 dirty）。
func rstAssertClean(ctx context.Context, app syncapp.Application, relationID string) error {
	ws, err := app.GetWorkspace(ctx, relationID)
	if err != nil {
		return err
	}
	if ws.State.DiffState != "clean" {
		return fmt.Errorf("diff_state = %s，期望 clean", ws.State.DiffState)
	}
	return nil
}

func rstAssertDirty(ctx context.Context, app syncapp.Application, relationID string) error {
	ws, err := app.GetWorkspace(ctx, relationID)
	if err != nil {
		return err
	}
	if ws.State.DiffState != "dirty" {
		return fmt.Errorf("partial 后 diff_state = %s，期望 dirty（红线④：不谎报 clean）", ws.State.DiffState)
	}
	return nil
}

// rstScan 扫描至完成。
func rstScan(ctx context.Context, app syncapp.Application, relationID string) error {
	tv, err := app.StartScan(ctx, relationID)
	if err != nil {
		return fmt.Errorf("StartScan: %w", err)
	}
	if _, err := rstWaitTask(ctx, app, tv.TaskID); err != nil {
		return err
	}
	return nil
}

// rstWaitTask 轮询任务至任一终态。
func rstWaitTask(ctx context.Context, app syncapp.Application, taskID string) (view.TaskView, error) {
	deadline := time.Now().Add(restorePollTimeout)
	for time.Now().Before(deadline) {
		tv, err := app.GetTask(ctx, taskID)
		if err != nil {
			return view.TaskView{}, err
		}
		switch tv.Status {
		case model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
			return tv, nil
		}
		time.Sleep(restorePollInterval)
	}
	return view.TaskView{}, fmt.Errorf("任务 %s 超时未结束", taskID)
}

// rstFindItem 按 ResourceID 查判定行（缺失返回 nil）。
func rstFindItem(p view.RestorePlanView, resourceID string) *view.RestorePlanItemView {
	for i := range p.Items {
		if string(p.Items[i].ResourceID) == resourceID {
			return &p.Items[i]
		}
	}
	return nil
}

// rstAssertMarker 断言行标记。
func rstAssertMarker(p view.RestorePlanView, resourceID string, marker model.RestoreMarker) error {
	it := rstFindItem(p, resourceID)
	if it == nil {
		return fmt.Errorf("计划缺少 %s 判定行", resourceID)
	}
	if it.Marker != marker {
		return fmt.Errorf("%s 判定 = %s，期望 %s", resourceID, it.Marker, marker)
	}
	return nil
}
