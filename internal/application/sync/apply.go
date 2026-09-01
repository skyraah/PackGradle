package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/syncstage"
)

// Apply 引擎（票 #37）：接管 tasks(kind=apply, status=queued)，把运行从
// prepared 推到 committed（或 recovery_required）。编排沿 ADR-0004 §5 成功路径：
//
//	staged（逐操作前置条件复核 + before CAS 保全 + after 内容暂存复核 + proof）
//	→ applying（两段式批量化：批前单事务持久化整批 running 意图，批内文件动作
//	  有界并行，批后单事务记录整批结果——每操作的意图仍严格先于其动作）
//	→ verifying（受管范围完整复扫，快照与计划目标一致才过）
//	→ committed（单 RunInTx：验证快照 + 新 Baseline + SyncCommit + object_refs
//	  + Relation head + run 终态 + 操作 verified；事务提交成功后才清 staging）。
//
// 失败面（票 #37.5）：任一操作失败/取消/写满/复扫不一致——不推 Baseline、不建
// Commit，run→recovery_required + 任务终态 recovery_required（P1 死值点亮），
// staging 证据保留。事件只在事务提交后发布（ADR-0004 §6）；事件不负载数据，
// 进度以任务投影为准（契约 05 §4 D1）。
//
// 批量化的恢复安全性（票 #48，ADR-0004 §4 裁决矩阵的本源性）：事务边界按批
// 摊薄后，崩溃可能留下三种 journal/文件形态，全部落在既有矩阵四路裁决内——
//   - 批前崩溃：整批 pending（意图事务未提交）→ redo；
//   - 批中崩溃：已执行操作 running/applied + 目标已达 after digest + 所有权
//     证明匹配 → 第一行 mark-applied；未执行操作 running + 目标未写 + staging
//     完整 → 第二行幂等 redo；
//   - 批后崩溃：整批 applied → mark-applied。
// 「running 未执行」不是漏洞而是矩阵存在的原因：裁决凭 probe（目标 digest、
// 暂存证据、所有权证明），不凭操作行状态猜测。

// startApply 在 ConfirmPlan 提交成功后启动引擎协程接管任务（StartScan 先例：
// ctx WithoutCancel 派生，取消句柄注册进 runner 供 CancelTask 触发）。
func (a *App) startApply(t model.Task) {
	applyCtx, cancel := context.WithCancel(ctxWithoutCancel(context.Background()))
	a.runner.RegisterCancel(t.TaskID, cancel)
	go func() {
		defer a.runner.UnregisterCancel(t.TaskID)
		a.runApply(applyCtx, t)
	}()
}

