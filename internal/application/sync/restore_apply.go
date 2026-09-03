package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/download"
	"packgradle/internal/errs"
	"packgradle/internal/syncstage"
)

// restore 运行执行器（ADR-0006 §7–§9 + 契约 06 §3.4/§6；票 #60）。复用 P2
// 全套管线（apply_runs/operation_journal/staging/原子写/sha256 复核/所有权
// 证明/恢复探测/acknowledge），不另起写路径；编排沿 runApply（apply.go）同构：
//
//	staged（前置条件复核 + 删除面 before 保全 + 写回内容暂存复核 + proof）
//	→ applying（复用 runApplyingBatches 批量化两段式）
//	→ verifying（复扫逐操作字节复验 + 非操作资源漂移断言）
//	→ committed（单 RunInTx：验证快照 + 结果基线 + kind=restore 提交 + head
//	  推进 + run 终态；事务提交成功后清 staging 并发布 relation_invalidated）。
//
// 写回内容三类来源（行 marker 决定，ADR-0006 §10.2 staging 边界复核）：
//   - restorable_from_cas：CAS 对象流（零网络零用户介入），StageContent 按目标
//     digest 复核——对象缺失/字节被篡改即该操作失败；
//   - redownload_required：runtime 侧经票 #58 引擎直链重取（已过声明 hash 校验
//     的字节），StageContent 以目标 digest 二次复核；project 侧（metafile）不在
//     CDN 上、mod 字节不进 CAS（ADR-0005 §7），其目标内容缺失即操作失败；
//   - user_object_required：消费计划暂存锚（restoreStagingAnchor，根 draft 的
//     plan_id）上已验收的用户字节，StageContent 再次复核。
//
// 失败语义（契约 06 §6，Q8——与 sync 剔除语义的刻意不对称，ADR-0008 §7）：
//   - staging 相位（prepared，尚未触碰任何目标文件）任一操作失败 ⇒ 整场
//     failed 终局：run=failed + task=failed + Problem 承载原因码（下载失败为
//     err.download.*），零部分提交、不进 recovery_required、不标关系健康；
//     同 plan 可重新 Confirm 建新运行（暂存锚上的用户字节跨运行延续）；
//   - applying 及之后失败仍走 ADR-0004 恢复矩阵（recoverRestore：文件一致性
//     风险面，recovery_required 等恢复探测/人工确认）。

// startRestore 在 ConfirmRestorePlan 提交成功后启动引擎协程接管任务
//（startApply 先例：ctx WithoutCancel 派生，取消句柄注册进 runner）。
func (a *App) startRestore(t model.Task) {
	restoreCtx, cancel := context.WithCancel(ctxWithoutCancel(context.Background()))
	a.runner.RegisterCancel(t.TaskID, cancel)
	go func() {
		defer a.runner.UnregisterCancel(t.TaskID)
		a.runRestore(restoreCtx, t)
	}()
}

