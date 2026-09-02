package main

// pgrecovery -mode restore（票 #66；验收规格 §4）：restore 运行强杀注入——
// 复用 P2 apply 强杀 harness 骨架（killwindow 观察相位标记 → 随机延迟
// taskkill /F → 重启恢复管线 → 幂等核对），restore 语义的增量：
//
//   - 子进程 = pgheadless -restore-target（自建夹具历史 → PrepareRestore →
//     armed 行（== ConfirmRestorePlan ==）→ restore 运行）；armed 之前的相位
//     标记属夹具建立期，killwindow 忽略；
//   - 假 CDN 由 harness 拉起（pgfixture -serve）供给子进程（redownload 行的
//     staging 下载相位）；
//   - restore 特有不变式：R5 绝不假 committed（committed 轮收口后 diff 归零
//     + 运行暂存目录无 .part 残留——staging 下载期被杀的 .part/用户对象按
//     ADR-0004 恢复矩阵处置）；R6 kind=restore 终局后历史不改写（armed 链
//     原位 + 新头 kind=restore）；
//   - recovery_required 轮以 AcknowledgeRecovery 收口后复跑 -restore-target
//     （链非空跳过建历史）至 committed。

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/cdnproc"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

const (
	restoreRerunLimit  = 4 * time.Minute
	rtArmedMarker      = "== ConfirmRestorePlan ==" // armed 前置标记
	rtChainMarker      = "== restore-chain == "     // 提交链行（R6 断言基准）
	restoreMaxAttempts = 4
)

// runRestoreMode 执行 restore 强杀注入（acceptance:recovery:restore 主流程）。
func runRestoreMode(o restoreOptions) {
	// ---- 假 CDN：harness 拉起供给子进程（验收规格 §4）----
	cdn, err := cdnproc.StartServe(o.pgfixtureBin, "127.0.0.1:0")
	fatalOn(err, "拉起假 CDN 进程（pgfixture -serve）")
	defer cdn.Close()
	o.cdn = cdn
	fmt.Printf("== 假 CDN == %s（managed，harness 拉起）\n", cdn.URL())

	rec := newRestoreRecord(o.seed, o.fixtureSeed, o.mods, o.textFiles)
	rec.CDN = cdnInfo{Mode: "managed", BaseURL: cdn.URL()}
	allPass := true

	// 强杀相位调度（种子驱动，一次抽取入记录）：轮 1-3 洗牌保证 staging 下载/
	// applying/verifying 三相位全覆盖，轮 4-5 随机补采。
	schedule := make([]string, o.rounds)
	perm := rec.rng.Perm(len(schedulePhases))
	for i := range schedule {
		if i < len(schedulePhases) {
			schedule[i] = schedulePhases[perm[i]]
		} else {
			schedule[i] = schedulePhases[rec.rng.Intn(len(schedulePhases))]
		}
	}

	for r := 1; r <= o.rounds; r++ {
		fmt.Printf("\n===== restore 第 %d/%d 轮 =====（目标相位=%s）\n", r, o.rounds, schedule[r-1])
		rr := runRestoreRound(restoreRoundContext{
			index:       r,
			targetPhase: schedule[r-1],
			rng:         rec.rng,
			headlessBin: o.headlessBin,
			workRoot:    o.work,
			killWindow:  o.killWindow,
			cdnURL:      cdn.URL(),
		})
		rec.Rounds = append(rec.Rounds, rr)
		allPass = allPass && rr.Passed()
		fmt.Printf("== restore 第 %d 轮结论 == verdict=%s 收口=%s I1=%v I2=%v I3=%v I4=%v R5=%v R6=%v 违规=%d\n",
			r, rr.Verdict, rr.ClosurePath, rr.Invariant1, rr.Invariant2, rr.Invariant3, rr.Invariant4, rr.R5, rr.R6, len(rr.Violations))
		for _, v := range rr.Violations {
			fmt.Printf("  [违规] %s\n", v)
		}
	}

	rec.finish(allPass)
	b, err := json.MarshalIndent(rec, "", "  ")
	fatalOn(err, "记录序列化")
	if o.recordPath != "-" {
		path := o.recordPath
		if path == "" {
			path = defaultRestoreRecordPath()
		}
		fatalOn(os.MkdirAll(filepath.Dir(path), 0o755), "创建记录目录")
		fatalOn(os.WriteFile(path, b, 0o644), "写入记录")
		fmt.Printf("\n== 记录 == %s\n", path)
	}
	fmt.Printf("\n== acceptance:recovery:restore 总结论 == 全部通过=%v（%d 轮）\n", allPass, rec.RoundsFixed)
	if !allPass {
		os.Exit(1)
	}
}

