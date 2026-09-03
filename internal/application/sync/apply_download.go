package sync

import (
	"context"
	"log/slog"

	"packgradle/internal/core/model"
	"packgradle/internal/download"
	"packgradle/internal/errs"
	"packgradle/internal/syncstage"
)

// Apply 引擎的下载相位（票 #63，ADR-0008 §6/§7）：download 行在 staging 之前
// 经票 #58 引擎批量并发取数——`.part` 与成品落运行暂存目录 downloads/ 子目录
//（run 内续传、跨 run 不复用：新任务新目录天然隔离；崩溃随暂存目录按
// ADR-0004 恢复矩阵处置）。引擎产「已过声明 hash 校验的字节」（两层校验第一
// 层「取对了」），接线层取回后回填成品实测 sha256 作 StageContent 复核基准
//（第二层「写对了」，既有机器零新增）。单文件失败不中断批次（FetchAll 语义），
// 失败行标 dlFail 走剔除语义的跳过清单；全部失败（无可提交）由 runApply 收口
// failed 终局。

// fetchDownloadOperations 对 plans 中的 download 行执行批量取数，成功行原位
// 回填 dlPath/dlDone/afterDigest（成品实测 sha256），失败行标 dlFailCode 由
// staging 期落跳过清单（此处只记账，不中断批次）。返回错误仅表示调用方 ctx
// 取消（FetchAll 停止排程后透传 ctx.Err()），由调用方沿既有取消语义收口。
func (a *App) fetchDownloadOperations(ctx context.Context, run *syncstage.Run,
	plans []applyFilePlan) error {

	reqs := make([]download.Request, 0, len(plans))
	// plansIdx[k] 是 reqs[k] 所属的 plans 行下标（对位记账）：结果按引擎回调的
	// 对位下标 k 归集与回填，不以 req 值作 map 键——两操作行的请求字段全同时
	//（同 FileID+Filename+Hash），值语义键会塌缩、一行静默丢回填。
	plansIdx := make([]int, 0, len(plans))
	for i := range plans {
		fp := &plans[i]
		if fp.action != applyActionCreate && fp.action != applyActionModify {
			continue // 删除无取数面
		}
		if fp.targetReady || fp.dlReq == nil || fp.blockedCode != "" {
			continue // 幂等重放 / 非 download / 推导期已判不可执行
		}
		reqs = append(reqs, *fp.dlReq)
		plansIdx = append(plansIdx, i)
	}
	if len(reqs) == 0 {
		return nil
	}
	if a.deps.Downloads == nil {
		// 生产装配（bootstrap）恒提供引擎；nil 只出现在未接下载面的测试夹具——
		// download 行全部按取数失败剔除，不伪装成功也不进恢复面。
		for i := range plans {
			fp := &plans[i]
			if fp.dlReq != nil && fp.blockedCode == "" {
				fp.blockedCode = resultContentUnavailable
			}
		}
		slog.Warn("apply: 下载引擎未装配，download 行按取数失败剔除", "count", len(reqs))
		return nil
	}

	// 结果按对位下标 k 归集（onResult 在引擎 worker goroutine 上回调，乱序且
	// 禁止并发写 plans；每请求恰好回调一次，k 与 reqs/plansIdx 严格对位——引擎契约）。
	type dlOutcome struct {
		res  *download.Result
		err  error
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
			// 分桶码直作跳过原因（err.download.*；hash_format_unsupported 信号
			// 同字面透出——接线层 CodeOf 直作跳过原因，票 #58 预期接法）。
			fp.dlFailCode = errs.CodeOf(o.err)
			if fp.dlFailCode == "" {
				fp.dlFailCode = skipReasonContentUnavailable
			}
			fp.dlFailArgs = errs.ArgsOf(o.err)
			fp.dlFailCause = o.err.Error()
		case o.res == nil:
			fp.dlFailCode, fp.dlFailCause = skipReasonContentUnavailable, "引擎未返回下载结果"
		default:
			// 成品实测 sha256：download 行的 after 权威 digest（声明可能非
			// sha256；journal after_digest / StageContent 复核 / verify 目标
			// 达成判定统一回填到该值）。
			ref, hashErr := syncstage.HashFile(o.res.Path)
			if hashErr != nil {
				fp.dlFailCode, fp.dlFailCause = skipReasonContentUnavailable, hashErr.Error()
				continue
			}
			fp.dlPath = o.res.Path
			fp.dlDone = true
			fp.afterDigest = ref.Digest
		}
	}
	return fetchErr
}