// runRestore 执行 restore 运行编排（staged → applying → verifying → committed）。
func (a *App) runRestore(ctx context.Context, queued model.Task) {
	// 同 Relation 串行互斥（ADR-0004 §6/ADR-0006 §8）：与 Scan/Apply 共用同一把关系锁。
	gate := a.relationGate(queued.RelationID)
	gate.Lock()
	defer gate.Unlock()

	commitCtx := context.WithoutCancel(ctx)
	timing := view.ApplyTimingView{RelationID: queued.RelationID}
	runStart := time.Now()
	defer func() {
		timing.TotalMs = time.Since(runStart).Milliseconds()
		a.recordApplyTiming(timing)
	}()

	// 接管检查（runApply 同构）：仅 queued 任务可被引擎拾起。
	t, err := a.deps.Tasks.Get(ctx, queued.TaskID)
	if err != nil {
		slog.Warn("restore: 读取任务失败，放弃接管", "task", queued.TaskID, "err", err)
		return
	}
	if t.Status != model.TaskStatusQueued {
		if t.Status == model.TaskStatusCancelled {
			a.abandonCancelledRun(commitCtx, t)
		}
		return
	}
	run, err := a.deps.ApplyRuns.Get(ctx, t.TaskID)
	if err != nil {
		slog.Warn("restore: 读取任务的运行头失败", "task", t.TaskID, "err", err)
		return
	}
	if run.State != model.ApplyRunPrepared {
		return // 非初始态（重启后由恢复管线裁决）
	}

	// 计划/关系/端点/输入快照/目标基线装载。restore 计划的 BaseBaselineID 即
	// 回滚目标提交的 result baseline（写回目标，双端强一致化，ADR-0006 §1）。
	// 装载失败发生在 staging 相位（零目标写入）：failed 终局，无恢复面可言。
	plan, err := a.deps.Plans.Get(ctx, run.PlanID)
	if err != nil {
		a.failRestoreFailed(commitCtx, t, run, resultPreconditionViolated, nil,
			fmt.Errorf("读取计划 %s: %w", run.PlanID, err))
		return
	}
	rel, err := a.deps.Relations.Get(ctx, run.RelationID)
	if err != nil {
		a.failRestoreFailed(commitCtx, t, run, resultIOError, nil, fmt.Errorf("读取关系: %w", err))
		return
	}
	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	rt, err2 := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil || err2 != nil {
		a.failRestoreFailed(commitCtx, t, run, resultIOError, nil, fmt.Errorf("读取端点: %v/%v", err, err2))
		return
	}
	snapP, err := a.deps.Snapshots.Get(ctx, plan.InputProjectSnapshotID)
	if err != nil {
		a.failRestoreFailed(commitCtx, t, run, resultPreconditionViolated, nil, fmt.Errorf("读取输入快照: %w", err))
		return
	}
	snapR, err := a.deps.Snapshots.Get(ctx, plan.InputRuntimeSnapshotID)
	if err != nil {
		a.failRestoreFailed(commitCtx, t, run, resultPreconditionViolated, nil, fmt.Errorf("读取输入快照: %w", err))
		return
	}
	base, err := a.deps.Baselines.Get(ctx, plan.BaseBaselineID)
	if err != nil {
		a.failRestoreFailed(commitCtx, t, run, resultPreconditionViolated, nil, fmt.Errorf("读取目标基线: %w", err))
		return
	}

	state := run.State
	// staging 相位失败出口：整场 failed 终局（零部分提交，网络/取数失败 ≠ 恢复面）。
	failStaging := func(code string, args []string, cause error) {
		a.failRestoreFailed(commitCtx, t, run, code, args, cause)
	}
	// applying 及之后失败出口：ADR-0004 恢复矩阵（文件一致性风险面）。
	failRun := func(code string, cause error) {
		a.recoverRestore(commitCtx, t, run, state, code, cause)
	}
	// 进度落库用 commitCtx（runApply 同构：取消的响应点是显式的操作边界检查，
	// 而不是记账写入被 ctx 取消连带触发失败面）。
	advance := func(tt model.Task) bool {
		next, err := a.runner.Update(commitCtx, tt)
		if err == nil {
			t = next
			return true
		}
		if state == model.ApplyRunPrepared {
			failStaging(resultIOError, nil, fmt.Errorf("任务状态更新失败: %w", err))
		} else {
			failRun(resultIOError, fmt.Errorf("任务状态更新失败: %w", err))
		}
		return false
	}

	total := len(plan.Operations)
	t.Status = model.TaskStatusRunning
	t.CanCancel = true
	t.Total = total
	t.Completed = 0
	t.Phase = "staging"
	t.MessageKey = "msg.task.restore.staging"
	if !advance(t) {
		return
	}

	// ---- staged：前置条件复核 + 删除面保全 + 写回内容暂存复核 + proof ----
	stageStart := time.Now()
	stgRun, err := syncstage.OpenRun(a.deps.StagingRoot, t.TaskID)
	if err != nil {
		failStaging(applyResultCode(err), nil, fmt.Errorf("打开暂存运行: %w", err))
		return
	}
	projAct, err := syncstage.NewActions(stgRun, proj.RootPath)
	if err != nil {
		failStaging(applyResultCode(err), nil, err)
		return
	}
	rtAct, err := syncstage.NewActions(stgRun, rt.RootPath)
	if err != nil {
		failStaging(applyResultCode(err), nil, err)
		return
	}
	actionsBySide := map[model.Side]*syncstage.Actions{
		model.SideProject: projAct, model.SideRuntime: rtAct,
	}
	rootBySide := map[model.Side]string{model.SideProject: proj.RootPath, model.SideRuntime: rt.RootPath}
	snaps := map[model.Side]model.ObservedSnapshot{model.SideProject: snapP, model.SideRuntime: snapR}

	// 操作推导：skip 决议资源剔出本场，写回来源按行 marker 分派。
	// 用户补全字节的暂存锚 = 根 draft 的 plan_id（#59 resolve 产新 plan_id 后
	// 锚仍指根 draft 的 StagingPlanID——执行消费同用 restoreStagingAnchor）。
	anchorDir := filepath.Join(a.deps.StagingRoot, restoreStagingAnchor(plan))
	keepPlans, skipIDs := deriveRestoreFilePlans(plan, &base, snapP, snapR, proj.RootPath, rt.RootPath, anchorDir)

	// 重取相位（redownload 行）：批量并发经票 #58 引擎取数（.part 落运行暂存
	// downloads/ 子目录，run 内续传、跨 run 不复用）。restore 无剔除语义：
	// 任一行取数失败 = 整场 failed 终局（首个失败行按计划序定因）。
	if err := a.fetchRestoreDownloads(ctx, stgRun, keepPlans); err != nil {
		failStaging(resultCancelled, nil, fmt.Errorf("重取相位中止: %w", err))
		return
	}

	staged := stageRestoreOperations(ctx, a, stgRun, rel.RelationID, keepPlans, snaps, rootBySide)
	timing.StagingMs = time.Since(stageStart).Milliseconds()
	timing.OperationCount = total

	// 事实落库（runApply 同构：journal 批 + 运行级恢复引用）；首个失败行驱动
	// failed 终局——staging 相位零目标写入，无恢复面可言。
	rows, failedIdx := buildJournalRows(t.TaskID, keepPlans, staged)
	if err := a.deps.Journal.InsertBatch(ctx, rows, a.nowStr()); err != nil {
		failStaging(applyResultCode(err), nil, fmt.Errorf("写入操作日志: %w", err))
		return
	}
	refs := make([]map[string]string, 0, len(staged))
	for _, s := range staged {
		if s.casRef != nil {
			refs = append(refs, map[string]string{
				"operation_id": s.fp.op.ID, "kind": "cas",
				"algorithm": s.casRef.Algorithm, "digest": s.casRef.Digest, "purpose": s.casRef.Purpose,
			})
		}
		if s.tempRel != "" {
			refs = append(refs, map[string]string{
				"operation_id": s.fp.op.ID, "kind": "staging", "temp_relative_path": s.tempRel,
			})
		}
	}
	if err := a.deps.ApplyRuns.SetRecoveryRefs(ctx, t.TaskID, marshalJSONRaw(refs), a.nowStr()); err != nil {
		failStaging(applyResultCode(err), nil, fmt.Errorf("落恢复引用: %w", err))
		return
	}
	if failedIdx >= 0 {
		s := staged[failedIdx]
		failStaging(s.failCode, s.failArgs, fmt.Errorf("操作 %s staging 失败: %v", s.fp.op.ID, s.failErr))
		return
	}
	if err := a.deps.ApplyRuns.AdvanceState(ctx, t.TaskID, model.ApplyRunStaged, a.nowStr()); err != nil {
		failStaging(applyResultCode(err), nil, fmt.Errorf("推进运行至 staged: %w", err))
		return
	}
	state = model.ApplyRunStaged

	// ---- applying：复用 P2 批量化两段式（内容取 staging 期已复核的暂存副本，
	// StageContent 复用短路；批前意图/批后结果两事务，票 #48 形态不变） ----
	applyingStart := time.Now()
	t.Phase = "applying"
	t.MessageKey = "msg.task.restore.applying"
	t.Completed = 0
	if !advance(t) {
		return
	}
	if err := a.deps.ApplyRuns.AdvanceState(ctx, t.TaskID, model.ApplyRunApplying, a.nowStr()); err != nil {
		failRun(applyResultCode(err), fmt.Errorf("推进运行至 applying: %w", err))
		return
	}
	state = model.ApplyRunApplying

	if !a.runApplyingBatches(ctx, commitCtx, &t, staged, actionsBySide, advance, failRun) {
		return
	}
	timing.ApplyingMs = time.Since(applyingStart).Milliseconds()
	if err := a.deps.ApplyRuns.AdvanceState(ctx, t.TaskID, model.ApplyRunVerifying, a.nowStr()); err != nil {
		failRun(applyResultCode(err), fmt.Errorf("推进运行至 verifying: %w", err))
		return
	}
	state = model.ApplyRunVerifying

	// ---- verifying：受管范围完整复扫；逐操作目标侧字节复验（权威判定只认
	// 字节与 digest，.index 不回写不参与裁决，ADR-0006 §11）+ 非操作资源对
	// 目标的漂移断言 ----
	verifyStart := time.Now()
	t.Phase = "verifying"
	t.MessageKey = "msg.task.restore.verifying"
	if !advance(t) {
		return
	}
	if ctx.Err() != nil {
		failRun(resultCancelled, ctx.Err())
		return
	}
	rescanP, rescanR, err := a.rescanEndpoints(ctx, rel, proj, rt)
	if err != nil {
		failRun(resultIOError, fmt.Errorf("验证复扫失败: %w", err))
		return
	}
	violations, remaining, err := verifyRestore(plan, keepPlans, rescanP, rescanR, &base, skipIDs)
	if err != nil {
		failRun(resultVerifyMismatch, fmt.Errorf("验证比较失败: %w", err))
		return
	}
	if len(violations) > 0 {
		failRun(resultVerifyMismatch, fmt.Errorf("复扫与回滚目标不一致: %v", violations))
		return
	}
	timing.VerifyingMs = time.Since(verifyStart).Milliseconds()

	completeness := model.TaskOutcomeExact
	if remaining > 0 {
		completeness = model.TaskOutcomePartial
	}

	// ---- committed：单 RunInTx 原子收口（runApply 同构）----
	commitID := a.deps.IDs("commit_")
	baselineID := a.deps.IDs("base_")
	nowStr := a.nowStr()
	newBaseline := buildRestoreResultBaseline(rel.RelationID, rel.HeadBaselineID, rescanP, rescanR, &base, skipIDs)
	newBaseline.BaselineID = baselineID
	newBaseline.CreatedAt = nowStr
	rescanP.SnapshotID = a.deps.IDs("snap_")
	rescanP.CapturedAt = nowStr
	rescanR.SnapshotID = a.deps.IDs("snap_")
	rescanR.CapturedAt = nowStr
	// 基线内容摄取（ADR-0012 §2/规格 §F2）：restore 结果基线与 sync 同款通道，
	// 项目侧 mod 表示按 Content digest 统一从工作树读字节入 CAS（Put 幂等；
	// 竞态降级记诊断不失败提交）。引用行与提交同事务落 object_refs。
	contentRefs, ingestDiags := a.ingestBaselineProjectContent(ctx, proj.RootPath, &newBaseline)
	commit := buildSyncCommit(rel, plan, commitID, baselineID, nowStr, completeness, remaining,
		rescanP.SnapshotID, rescanR.SnapshotID, buildRestoreCommitChanges(keepPlans, snapP, snapR, rescanP, rescanR), nil, ingestDiags)
	// restore 账目（ADR-0006 §9）：parent=当前 head（历史追加不改写）、
	// previous_baseline=回滚前的头基线（计划 BaseBaselineID 是目标基线，不是
	// 回滚前的头）；kind=restore 由 buildSyncCommit 沿 plan.Kind 承载。
	commit.PreviousBaselineID = rel.HeadBaselineID

	err = a.deps.Tx.RunInTx(commitCtx, func(repos ports.Repos) error {
		if err := repos.Snapshots.Insert(ctx, rescanP); err != nil {
			return fmt.Errorf("写入验证快照: %w", err)
		}
		if err := repos.Snapshots.Insert(ctx, rescanR); err != nil {
			return fmt.Errorf("写入验证快照: %w", err)
		}
		if err := repos.Baselines.Insert(ctx, newBaseline); err != nil {
			return fmt.Errorf("写入结果基线: %w", err)
		}
		if err := repos.Commits.Insert(ctx, commit); err != nil {
			return fmt.Errorf("写入提交: %w", err)
		}
		var casRefs []ports.ObjectRefRow
		for _, s := range staged {
			if s.casRef != nil {
				casRefs = append(casRefs, *s.casRef)
			}
		}
		casRefs = append(casRefs, contentRefs...)
		if err := repos.Commits.InsertObjectRefs(ctx, "commit", commitID, casRefs); err != nil {
			return fmt.Errorf("写入对象引用: %w", err)
		}
		if err := repos.Relations.UpdateHeadBaseline(ctx, rel.RelationID, baselineID); err != nil {
			return err
		}
		if err := repos.Relations.UpdateHeadCommit(ctx, rel.RelationID, commitID); err != nil {
			return err
		}
		if err := repos.ApplyRuns.AttachCommit(ctx, t.TaskID, commitID, nowStr); err != nil {
			return err
		}
		if err := repos.ApplyRuns.AdvanceState(ctx, t.TaskID, model.ApplyRunCommitted, nowStr); err != nil {
			return err
		}
		verifiedAt := nowStr
		for _, s := range staged {
			if err := repos.Journal.AdvanceStatus(ctx, t.TaskID, s.fp.op.ID,
				model.OperationStatusVerified, verifiedAt, nil); err != nil {
				return fmt.Errorf("操作 %s 标记 verified: %w", s.fp.op.ID, err)
			}
		}
		a.consumePlanConfirmation(ctx, repos, plan.PlanID)
		return nil
	})
	if err != nil {
		// 事务回滚零残留：无 Baseline/Commit/head 推进；applying 已触目标，
		// 属文件一致性风险面 → 恢复矩阵（ADR-0004 不变）。
		failRun(resultIOError, fmt.Errorf("提交事务失败: %w", err))
		return
	}
	state = model.ApplyRunCommitted

	// staging 仅在提交事务成功后清理（ADR-0004 §5）：本运行目录 + 计划暂存锚
	//（用户补全字节提交后随计划清理，契约 06 §0.3；failed 重试路径不清锚，
	// 跨运行延续）。清理幂等可重试，失败保留证据（staging_cleared 保持 false）。
	if err := syncstage.CleanupRun(a.deps.StagingRoot, t.TaskID); err != nil {
		slog.Warn("restore: 清理暂存失败（staging_cleared 保持未清理，可重试）", "err", err)
	} else if err := a.deps.ApplyRuns.MarkStagingCleared(commitCtx, t.TaskID, a.nowStr()); err != nil {
		slog.Warn("restore: 记录 staging_cleared 失败", "err", err)
	}
	if anchor := restoreStagingAnchor(plan); anchor != t.TaskID {
		if err := syncstage.CleanupRun(a.deps.StagingRoot, anchor); err != nil {
			slog.Warn("restore: 清理计划暂存锚失败（可重试）", "anchor", anchor, "err", err)
		}
	}

	t.Status = model.TaskStatusSucceeded
	t.Phase = "done"
	t.MessageKey = "msg.task.restore.succeeded"
	t.Completed = total
	t.CommitID = commitID
	t.Outcome = completeness
	if _, err := a.runner.Update(commitCtx, t); err != nil {
		slog.Warn("restore: 任务成功终态落库失败", "task", t.TaskID, "err", err)
		return
	}
	// relation_invalidated 在 committed 事务提交后发射（契约 06 §7 新发射点，
	// 事件零新类型）；发布失败不影响已提交事实。
	_ = a.pub.PublishRelationInvalidated(commitCtx, rel.RelationID)
}