// restoreOptions 是 restore 模式的运行参数（main 解析后传入）。
type restoreOptions struct {
	rounds       int
	seed         int64
	mods         int
	textFiles    int
	fixtureSeed  int64
	headlessBin  string
	pgfixtureBin string
	work         string
	recordPath   string
	killWindow   time.Duration
	cdn          *cdnproc.Serve // runRestoreMode 内部拉起后回填
}

// restoreRoundContext 携带 restore 单轮输入。
type restoreRoundContext struct {
	index       int
	targetPhase string
	rng         *rand.Rand
	headlessBin string
	workRoot    string
	killWindow  time.Duration
	cdnURL      string
}

// runRestoreRound 执行 restore 强杀单轮。
func runRestoreRound(rc restoreRoundContext) restoreRoundRecord {
	rr := restoreRoundRecord{Round: rc.index, Schedule: rc.targetPhase}

	// ---- 阶段一：强杀尝试（armed 前击杀重试，延迟减半）----
	roundDir := filepath.Join(rc.workRoot, fmt.Sprintf("round-%d", rc.index))
	var (
		final     *attempt
		finalDir  string
		prevDelay int
		lastOut   string
	)
	for k := 1; k <= restoreMaxAttempts; k++ {
		attemptDir := filepath.Join(roundDir, fmt.Sprintf("attempt-%d", k))
		if err := os.RemoveAll(attemptDir); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("清理尝试目录失败: %v", err))
			return rr
		}
		lo, hi := delayRangeMS[rr.Schedule][0], delayRangeMS[rr.Schedule][1]
		delay := lo + rc.rng.Intn(hi-lo+1)
		if k > 1 {
			delay = maxInt(lo, prevDelay/2)
		}
		prevDelay = delay

		dataDir := filepath.Join(attemptDir, "data")
		a := &attempt{index: k, targetPhase: rr.Schedule, delayMS: delay}
		child, err := spawnChild(rc.headlessBin, "-project",
			filepath.Join(attemptDir, "fixture", "project"),
			"-instance", filepath.Join(attemptDir, "fixture", "instance"),
			"-data", dataDir, "-restore-target", "-cdn", rc.cdnURL)
		if err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("启动 restore 子进程失败: %v", err))
			return rr
		}
		fmt.Printf("  [attempt %d] pid=%d 目标相位=%s 延迟=%dms\n", k, child.pid(), a.targetPhase, a.delayMS)
		child.runKillWindow(a, rc.killWindow, rtArmedMarker, rtArmedMarker)

		ar := attemptRec{
			Index: k, TargetPhase: a.targetPhase, DelayMS: a.delayMS, Outcome: a.outcome,
			Kill: killFacts{Verified: a.killed && a.taskkillOK, TaskkillOutput: a.taskkillOut,
				LandedPhase: a.landedPhase, Markers: a.markers},
			StderrTail: tail(child.stderr.String(), 2000),
		}
		rr.Attempts = append(rr.Attempts, ar)
		// armed 行透出的提交链（R6 断言基准；armed 前击杀无链）。
		rr.Chain = parseChain(child.stdoutAll())
		fmt.Printf("  [attempt %d] 结果=%s kill_verified=%v armed=%v 退出=%v\n",
			k, a.outcome, ar.Kill.Verified, a.armed, a.childErr)

		lastOut = a.outcome
		if a.outcome == outcomeKilled {
			final, finalDir = a, attemptDir
			rr.Kill = ar.Kill
			break
		}
	}
	rr.AttemptCount = len(rr.Attempts)
	if final == nil {
		rr.Violations = append(rr.Violations,
			fmt.Sprintf("%d 次尝试均未获得真实强杀（最后结果=%s）", restoreMaxAttempts, lastOut))
		return rr
	}
	if len(rr.Chain) < 2 {
		rr.Violations = append(rr.Violations,
			fmt.Sprintf("armed 链缺失（%d 项）——restore 运行未过确认行", len(rr.Chain)))
		return rr
	}

	fixtureRoot := filepath.Join(finalDir, "fixture")
	dataDir := filepath.Join(finalDir, "data")
	ctx := context.Background()

	// ---- 阶段二：三次启动——恢复管线 + 幂等核对（不变式 4）----
	stack1, s1, err := restartAndObserve(ctx, dataDir, fixtureRoot)
	if err != nil {
		rr.Violations = append(rr.Violations, fmt.Sprintf("重启 1（恢复管线）失败: %v", err))
		return rr
	}
	stack2, s2, err := restartAndObserve(ctx, dataDir, fixtureRoot)
	if err != nil {
		stack1.Close()
		rr.Violations = append(rr.Violations, fmt.Sprintf("重启 2 失败: %v", err))
		return rr
	}
	stack2.Close()
	stack3, s3, err := restartAndObserve(ctx, dataDir, fixtureRoot)
	if err != nil {
		stack1.Close()
		rr.Violations = append(rr.Violations, fmt.Sprintf("重启 3 失败: %v", err))
		return rr
	}
	defer stack3.Close()

	rr.Invariant4 = true
	for _, pair := range []struct {
		label     string
		prev, cur roundState
	}{{"重启2", s1, s2}, {"重启3", s1, s3}} {
		vs := idempotencyViolations(pair.prev, pair.cur, pair.label)
		if len(vs) > 0 {
			rr.Invariant4 = false
			rr.Violations = append(rr.Violations, vs...)
		}
	}

	// ---- 阶段三：不变式 1（无部分完成假象；restore 终局口径同 P2）----
	if !s1.RunFound {
		rr.Violations = append(rr.Violations, "重启后找不到 restore 运行（armed 已确认）")
		return rr
	}
	rr.OpTally = map[string]int{}
	for _, op := range s1.Ops {
		rr.OpTally[op.Status]++
	}
	switch s1.RunState {
	case "committed":
		rr.Verdict = "committed"
		rr.Invariant1 = assertCommitted(s1, &rr.Violations)
	case "recovery_required":
		rr.Verdict = "recovery_required"
		rr.Invariant1 = assertRecoveryRequired(s1, len(rr.Chain), &rr.Violations)
	default:
		rr.Violations = append(rr.Violations,
			fmt.Sprintf("不变式1：restore 运行终态=%s，期望 committed 或 recovery_required", s1.RunState))
	}
	if rr.Verdict == "" {
		return rr
	}

	// ---- R5：绝不假 committed（committed run 的 staging 目录应随提交清理；
	// recovery_required 轮在复跑收口后按复跑 run 复核。ack 保留的旧 run
	// staging 是恢复矩阵的正确行为，不计入）----

	// ---- R6：历史不改写（armed 链原位）----
	assertHistoryChain(ctx, stack3.App, s1.RelationID, rr.Chain, rr.Verdict, &rr)

	// ---- 阶段四：收口与不变式 2/3 ----
	switch rr.Verdict {
	case "committed":
		rr.ClosurePath = "auto_committed"
		// 不变式 2：收口后完整复扫 diff 归零（= 写回真达成目标——绝不假 committed）。
		if err := rescan(ctx, stack3.App, s1.RelationID); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("收口后复扫失败: %v", err))
		} else if sum, err := changesSummary(ctx, stack3.App, s1.RelationID); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("GetChanges 失败: %v", err))
		} else if sum.pendingCounts() != 0 {
			rr.Invariant2 = false
			rr.Violations = append(rr.Violations, fmt.Sprintf("不变式2：收口后 diff 未归零 %+v", sum))
		} else {
			rr.Invariant2 = true
		}
		rr.Invariant3 = true // committed 轮无复跑需求（门禁面在 recovery_required 轮断言）
		rr.NewCommit = s1.RunCommitID
		rr.StagingParts = countStagingParts(stack3.Layout.StagingDir, s1.TaskID)

	case "recovery_required":
		rr.ClosurePath = "acknowledge_rerestore_committed"
		app := stack3.App
		// 不变式 3（前半）：恢复期 restore 门禁——PrepareRestore 与
		// ConfirmRestorePlan 同门禁（ADR-0006 §8）。
		if gateErr := checkRestoreGate(ctx, app, s1, rr.Chain[len(rr.Chain)-1]); gateErr != "" {
			rr.Violations = append(rr.Violations, gateErr)
		}
		// §2.4 授权的 L0 唯一人工路径自动化：AcknowledgeRecovery 收口。
		if _, err := app.AcknowledgeRecovery(ctx, s1.TaskID); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("AcknowledgeRecovery 失败: %v", err))
			return rr
		}
		if err := rescan(ctx, app, s1.RelationID); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("确认后复扫失败: %v", err))
		} else if sum, err := changesSummary(ctx, app, s1.RelationID); err == nil {
			rr.RemainingDiffAfterAck = &sum
			fmt.Printf("  [收口] 确认后剩余 diff: %+v\n", sum)
		}
		stack3.Close()

		// 不变式 3（后半）：复跑 -restore-target（链非空跳过建历史）至 committed。
		rr.Invariant3 = rerestore(ctx, rc, fixtureRoot, dataDir, &rr.Violations)

		// 复跑后终态断言（R5/R6/I2 的复跑面）：重开栈核对运行 committed、
		// diff 归零、staging 无 .part、历史链原位 + 新头 kind=restore。
		stack4, s4, err := restartAndObserve(ctx, dataDir, fixtureRoot)
		if err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("复跑后重启失败: %v", err))
			return rr
		}
		defer stack4.Close()
		if s4.RunState != "committed" {
			rr.Violations = append(rr.Violations, fmt.Sprintf("复跑后 restore 运行终态=%s，期望 committed", s4.RunState))
			return rr
		}
		rr.NewCommit = s4.RunCommitID
		rr.StagingParts = countStagingParts(stack4.Layout.StagingDir, s4.TaskID)
		rr.Invariant2 = changesZero(ctx, stack4.App, s4.RelationID, &rr.Violations)
		assertHistoryChain(ctx, stack4.App, s4.RelationID, rr.Chain, "committed", &rr)
	}

	// R5 汇总：committed 终局下 diff 归零（I2）+ staging 无 .part 残留。
	rr.R5 = rr.Invariant2 && rr.StagingParts == 0
	if rr.StagingParts > 0 {
		rr.Violations = append(rr.Violations,
			fmt.Sprintf("R5：staging 根残留 %d 个 .part（committed 终局应随暂存清理）", rr.StagingParts))
	}
	return rr
}

