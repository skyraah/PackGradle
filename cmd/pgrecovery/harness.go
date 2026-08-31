package main

// 单轮编排（票 #41）：强杀尝试（含重试）→ 三次启动（恢复管线 + 两次幂等核对）
// → 四不变式断言 → 收口（committed：复扫归零 + 复跑；recovery_required：
// apply 门禁 + AcknowledgeRecovery + 复跑 + 复扫归零）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/errs"
	"packgradle/internal/perffixture"
)

const (
	maxAttempts = 4 // 单轮最多尝试数（miss/未过 ConfirmPlan 时重试）
	rerunLimit  = 4 * time.Minute
	scanLimit   = 2 * time.Minute
)

// schedulePhases 是强杀目标相位全集（前 3 轮洗牌保证全覆盖，其余随机补采）。
var schedulePhases = []string{"staging", "applying", "verifying"}

// delayRangeMS 是目标相位标记出现后的随机延迟区间（毫秒）。参考同规模实测
// 分相时长（staging ~1s / applying ~2s / verifying ~0.4s，120 操作），区间上限
// 低于相距时长，落点相位与目标一致的期望很高；相位提前推进时立即击杀兜底。
var delayRangeMS = map[string][2]int{
	"staging":   {60, 400},
	"applying":  {80, 1400},
	"verifying": {20, 180},
}

// roundContext 携带单轮所需的输入。
type roundContext struct {
	index       int
	targetPhase string // 种子调度的目标强杀相位（main 统一抽取，轮 1-3 三相位全覆盖）
	rng         *rand.Rand
	mods        int
	textFiles   int
	fixtureSeed int64
	headlessBin string
	workRoot    string
	killWindow  time.Duration
}

// opSnapshot 是操作日志行的幂等核对投影。
type opSnapshot struct {
	Ordinal    int    `json:"ordinal"`
	Status     string `json:"status"`
	ResultCode string `json:"result_code,omitempty"`
}

// roundState 是一次启动后观察到的完整状态快照（幂等核对的数据面）。
type roundState struct {
	Manifest          map[string]string
	Health            string
	ApplySync         bool
	ApplySyncReason   string
	RunFound          bool
	RunState          string
	RunCommitID       string
	RunAcknowledgedAt string
	RunPlanID         string
	TaskID            string
	TaskStatus        string
	TaskOutcome       string
	Ops               []opSnapshot
	CommitCount       int
	RelationID        string
}