// deriveRestoreFilePlans 从 restore 计划推导可执行操作的文件执行计划（纯函数，
// 与 sync 侧 deriveApplyFilePlans 的关键差异）：
//
//   - skip 决议的资源整行剔除（ADR-0006 §3：决议固化于 resolved plan）——不
//     暂存、不写入、不进 journal、不参与验证；返回 skipIDs 供收口账目
//    （ADR-0006 §9 partial 公式的「skip + user_object 未提供 + unrecoverable」
//     全部经 ResolveRestorePlan 固化为 skip，remaining 即其计数）；
//   - 写回内容来源按行 marker 分派（本文件包注释三类）；
//   - 删除动作的保全策略沿 defaultRecoverability（非 mod → cas 保全，mod →
//     redownload 不保全）——删除面三类的执行口径（ADR-0006 §5）。
func deriveRestoreFilePlans(plan model.SyncPlan, base *model.SyncBaseline,
	snapP, snapR model.ObservedSnapshot, projRoot, rtRoot, anchorDir string) ([]applyFilePlan, []model.ResourceID) {

	skipSet := make(map[model.ResourceID]struct{}, len(plan.Resolutions))
	for _, r := range plan.Resolutions {
		if r.Choice == model.ChoiceSkip {
			skipSet[r.ResourceID] = struct{}{}
		}
	}
	// skipIDs 按计划操作序收集（Operations 已确定性排序），账目可复现。
	var skipIDs []model.ResourceID
	seenSkip := map[model.ResourceID]bool{}
	for _, op := range plan.Operations {
		if _, ok := skipSet[op.ResourceID]; ok && !seenSkip[op.ResourceID] {
			seenSkip[op.ResourceID] = true
			skipIDs = append(skipIDs, op.ResourceID)
		}
	}

	rootBySide := map[model.Side]string{model.SideProject: projRoot, model.SideRuntime: rtRoot}
	snaps := map[model.Side]model.ObservedSnapshot{model.SideProject: snapP, model.SideRuntime: snapR}
	out := make([]applyFilePlan, 0, len(plan.Operations))
	for _, op := range plan.Operations {
		if _, ok := skipSet[op.ResourceID]; ok {
			continue // 剔出本场（决议固化，非取数面临时剔除）
		}
		fp := applyFilePlan{
			op:              op,
			recoverability:  defaultRecoverability(op.ResourceID),
			materialization: model.MaterializationCopy,
		}
		_, tgtSide, known := applySideForOp(op)
		if !known {
			fp.blockedCode = resultUnsupportedOp
			out = append(out, fp)
			continue
		}
		fp.targetSide = tgtSide
		fp.root = rootBySide[tgtSide]

		var pre *model.Precondition
		for i := range op.Preconditions {
			if op.Preconditions[i].Side == string(tgtSide) {
				pre = &op.Preconditions[i]
				break
			}
		}

		if op.Kind == model.OpRemoveRuntime || op.Kind == model.OpRemoveProject {
			// 删除行先于目标基线查找分派：删除行的资源在目标基线中缺席
			//（target absent 是删除行的定义，ADR-0006 §5），路径取输入快照
			// 被删侧观察；前置条件 present + 指纹（外部改动即拒绝，
			// ApplyDelete 复核）。
			tgtObs := snapshotObs(snaps[tgtSide], op.ResourceID)
			if tgtObs == nil {
				fp.blockedCode = resultPreconditionViolated
				out = append(out, fp)
				continue
			}
			fp.action = applyActionDelete
			fp.targetRel = tgtObs.Representation.RelativePath
			if pre != nil && pre.Expected != nil {
				fp.beforeDigest = pre.Expected.Digest
			}
			out = append(out, fp)
			continue
		}

		// 写回：目标路径与目标内容摘要以目标基线该侧表示为唯一权威
		//（双端强一致化，ADR-0006 §1）。
		tgtRep := (*model.Representation)(nil)
		if base != nil {
			if res, ok := base.Resources[op.ResourceID]; ok {
				tgtRep = baselineRep(&res, tgtSide)
			}
		}
		if tgtRep == nil {
			fp.blockedCode = resultPreconditionViolated
			out = append(out, fp)
			continue
		}
		fp.targetRel = tgtRep.RelativePath

		// 写回：after = 目标内容 sha256（操作 ObjectRefs 固化，prepare 时点从
		// 目标基线解析）。
		for _, ref := range op.ObjectRefs {
			if ref.Algorithm == "sha256" && ref.Digest != "" {
				fp.afterDigest = ref.Digest
				break
			}
		}
		if fp.afterDigest == "" {
			fp.blockedCode = resultContentUnavailable
			out = append(out, fp)
			continue
		}
		if pre != nil && pre.Existence == "present" {
			fp.action = applyActionModify
			if pre.Expected != nil {
				fp.beforeDigest = pre.Expected.Digest
			}
		} else {
			fp.action = applyActionCreate
		}

		// 内容来源分派（本文件包注释三类来源）：
		item := restoreFindItem(plan, op.ResourceID)
		switch {
		case item != nil && item.StageRel != "" && item.ExpectedDigest != "" &&
			item.ExpectedDigest == fp.afterDigest:
			// 用户补全字节（暂存锚上已验收；digest 与本操作目标一致才可用——
			// file 资源双侧同内容共用一份补全，mod 的 jar 摘要不会匹配 metafile
			// 侧操作，后者落入 CAS 缺省分支在 staging 边界暴露缺失）。
			if rel, relErr := syncstage.StagedRel(item.StageRel); relErr == nil {
				fp.sourceRoot = anchorDir
				fp.sourceRel = rel
			} else {
				fp.blockedCode = resultPathEscape
			}
		case item != nil && item.Marker == model.MarkerRedownloadRequired &&
			tgtSide == model.SideRuntime && item.Redownload != nil:
			// CF 重取（仅 runtime 侧 jar；声明 hash 校验在引擎「取对了」，目标
			// digest 复核在 StageContent「写对了」——两层校验）。
			fp.dlReq = &download.Request{
				FileID:     item.Redownload.FileID,
				Filename:   item.Redownload.Filename,
				HashFormat: item.Redownload.HashFormat,
				Hash:       item.Redownload.DeclaredHash,
			}
		default:
			// CAS 写回（restorable_from_cas 主路径；其余 marker 的内容缺失在
			// staging 边界暴露为操作失败）。
			fp.afterFromCAS = fp.afterDigest
		}
		out = append(out, fp)
	}
	return out, skipIDs
}