// runApply 执行 Apply 六阶段编排。
func (a *App) runApply(ctx context.Context, queued model.Task) {
	// 同 Relation 单 Apply（ADR-0004 §6）：进程内以 relation gate 串行化引擎；
	// 持锁期间 StartScan 的创建段同样被挡（同一把关系锁），杜绝扫描与 Apply
	// 并发改端点。跨进程/跨计划互斥由 ConfirmPlan 的 apply_runs 查询守卫承担。
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

	// 接管检查：仅 queued 任务可被引擎拾起。queued 窗口被取消的任务按取消面
	// 收口——run 不得滞留活跃态（ADR-0004 §4：未完成 journal 阻止新 Apply）。
	t, err := a.deps.Tasks.Get(ctx, queued.TaskID)
	if err != nil {
		log.Printf("apply: 读取任务 %s 失败，放弃接管: %v", queued.TaskID, err)
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
		log.Printf("apply: 读取任务 %s 的运行头失败: %v", t.TaskID, err)
		return
	}
	if run.State != model.ApplyRunPrepared {
		return // 非初始态（重启后由恢复管线裁决，T05）
	}

	plan, err := a.deps.Plans.Get(ctx, run.PlanID)
	if err != nil {
		a.recoverApply(commitCtx, t, run, model.ApplyRunPrepared, resultPreconditionViolated,
			fmt.Errorf("读取计划 %s: %w", run.PlanID, err))
		return
	}
	rel, err := a.deps.Relations.Get(ctx, run.RelationID)
	if err != nil {
		a.recoverApply(commitCtx, t, run, model.ApplyRunPrepared, resultIOError,
			fmt.Errorf("读取关系: %w", err))
		return
	}
	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	rt, err2 := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil || err2 != nil {
		a.recoverApply(commitCtx, t, run, model.ApplyRunPrepared, resultIOError,
			fmt.Errorf("读取端点: %v/%v", err, err2))
		return
	}
	snapP, err := a.deps.Snapshots.Get(ctx, plan.InputProjectSnapshotID)
	if err != nil {
		a.recoverApply(commitCtx, t, run, model.ApplyRunPrepared, resultPreconditionViolated,
			fmt.Errorf("读取输入快照: %w", err))
		return
	}
	snapR, err := a.deps.Snapshots.Get(ctx, plan.InputRuntimeSnapshotID)
	if err != nil {
		a.recoverApply(commitCtx, t, run, model.ApplyRunPrepared, resultPreconditionViolated,
			fmt.Errorf("读取输入快照: %w", err))
		return
	}
	var base *model.SyncBaseline
	if plan.BaseBaselineID != "" {
		b, err := a.deps.Baselines.Get(ctx, plan.BaseBaselineID)
		if err != nil {
			a.recoverApply(commitCtx, t, run, model.ApplyRunPrepared, resultPreconditionViolated,
				fmt.Errorf("读取基线: %w", err))
			return
		}
		base = &b
	}

	// 引擎跟踪的运行阶段（AdvanceState 以库中当前态校验迁移合法性）。
	state := run.State
	failRun := func(code string, cause error) {
		a.recoverApply(commitCtx, t, run, state, code, cause)
	}
	// 进度落库用 commitCtx：取消的响应点是显式的操作边界检查（票 #37.6），
	// 而不是记账写入被 ctx 取消连带触发恢复面。
	advance := func(tt model.Task) bool {
		next, err := a.runner.Update(commitCtx, tt)
		if err == nil {
			t = next
			return true
		}
		// Apply 是破坏性管线：进度更新异常（库不可写等）进恢复面，绝不静默继续
		failRun(resultIOError, fmt.Errorf("任务状态更新失败: %w", err))
		return false
	}

	total := len(plan.Operations)
	t.Status = model.TaskStatusRunning
	t.CanCancel = true // 取消语义随引擎接管收口（T03 留注）：运行中可取消，操作边界响应
	t.Total = total
	t.Completed = 0
	t.Phase = "staging"
	t.MessageKey = "msg.task.apply.staging"
	if !advance(t) {
		return
	}

	// ---- staged：前置条件复核 + before CAS 保全 + after 暂存 + proof ----
	stageStart := time.Now()
	stgRun, err := syncstage.OpenRun(a.deps.StagingRoot, t.TaskID)
	if err != nil {
		failRun(applyResultCode(err), fmt.Errorf("打开暂存运行: %w", err))
		return
	}
	projAct, err := syncstage.NewActions(stgRun, proj.RootPath)
	if err != nil {
		failRun(applyResultCode(err), err)
		return
	}
	rtAct, err := syncstage.NewActions(stgRun, rt.RootPath)
	if err != nil {
		failRun(applyResultCode(err), err)
		return
	}
	actionsBySide := map[model.Side]*syncstage.Actions{
		model.SideProject: projAct, model.SideRuntime: rtAct,
	}
	rootBySide := map[model.Side]string{model.SideProject: proj.RootPath, model.SideRuntime: rt.RootPath}
	snaps := map[model.Side]model.ObservedSnapshot{model.SideProject: snapP, model.SideRuntime: snapR}

	plans := deriveApplyFilePlans(plan, snapP, snapR, base, proj.RootPath, rt.RootPath)

	// 下载相位（票 #63）：download 行批量并发取数（.part 置运行暂存 downloads/
	// 子目录，run 内续传、跨 run 不复用）。单行失败不中断批次——失败行随后在
	// staging 期按剔除语义落跳过清单；ctx 取消沿既有取消语义收口。
	if err := a.fetchDownloadOperations(ctx, stgRun, plans); err != nil {
		failRun(applyResultCode(err), fmt.Errorf("下载相位中止: %w", err))
		return
	}

	staged := stageApplyOperations(ctx, a, stgRun, rel.RelationID, plans, snaps, rootBySide)
	timing.StagingMs = time.Since(stageStart).Milliseconds()
	timing.OperationCount = total

	// 剔除分流（ADR-0008 §7，票 #63 唯一语义反转）：取数失败剔出本场——不暂存、
	// 不写入、不进 journal；保留集照常走 applying/verify/commit 原子收口。
	keep, skips := applySkips(staged)
	keepPlans := make([]applyFilePlan, 0, len(keep))
	for _, s := range keep {
		keepPlans = append(keepPlans, s.fp)
	}

	// 事实落库：整批操作行（含 staging 事实与逐操作结果）一次写入；随后推进
	// 运行状态。两步各自原子，崩溃窗口由 T05 恢复管线按 journal/文件证据裁决。
	rows, failedIdx := buildJournalRows(t.TaskID, keepPlans, keep)
	if err := a.deps.Journal.InsertBatch(ctx, rows, a.nowStr()); err != nil {
		failRun(applyResultCode(err), fmt.Errorf("写入操作日志: %w", err))
		return
	}
	refs := make([]map[string]string, 0, len(keep))
	for _, s := range keep {
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
		failRun(applyResultCode(err), fmt.Errorf("落恢复引用: %w", err))
		return
	}
	if failedIdx >= 0 {
		s := keep[failedIdx]
		failRun(s.failCode, fmt.Errorf("操作 %s staging 失败: %v", s.fp.op.ID, s.failErr))
		return
	}
	// 全部失败（无可提交）才 failed 终局（ADR-0008 §7）：网络失败 ≠ 恢复面，
	// 不进崩溃恢复、不标关系健康；重试 = 重新快速更新（重扫新计划只剩未更新项）。
	if len(keep) == 0 && len(skips) > 0 {
		a.failApplyFailed(commitCtx, t, run, skips)
		return
	}
	if err := a.deps.ApplyRuns.AdvanceState(ctx, t.TaskID, model.ApplyRunStaged, a.nowStr()); err != nil {
		failRun(applyResultCode(err), fmt.Errorf("推进运行至 staged: %w", err))
		return
	}
	state = model.ApplyRunStaged

	// ---- applying：两段式批量化（意图先行不变，事务边界按批摊薄，票 #48） ----
	applyingStart := time.Now()
	t.Phase = "applying"
	t.MessageKey = "msg.task.apply.applying"
	t.Completed = 0
	if !advance(t) {
		return
	}
	if err := a.deps.ApplyRuns.AdvanceState(ctx, t.TaskID, model.ApplyRunApplying, a.nowStr()); err != nil {
		failRun(applyResultCode(err), fmt.Errorf("推进运行至 applying: %w", err))
		return
	}
	state = model.ApplyRunApplying

	if !a.runApplyingBatches(ctx, commitCtx, &t, keep, actionsBySide, advance, failRun) {
		return
	}
	timing.ApplyingMs = time.Since(applyingStart).Milliseconds()
	if err := a.deps.ApplyRuns.AdvanceState(ctx, t.TaskID, model.ApplyRunVerifying, a.nowStr()); err != nil {
		failRun(applyResultCode(err), fmt.Errorf("推进运行至 verifying: %w", err))
		return
	}
	state = model.ApplyRunVerifying

	// ---- verifying：受管范围完整复扫，快照与计划目标一致（ADR-0004 §5） ----
	verifyStart := time.Now()
	t.Phase = "verifying"
	t.MessageKey = "msg.task.apply.verifying"
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
	violations, remaining, err := verifyRescan(plan, keepPlans, rescanP, rescanR, base, skips)
	if err != nil {
		failRun(resultVerifyMismatch, fmt.Errorf("验证比较失败: %w", err))
		return
	}
	if len(violations) > 0 {
		failRun(resultVerifyMismatch, fmt.Errorf("复扫与计划目标不一致: %v", violations))
		return
	}
	timing.VerifyingMs = time.Since(verifyStart).Milliseconds()
	timing.OperationCount = total

	completeness := model.TaskOutcomeExact
	if remaining > 0 {
		completeness = model.TaskOutcomePartial
	}

	// ---- committed：单 RunInTx 原子收口（redesign §6.6 步骤 5） ----
	commitID := a.deps.IDs("commit_")
	baselineID := a.deps.IDs("base_")
	nowStr := a.nowStr()
	newBaseline, err := buildVerifiedBaseline(rel.RelationID, plan.BaseBaselineID, rescanP, rescanR, base)
	if err != nil {
		failRun(resultIOError, fmt.Errorf("构造结果基线: %w", err))
		return
	}
	newBaseline.BaselineID = baselineID
	newBaseline.CreatedAt = nowStr
	rescanP.SnapshotID = a.deps.IDs("snap_")
	rescanP.CapturedAt = nowStr
	rescanR.SnapshotID = a.deps.IDs("snap_")
	rescanR.CapturedAt = nowStr
	commit := buildSyncCommit(rel, plan, commitID, baselineID, nowStr, completeness, remaining,
		rescanP.SnapshotID, rescanR.SnapshotID, buildCommitChanges(keepPlans, snapP, snapR, rescanP, rescanR), skips)

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
		for _, s := range keep {
			if s.casRef != nil {
				casRefs = append(casRefs, *s.casRef)
			}
		}
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
		for _, s := range keep {
			if err := repos.Journal.AdvanceStatus(ctx, t.TaskID, s.fp.op.ID,
				model.OperationStatusVerified, verifiedAt, nil); err != nil {
				return fmt.Errorf("操作 %s 标记 verified: %w", s.fp.op.ID, err)
			}
		}
		a.consumePlanConfirmation(ctx, repos, plan.PlanID)
		return nil
	})
	if err != nil {
		// 事务回滚零残留：无 Baseline/Commit/head 推进，run 走恢复面
		failRun(resultIOError, fmt.Errorf("提交事务失败: %w", err))
		return
	}
	state = model.ApplyRunCommitted

	// staging 仅在提交事务成功后清理（ADR-0004 §5）：按本运行 ownership 隔离
	// 子树删除，幂等可重试；失败保留证据（staging_cleared 保持 false）。
	if err := syncstage.CleanupRun(a.deps.StagingRoot, t.TaskID); err != nil {
		log.Printf("apply: 清理暂存失败（staging_cleared 保持未清理，可重试）: %v", err)
	} else if err := a.deps.ApplyRuns.MarkStagingCleared(commitCtx, t.TaskID, a.nowStr()); err != nil {
		log.Printf("apply: 记录 staging_cleared 失败: %v", err)
	}

	t.Status = model.TaskStatusSucceeded
	t.Phase = "done"
	t.MessageKey = "msg.task.apply.succeeded"
	t.Completed = total
	t.CommitID = commitID
	t.Outcome = completeness
	if _, err := a.runner.Update(commitCtx, t); err != nil {
		log.Printf("apply: 任务 %s 成功终态落库失败: %v", t.TaskID, err)
		return
	}
	_ = a.pub.PublishRelationInvalidated(commitCtx, rel.RelationID)
}