// markSkipped 把取数失败事实落到 stagedOp（剔除语义的跳过清单条目）。
func markSkipped(s *stagedOp, code string, args []string, cause string) {
	s.skipped = true
	s.skipCode = code
	s.skipArgs = args
	s.skipCause = cause
}

// applySkips 分离 staged 为保留集与跳过清单（ADR-0008 §7：跳过项不进 journal、
// 不参与 applying/verify/commit，保留集保持原序前缀契约）。
func applySkips(staged []stagedOp) (keep, skips []stagedOp) {
	keep = make([]stagedOp, 0, len(staged))
	for i := range staged {
		if staged[i].skipped {
			skips = append(skips, staged[i])
			continue
		}
		keep = append(keep, staged[i])
	}
	return keep, skips
}

// compileSkipped 清单编译为提交摘要/任务投影形状（按资源 ID 排序保证确定性）。
type skippedEntry struct {
	ResourceID string   `json:"resource_id"`
	ReasonCode string   `json:"reason_code"`
	ReasonArgs []string `json:"reason_args,omitempty"`
}

// failApplyFailed 收口「全部失败（无可提交）」的 failed 终局（ADR-0008 §7，
// 票 #63）：run→failed（终局）；网络失败 ≠ 恢复面——不标关系健康、不进崩溃
// 恢复、不保留暂存证据（.part 不跨 run 复用，重试即全新运行）；任务终态
// failed + Problem 承载首个跳过原因码（契约 06 Q8：problem_json 承载
// err.download.*）。重试 = 重新快速更新（重扫新计划只剩未更新项）或同 plan
// 重新 Confirm（failed 终局可重入）。
func (a *App) failApplyFailed(ctx context.Context, t model.Task, run model.ApplyRun, skips []stagedOp) {
	if err := a.deps.ApplyRuns.AdvanceState(ctx, run.TaskID, model.ApplyRunFailed, a.nowStr()); err != nil {
		slog.Warn("apply: 运行推进 failed 失败", "run", run.TaskID, "err", err)
	}
	// 暂存证据无恢复价值：failed 不进恢复矩阵，随终局清理（幂等可重试）。
	if err := syncstage.CleanupRun(a.deps.StagingRoot, run.TaskID); err != nil {
		slog.Warn("apply: 清理 failed 运行暂存失败（可重试）", "err", err)
	} else if err := a.deps.ApplyRuns.MarkStagingCleared(ctx, run.TaskID, a.nowStr()); err != nil {
		slog.Warn("apply: 记录 staging_cleared 失败", "err", err)
	}

	first := skips[0]
	t.Status = model.TaskStatusFailed
	t.Phase = "done"
	t.MessageKey = "msg.task.apply.failed"
	t.Completed = len(skips) // 全部操作已处置（处置结果=跳过）
	t.CanCancel = false
	t.Problem = &model.Problem{Code: first.skipCode, Args: first.skipArgs, Detail: first.skipCause}
	if _, err := a.runner.Update(ctx, t); err != nil {
		slog.Warn("apply: 任务 failed 终态落库失败", "task", t.TaskID, "err", err)
		return
	}
	slog.Warn("apply: 运行全部操作取数失败，failed 终局",
		"run", run.TaskID, "skipped", len(skips), "first_code", first.skipCode)
}