// fetchRestoreDownloads 对重取行执行批量取数（fetchDownloadOperations 的
// restore 变体）。与 sync 侧的刻意差异：成功行只落 dlPath/dlDone，不回填
// afterDigest——restore 的写回目标是固定的目标摘要（StageContent 按其复核，
// 来源字节错即失败）；失败行标 dlFail（契约 06 §6 的 err.download.* 载体），
// 由 staging 期整场 failed 终局（restore 无剔除语义）。返回错误仅表示 ctx 取消。
func (a *App) fetchRestoreDownloads(ctx context.Context, run *syncstage.Run, plans []applyFilePlan) error {
	reqs := make([]download.Request, 0, len(plans))
	// plansIdx[k] 是 reqs[k] 所属的 plans 行下标（对位记账）：结果按引擎回调的
	// 对位下标 k 归集与回填，不以 req 值作 map 键——两重取行的请求字段全同时
	//（同 FileID+Filename+Hash），值语义键会塌缩、一行静默丢回填。
	plansIdx := make([]int, 0, len(plans))
	for i := range plans {
		fp := &plans[i]
		if fp.dlReq == nil || fp.blockedCode != "" {
			continue
		}
		reqs = append(reqs, *fp.dlReq)
		plansIdx = append(plansIdx, i)
	}
	if len(reqs) == 0 {
		return nil
	}
	if a.deps.Downloads == nil {
		// 引擎未装配（未接下载面的测试夹具）：重取行按取数失败处置——staging
		// 期整场 failed 终局，绝不伪装成功。
		for i := range plans {
			fp := &plans[i]
			if fp.dlReq != nil && fp.blockedCode == "" {
				fp.blockedCode = resultContentUnavailable
			}
		}
		slog.Warn("restore: 下载引擎未装配，重取行按取数失败处置", "count", len(reqs))
		return nil
	}

	// 结果按对位下标 k 归集（onResult 在引擎 worker goroutine 上回调，乱序且
	// 禁止并发写 plans；每请求恰好回调一次，k 与 reqs/plansIdx 严格对位——
	// 引擎契约）。
	type dlOutcome struct {
		res *download.Result
		err error
	}
	outcomes := make([]dlOutcome, len(reqs))
	fetchErr := a.deps.Downloads.FetchAll(ctx, run.DlDir(), reqs,
		func(k int, _ download.Request, res *download.Result, ferr error) {
			outcomes[k] = dlOutcome{res: res, err: ferr}
		})
	for k, o := range outcomes {
		fp := &plans[plansIdx[k]]
		switch {
		case o.err != nil:
			// 分桶码直作失败原因（err.download.*；契约 06 §6 Problem 载体）。
			fp.dlFailCode = errs.CodeOf(o.err)
			if fp.dlFailCode == "" {
				fp.dlFailCode = resultContentUnavailable
			}
			fp.dlFailArgs = errs.ArgsOf(o.err)
			fp.dlFailCause = o.err.Error()
		case o.res == nil:
			fp.dlFailCode, fp.dlFailCause = resultContentUnavailable, "引擎未返回重取结果"
		default:
			// 成品已过声明 hash 校验（两层校验第一层）；目标 digest 复核留给
			// StageContent（第二层）。afterDigest 保持目标值不回填实测——
			// restore 的写回契约是「回到目标」，不是「拿到什么写什么」。
			fp.dlPath = o.res.Path
			fp.dlDone = true
		}
	}
	return fetchErr
}