// parseChain 从子进程输出解析 armed 链行（== restore-chain == c1,c2）。
func parseChain(stdout string) []string {
	for _, line := range strings.Split(stdout, "\n") {
		if i := strings.Index(line, rtChainMarker); i >= 0 {
			rest := strings.TrimSpace(line[i+len(rtChainMarker):])
			if rest == "" {
				return nil
			}
			return strings.Split(rest, ",")
		}
	}
	return nil
}

// checkRestoreGate 实际尝试 PrepareRestore，验证恢复期 restore 门禁
// （不变式 3 前半的 restore 面）。返回空串=门禁成立。
func checkRestoreGate(ctx context.Context, app syncapp.Application, s1 roundState, targetCommit string) string {
	if targetCommit == "" {
		return ""
	}
	if _, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: s1.RelationID, CommitID: targetCommit}); err == nil {
		return "不变式3：recovery_required 期间 PrepareRestore 意外成功"
	} else if code := errs.CodeOf(err); code != codeRecoveryInProgress {
		return fmt.Sprintf("不变式3：PrepareRestore 拆码=%q，期望 %s（err=%v）", code, codeRecoveryInProgress, err)
	}
	fmt.Printf("  [门禁] recovery_required 期间 PrepareRestore 被拒（%s）\n", codeRecoveryInProgress)
	return ""
}