// applyWorkers 是 staging 与 applying 批内文件动作共用的有界并行度（票 #48：
// writeFileAtomic 逐文件 fsync 是 staging/applying 相大头，目标路径互斥的操作
// 可安全并行；票面 4-8 worker 的上限值，多核开发机 16 逻辑 CPU 下实测最优）。
const applyWorkers = 8

// applyBatchFirst/applyBatchMax 是 applying 的自适应批边界（票 #48）：首批判
// 最小（运行初期取消/失败时暴露的「running 未执行」形态最少，中断粒度最细，
// 与 T04 取消边界测试的逐操作语义对齐），此后逐批翻倍摊薄事务开销，上限
// applyBatchMax。批大小的正确性中立：意图先行的保证来自批边界时序（批内任一
// 动作在批意图事务提交后才开始），与批大小无关。
const (
	applyBatchFirst = 1
	applyBatchMax   = 32
)

// stagedOp 是单操作 staging 期产物（journal 引用事实 + applying 执行输入）。
type stagedOp struct {
	fp       applyFilePlan
	proof    syncstage.OwnershipProof
	tempRel  string
	casRef   *ports.ObjectRefRow
	failCode string
	failErr  error
	// skipped 标记取数失败被剔出本场（ADR-0008 §7，票 #63 唯一语义反转）：
	// 不暂存、不写入、不进 journal，其余操作照常原子提交；skipCode/skipArgs
	// 是跳过清单的原因码与插值参数（err.download.* / hash_format_unsupported /
	// content_unavailable），skips>0 且有可提交时 commit=partial。
	skipped   bool
	skipCode  string
	skipArgs  []string
	skipCause string
}