// stageOneRestoreOperation 执行单操作 staging（stageOneOperation 的 restore
// 变体）。与 sync 侧的刻意差异：没有剔除分支——前置条件不成立、内容不可得、
// 暂存复核失败都按硬失败返回（restore 整场 failed 终局语义，零部分提交）；
// 删除面的 before 保全沿 defaultRecoverability（非 mod 进 CAS 可找回，mod 照删
// 不保全，ADR-0006 §5 三类）。
func stageOneRestoreOperation(ctx context.Context, a *App, run *syncstage.Run, relationID string,
	fp applyFilePlan, snaps map[model.Side]model.ObservedSnapshot,
	rootBySide map[model.Side]string) stagedOp {

	s := stagedOp{fp: fp}
	if fp.blockedCode != "" {
		s.failCode, s.failErr = fp.blockedCode, fmt.Errorf("回滚操作不可执行（%s）", fp.blockedCode)
		return s
	}
	if fp.dlFailCode != "" {
		// 重取相位失败（err.download.* 分桶）：整场 failed 终局的定因行。
		s.failCode = fp.dlFailCode
		s.failArgs = fp.dlFailArgs
		s.failErr = errors.New(fp.dlFailCause)
		return s
	}
	if code, _ := verifyApplyPreconditions(fp.op, snaps, rootBySide); code != "" {
		// prepare 时点现状断言在磁盘上不成立（确认后被外部改动）：目标一致性
		// 风险；restore 在 staging 相位（零写入）按 failed 终局退出——零部分
		// 提交、重新 prepare 后即可重试。
		s.failCode, s.failErr = code, fmt.Errorf("回滚前置条件在磁盘上不成立（%s）", code)
		return s
	}
	// 删除/覆盖面的 before 保全（ADR-0004 §3 既有机器复用）：非 mod 进 CAS
	//（回滚删除面「可从对象库找回」的一侧），mod 不保全（packwiz 管理可重取、
	// 手放照删——删除行判定面已带 deletion_warn 警示）。
	if fp.action == applyActionModify || fp.action == applyActionDelete {
		if syncstage.RequiresCASBackup(fp.recoverability) {
			ref, preserved, err := syncstage.PreserveBeforeContent(ctx, a.deps.CAS,
				filepath.Join(fp.root, filepath.FromSlash(fp.targetRel)), fp.recoverability)
			if err != nil {
				s.failCode, s.failErr = applyResultCode(err), err
				return s
			}
			if preserved {
				s.casRef = &ports.ObjectRefRow{
					Algorithm: ref.Algorithm, Digest: ref.Digest,
					Purpose: objectRefPurposeBefore, Size: ref.Size,
				}
			}
		}
	}
	// 写回内容暂存：CAS 对象流/重取成品/用户补全字节（afterContentReader 按推导
	// 分派打开），StageContent 原子落盘并按目标 sha256 复核——CAS 字节被篡改、
	// CDN 字节漂移、补全字节损坏都在这条复核线上拒绝且目标不被触碰
	//（ADR-0006 §10.2 staging 边界复核；验收红线①「CAS 篡改拒绝写回」）。
	if fp.action == applyActionCreate || fp.action == applyActionModify {
		reader, closer, err := a.afterContentReader(ctx, fp)
		if err != nil {
			s.failCode, s.failErr = resultContentUnavailable, fmt.Errorf("写回内容不可得: %w", err)
			return s
		}
		tempRel, stageErr := run.StageContent(fp.targetRel, reader, fp.afterDigest)
		closer()
		if stageErr != nil {
			s.failCode, s.failErr = applyResultCode(stageErr), stageErr
			return s
		}
		s.tempRel = tempRel
	}
	// 所有权证明（P2 既有机器）：签发后落暂存证据，与 journal 列副本互验。
	proof, err := run.IssueProof(relationID, fp.op.ID, fp.targetRel, fp.beforeDigest, fp.afterDigest)
	if err != nil {
		s.failCode, s.failErr = resultProofInvalid, err
		return s
	}
	if err := run.SaveProof(proof); err != nil {
		s.failCode, s.failErr = applyResultCode(err), err
		return s
	}
	s.proof = proof
	return s
}