// runRound 执行单轮并返回记录。
func runRound(rc roundContext) roundRecord {
	rr := roundRecord{Round: rc.index, Schedule: rc.targetPhase}
	fmt.Printf("  [调度] 目标相位=%s\n", rr.Schedule)

	// ---- 阶段一：强杀尝试（miss 重试，目标相位不变、延迟减半）----
	roundDir := filepath.Join(rc.workRoot, fmt.Sprintf("round-%d", rc.index))
	var (
		final      *attempt
		finalDir   string
		prevDelay  int
		lastResult string
	)
	for k := 1; k <= maxAttempts; k++ {
		attemptDir := filepath.Join(roundDir, fmt.Sprintf("attempt-%d", k))
		if err := os.RemoveAll(attemptDir); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("清理尝试目录失败: %v", err))
			return rr
		}
		lo, hi := delayRangeMS[rr.Schedule][0], delayRangeMS[rr.Schedule][1]
		delay := lo + rc.rng.Intn(hi-lo+1)
		if k > 1 {
			delay = maxInt(lo, prevDelay/2) // 重试减半：上一相位窗口被打穿则提前击杀
		}
		prevDelay = delay

		gen, err := perffixture.Generate(context.Background(), perffixture.Options{
			OutDir: filepath.Join(attemptDir, "fixture"), Seed: rc.fixtureSeed,
			Mods: rc.mods, TextFiles: rc.textFiles,
		})
		if err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("fixture 生成失败: %v", err))
			return rr
		}
		dataDir := filepath.Join(attemptDir, "data")

		a := &attempt{index: k, targetPhase: rr.Schedule, delayMS: delay}
		child, err := spawnApplyChild(rc.headlessBin, gen.ProjectRoot, gen.InstanceDir, dataDir)
		if err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("启动 apply 子进程失败: %v", err))
			return rr
		}
		fmt.Printf("  [attempt %d] pid=%d 目标相位=%s 延迟=%dms\n", k, child.pid(), a.targetPhase, a.delayMS)
		child.runKillWindow(a, rc.killWindow)
		lastResult = a.outcome

		ar := attemptRec{
			Index: k, TargetPhase: a.targetPhase, DelayMS: a.delayMS, Outcome: a.outcome,
			Kill: killFacts{Verified: a.killed && a.taskkillOK, TaskkillOutput: a.taskkillOut,
				LandedPhase: a.landedPhase, Markers: a.markers},
			StderrTail: tail(child.stderr.String(), 2000),
		}
		rr.Attempts = append(rr.Attempts, ar)
		fmt.Printf("  [attempt %d] 结果=%s kill_verified=%v confirm_plan=%v 退出=%v\n",
			k, a.outcome, ar.Kill.Verified, a.confirmed, a.childErr)

		if a.outcome == outcomeKilled {
			final, finalDir = a, attemptDir
			rr.Kill = ar.Kill
			break
		}
	}
	rr.AttemptCount = len(rr.Attempts)
	if final == nil {
		rr.Violations = append(rr.Violations,
			fmt.Sprintf("%d 次尝试均未获得真实强杀（最后结果=%s）", maxAttempts, lastResult))
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

	// 不变式 4：重启 2/3 相对重启 1 零变化（无重复补偿/删除/二次破坏）。
	rr.Invariant4 = true
	for _, pair := range []struct {
		label string
		prev  roundState
		cur   roundState
	}{{"重启2", s1, s2}, {"重启3", s1, s3}} {
		vs := idempotencyViolations(pair.prev, pair.cur, pair.label)
		if len(vs) > 0 {
			rr.Invariant4 = false
			rr.Violations = append(rr.Violations, vs...)
		}
	}

	// ---- 阶段三：不变式 1（无部分完成假象）----
	if !s1.RunFound {
		rr.Violations = append(rr.Violations, "重启后找不到 apply 运行（强杀前已观察到 ConfirmPlan）")
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
		rr.Invariant1 = assertRecoveryRequired(s1, &rr.Violations)
	default:
		rr.Violations = append(rr.Violations,
			fmt.Sprintf("不变式1：运行终态=%s，期望 committed 或 recovery_required", s1.RunState))
	}
	if rr.Verdict == "" {
		return rr
	}

	// ---- 阶段四：收口与不变式 2/3 ----
	switch rr.Verdict {
	case "committed":
		// 自动收口：probe 四路裁决后提交，或提交事务已落库而进程被杀（committed
		// 崩溃窗口）由重启簿记重建——二者对不变式等价，外部不可区分，落点相位
		// 细节以 kill.landed_phase/markers 记录为准。
		rr.ClosurePath = "auto_committed"
		// 不变式 2：收口后完整复扫，diff 归零。
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
		stack3.Close()
		// 不变式 3：收口后 apply 可重跑成功（真实 pgheadless -apply 子进程）；
		// 复跑后二次核对 diff 归零并入不变式 2。
		rr.Invariant3 = rerunApply(ctx, rc, fixtureRoot, dataDir, &rr.Violations)
		if !assertPostRerun(ctx, dataDir, fixtureRoot, &rr.Violations) {
			rr.Invariant2 = false
		}

	case "recovery_required":
		rr.ClosurePath = "acknowledge_reapply_committed"
		app := stack3.App
		// 不变式 3（前半）：recovery_required 期间 apply 不可用。
		if gateErr := checkApplyGate(ctx, app, s1); gateErr != "" {
			rr.Violations = append(rr.Violations, gateErr)
		}
		// §2.4 授权的 L0 唯一人工路径自动化：AcknowledgeRecovery 收口。
		if _, err := app.AcknowledgeRecovery(ctx, s1.TaskID); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("AcknowledgeRecovery 失败: %v", err))
			return rr
		}
		ws, err := app.GetWorkspace(ctx, s1.RelationID)
		if err != nil || ws.Relation.Health != "healthy" {
			rr.Violations = append(rr.Violations, fmt.Sprintf("确认后关系未复位 healthy: health=%v err=%v", wsRelHealth(ws), err))
		}
		// 记录确认后的剩余 diff（不设门槛：补偿/诚实部分完成本就允许剩余）。
		if err := rescan(ctx, app, s1.RelationID); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("确认后复扫失败: %v", err))
		} else if sum, err := changesSummary(ctx, app, s1.RelationID); err != nil {
			rr.Violations = append(rr.Violations, fmt.Sprintf("确认后 GetChanges 失败: %v", err))
		} else {
			rr.RemainingDiffAfterAck = &sum
			fmt.Printf("  [收口] 确认后剩余 diff: %+v\n", sum)
		}
		stack3.Close()
		// 不变式 3（后半）：复跑 apply 成功；不变式 2：复跑收口后 diff 归零。
		rr.Invariant3 = rerunApply(ctx, rc, fixtureRoot, dataDir, &rr.Violations)
		rr.Invariant2 = assertPostRerun(ctx, dataDir, fixtureRoot, &rr.Violations)
	}

	return rr
}