// stageOneOperation 执行单操作 staging（串行原语的逐操作体）：前置条件复核 →
// before CAS 保全 → after 内容暂存复核 → 所有权证明签发与落盘（ADR-0004 §3：
// 进入 staged 前，将被覆盖/删除且策略要求保留的旧内容已入 CAS 并完成 hash
// 复核；暂存副本落盘即复核 digest）。失败记录在返回值的 failCode/failErr。
func stageOneOperation(ctx context.Context, a *App, run *syncstage.Run, relationID string,
	fp applyFilePlan, snaps map[model.Side]model.ObservedSnapshot,
	rootBySide map[model.Side]string) stagedOp {

	s := stagedOp{fp: fp}
	if fp.dlFailCode != "" {
		// 下载相位取数失败（err.download.* 分桶 / hash_format_unsupported 信号）：
		// 剔出本场（ADR-0008 §7），不终场。
		markSkipped(&s, fp.dlFailCode, fp.dlFailArgs, fp.dlFailCause)
		return s
	}
	if fp.blockedCode != "" {
		// blocked 分流（票 #63）：取数面不可得（content_unavailable）按剔除语义
		// 跳过——不终场；其余（前置结构不可执行/未知操作）保持整场失败恢复面。
		if fp.blockedCode == resultContentUnavailable {
			markSkipped(&s, skipReasonContentUnavailable, nil, "本地数据源无目标字节（copy 不可得）")
		} else {
			s.failCode, s.failErr = fp.blockedCode, fmt.Errorf("操作不可执行（%s）", fp.blockedCode)
		}
		return s
	}
	if code, srcFailed := verifyApplyPreconditions(fp.op, snaps, rootBySide); code != "" {
		if srcFailed {
			// 源侧（取数面）失效——文件在确认后被删/改写，after 字节不可得：
			// copy/download 一条规矩（ADR-0008 §7，票 #63），剔出本场不终场；
			// 目标侧失效（文件一致性风险面）保持整场失败恢复面。
			markSkipped(&s, skipReasonContentUnavailable, nil, "源侧数据在应用前失效（取数不可得）")
			return s
		}
		s.failCode, s.failErr = code, fmt.Errorf("前置条件在磁盘上不成立（%s）", code)
		return s
	}
	// before-content CAS 保全：modify/delete 且 recoverability 策略要求时
	// 先落 CAS 并独立复核（PreserveBeforeContent 失败零引用）。
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
	// after 内容暂存（create/modify）：暂存副本镜像目标路径，StageContent
	// 原子落盘 + digest 复核，失败即删零残留。目标已达成（幂等重放）跳过。
	if fp.action == applyActionCreate || fp.action == applyActionModify {
		if !fp.targetReady {
			reader, closer, err := a.afterContentReader(ctx, fp)
			if err != nil {
				// 取数失败（copy/download 一条规矩，ADR-0008 §7，票 #63 语义
				// 反转）：剔出本场不进 journal，不终止整场；其余操作照常。
				s.skipped = true
				s.skipCode = skipReasonContentUnavailable
				s.skipCause = err.Error()
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
	}
	// 所有权证明：签发后落暂存证据（proofs/<op_id>.json），与 journal 列副本互验。
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

// stageApplyOperations 有界并行执行 staging（P2-T14）：逐操作工作互相独立——
// 各操作只触碰自己的目标/源文件与按操作隔离的暂存/证明路径（计划每资源一
// 操作，路径互斥），运行密钥只读，CAS Put 按 digest 寻址并发安全。结果按序
// 回收。失败分流（票 #63 剔除语义）：skipped（取数失败剔出本场）不截断批次、
// 不进 journal；首个硬失败（最低 ordinal，前置/暂存复核/证明面）即停：停止
// 派发后续操作，在途操作自然完成（其暂存事实截断出 journal，孤儿文件留在
// 运行目录内，随运行清理回收），返回切片保持 plans 前缀契约（buildJournalRows）。
// 取消在操作边界响应：已在途的操作做完（仅暂存，不触目标），未开始的保持
// 未 staging。
func stageApplyOperations(ctx context.Context, a *App, run *syncstage.Run, relationID string,
	plans []applyFilePlan, snaps map[model.Side]model.ObservedSnapshot,
	rootBySide map[model.Side]string) []stagedOp {

	results := make([]stagedOp, len(plans))
	executed := make([]bool, len(plans))
	var stop atomic.Bool // 首个硬失败后停止派发（在途操作做完；skipped 不截断）
	work := make(chan int)
	workers := applyWorkers
	if len(plans) < workers {
		workers = len(plans)
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				if ctx.Err() != nil || stop.Load() {
					continue // 未开始的操作保持未 staging
				}
				results[i] = stageOneOperation(ctx, a, run, relationID, plans[i], snaps, rootBySide)
				executed[i] = true
				if results[i].failCode != "" {
					stop.Store(true)
				}
			}
		}()
	}
	for i := range plans {
		work <- i
	}
	close(work)
	wg.Wait()

	// 按序组装：skipped（剔出本场）保留在输出中（成功 N + 跳过 M 清单的来源，
	// buildJournalRows 及后续管线跳过）；硬失败截断（含取消点）。
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

// batchOutcome 是批内单动作的执行事实。started=false 表示「running 意图已
// 持久化但动作未开始」（取消/失败边界后未执行）——操作行保持 running，
// 交恢复矩阵按文件事实裁决（redo），不伪装成 applied/failed。
type batchOutcome struct {
	started bool
	res     syncstage.ApplyResult
	code    string
	err     error
}

// runApplyingBatches 执行 applying 相的批量化两段式（票 #48）。逐批：
//
//  1. 批边界取消检查（下一批 running 意图尚未持久化，未开始操作保持 pending
//     ——T04 取消边界测试锁定的形态）；
//  2. 批前：单事务持久化整批 running 意图（意图先行的批形态：批内任一动作
//     都在本事务提交后才开始）；失败降级逐操作串行（隔离毒化操作——如触发器
//     拒绝单一操作的意图，整批回滚后串行路径给出与 T04 一致的失败形态）；
//  3. 批内：文件动作有界并行（目标路径互斥，原语无共享状态）；
//  4. 批后：单事务记录整批结果（applied/failed + result_json；未 started 的
//     操作保持 running）。批后崩溃窗口由裁决矩阵覆盖（见包注释）。
//
// 返回 false 表示已 failRun（调用方直接返回）。
func (a *App) runApplyingBatches(ctx, commitCtx context.Context, t *model.Task,
	staged []stagedOp, actionsBySide map[model.Side]*syncstage.Actions,
	advance func(model.Task) bool, failRun func(string, error)) bool {

	taskID := t.TaskID
	total := len(staged)
	start := 0
	size := applyBatchFirst
	for start < total {
		// 取消在批边界响应（票 #37.6）：已 started 的操作已在上一批做完，
		// 本批意图未落、动作未启，未开始的操作保持 pending。
		if ctx.Err() != nil {
			failRun(resultCancelled, ctx.Err())
			return false
		}
		end := start + size
		if end > total {
			end = total
		}
		batch := staged[start:end]

		if err := a.persistBatchIntents(ctx, taskID, batch); err != nil {
			// 批意图事务失败：逐操作串行重放（意图逐个落、动作逐个执行，
			// 首个失败按 T04 语义收口）。整批已回滚为 pending，毒化操作
			// （触发器拒 running 等）被隔离为单操作失败。
			if !a.applyBatchSerially(ctx, commitCtx, t, batch, start, actionsBySide, advance, failRun) {
				return false
			}
			start = end
			size = nextBatchSize(size)
			continue
		}

		outcomes := a.runBatchActions(ctx, batch, actionsBySide)
		if err := a.flushBatchResults(commitCtx, taskID, batch, outcomes); err != nil {
			failRun(applyResultCode(err), fmt.Errorf("批结果落库: %w", err))
			return false
		}
		firstFail := -1
		executed := 0
		for i := range outcomes {
			if outcomes[i].started {
				executed++
			}
			if firstFail < 0 && outcomes[i].err != nil {
				firstFail = i
			}
		}
		t.Completed = start + executed
		if !advance(*t) {
			return false
		}
		if firstFail >= 0 {
			failRun(outcomes[firstFail].code, outcomes[firstFail].err)
			return false
		}
		start = end
		size = nextBatchSize(size)
	}
	return true
}

// nextBatchSize 返回下一批大小：指数爬坡至 applyBatchMax（运行初期中断粒度
// 最细，随推进摊薄事务开销）。
func nextBatchSize(cur int) int {
	next := cur * 2
	if next > applyBatchMax {
		next = applyBatchMax
	}
	return next
}

// persistBatchIntents 在单事务内持久化整批 running 意图（逐操作仍走
// AdvanceStatus 的追加历史+状态机校验，事务边界按批摊薄——RunInTx 事务域内
// 仓库自动加入外层事务）。意图先行的批形态不变量：本事务提交后批内才可能
// 有任何文件动作（探针测试 apply_t14 逐动作断言）。
func (a *App) persistBatchIntents(ctx context.Context, taskID string, batch []stagedOp) error {
	return a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		for i := range batch {
			fp := batch[i].fp
			intent := marshalJSONRaw(map[string]string{"intent": "apply", "action": fp.action, "target": fp.targetRel})
			if err := repos.Journal.AdvanceStatus(ctx, taskID, fp.op.ID,
				model.OperationStatusRunning, a.nowStr(), intent); err != nil {
				return fmt.Errorf("操作 %s 持久化 running 意图: %w", fp.op.ID, err)
			}
		}
		return nil
	})
}