// stageRestoreOperations 有界并行执行 restore staging（stageApplyOperations
// 同构：首个硬失败停止派发、取消在操作边界响应、输出保持 plans 前缀契约）。
// restore 的特有约束：计划按侧建操作，同资源双侧写回（file 资源两侧同路径）
// 会映射到同一暂存副本——StageContent 的复用短路依赖「同路径串行」（并发对
// 同一目标原子 rename 在 Windows 上会 Access denied），故按 targetRel 分桶：
// 桶内路径互斥可并行，桶间串行。restore 无 skip 分支：硬失败即截断。
func stageRestoreOperations(ctx context.Context, a *App, run *syncstage.Run, relationID string,
	plans []applyFilePlan, snaps map[model.Side]model.ObservedSnapshot,
	rootBySide map[model.Side]string) []stagedOp {

	// 贪心着色分桶：同桶内暂存路径互不相同（可并行），共享同一 targetRel 的
	// 操作落在不同桶（跨桶串行——同路径写回按计划序先后执行，后到者命中
	// StageContent 复用短路，逐字节不变）。
	var buckets [][]int
	bucketPaths := []map[string]struct{}{}
	for i, fp := range plans {
		b := -1
		for k, paths := range bucketPaths {
			if _, taken := paths[fp.targetRel]; !taken {
				b = k
				break
			}
		}
		if b == -1 {
			bucketPaths = append(bucketPaths, map[string]struct{}{})
			buckets = append(buckets, nil)
			b = len(bucketPaths) - 1
		}
		bucketPaths[b][fp.targetRel] = struct{}{}
		buckets[b] = append(buckets[b], i)
	}

	results := make([]stagedOp, len(plans))
	executed := make([]bool, len(plans))
	var stop atomic.Bool // 首个硬失败后不再处理后续桶（在途操作做完）
	for _, bucket := range buckets {
		if ctx.Err() != nil || stop.Load() {
			break // 未开始的操作保持未 staging
		}
		work := make(chan int, len(bucket))
		for _, i := range bucket {
			work <- i
		}
		close(work)
		workers := applyWorkers
		if len(bucket) < workers {
			workers = len(bucket)
		}
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range work {
					if ctx.Err() != nil || stop.Load() {
						continue
					}
					results[i] = stageOneRestoreOperation(ctx, a, run, relationID, plans[i], snaps, rootBySide)
					executed[i] = true
					if results[i].failCode != "" {
						stop.Store(true)
					}
				}
			}()
		}
		wg.Wait()
	}

	out := make([]stagedOp, 0, len(plans))
	for i := range plans {
		if !executed[i] {
			if ctx.Err() != nil {
				out = append(out, stagedOp{fp: plans[i], failCode: resultCancelled, failErr: ctx.Err()})
			}
			break
		}
		out = append(out, results[i])
		if results[i].failCode != "" {
			break
		}
	}
	return out
}