// assertCommitted 校验 committed 终态的一致性（不变式 1 的 committed 分支）。
func assertCommitted(s1 roundState, violations *[]string) bool {
	ok := true
	v := func(format string, args ...any) {
		ok = false
		*violations = append(*violations, "不变式1："+fmt.Sprintf(format, args...))
	}
	if s1.TaskStatus != "succeeded" {
		v("committed 运行的任务状态=%s，期望 succeeded", s1.TaskStatus)
	}
	if s1.RunCommitID == "" {
		v("committed 运行缺 commit_id")
	}
	if s1.CommitCount < 1 {
		v("committed 运行但历史提交数=%d", s1.CommitCount)
	}
	if s1.Health != "healthy" {
		v("committed 运行但关系健康=%s，期望 healthy", s1.Health)
	}
	if s1.TaskOutcome != "exact" && s1.TaskOutcome != "partial" {
		v("任务 outcome=%q，期望 exact|partial", s1.TaskOutcome)
	}
	for _, op := range s1.Ops {
		if op.Status != "verified" {
			v("操作 %d 状态=%s，期望 verified（基线推进必须伴随内容完整）", op.Ordinal, op.Status)
		}
	}
	return ok
}

// assertRecoveryRequired 校验 recovery_required 终态（不变式 1 的恢复分支）：
// 基线绝不推进（零提交）、恢复门落库、apply 不可用。
func assertRecoveryRequired(s1 roundState, violations *[]string) bool {
	ok := true
	v := func(format string, args ...any) {
		ok = false
		*violations = append(*violations, "不变式1："+fmt.Sprintf(format, args...))
	}
	if s1.TaskStatus != "recovery_required" {
		v("recovery_required 运行的任务状态=%s，期望 recovery_required", s1.TaskStatus)
	}
	if s1.CommitCount != 0 {
		v("recovery_required 期间存在 %d 条提交（基线推进 = 部分完成假象）", s1.CommitCount)
	}
	if s1.Health != "recovery_required" {
		v("关系健康=%s，期望 recovery_required", s1.Health)
	}
	if s1.ApplySync {
		v("recovery_required 期间 apply_sync available=true")
	} else if s1.ApplySyncReason != codeRecoveryInProgress {
		v("apply_sync 不可用原因码=%q，期望 %s", s1.ApplySyncReason, codeRecoveryInProgress)
	}
	return ok
}

// checkApplyGate 实际尝试 ConfirmPlan，验证恢复期 apply 门禁（不变式 3 前半）。
// 返回空串=门禁成立。
func checkApplyGate(ctx context.Context, app syncapp.Application, s1 roundState) string {
	if _, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: s1.RunPlanID}); err == nil {
		return "不变式3：recovery_required 期间 ConfirmPlan 意外成功"
	} else if code := errs.CodeOf(err); code != codeRecoveryInProgress {
		return fmt.Sprintf("不变式3：ConfirmPlan 拆码=%q，期望 %s（err=%v）", code, codeRecoveryInProgress, err)
	}
	fmt.Printf("  [门禁] recovery_required 期间 ConfirmPlan 被拒（%s）\n", codeRecoveryInProgress)
	return ""
}

// rerunApply 复跑 pgheadless -apply 至自然退出（不变式 3：收口后 apply 可重跑
// 成功——子进程自带 committed/逐操作 verified/diff 归零断言链，退出码 0 即通过）。
func rerunApply(ctx context.Context, rc roundContext, fixtureRoot, dataDir string, violations *[]string) bool {
	cctx, cancel := context.WithTimeout(ctx, rerunLimit)
	defer cancel()
	code, out, err := runApplyToCompletion(cctx, rc.headlessBin,
		filepath.Join(fixtureRoot, "project"), filepath.Join(fixtureRoot, "instance"), dataDir)
	if err != nil || code != 0 {
		*violations = append(*violations, fmt.Sprintf("不变式3：复跑 apply 退出码=%d err=%v 输出尾部:\n%s", code, err, out))
		return false
	}
	fmt.Printf("  [复跑] pgheadless -apply 退出码 0（committed 断言链全过）\n")
	return true
}