// applyBatchSerially 是批意图事务失败后的串行降级路径：与 T04 逐操作两段式
// 完全同构（applyOneOperation：意图单事务→动作→结果单事务）。返回 false 表示
// 已 failRun。
func (a *App) applyBatchSerially(ctx, commitCtx context.Context, t *model.Task,
	batch []stagedOp, base int, actionsBySide map[model.Side]*syncstage.Actions,
	advance func(model.Task) bool, failRun func(string, error)) bool {

	for i := range batch {
		// 取消在操作边界响应：已 started 的操作做完再停（票 #37.6）。
		if ctx.Err() != nil {
			failRun(resultCancelled, ctx.Err())
			return false
		}
		s := &batch[i]
		if err := a.applyOneOperation(ctx, commitCtx, t.TaskID, s, actionsBySide[s.fp.targetSide]); err != nil {
			failRun(err.resultCode, err.cause)
			return false
		}
		t.Completed = base + i + 1
		if !advance(*t) {
			return false
		}
	}
	return true
}

// runBatchActions 有界并行执行批内文件动作（票 #48 次矛盾）。安全边界：
// 目标路径互斥（计划每资源一操作），syncstage 原语无共享状态（运行密钥只读）；
// 取消/首个失败后未开始的动作不再启动（已 started 做完，票 #37.6）。
func (a *App) runBatchActions(ctx context.Context, batch []stagedOp,
	actionsBySide map[model.Side]*syncstage.Actions) []batchOutcome {

	outcomes := make([]batchOutcome, len(batch))
	var abort atomic.Bool
	work := make(chan int)
	workers := applyWorkers
	if len(batch) < workers {
		workers = len(batch)
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				if ctx.Err() != nil || abort.Load() {
					continue // 未 started：操作行保持 running，恢复矩阵裁决
				}
				s := &batch[i]
				o := batchOutcome{started: true}
				var content io.Reader
				if s.fp.action == applyActionCreate || s.fp.action == applyActionModify {
					// 批路径内容恒取 staging 期已复核的暂存副本（s.tempRel 或
					// targetReady 时动作为 already_applied 不消费内容）：不再打开
					// 源文件。暂存副本丢失的退化路径安全——StageContent 以空内容
					// 落盘即被自身 digest 复核拒绝（副本已删、目标未触碰），操作
					// 以 digest_mismatch 进恢复面，绝不写坏目标。
					content = strings.NewReader("")
				}
				res, execErr := applyActionRunner(actionsBySide[s.fp.targetSide], s.fp.action, s.proof, content)
				if execErr != nil {
					o.code, o.err = applyResultCode(execErr), execErr
				} else {
					o.res = res
				}
				if o.err != nil {
					abort.Store(true) // 失败即停：批内未 started 的动作不再启动
				}
				outcomes[i] = o
			}
		}()
	}
	for i := range batch {
		work <- i
	}
	close(work)
	wg.Wait()
	return outcomes
}