// buildRestoreCommitChanges 把已执行操作编译为提交变化行（buildCommitChanges
// 的 restore 变体）：restore 计划按侧建操作、同一资源可同时有双侧写回操作，
// 而 commit_changes 每资源一行（UNIQUE(commit_id, resource_id)）——逐侧字段
// 合并进同一行（前=输入快照该侧观察，后=复扫该侧观察），保持资源首次出现序。
func buildRestoreCommitChanges(plans []applyFilePlan, inP, inR, rescanP, rescanR model.ObservedSnapshot) []model.CommitChange {
	inBySide := map[model.Side]model.ObservedSnapshot{model.SideProject: inP, model.SideRuntime: inR}
	rescanBySide := map[model.Side]model.ObservedSnapshot{model.SideProject: rescanP, model.SideRuntime: rescanR}
	byResource := map[model.ResourceID]*model.CommitChange{}
	out := make([]model.CommitChange, 0, len(plans))
	for _, fp := range plans {
		if fp.action == "" {
			continue
		}
		ch := byResource[fp.op.ResourceID]
		if ch == nil {
			out = append(out, model.CommitChange{ResourceID: fp.op.ResourceID, ChangeKind: string(actionChangeKind(fp.action))})
			ch = &out[len(out)-1]
			byResource[fp.op.ResourceID] = ch
		}
		before := repOf(inBySide[fp.targetSide], fp.op.ResourceID)
		after := repOf(rescanBySide[fp.targetSide], fp.op.ResourceID)
		if fp.targetSide == model.SideRuntime {
			ch.RuntimeBefore, ch.RuntimeAfter = before, after
		} else {
			ch.ProjectBefore, ch.ProjectAfter = before, after
		}
	}
	return out
}

// verifyRestore 验证复扫与回滚目标一致（verifyRescan 的 restore 变体）：
//  1. 已执行写操作：目标侧实测 sha256 = 操作固化的目标摘要（逐字节+digest 复验，
//     验收规格 §2 红线①的落点；不比双侧语义——.index 不回写不参与裁决，
//     ADR-0006 §11）；
//  2. 已执行删除操作：目标侧缺席；
//  3. skip 资源豁免 violation，恒计入剩余差异（partial 不谎报 exact，红线④；
//     ADR-0006 §9 账目公式：skip + user_object 未提供 + unrecoverable——三者
//     均经 resolve 固化为 skip，故 remaining = len(skipIDs)）；
//  4. 其余（无操作行）资源按 prepare 同款折叠对照目标：任一侧漂移即 violation
//    （运行期间的外部改动属一致性风险，走恢复面）。
func verifyRestore(plan model.SyncPlan, plans []applyFilePlan, rescanP, rescanR model.ObservedSnapshot,
	base *model.SyncBaseline, skipIDs []model.ResourceID) (violations []string, remaining int, err error) {

	snaps := map[model.Side]model.ObservedSnapshot{model.SideProject: rescanP, model.SideRuntime: rescanR}
	executed := map[model.ResourceID]bool{}
	for _, fp := range plans {
		executed[fp.op.ResourceID] = true
		switch actionChangeKind(fp.action) {
		case model.ChangeCreate, model.ChangeModify:
			obs := snapshotObs(snaps[fp.targetSide], fp.op.ResourceID)
			if obs == nil || obs.Representation.Content == nil {
				violations = append(violations,
					fmt.Sprintf("write %s: 复扫目标侧 %s 缺失或无内容指纹", fp.op.ResourceID, fp.targetSide))
				continue
			}
			if obs.Representation.Content.Digest != fp.afterDigest {
				violations = append(violations,
					fmt.Sprintf("write %s: 复扫目标侧 digest %s ≠ 目标 %s",
						fp.op.ResourceID, obs.Representation.Content.Digest, fp.afterDigest))
			}
		case model.ChangeDelete:
			if snapshotObs(snaps[fp.targetSide], fp.op.ResourceID) != nil {
				violations = append(violations,
					fmt.Sprintf("delete %s: 复扫目标侧 %s 仍存在", fp.op.ResourceID, fp.targetSide))
			}
		}
	}

	skipSet := make(map[model.ResourceID]struct{}, len(skipIDs))
	for _, id := range skipIDs {
		skipSet[id] = struct{}{}
	}
	remaining = len(skipIDs)

	if base == nil {
		return violations, remaining, nil
	}
	ids := make(map[model.ResourceID]struct{}, len(base.Resources))
	for id := range base.Resources {
		ids[id] = struct{}{}
	}
	for id := range rescanP.Resources {
		ids[id] = struct{}{}
	}
	for id := range rescanR.Resources {
		ids[id] = struct{}{}
	}
	for id := range ids {
		if executed[id] {
			continue
		}
		if _, skipped := skipSet[id]; skipped {
			continue // 决议跳过：预期残留，计入剩余不判违规
		}
		tgt := base.Resources[id]
		for _, side := range restoreSides {
			tRep := baselineRep(&tgt, side)
			curObs := sideObservation(snapshotObs(rescanP, id), snapshotObs(rescanR, id), side)
			tSem, serr := restoreTargetSemantic(id, tRep)
			if serr != nil {
				return nil, 0, serr
			}
			cSem, serr := restoreCurrentSemantic(id, curObs)
			if serr != nil {
				return nil, 0, serr
			}
			drifted := (tRep != nil && (curObs == nil || cSem != tSem)) ||
				(tRep == nil && curObs != nil)
			if drifted {
				violations = append(violations,
					fmt.Sprintf("unselected %s: 复扫 %s 侧与回滚目标不一致", id, side))
			}
		}
	}
	return violations, remaining, nil
}