// assertHistoryChain 断言历史不改写：armed 链全部原位且相对序不变；
// committed 终局下新头行 kind=restore。
func assertHistoryChain(ctx context.Context, app syncapp.Application, relationID string, chain []string, verdict string, rr *restoreRoundRecord) {
	rr.R6 = true
	page, err := app.ListCommits(ctx, relationID, ports.PageRequest{Limit: ports.MaxPageLimit})
	if err != nil {
		rr.R6 = false
		rr.Violations = append(rr.Violations, "R6：ListCommits 失败: "+err.Error())
		return
	}
	// 链原位：ListCommits 为 DESC 序（新→旧），期望序列 = 链反转；按序匹配
	//（容忍中间插入 restore 行，绝不改写/重排原链的相对序）。
	expected := make([]string, len(chain))
	for i, c := range chain {
		expected[len(chain)-1-i] = c
	}
	pos := 0
	for _, c := range page.Items {
		if pos < len(expected) && c.CommitID == expected[pos] {
			pos++
		}
	}
	if pos != len(expected) {
		rr.R6 = false
		rr.Violations = append(rr.Violations,
			fmt.Sprintf("R6：armed 链 %d 项仅 %d 项按原序保留（历史被改写/重排）", len(expected), pos))
	}
	if verdict == "committed" {
		head := page.Items[0]
		if head.CommitID == chain[len(chain)-1] || head.Kind != string(model.PlanRestore) {
			rr.R6 = false
			rr.Violations = append(rr.Violations,
				fmt.Sprintf("R6：committed 终局新头应 kind=restore（got %s/%s）", head.CommitID, head.Kind))
		}
	}
}

