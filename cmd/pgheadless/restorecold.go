package main

// pgheadless -restore-cold（P3 票 #66；验收规格 §7）：3,000 fixture 上的
// restore 冷链路度量——新门槛「restore 全链路冷 ≤30s / restore 峰值内存增量
// <256MiB」的供数面（-metrics 记录 p3-perf-run/1 的 restore 段，pgfixture
// -eval 评估）。
//
// 流程：外部漂移（删运行端全部受管文本——2,400 个）→ 重扫 → PrepareRestore
//（initialize 提交）→ 全部 create 行经 CAS 写回（文本对象在 initialize 的
// before 保全已进 CAS；mod jar 在 runtime 侧无漂移、不产生判定行——零网络，
// 下载相位如实记 0）→ resolve exact → confirm → 轮询至 committed（内存采样
// 随轮询）→ committed exact 断言 + 历史不改写。
//
// 前置：perf 链先跑 -apply（initialize 全量收口）。分相计时的 staging 下载
// 子相位无 redownload 行（3000 fixture 的 mod 声明 hash 为占位值，真下载必然
// hash_mismatch——规格「download 相位只记录不设门槛」，记录 0 并注记）。

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// restoreChainStats 是 -restore-cold 链路的度量产物（p3-perf-run/1 记录的
// restore 段；main.go -metrics 消费，pgfixture -eval 评 restore 冷 ≤30s 与
// 内存增量 <256MiB 新门槛）。分相口径：prepare_ms 为链路侧 PrepareRestore
// 墙钟（含计划判定与 CF 尽力探测）；staging/applying/verifying 为引擎侧
// LastApplyTiming；staging_download_ms 是 staging 内重取子相位（无 redownload
// 行如实记 0——download 相位只记录不设门槛）。
type restoreChainStats struct {
	Kind              string          `json:"kind"`
	OperationCount    int             `json:"operation_count"`
	PrepareMS         int64           `json:"prepare_ms"`
	PhasesMS          applyPhaseMS    `json:"phases_ms"`
	StagingDownloadMS int64           `json:"staging_download_ms"`
	DownloadNote      string          `json:"download_note"`
	RestoreTotalMS    int64           `json:"restore_total_ms"`
	ChainTotalMS      int64           `json:"chain_total_ms"`
	PeakMemory        applyPeakMemory `json:"peak_memory_delta"`
}