// buildRestoreResultBaseline 从复扫快照构造 restore 结果基线（复扫新建，不指针
// 回拨；parent=回滚前的头基线，历史链延续）。partial 的 skip 资源保持目标基线
// 条目（ADR-0006 §9：partial 后 relation 保持 dirty 不显示 clean——缺失资源
// 不得被复扫快照「收编」成已认可状态，diff 对残留差异保持可见，重扫自然重现
// 未完成的回滚面）。
func buildRestoreResultBaseline(relID, parentID string, rescanP, rescanR model.ObservedSnapshot,
	base *model.SyncBaseline, skipIDs []model.ResourceID) model.SyncBaseline {

	b, err := buildVerifiedBaseline(relID, parentID, rescanP, rescanR, base)
	if err != nil {
		// buildVerifiedBaseline 仅在表示不可语义摘要时报错；restore 的写回目标
		// 已在 prepare/verify 两处按同一口径摘要成功，此分支不可达，防御性返回
		//（调用方 committed 事务内插入失败走恢复面）。
		slog.Warn("restore: 构造结果基线失败（防御分支）", "err", err)
		return model.SyncBaseline{}
	}
	if len(skipIDs) > 0 && base != nil {
		for _, id := range skipIDs {
			if res, ok := base.Resources[id]; ok {
				b.Resources[id] = res
			}
		}
		if b.BaselineDigest, err = normalize.BaselineDigest(b); err != nil {
			slog.Warn("restore: 重算结果基线摘要失败（防御分支）", "err", err)
		}
	}
	return b
}

// failRestoreFailed 收口 restore staging 相位失败的 failed 终局（契约 06 §6
// Q8；failApplyFailed 的 restore 同构）：run→failed（终局，prepared→failed 合法
// 迁移）+ 任务终态 failed + Problem 承载原因码（下载失败 = err.download.*，
// 其余 = 操作终局结果码）；本运行 staging 目录随终局清理（.part 不跨运行复用），
// 计划暂存锚保留（用户补全字节跨运行延续，同 plan 重 Confirm 直接复用）。
// 不标关系健康、不进崩溃恢复（网络/取数失败 ≠ 恢复面）；零部分提交。
func (a *App) failRestoreFailed(ctx context.Context, t model.Task, run model.ApplyRun,
	code string, args []string, cause error) {

	if err := a.deps.ApplyRuns.AdvanceState(ctx, run.TaskID, model.ApplyRunFailed, a.nowStr()); err != nil {
		slog.Warn("restore: 运行推进 failed 失败", "run", run.TaskID, "err", err)
	}
	if err := syncstage.CleanupRun(a.deps.StagingRoot, run.TaskID); err != nil {
		slog.Warn("restore: 清理 failed 运行暂存失败（可重试）", "err", err)
	} else if err := a.deps.ApplyRuns.MarkStagingCleared(ctx, run.TaskID, a.nowStr()); err != nil {
		slog.Warn("restore: 记录 staging_cleared 失败", "err", err)
	}

	t.Status = model.TaskStatusFailed
	t.Phase = "done"
	t.MessageKey = "msg.task.restore.failed"
	t.CanCancel = false
	t.Problem = &model.Problem{Code: code, Args: args, Detail: cause.Error()}
	if _, err := a.runner.Update(ctx, t); err != nil {
		slog.Warn("restore: 任务 failed 终态落库失败", "task", t.TaskID, "err", err)
		return
	}
	slog.Warn("restore: 运行 staging 相位失败，failed 终局（零部分提交，不进恢复面）",
		"run", run.TaskID, "code", code, "cause", cause)
}

// recoverRestore 是 restore 运行 applying 及之后失败的恢复出口（recoverApply
// 的 restore 同构，ADR-0004 不变）：run→recovery_required + 关系恢复门 +
// 任务终态 recovery_required；staging 证据保留供恢复探测/人工确认。
func (a *App) recoverRestore(ctx context.Context, t model.Task, run model.ApplyRun, fromState, code string, cause error) {
	if !applyRunTerminal(fromState) {
		if err := a.deps.ApplyRuns.AdvanceState(ctx, run.TaskID, model.ApplyRunRecoveryRequired, a.nowStr()); err != nil {
			slog.Warn("restore: 运行推进 recovery_required 失败（已是终态或库不可写）", "run", run.TaskID, "err", err)
		}
	}
	if err := a.deps.Relations.UpdateHealth(ctx, run.RelationID, model.HealthRecoveryRequired); err != nil {
		slog.Warn("restore: 关系标记恢复态失败", "relation", run.RelationID, "err", err)
	}
	t.Status = model.TaskStatusRecoveryRequired
	t.MessageKey = "msg.task.restore.recovery_required"
	t.Problem = &model.Problem{Code: CodeRecoveryInProgress, Detail: cause.Error()}
	if _, err := a.runner.Update(ctx, t); err != nil {
		slog.Warn("restore: 任务恢复终态落库失败", "task", t.TaskID, "err", err)
		return
	}
	slog.Warn("restore: 运行进入 recovery_required", "run", run.TaskID, "code", code, "cause", cause)
}