// flushBatchResults 在单事务内记录整批动作结果：成功→applied（outcome 摘要），
// 失败→failed+result_json（首个失败驱动 failRun，其余失败如实入账），未
// started 的操作跳过（保持 running）。commitCtx 落库：取消不连带记账失败。
// 事务失败即恢复面（结果未落库的崩溃窗口由矩阵 mark-applied/redo 覆盖）。
func (a *App) flushBatchResults(ctx context.Context, taskID string, batch []stagedOp,
	outcomes []batchOutcome) error {

	hasWork := false
	for i := range outcomes {
		if outcomes[i].started {
			hasWork = true
			break
		}
	}
	if !hasWork {
		return nil
	}
	return a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		for i := range outcomes {
			o := outcomes[i]
			if !o.started {
				continue
			}
			if o.err != nil {
				detail := marshalJSONRaw(map[string]string{"code": o.code, "detail": o.err.Error()})
				if err := repos.Journal.AdvanceStatus(ctx, taskID, batch[i].fp.op.ID,
					model.OperationStatusFailed, a.nowStr(), detail); err != nil {
					return fmt.Errorf("操作 %s 推进 failed: %w", batch[i].fp.op.ID, err)
				}
				if err := repos.Journal.MarkResult(ctx, taskID, batch[i].fp.op.ID, detail); err != nil {
					return fmt.Errorf("操作 %s 记录结果: %w", batch[i].fp.op.ID, err)
				}
				continue
			}
			outcome := marshalJSONRaw(map[string]string{"outcome": string(o.res.Outcome)})
			if err := repos.Journal.AdvanceStatus(ctx, taskID, batch[i].fp.op.ID,
				model.OperationStatusApplied, a.nowStr(), outcome); err != nil {
				return fmt.Errorf("操作 %s 推进 applied: %w", batch[i].fp.op.ID, err)
			}
		}
		return nil
	})
}

// applyOpError 是操作级失败的携带结果码的错误（failRun 据此记原因）。
type applyOpError struct {
	resultCode string
	cause      error
}

func (e *applyOpError) Error() string { return e.cause.Error() }

// applyOneOperation 执行单个已 staged 操作（ADR-0004 §2 逐操作两段式，票 #48
// 起为批意图事务失败后的串行降级路径）：先持久化 running 意图（操作行 + 追加
// 历史，意图先行的铁律——意图写失败时绝不执行文件动作），再执行文件动作
// （幂等原语，重放安全），成功→applied，失败→failed + result。
func (a *App) applyOneOperation(ctx, commitCtx context.Context, taskID string, s *stagedOp,
	act *syncstage.Actions) *applyOpError {

	fp := s.fp
	intent := marshalJSONRaw(map[string]string{"intent": "apply", "action": fp.action, "target": fp.targetRel})
	if err := a.deps.Journal.AdvanceStatus(ctx, taskID, fp.op.ID, model.OperationStatusRunning,
		a.nowStr(), intent); err != nil {
		code := applyResultCode(err)
		// 意图未落库/落库失败：文件动作绝不执行。按 pending→failed 收口操作行
		// （意图实际已落库则此步为 running→failed 被状态机拒绝，仅记日志）。
		detail := marshalJSONRaw(map[string]string{"code": code, "detail": err.Error()})
		if err2 := a.deps.Journal.AdvanceStatus(commitCtx, taskID, fp.op.ID,
			model.OperationStatusFailed, a.nowStr(), detail); err2 == nil {
			_ = a.deps.Journal.MarkResult(commitCtx, taskID, fp.op.ID, detail)
		}
		return &applyOpError{resultCode: code, cause: fmt.Errorf("持久化 running 意图: %w", err)}
	}
	var content io.Reader = nil
	if fp.action == applyActionCreate || fp.action == applyActionModify {
		reader, closer, err := a.afterContentReader(ctx, fp)
		if err != nil {
			return a.failStartedOperation(commitCtx, taskID, s, resultContentUnavailable, err)
		}
		defer closer()
		content = reader
	}
	res, execErr := applyActionRunner(act, fp.action, s.proof, content)
	if execErr != nil {
		return a.failStartedOperation(commitCtx, taskID, s, applyResultCode(execErr), execErr)
	}
	outcome := marshalJSONRaw(map[string]string{"outcome": string(res.Outcome)})
	if err := a.deps.Journal.AdvanceStatus(commitCtx, taskID, fp.op.ID,
		model.OperationStatusApplied, a.nowStr(), outcome); err != nil {
		return &applyOpError{resultCode: applyResultCode(err), cause: fmt.Errorf("推进 applied: %w", err)}
	}
	return nil
}