// countStagingParts 统计指定 run 的暂存目录下残留的 .part 文件数（committed
// 终局应零——该 run 的 staging 随提交清理；staging 下载期被杀的 .part 按
// ADR-0004 恢复矩阵处置，绝不留到 committed 之后）。recovery_required 轮被
// ack 的旧 run staging 保留是矩阵正确行为，不在本断言范围。
func countStagingParts(stagingRoot, taskID string) int {
	n := 0
	runDir := filepath.Join(stagingRoot, taskID)
	_ = filepath.WalkDir(runDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".part") {
			n++
		}
		return nil
	})
	return n
}

// rerestore 复跑 -restore-target 至自然退出（不变式 3 后半：恢复收口后
// restore 可重入——子进程链非空跳过建历史，退出码 0 即 committed 断言链全过）。
func rerestore(ctx context.Context, rc restoreRoundContext, fixtureRoot, dataDir string, violations *[]string) bool {
	cctx, cancel := context.WithTimeout(ctx, restoreRerunLimit)
	defer cancel()
	code, out, err := runChildToCompletion(cctx, rc.headlessBin, "-project",
		filepath.Join(fixtureRoot, "project"), "-instance", filepath.Join(fixtureRoot, "instance"),
		"-data", dataDir, "-restore-target", "-cdn", rc.cdnURL)
	if err != nil || code != 0 {
		*violations = append(*violations, fmt.Sprintf("不变式3：复跑 restore 退出码=%d err=%v 输出尾部:\n%s", code, err, out))
		return false
	}
	fmt.Printf("  [复跑] pgheadless -restore-target 退出码 0（committed 断言链全过）\n")
	return true
}

// changesZero 断言 diff 归零（复跑收口面）。
func changesZero(ctx context.Context, app syncapp.Application, relationID string, violations *[]string) bool {
	if err := rescan(ctx, app, relationID); err != nil {
		*violations = append(*violations, "复跑后复扫失败: "+err.Error())
		return false
	}
	sum, err := changesSummary(ctx, app, relationID)
	if err != nil {
		*violations = append(*violations, "复跑后 GetChanges 失败: "+err.Error())
		return false
	}
	if sum.pendingCounts() != 0 {
		*violations = append(*violations, fmt.Sprintf("不变式2：复跑收口后 diff 未归零 %+v", sum))
		return false
	}
	return true
}