// runRestoreCold 执行 3000 fixture 的 restore 冷链路并返回度量段。
func runRestoreCold(ctx context.Context, app syncapp.Application, rel view.RelationView, instanceDir string) (*restoreChainStats, error) {
	commits, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 10})
	if err != nil {
		return nil, err
	}
	if len(commits.Items) == 0 {
		return nil, fmt.Errorf("-restore-cold 需先跑 -apply 建基线（当前 0 提交）")
	}
	target := commits.Items[len(commits.Items)-1].CommitID // initialize 提交

	// ---- 外部漂移：改写运行端全部受管文本（config/kubejs/scripts 前缀）。
	// 改写而非删除：漂移需经一轮 sync apply（c'）收口，让被覆盖的旧内容经
	// before 保全进入 CAS——restore 写回的内容源（initialize 双侧一致入基线
	// 时字节不进 CAS，直接删除会使 cas 写回行 CAS miss → exact_infeasible）。
	gameDir := filepath.Join(instanceDir, "minecraft")
	drifted := 0
	for _, prefix := range []string{"config", "kubejs", "scripts"} {
		dir := filepath.Join(gameDir, prefix)
		if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				old, rerr := os.ReadFile(path)
				if rerr != nil {
					return rerr
				}
				if werr := os.WriteFile(path, append(old, []byte("\n# drifted by restore-cold\n")...), 0o644); werr != nil {
					return werr
				}
				drifted++
			}
			return nil
		}); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("漂移改写 %s: %w", prefix, err)
		}
	}
	fmt.Printf("== -restore-cold == 漂移：改写运行端受管文本 %d 个\n", drifted)

	// ---- 漂移收口（c'：copy 传播，before 保全使旧内容进 CAS）----
	preRestoreHead, err := rtApplyRound(ctx, app, rel, applyResolutions, "漂移收口 apply")
	if err != nil {
		return nil, err
	}

	// ---- 重扫 → PrepareRestore（prepare 分相打点）----
	scanStart := time.Now()
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return nil, fmt.Errorf("漂移后重扫: %w", err)
	}
	fmt.Printf("== -restore-cold == 重扫完成（%v）\n", time.Since(scanStart).Round(time.Millisecond))

	prepareStart := time.Now()
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: target})
	if err != nil {
		return nil, fmt.Errorf("PrepareRestore(%s): %w", target, err)
	}
	prepareMS := time.Since(prepareStart).Milliseconds()
	fmt.Printf("== -restore-cold == 计划=%s 行=%d exact_feasible=%v（prepare=%dms）\n",
		draft.PlanID, len(draft.Items), draft.ExactFeasible, prepareMS)
	if len(draft.Items) == 0 {
		return nil, fmt.Errorf("restore 计划为空——漂移未生效")
	}

	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return nil, fmt.Errorf("ResolveRestorePlan: %w", err)
	}

	// ---- confirm → 轮询（内存采样随轮询；restore_total 引擎侧打点）----
	mem := beginMemPeakSample()
	chainStart := time.Now()
	tv, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolved.PlanID})
	if err != nil {
		return nil, fmt.Errorf("ConfirmRestorePlan: %w", err)
	}
	final, err := waitTask(ctx, app, tv.TaskID, taskWait{
		interval: applyPollInterval, timeout: applyPollBaseTimeout + 4*time.Minute, mem: mem, onPhase: applyPollProgress,
	})
	if err != nil {
		return nil, err
	}
	mem.sample()
	chainMS := time.Since(chainStart).Milliseconds()
	if final.Status != model.TaskStatusSucceeded || final.Outcome != model.TaskOutcomeExact {
		dumpApplyFailure(ctx, app, rel.RelationID, final)
		return nil, fmt.Errorf("restore 任务终态 %s/%s（期望 succeeded/exact）problem=%s",
			final.Status, final.Outcome, problemText(final.Problem))
	}

	// ---- 断言：diff 归零 + 历史追加不改写（头两行 = restore 新提交/原 head）----
	changes, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID})
	if err != nil {
		return nil, err
	}
	if s := changes.Summary; s.CreateCount != 0 || s.ModifyCount != 0 || s.DeleteCount != 0 ||
		s.ConflictCount != 0 || s.InitChoiceCount != 0 {
		return nil, fmt.Errorf("restore 收口后 diff 未归零: %+v", s)
	}
	head, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 3})
	if err != nil || len(head.Items) < 3 {
		return nil, fmt.Errorf("ListCommits: %w", err)
	}
	if head.Items[0].CommitID != final.CommitID || head.Items[0].Kind != string(model.PlanRestore) ||
		head.Items[1].CommitID != preRestoreHead {
		return nil, fmt.Errorf("历史不改写断言失败: 头三行 %s/%s/%s（期望头=%s 第二=%s）",
			head.Items[0].CommitID, head.Items[1].CommitID, head.Items[2].CommitID,
			final.CommitID, preRestoreHead)
	}

	timing := lastApplyTiming(app)
	stats := &restoreChainStats{
		Kind:           string(model.PlanRestore),
		OperationCount: final.Total,
		PrepareMS:      prepareMS,
		PhasesMS: applyPhaseMS{
			Staging:   timing.StagingMs,
			Applying:  timing.ApplyingMs,
			Verifying: timing.VerifyingMs,
		},
		RestoreTotalMS:    timing.TotalMs,
		ChainTotalMS:      chainMS,
		PeakMemory:        mem.result(),
		StagingDownloadMS: 0,
		DownloadNote:      "无 redownload 行（perf 夹具 mod 声明 hash 为占位值，零网络口径）——download 相位只记录不设门槛",
	}
	fmt.Printf("== -restore-cold == committed %s exact（restore=%dms 链路=%dms prepare=%dms "+
		"staging=%dms applying=%dms verifying=%dms 峰值内存增量 %.1f MiB）\n",
		final.CommitID, timing.TotalMs, chainMS, prepareMS, timing.StagingMs, timing.ApplyingMs,
		timing.VerifyingMs, stats.PeakMemory.DeltaMiB)
	return stats, nil
}