// failStartedOperation 把已 started 的失败操作收口为 failed + result
// （running→failed 合法迁移；result_json 供 T06 投影 ResultCode）。
func (a *App) failStartedOperation(ctx context.Context, taskID string, s *stagedOp, code string, cause error) *applyOpError {
	detail := marshalJSONRaw(map[string]string{"code": code, "detail": cause.Error()})
	if err := a.deps.Journal.AdvanceStatus(ctx, taskID, s.fp.op.ID,
		model.OperationStatusFailed, a.nowStr(), detail); err != nil {
		log.Printf("apply: 操作 %s 失败收口落库失败: %v", s.fp.op.ID, err)
	}
	if err := a.deps.Journal.MarkResult(ctx, taskID, s.fp.op.ID, detail); err != nil {
		log.Printf("apply: 操作 %s 结果落库失败: %v", s.fp.op.ID, err)
	}
	return &applyOpError{resultCode: code, cause: cause}
}

// rescanEndpoints 对两端点做完整受管范围复扫并组装验证快照（runScan 四相中的
// 扫描+组装先例；hash cache 沿用，只服务性能不参与裁决，ADR-0004 §6）。
func (a *App) rescanEndpoints(ctx context.Context, rel model.Relation, proj model.Project, rt model.Runtime) (model.ObservedSnapshot, model.ObservedSnapshot, error) {
	pol, err := a.deps.Mappings.GetPolicy(ctx, rel.RelationID)
	if err != nil {
		return model.ObservedSnapshot{}, model.ObservedSnapshot{}, err
	}
	polDigest, err := normalize.PolicyDigest(pol)
	if err != nil {
		return model.ObservedSnapshot{}, model.ObservedSnapshot{}, err
	}
	reportP, err := a.deps.ProjectScan.Scan(ctx, proj.RootPath, ports.ScanOptions{
		Policy:   pol,
		HashFile: a.cachedHash(proj.BindingFingerprint, proj.RootPath),
	})
	if err != nil {
		return model.ObservedSnapshot{}, model.ObservedSnapshot{}, fmt.Errorf("项目侧复扫: %w", err)
	}
	reportR, err := a.deps.RuntimeScan.Scan(ctx, rt.RootPath, ports.ScanOptions{
		Policy:   pol,
		Hint:     buildScanHint(reportP),
		HashFile: a.cachedHash(rt.BindingFingerprint, rt.RootPath),
	})
	if err != nil {
		return model.ObservedSnapshot{}, model.ObservedSnapshot{}, fmt.Errorf("运行时侧复扫: %w", err)
	}
	rescanP, err := assembleSnapshot(rel.RelationID, model.SideProject, proj.BindingFingerprint, polDigest, a.deps.ProjectScan, reportP)
	if err != nil {
		return model.ObservedSnapshot{}, model.ObservedSnapshot{}, err
	}
	rescanR, err := assembleSnapshot(rel.RelationID, model.SideRuntime, rt.BindingFingerprint, polDigest, a.deps.RuntimeScan, reportR)
	if err != nil {
		return model.ObservedSnapshot{}, model.ObservedSnapshot{}, err
	}
	return rescanP, rescanR, nil
}

// recoverApply 是失败面的唯一出口（票 #37.5）：run→recovery_required（终态，
// best-effort——已终态不再迁移）+ 关系健康标记（ADR-0004 §4：未收口恢复禁新
// Apply）+ 任务终态 recovery_required（P1 死值点亮，Problem 复用既有
// err.recovery.in_progress 码）。staging 证据一律保留：不触暂存目录、
// 不推 Baseline、不建 Commit。
func (a *App) recoverApply(ctx context.Context, t model.Task, run model.ApplyRun, fromState, code string, cause error) {
	if !applyRunTerminal(fromState) {
		if err := a.deps.ApplyRuns.AdvanceState(ctx, run.TaskID, model.ApplyRunRecoveryRequired, a.nowStr()); err != nil {
			log.Printf("apply: 运行 %s 推进 recovery_required 失败（已是终态或库不可写）: %v", run.TaskID, err)
		}
	}
	if err := a.deps.Relations.UpdateHealth(ctx, run.RelationID, model.HealthRecoveryRequired); err != nil {
		log.Printf("apply: 关系 %s 标记恢复态失败: %v", run.RelationID, err)
	}
	t.Status = model.TaskStatusRecoveryRequired
	t.MessageKey = "msg.task.apply.recovery_required"
	t.Problem = &model.Problem{Code: CodeRecoveryInProgress, Detail: cause.Error()}
	if _, err := a.runner.Update(ctx, t); err != nil {
		log.Printf("apply: 任务 %s 恢复终态落库失败: %v", t.TaskID, err)
		return
	}
	log.Printf("apply: 运行 %s 进入 recovery_required（code=%s）: %v", run.TaskID, code, cause)
}

// abandonCancelledRun 收口 queued 窗口被取消的任务：run 不得滞留 prepared 活跃态
// （ADR-0004 §6：未终局运行阻止该 Relation 新 Apply——取消是运行级事实，不是
// 可无痕回滚的操作）。任务已终态（cancelled），不再改写。
func (a *App) abandonCancelledRun(ctx context.Context, t model.Task) {
	if err := a.deps.ApplyRuns.AdvanceState(ctx, t.TaskID, model.ApplyRunRecoveryRequired, a.nowStr()); err != nil {
		log.Printf("apply: 取消任务 %s 的运行收口失败: %v", t.TaskID, err)
		return
	}
	if err := a.deps.Relations.UpdateHealth(ctx, t.RelationID, model.HealthRecoveryRequired); err != nil {
		log.Printf("apply: 关系 %s 标记恢复态失败: %v", t.RelationID, err)
	}
}