// assertPostRerun 复跑收口后重开栈断言 diff 归零与运行终态（不变式 2 的
// recovery_required 分支；committed 分支作二次证据）。
func assertPostRerun(ctx context.Context, dataDir, fixtureRoot string, violations *[]string) bool {
	stack, st, err := restartAndObserve(ctx, dataDir, fixtureRoot)
	if err != nil {
		*violations = append(*violations, fmt.Sprintf("复跑后重启失败: %v", err))
		return false
	}
	defer stack.Close()
	if st.RunState != "committed" {
		*violations = append(*violations, fmt.Sprintf("复跑后运行终态=%s，期望 committed", st.RunState))
		return false
	}
	sum, err := changesSummary(ctx, stack.App, st.RelationID)
	if err != nil {
		*violations = append(*violations, fmt.Sprintf("复跑后 GetChanges 失败: %v", err))
		return false
	}
	if sum.pendingCounts() != 0 {
		*violations = append(*violations, fmt.Sprintf("不变式2：复跑收口后 diff 未归零 %+v", sum))
		return false
	}
	return true
}

// idempotencyViolations 比对两次启动的状态快照（不变式 4）。
func idempotencyViolations(prev, cur roundState, label string) []string {
	var out []string
	v := func(format string, args ...any) {
		out = append(out, "不变式4（"+label+"）："+fmt.Sprintf(format, args...))
	}
	diffs := manifestDiffs(prev.Manifest, cur.Manifest)
	if len(diffs) > 0 {
		v("fixture 文件树发生变化（%d 处）: %s", len(diffs), strings.Join(diffs, "; "))
	}
	if prev.RunState != cur.RunState {
		v("运行状态 %s → %s", prev.RunState, cur.RunState)
	}
	if prev.TaskStatus != cur.TaskStatus {
		v("任务状态 %s → %s", prev.TaskStatus, cur.TaskStatus)
	}
	if prev.RunAcknowledgedAt != cur.RunAcknowledgedAt {
		v("acknowledged_at %q → %q", prev.RunAcknowledgedAt, cur.RunAcknowledgedAt)
	}
	if prev.CommitCount != cur.CommitCount {
		v("提交数 %d → %d", prev.CommitCount, cur.CommitCount)
	}
	if prev.Health != cur.Health {
		v("关系健康 %s → %s", prev.Health, cur.Health)
	}
	if len(prev.Ops) != len(cur.Ops) {
		v("操作日志行数 %d → %d", len(prev.Ops), len(cur.Ops))
	} else {
		for i := range prev.Ops {
			if prev.Ops[i] != cur.Ops[i] {
				v("操作 %d 状态变化 %+v → %+v", prev.Ops[i].Ordinal, prev.Ops[i], cur.Ops[i])
			}
		}
	}
	return out
}

// restartAndObserve 以共享启动路径（bootstrap.Build → RecoverInterruptedTasks
// 同步执行恢复管线）重启一次并观察状态。调用方负责 Close。
func restartAndObserve(ctx context.Context, dataDir, fixtureRoot string) (*bootstrap.Stack, roundState, error) {
	stack, err := bootstrap.Build(dataDir)
	if err != nil {
		return nil, roundState{}, fmt.Errorf("bootstrap 装配（含启动恢复）: %w", err)
	}
	st, err := observe(ctx, stack.App, fixtureRoot)
	if err != nil {
		stack.Close()
		return nil, roundState{}, err
	}
	return stack, st, nil
}