// consumePlanConsumer 消费本计划的一枚未消费确认令牌（契约 05 §7 D4：令牌一次性
// 语义）。过期/已消费不阻断提交——引擎不因令牌时间属性中止已验证的运行。
func (a *App) consumePlanConfirmation(ctx context.Context, repos ports.Repos, planID string) {
	toks, err := repos.PlanConfirmations.ListByPlan(ctx, planID)
	if err != nil {
		log.Printf("apply: 读取计划 %s 确认令牌失败: %v", planID, err)
		return
	}
	for _, tok := range toks {
		if tok.ConsumedAt != "" {
			continue
		}
		if err := repos.PlanConfirmations.MarkConsumed(ctx, planID, tok.ConfirmationToken); err != nil {
			log.Printf("apply: 消费确认令牌失败（不阻断提交）: %v", err)
		}
		return
	}
}

// buildJournalRows 把 staged 事实编译为 journal 行批：成功路径全部 pending
// （初始意图，附暂存路径/恢复引用/所有权证明）；失败路径首个失败操作以 failed
// 初始意图入库（含结果码），其余保持 pending。返回行批与失败下标（无失败 -1）。
func buildJournalRows(taskID string, plans []applyFilePlan, staged []stagedOp) ([]model.JournalOperation, int) {
	failedIdx := -1
	rows := make([]model.JournalOperation, 0, len(plans))
	for i := range plans {
		fp := plans[i]
		var s stagedOp
		if i < len(staged) {
			s = staged[i]
		}
		row := model.JournalOperation{
			TaskID:             taskID,
			OperationID:        fp.op.ID,
			Ordinal:            i + 1,
			TargetRelativePath: fp.targetRel,
			BeforeDigest:       fp.beforeDigest,
			AfterDigest:        fp.afterDigest,
			TempRelativePath:   s.tempRel,
		}
		if opJSON, err := json.Marshal(fp.op); err == nil {
			row.Operation = opJSON
		}
		if s.proof.Signature != "" {
			if proofJSON, err := json.Marshal(s.proof); err == nil {
				row.OwnershipProof = proofJSON
			}
		}
		if s.casRef != nil {
			if refJSON, err := json.Marshal(map[string]any{
				"purpose": s.casRef.Purpose, "algorithm": s.casRef.Algorithm,
				"digest": s.casRef.Digest, "size": s.casRef.Size,
			}); err == nil {
				row.RecoveryRef = refJSON
			}
		}
		if s.failCode != "" {
			failedIdx = i
			row.Status = model.OperationStatusFailed
			row.Result = marshalJSONRaw(map[string]string{"code": s.failCode, "detail": s.failErr.Error()})
		}
		rows = append(rows, row)
		if failedIdx >= 0 {
			break
		}
	}
	return rows, failedIdx
}

// buildCommitChanges 把已执行操作编译为提交变化行（目标侧前后表示；
// before 取输入快照目标侧观察，after 取复扫目标侧观察，delete 的 after 为 nil）。
func buildCommitChanges(plans []applyFilePlan, inP, inR, rescanP, rescanR model.ObservedSnapshot) []model.CommitChange {
	inBySide := map[model.Side]model.ObservedSnapshot{model.SideProject: inP, model.SideRuntime: inR}
	rescanBySide := map[model.Side]model.ObservedSnapshot{model.SideProject: rescanP, model.SideRuntime: rescanR}
	out := make([]model.CommitChange, 0, len(plans))
	for _, fp := range plans {
		if fp.action == "" {
			continue
		}
		ch := model.CommitChange{ResourceID: fp.op.ResourceID, ChangeKind: string(actionChangeKind(fp.action))}
		before := repOf(inBySide[fp.targetSide], fp.op.ResourceID)
		after := repOf(rescanBySide[fp.targetSide], fp.op.ResourceID)
		if fp.targetSide == model.SideRuntime {
			ch.RuntimeBefore, ch.RuntimeAfter = before, after
		} else {
			ch.ProjectBefore, ch.ProjectAfter = before, after
		}
		out = append(out, ch)
	}
	return out
}

// buildSyncCommit 组装提交头（契约 05 §3.5；completeness 由剩余差异数推导）。
// skips 非空时 summary 附跳过清单（成功 N + 跳过 M 及逐项原因码，GetCommit
// 投影消费；票 #63 剔除语义的透出面）。
func buildSyncCommit(rel model.Relation, plan model.SyncPlan, commitID, baselineID, nowStr, completeness string,
	remaining int, verifiedP, verifiedR string, changes []model.CommitChange, skips []stagedOp) model.SyncCommit {

	summaryObj := map[string]any{
		"operation_count": len(plan.Operations),
		"plan_kind":       string(plan.Kind),
		"success_count":   len(changes),
		"skip_count":      len(skips),
	}
	if len(skips) > 0 {
		entries := make([]skippedEntry, 0, len(skips))
		for _, s := range skips {
			entries = append(entries, skippedEntry{
				ResourceID: string(s.fp.op.ResourceID),
				ReasonCode: s.skipCode,
				ReasonArgs: s.skipArgs,
			})
		}
		summaryObj["skipped"] = entries
	}
	return model.SyncCommit{
		CommitID:                  commitID,
		RelationID:                rel.RelationID,
		ParentCommitID:            rel.HeadCommitID,
		CreatedAt:                 nowStr,
		PlanID:                    plan.PlanID,
		VerifiedProjectSnapshotID: verifiedP,
		VerifiedRuntimeSnapshotID: verifiedR,
		PreviousBaselineID:        plan.BaseBaselineID,
		ResultBaselineID:          baselineID,
		CommitKind:                string(plan.Kind),
		Completeness:              completeness,
		RemainingChangeCount:      remaining,
		Summary:                   marshalJSONRaw(summaryObj),
		Changes:                   changes,
	}
}

// marshalJSONRaw 序列化为 RawMessage；失败返回 null 字面量（纯 map 输入不会失败，
// 兜底保证列非空约束）。
func marshalJSONRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}