// observe 收集当前启动下的工作区/运行/任务/操作/历史/文件树快照。
func observe(ctx context.Context, app syncapp.Application, fixtureRoot string) (roundState, error) {
	st := roundState{Manifest: map[string]string{}}
	wsPage, err := app.ListWorkspaces(ctx, ports.PageRequest{Limit: 10})
	if err != nil {
		return st, fmt.Errorf("ListWorkspaces: %w", err)
	}
	if len(wsPage.Items) != 1 {
		return st, fmt.Errorf("工作区数=%d，期望 1", len(wsPage.Items))
	}
	rel := wsPage.Items[0].Relation
	st.RelationID = rel.RelationID

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return st, fmt.Errorf("GetWorkspace: %w", err)
	}
	st.Health = ws.Relation.Health
	for _, a := range ws.Availability {
		if a.Action == "apply_sync" {
			st.ApplySync, st.ApplySyncReason = a.Available, a.ReasonCode
		}
	}

	manifest, err := treeManifest(filepath.Join(fixtureRoot, "project"), filepath.Join(fixtureRoot, "instance"))
	if err != nil {
		return st, fmt.Errorf("文件树清单: %w", err)
	}
	st.Manifest = manifest

	run, err := app.GetApplyRun(ctx, rel.RelationID)
	if err != nil {
		return st, nil // 无运行（强杀落于 ConfirmPlan 前的防御分支）：RunFound=false
	}
	st.RunFound = true
	st.RunState, st.RunCommitID = run.State, run.CommitID
	st.RunAcknowledgedAt, st.RunPlanID, st.TaskID = run.AcknowledgedAt, run.PlanID, run.TaskID

	if tv, err := app.GetTask(ctx, run.TaskID); err == nil {
		st.TaskStatus, st.TaskOutcome = tv.Status, tv.Outcome
	}
	cursor := ""
	for {
		page, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
			RelationID: rel.RelationID, TaskID: run.TaskID, Cursor: cursor, Limit: ports.MaxPageLimit,
		})
		if err != nil {
			return st, fmt.Errorf("ListApplyOperations: %w", err)
		}
		for _, op := range page.Items {
			st.Ops = append(st.Ops, opSnapshot{Ordinal: op.Ordinal, Status: op.Status, ResultCode: op.ResultCode})
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if commits, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 50}); err == nil {
		st.CommitCount = len(commits.Items)
	}
	return st, nil
}

// rescan 触发完整复扫并等待无活动任务（事件不是事实源，以查询 API 为准）。
func rescan(ctx context.Context, app syncapp.Application, relationID string) error {
	if _, err := app.StartScan(ctx, relationID); err != nil {
		return fmt.Errorf("StartScan: %w", err)
	}
	deadline := time.Now().Add(scanLimit)
	for {
		page, err := app.ListTasks(ctx, relationID, true, ports.PageRequest{Limit: 5})
		if err == nil && len(page.Items) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("复扫超时未结束（relation=%s）", relationID)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// changesSummary 取最新快照对的变更 summary（记录投影）。
func changesSummary(ctx context.Context, app syncapp.Application, relationID string) (changesSummaryRec, error) {
	page, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: relationID})
	if err != nil {
		return changesSummaryRec{}, err
	}
	s := page.Summary
	return changesSummaryRec{
		Total: s.Total, CreateCount: s.CreateCount, ModifyCount: s.ModifyCount,
		DeleteCount: s.DeleteCount, ConflictCount: s.ConflictCount, InitChoiceCount: s.InitChoiceCount,
	}, nil
}

// treeManifest 计算多个目录树的逐文件 sha256 清单（键 = 子目录名/相对路径），
// 幂等核对的数据面（无重复补偿/删除/二次破坏 ⇒ 清单逐字节不变）。
func treeManifest(roots ...string) (map[string]string, error) {
	m := map[string]string{}
	for _, root := range roots {
		base := filepath.Base(root)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			key := base + "/" + filepath.ToSlash(rel)
			if !d.Type().IsRegular() {
				m[key] = "<non-regular>"
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			m[key] = hex.EncodeToString(h.Sum(nil))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

// manifestDiffs 比较两份清单，返回至多 8 条差异摘要。
func manifestDiffs(prev, cur map[string]string) []string {
	var out []string
	keys := map[string]bool{}
	for k := range prev {
		keys[k] = true
	}
	for k := range cur {
		keys[k] = true
	}
	for k := range keys {
		if prev[k] != cur[k] {
			reason := "digest 变化"
			switch {
			case prev[k] == "":
				reason = "新增"
			case cur[k] == "":
				reason = "缺失"
			}
			out = append(out, k+"("+reason+")")
		}
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = append(out[:8], fmt.Sprintf("…共 %d 处", len(out)))
	}
	return out
}

func wsRelHealth(ws view.WorkspaceView) string {
	return ws.Relation.Health
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
