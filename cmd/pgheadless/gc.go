package main

// pgheadless -gc / -commits / -revive（票 #64；验收规格 §6 GC 五件套）：
//
//   -commits N    连续小 apply 造 N 个提交（每轮 project 侧加一个新文件 →
//                 StartScan → PrepareSync → ResolvePlan → ConfirmPlan → 等终态），
//                 作为 acceptance:gc 的历史夹具。
//   -gc           CLI 通道触发 GC（RequestGC → 轮询任务至终态）并执行验收断言：
//                 墓碑计数>0 且被裁行消失、存活提交 ≥ K=3、引用图不变式逐 digest
//                 对账（core/gc.Audit）、最老存活提交可达闭包逐字节复验
//                 （「回滚承诺不缩水」的数据前提；restore 服务面归票 #60）。
//   -gc -probes   三红线正例场景（keep_commits=5 下运行）：①活跃 draft 计划
//                 base 基线屏障（修剪在屏障提交前截断）；②进行中 run 的
//                 staging/cas 绑定引用；③recovery_required 运行引用（安全
//                 窗口关闭 → GC pending 排队 → 夹具收口后自动续排执行）。
//   -revive D     回收站人工复活（解压回 objects + 隔离行置回 ready）。
//   -keep-commits N 写 config.toml [retention] keep_commits（K=3 保底验收用）。
//
// 任一断言不符即非零退出（Taskfile acceptance:gc 编排逐进程断言链）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"packgradle/internal/appconfig"
	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/core/gc"
	"packgradle/internal/core/model"
	"packgradle/internal/store"
)

const (
	gcPollInterval = 200 * time.Millisecond
	gcPollTimeout  = 2 * time.Minute
	// gcWindowProbeTimeout 是安全窗口正例的排队观察窗（引擎 2s 轮询兜底内
	// 必然出现 waiting 文案；夹具收口后自动续排在同一窗口内完成）。
	gcWindowProbeTimeout = 30 * time.Second
)

// gcChainStats 是 -gc 链路的度量产物（p3-perf-run/1 记录的 gc 段；票 #66
// -metrics 消费，pgfixture -eval 评 GC ≤30s 新门槛）。GCTotalMS 为 CLI 通道
// RequestGC → 任务终态的全墙钟（main 打点）；断言计数随 -gc 链填充。
type gcChainStats struct {
	Kind                  string `json:"kind"`
	Probes                bool   `json:"probes"`
	GCTotalMS             int64  `json:"gc_total_ms"`
	Tombstones            int    `json:"tombstones"`
	AliveCommits          int    `json:"alive_commits"`
	AuditViolations       int    `json:"audit_violations"`
	OldestVerifiedObjects int    `json:"oldest_verified_objects"`
	// 孤儿快照扩展（票 #89，ADR-0011 §4）：本轮断言被清扫的快照份数与
	// 对账后的残留孤儿数（残留必须为 0）。
	OrphanSnapshotsSwept int `json:"orphan_snapshots_swept"`
	OrphanSnapshotsLeft  int `json:"orphan_snapshots_left"`
}

// runRevive 执行 -revive：解压回收站副本回 objects 并把隔离行置回 ready
// （ADR-0007 §5「GC 误收的最后一道保险」，两步幂等可重入）。
func runRevive(dataRoot, digest string) {
	root := dataRoot
	if root == "" {
		var err error
		root, err = store.DefaultRoot()
		if err != nil {
			log.Fatalf("定位用户数据目录失败: %v", err)
		}
	}
	stack, err := bootstrap.Build(root)
	if err != nil {
		log.Fatalf("装配失败: %v", err)
	}
	defer stack.Close()
	if err := stack.SyncApp.ReviveObject(context.Background(), digest); err != nil {
		log.Fatalf("复活 %s 失败: %v", digest, err)
	}
	state, err := gcObjectState(stack.DB, digest)
	if err != nil {
		log.Fatalf("复核对象状态失败: %v", err)
	}
	fmt.Printf("== -revive == %s 已复活（objects.state=%s）\n", digest, state)
}

// writeKeepCommits 写 config.toml [retention] keep_commits（其余键保持默认）
// ——K=3 保底验收（keep_commits=5 仍 ≥3）的参数面。与产品同源
//（appconfig.ConfigManager，装配读同一文件）。
func writeKeepCommits(root string, n int) {
	mgr := appconfig.NewConfigManagerAt(filepath.Join(root, "config.toml"))
	s := model.DefaultRetention()
	s.KeepCommits = n
	if _, err := mgr.SetRetention(s); err != nil {
		log.Fatalf("写入 keep_commits=%d 失败: %v", n, err)
	}
	fmt.Printf("== -keep-commits == config.toml [retention] keep_commits=%d 已写入\n", n)
}

// seedCommits 连续小 apply 造 n 个提交：每轮 project 侧新增一个带唯一内容的
// 小文件 → 完整同步链（scan → plan → resolve → confirm → 等终态）。新文件
// 无冲突（added），defaultResolutions 空决议即可。
func seedCommits(ctx context.Context, app syncapp.Application, rel view.RelationView, projectRoot string, n int) {
	seedDir := filepath.Join(projectRoot, "config", "gc-seed") // 受管范围（PrepareRelation 建议 config/kubejs/scripts）
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		log.Fatalf("创建 gc-seed 目录失败: %v", err)
	}
	for i := 0; i < n; i++ {
		// 滚动文件每轮覆盖：第 2 轮起产生 modify（覆盖）操作 → before 内容
		// 保全进 CAS（object_refs purpose=before_preservation）——GC 链的
		// 对象与引用历史的血肉：裁提交后旧滚动版本失引用成回收候选。
		if err := os.WriteFile(filepath.Join(seedDir, "rolling.txt"),
			[]byte(fmt.Sprintf("gc seed rolling %d @ %d\n", i+1, time.Now().UnixNano())), 0o644); err != nil {
			log.Fatalf("写滚动文件 %d 失败: %v", i+1, err)
		}
		if err := os.WriteFile(filepath.Join(seedDir, fmt.Sprintf("commit-%02d.txt", i+1)),
			[]byte(fmt.Sprintf("gc seed commit %d @ %d\n", i+1, time.Now().UnixNano())), 0o644); err != nil {
			log.Fatalf("写入种子文件 %d 失败: %v", i+1, err)
		}
		applyOneCommit(ctx, app, rel, i+1)
	}
}

// applyOneCommit 跑一轮完整同步链（scan → plan → resolve → confirm → 终态）。
func applyOneCommit(ctx context.Context, app syncapp.Application, rel view.RelationView, seq int) {
	if _, err := app.StartScan(ctx, rel.RelationID); err != nil {
		log.Fatalf("StartScan(seed %d): %v", seq, err)
	}
	waitScan(ctx, app, rel.RelationID)
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	fatalOn(err, "GetWorkspace(seed)")
	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.Relation.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	fatalOn(err, "PrepareSync(seed)")
	// mod 资源 skip（applyResolutions 同 -apply：P2 无下载器，fixture jar 的
	// copy 物化不可用）；skip 冲突使提交 partial——GC 链只依赖提交链与
	// 非 mod 文件的 object_refs，partial 不影响引用图。
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: plan.PlanID, Resolutions: applyResolutions(plan.Conflicts)})
	fatalOn(err, "ResolvePlan(seed)")
	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	fatalOn(err, "ConfirmPlan(seed)")
	final, err := waitApplyTask(ctx, app, tv.TaskID, nil, gcPollTimeout)
	fatalOn(err, "waitApplyTask(seed)")
	if final.Status != model.TaskStatusSucceeded {
		log.Fatalf("种子提交 %d 收口 %s（期望 succeeded）", seq, final.Status)
	}
}

// runGCChain 执行 -gc 验收链（acceptance:gc 主链，验收规格 §6 之 2/3）：
// CLI 通道触发 GC → 全部断言。probes=true 时先落三红线正例夹具
//（keep_commits=5 场景，进程前经 -keep-commits 5 写入配置）。
func runGCChain(ctx context.Context, stack *bootstrap.Stack, app syncapp.Application, rel view.RelationView, projectRoot string, stats *gcChainStats) error {
	probes := os.Getenv("PGHEADLESS_GC_PROBES") == "1"

	// ---- 孤儿快照扩展夹具（票 #89，ADR-0011 §4）----
	// 两轮纯扫描（不建计划）：第一对的快照随第二轮扫描失去「任一端最新」
	// 且从未进提交/计划 → GC 清扫判孤儿一并删；提交被修剪后其验证快照的
	// 转孤儿断言在 GC 收口后进行（preCommits 记 pre-GC 账面）。
	midSnapshots, err := seedScanOnlyRounds(ctx, app, rel)
	if err != nil {
		return fmt.Errorf("孤儿快照夹具: %w", err)
	}
	preVerified, err := commitVerifiedSnapshots(stack.DB)
	if err != nil {
		return fmt.Errorf("孤儿快照账面: %w", err)
	}

	// ---- 正例夹具（keep_commits=5 场景）----
	var probeA, probeB string    // 恢复引用/staged 绑定保护的观察 digest
	var planBaseDigests []string // 正例①活跃计划 base 基线引用的对象
	if probes {
		if err := seedGCProbes(ctx, stack, app, rel, projectRoot, &probeA, &probeB, &planBaseDigests); err != nil {
			return fmt.Errorf("正例夹具: %w", err)
		}
	}

	// ---- CLI 通道触发（ADR-0007 §3 通道③）----
	gcTask, err := stack.SyncApp.RequestGC(ctx)
	if err != nil {
		return fmt.Errorf("RequestGC: %w", err)
	}
	fmt.Printf("== -gc == RequestGC task=%s status=%s\n", gcTask.TaskID, gcTask.Status)

	if probes {
		// 正例 ②③ 的窗口面：活跃/未处置 run 引用存在 → 安全窗口关闭 →
		// 任务停 pending（排队文案「等待空闲时段（安全窗口未开 · 自动续排）」）
		// 且引用对象一律存活（GC 不执行即不回收）。
		if err := assertGCWaiting(ctx, app, gcTask.TaskID, probeA, probeB); err != nil {
			return err
		}
		// 夹具收口：staged run → committed、recovery_required run 走
		// AcknowledgeRecovery（真实恢复收口出口，含关系健康复位）。
		// 收口事件 kick GC 引擎 → 排队任务自动续排（同一 taskID 跑完）。
		if err := resolveGCProbes(ctx, stack); err != nil {
			return fmt.Errorf("正例收口: %w", err)
		}
		fmt.Println("== -gc == 夹具已收口，等待排队任务自动续排（开窗自动继续）")
	}

	final, err := waitTaskStatus(ctx, app, gcTask.TaskID, model.TaskStatusSucceeded, gcPollTimeout)
	if err != nil {
		return err
	}
	if final.Phase != "done" {
		return fmt.Errorf("gc 任务终相位 %s（期望 done）", final.Phase)
	}

	// ---- 断言面 ----
	// ①墓碑计数>0 且被裁行消失（CommitPageDTO.pruned_before_count）。
	commits, err := listAllCommits(ctx, app, rel.RelationID)
	if err != nil {
		return fmt.Errorf("ListCommits: %w", err)
	}
	if len(commits.Items) < gc.HardFloorKeep {
		return fmt.Errorf("存活提交 %d < K=%d 硬保底", len(commits.Items), gc.HardFloorKeep)
	}
	page, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 1})
	if err != nil {
		return fmt.Errorf("ListCommits(墓碑页): %w", err)
	}
	if page.PrunedBeforeCount <= 0 {
		return fmt.Errorf("墓碑计数=%d，期望 >0（本轮 GC 应有裁剪）", page.PrunedBeforeCount)
	}
	fmt.Printf("== 断言①墓碑 == pruned_before_count=%d 存活提交=%d\n", page.PrunedBeforeCount, len(commits.Items))

	// ③引用图不变式：GC 后 CAS 存活集 = 可达闭包 ∪ 隔离区，超集为零
	//（core/gc.Audit 逐 digest 对账）。
	findings, auditSummary, err := auditReferenceGraph(ctx, stack, app, rel.RelationID)
	if err != nil {
		return fmt.Errorf("引用图对账: %w", err)
	}
	for _, f := range findings {
		fmt.Printf("  [违例] %s %s\n", f.Kind, f.Digest)
	}
	if len(findings) > 0 {
		return fmt.Errorf("引用图不变式违例 %d 条（超集不为零或活引用缺文件）", len(findings))
	}
	fmt.Printf("== 断言③引用图不变式 == 可达=%d 隔离=%d ready行=%d 盘上=%d 违例=0\n",
		auditSummary["reachable"], auditSummary["quarantined"], auditSummary["ready_rows"], auditSummary["on_disk"])

	// ②最老存活提交可回滚的数据前提：其可达闭包（object_refs ∪ 结果基线）
	// 全部 ready 且逐字节 sha256 复验（restore 服务面归票 #60，本断言即
	// 「GC 不缩水回滚承诺」在数据面的等价口径）。
	oldest := commits.Items[len(commits.Items)-1]
	verified, err := verifyCommitRestorable(ctx, stack, rel.RelationID, oldest.CommitID)
	if err != nil {
		return fmt.Errorf("最老存活提交 %s 可回滚复验: %w", oldest.CommitID, err)
	}
	if stats != nil {
		stats.OldestVerifiedObjects = verified
	}
	fmt.Printf("== 断言②最老存活可回滚 == commit=%s 对象=%d 全部 ready 且逐字节复验通过\n", oldest.CommitID, verified)

	// 正例①：活跃计划 base 基线引用的对象 GC 后一律存活（ADR-0007 §4
	// 计划引用通道的对象面）。
	for i, d := range planBaseDigests {
		ok, err := stack.CAS.Has(ctx, d)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("正例①活跃计划引用对象 %s（#%d）不可用（Has=false）", d, i)
		}
	}
	if probes && len(planBaseDigests) > 0 {
		fmt.Printf("== 断言⑤正例① == 活跃计划引用对象 %d 个全部存活\n", len(planBaseDigests))
	}

	// 孤儿快照扩展（票 #89，ADR-0011 §4）：被裁提交的验证快照自然转孤儿
	// 一并删；从未进提交的中间扫描快照除最新外同删；resource_representations
	// 随行级联删；引用图不变式对账扩展至快照账面（清扫后孤儿必须为零）。
	swept, left, err := assertOrphanSnapshotSweep(ctx, stack, preVerified, midSnapshots)
	if err != nil {
		return err
	}
	if stats != nil {
		stats.OrphanSnapshotsSwept = swept
		stats.OrphanSnapshotsLeft = left
	}
	fmt.Printf("== 断言⑥快照账面 == 孤儿清扫 %d 份（对账残留 %d）\n", swept, left)

	// 正例收尾：续跑后 ②③ 的观察 digest 应已被回收（解除保护后成候选）。
	if stats != nil {
		stats.Tombstones = page.PrunedBeforeCount
		stats.AliveCommits = len(commits.Items)
		stats.AuditViolations = len(findings)
	}
	if probes {
		for name, digest := range map[string]string{"staged绑定": probeA, "recovery引用": probeB} {
			state, err := gcObjectState(stack.DB, digest)
			if err != nil {
				return err
			}
			if state == "ready" {
				return fmt.Errorf("正例观察 digest %s（%s）续跑后仍 ready，期望已被回收", digest, name)
			}
			fmt.Printf("== 断言⑤正例收尾 == %s %s 续跑后已回收（state=%s）\n", name, digest, state)
		}
	}
	fmt.Println("== -gc 链路完成 == 全部断言通过")
	return nil
}

// seedGCProbes 落三红线正例夹具（probes 模式；调用前提 keep_commits=5）：
//
//	前置：seed +7 提交（head 前移，使活跃计划的 base 基线落入可裁区）
//	①活跃计划：PrepareSync 建 draft 计划（base=当时头基线）→ 屏障令修剪
//	  在该基线所在提交前截断（存活 > N=5，证明屏障而非保底生效）；
//	②staged 绑定 / ③recovery_required 引用：SQL 注入两个未收口 run，
//	  recovery_refs 指向将被裁提交（链首区）的独占 digest → 安全窗口关闭。
func seedGCProbes(ctx context.Context, stack *bootstrap.Stack, app syncapp.Application, rel view.RelationView, projectRoot string, probeStaged, probeRecovery *string, planBaseDigests *[]string) error {
	before, err := listAllCommits(ctx, app, rel.RelationID)
	if err != nil {
		return err
	}
	if len(before.Items) < gc.HardFloorKeep+2 {
		return fmt.Errorf("前置提交数 %d 不足（期望历史链已建立）", len(before.Items))
	}
	// ①活跃 draft 计划：base = 当前头基线（PrepareSync 输入面推导）。
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return err
	}
	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.Relation.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		return fmt.Errorf("屏障计划: %w", err)
	}
	fmt.Printf("== 正例① == draft 计划 %s（base=%s）——屏障基线=当前头\n", plan.PlanID, plan.BaseBaselineID)

	// head 前移 7：base 基线所在提交进入可裁区，屏障产生可观察的截断差。
	seedDir := filepath.Join(projectRoot, "config", "gc-seed") // 受管范围（PrepareRelation 建议 config/kubejs/scripts）
	for i := 0; i < 7; i++ {
		name := filepath.Join(seedDir, fmt.Sprintf("probe-%02d.txt", i+1))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("gc probe %d @ %d\n", i+1, time.Now().UnixNano())), 0o644); err != nil {
			return err
		}
		applyOneCommit(ctx, app, rel, i+1)
	}

	// ②③观察 digest：链首区（probes 场景屏障截断后必然被裁）取有对象引用的
	// 两个不同提交——create 操作无 before 保全（零 refs），modify 才有，故
	// 动态挑选而非固定下标。
	picked := 0
	for _, c := range before.Items {
		if picked >= 2 {
			break
		}
		d, err := commitExclusiveDigest(stack.DB, rel.RelationID, c.CommitID)
		if err != nil {
			continue // 无引用提交（纯 create），取下一个
		}
		if picked == 0 {
			*probeStaged = d
		} else {
			*probeRecovery = d
		}
		picked++
	}
	if picked < 2 {
		return fmt.Errorf("链首区有引用提交不足（%d/2），夹具无法构造", picked)
	}

	// ②staged run：task+run 两行（run.state='staged'，恢复引用指向观察 digest）
	// ——「进行中 run 的 staging 绑定」：窗口关闭 + cas 引用受保护。
	if err := insertProbeRun(stack.DB, rel.RelationID, plan.PlanID, "staged", *probeStaged); err != nil {
		return fmt.Errorf("staged 夹具: %w", err)
	}
	// ③recovery_required run：窗口关闭的另一构成项（含关系健康面）。
	if err := insertProbeRun(stack.DB, rel.RelationID, plan.PlanID, "recovery_required", *probeRecovery); err != nil {
		return fmt.Errorf("recovery 夹具: %w", err)
	}
	if _, err := stack.DB.Exec("UPDATE relations SET health='recovery_required' WHERE id=?", rel.RelationID); err != nil {
		return err
	}
	fmt.Printf("== 正例②③ == staged 绑定=%s recovery 引用=%s（两个未收口 run 已注入，窗口应关闭）\n", *probeStaged, *probeRecovery)
	return nil
}

// assertGCWaiting 断言安全窗口关闭期间：任务停在 queued 且排队文案为
// msg.task.gc.waiting，两个观察 digest 保持 ready（不回收）。
func assertGCWaiting(ctx context.Context, app syncapp.Application, taskID, d1, d2 string) error {
	deadline := time.Now().Add(gcWindowProbeTimeout)
	for {
		tv, err := app.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if tv.MessageKey == "msg.task.gc.waiting" {
			if tv.Status != model.TaskStatusQueued {
				return fmt.Errorf("排队文案已出但 status=%s（期望 queued=pending 排队态）", tv.Status)
			}
			break
		}
		if tv.Status == model.TaskStatusSucceeded || tv.Status == model.TaskStatusFailed {
			return fmt.Errorf("gc 任务在窗口关闭期间走到 %s（安全窗口未生效）", tv.Status)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待排队文案超时（当前 status=%s phase=%s msg=%s）", tv.Status, tv.Phase, tv.MessageKey)
		}
		time.Sleep(gcPollInterval)
	}
	fmt.Println("== 断言④安全窗口 == 任务 pending，文案=msg.task.gc.waiting（等待空闲时段（安全窗口未开 · 自动续排））")
	tv, _ := app.GetTask(ctx, taskID)
	fmt.Printf("  （status=%s phase=%s）\n", tv.Status, tv.Phase)
	return nil
}

// resolveGCProbes 收口两个夹具 run：staged→committed（SQL）、
// recovery_required→AcknowledgeRecovery（真实恢复出口：幂等标记+健康复位+事件
// + kickGC）。收口后窗口打开，排队任务自动续排。
func resolveGCProbes(ctx context.Context, stack *bootstrap.Stack) error {
	if _, err := stack.DB.Exec(
		"UPDATE apply_runs SET state='committed', updated_at=? WHERE state='staged'",
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	var recTaskID string
	err := stack.DB.QueryRow(
		"SELECT task_id FROM apply_runs WHERE state='recovery_required' ORDER BY created_at DESC LIMIT 1").Scan(&recTaskID)
	if err != nil {
		return fmt.Errorf("找 recovery 夹具运行: %w", err)
	}
	var dbgState, dbgRel string
	if err := stack.DB.QueryRow("SELECT state, relation_id FROM apply_runs WHERE task_id=?", recTaskID).Scan(&dbgState, &dbgRel); err != nil {
		return fmt.Errorf("调试读取: %w", err)
	}
	fmt.Printf("  [调试] rec run=%s state=%s rel=%s\n", recTaskID, dbgState, dbgRel)
	if _, err := stack.SyncApp.AcknowledgeRecovery(ctx, recTaskID); err != nil {
		return fmt.Errorf("AcknowledgeRecovery: %w", err)
	}
	return nil
}

// auditReferenceGraph 采集四侧事实并调 core/gc.Audit 逐 digest 对账。
// 可达闭包 = 存活提交 object_refs ∪ 存活基线 logical_digest 命中 ∪ 活跃/未处置
// run 恢复引用（run 级 + journal 级，kind=cas）。
func auditReferenceGraph(ctx context.Context, stack *bootstrap.Stack, app syncapp.Application, relationID string) ([]gc.AuditFinding, map[string]int, error) {
	gcRepo := stack.GCRepo
	rels, err := stack.RelationIDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	reach := map[string]bool{}
	for _, relID := range rels {
		refs, err := gcRepo.RelationObjectRefs(ctx, relID)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range refs {
			reach[r.Digest] = true
		}
	}
	hits, err := gcRepo.BaselineDigestHits(ctx, rels)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range hits {
		reach[d] = true
	}
	planHits, err := gcRepo.PlanBaseDigestHits(ctx, rels)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range planHits {
		reach[d] = true
	}
	runRefs, err := gcRepo.UnresolvedRunRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	journalRefs, err := gcRepo.JournalCASRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, raw := range append(append([][]byte{}, runRefs...), journalRefs...) {
		var refs []map[string]string
		if json.Unmarshal(raw, &refs) == nil {
			for _, r := range refs {
				if r["kind"] == "cas" && r["digest"] != "" {
					reach[r["digest"]] = true
				}
			}
		}
	}
	ready, err := gcRepo.ReadyDigests(ctx)
	if err != nil {
		return nil, nil, err
	}
	quarantined, err := gcRepo.ListQuarantined(ctx)
	if err != nil {
		return nil, nil, err
	}
	qset := make([]string, 0, len(quarantined))
	for _, q := range quarantined {
		qset = append(qset, q.Digest)
	}
	// 隔离区含 trash 副本（账删文件未删/复活对账中的过渡态不算违例）。
	trashEntries, err := stack.GCTrash.ListTrash()
	if err != nil {
		return nil, nil, err
	}
	for _, e := range trashEntries {
		qset = append(qset, e.Digest)
	}
	files, err := stack.GCTrash.ListObjectFiles()
	if err != nil {
		return nil, nil, err
	}
	onDisk := make([]string, 0, len(files))
	for _, f := range files {
		onDisk = append(onDisk, f.Digest)
	}
	findings := gc.Audit(gc.AuditInput{
		Reachable:   setKeys(reach),
		Quarantined: qset,
		ReadyRows:   ready,
		OnDisk:      onDisk,
	})
	return findings, map[string]int{
		"reachable":   len(reach),
		"quarantined": len(qset),
		"ready_rows":  len(ready),
		"on_disk":     len(onDisk),
	}, nil
}

// verifyCommitRestorable 复验提交可回滚的数据前提：其 object_refs 与结果基线
// logical_digest 指向的全部对象 ready 且逐字节 sha256 复验。
func verifyCommitRestorable(ctx context.Context, stack *bootstrap.Stack, relationID, commitID string) (int, error) {
	digests, err := commitReachableDigests(stack.DB, commitID)
	if err != nil {
		return 0, err
	}
	if len(digests) == 0 {
		return 0, nil // 纯 create 提交零 CAS 对象（restore 走 copy/重取通道）
	}
	for _, d := range digests {
		ok, err := stack.CAS.Has(ctx, d)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("对象 %s 不可用（Has=false）", d)
		}
		rc, err := stack.CAS.Open(ctx, d)
		if err != nil {
			return 0, fmt.Errorf("打开对象 %s: %w", d, err)
		}
		h := sha256.New()
		_, err = io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return 0, err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != d {
			return 0, fmt.Errorf("对象 %s 内容复验不符（实际 %s）", d, got)
		}
	}
	return len(digests), nil
}

// ---- SQL 采集小助手（验收链对账/夹具注入；只读为主，夹具写入显式注释）----

// seedScanOnlyRounds 落「从未进提交的中间扫描快照」夹具（票 #89，ADR-0011
// §4）：两轮纯扫描（不建计划）。第一对快照随第二轮失去「任一端最新」，且
// 不被任何提交（verified_*）与计划（input_*）引用 → 孤儿；返回这对快照 id。
func seedScanOnlyRounds(ctx context.Context, app syncapp.Application, rel view.RelationView) ([]string, error) {
	var mid []string
	for round := 1; round <= 2; round++ {
		if _, err := app.StartScan(ctx, rel.RelationID); err != nil {
			return nil, fmt.Errorf("第 %d 轮 StartScan: %w", round, err)
		}
		waitScan(ctx, app, rel.RelationID)
		ws, err := app.GetWorkspace(ctx, rel.RelationID)
		if err != nil {
			return nil, fmt.Errorf("第 %d 轮 GetWorkspace: %w", round, err)
		}
		if round == 1 {
			mid = []string{ws.LatestProjectSnapshot.SnapshotID, ws.LatestRuntimeSnapshot.SnapshotID}
		}
	}
	fmt.Printf("== 孤儿快照夹具 == 中间扫描快照 %v（无引用，应随 GC 清扫删除）\n", mid)
	return mid, nil
}

// commitVerifiedSnapshots 提交 → 验证快照对（pre-GC 账面；post-GC 用存活集
// 求差得被裁集合）。
func commitVerifiedSnapshots(db *sql.DB) (map[string][2]string, error) {
	rows, err := db.Query(`SELECT id, verified_project_snapshot_id, verified_runtime_snapshot_id FROM sync_commits`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var id, vp, vr string
		if err := rows.Scan(&id, &vp, &vr); err != nil {
			return nil, err
		}
		out[id] = [2]string{vp, vr}
	}
	return out, rows.Err()
}

// assertOrphanSnapshotSweep 断言孤儿快照清扫生效（票 #89，ADR-0011 §4）：
//   - 被裁提交（pre-GC 有、post-GC 无）的验证快照已随资源表示行一并删除
//     ——「提交被修剪 → 其验证快照自然转孤儿一并删」；
//   - 中间扫描快照（从未进提交/计划的夹具）已删除；
//   - 引用图不变式对账扩展至快照账面：core/gc.OrphanSnapshots 对清扫后账面
//     判孤儿，残留必须为零。
//
// 返回本轮断言确认被清扫的快照份数与对账残留数（残留非零即失败）。
func assertOrphanSnapshotSweep(ctx context.Context, stack *bootstrap.Stack,
	preVerified map[string][2]string, midSnapshots []string) (int, int, error) {

	snapExists := func(id string) (bool, bool, error) {
		var snap, res int
		if err := stack.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM observed_snapshots WHERE id=?", id).Scan(&snap); err != nil {
			return false, false, err
		}
		if err := stack.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM resource_representations WHERE snapshot_id=?", id).Scan(&res); err != nil {
			return false, false, err
		}
		return snap > 0, res > 0, nil
	}

	swept := 0
	// 被裁提交的验证快照：转孤儿一并删。
	surviving := map[string]bool{}
	rows, err := stack.DB.QueryContext(ctx, "SELECT id FROM sync_commits")
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		surviving[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for id, ver := range preVerified {
		if surviving[id] {
			continue
		}
		for _, snapID := range ver {
			inSnap, inRes, err := snapExists(snapID)
			if err != nil {
				return 0, 0, err
			}
			if inSnap || inRes {
				return 0, 0, fmt.Errorf("被裁提交 %s 的验证快照 %s 未清扫（snapshot_row=%v resource_row=%v）",
					id, snapID, inSnap, inRes)
			}
			swept++
		}
	}
	// 中间扫描快照：除最新外同删（夹具对即「除最新」者）。
	for _, snapID := range midSnapshots {
		inSnap, inRes, err := snapExists(snapID)
		if err != nil {
			return 0, 0, err
		}
		if inSnap || inRes {
			return 0, 0, fmt.Errorf("中间扫描快照 %s 未清扫（snapshot_row=%v resource_row=%v）", snapID, inSnap, inRes)
		}
		swept++
	}

	// 快照账面对账：清扫后孤儿必须为零（引用图不变式的快照侧扩展）。
	facts, err := stack.GCRepo.SnapshotRefFacts(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("快照账面采集: %w", err)
	}
	orphans := gc.OrphanSnapshots(gc.SnapshotFacts{
		All:            facts.All,
		CommitVerified: facts.CommitVerified,
		PlanInput:      facts.PlanInput,
		Latest:         facts.Latest,
	})
	if len(orphans) > 0 {
		return 0, len(orphans), fmt.Errorf("快照账面对账残留孤儿 %d 份: %v", len(orphans), orphans)
	}
	return swept, 0, nil
}

// listAllCommits 跨页收集全部提交（created_at DESC）。
func listAllCommits(ctx context.Context, app syncapp.Application, relationID string) (view.CommitPage, error) {
	all := view.CommitPage{}
	cursor := ""
	for {
		page, err := app.ListCommits(ctx, relationID, ports.PageRequest{Cursor: cursor, Limit: ports.MaxPageLimit})
		if err != nil {
			return all, err
		}
		all.Items = append(all.Items, page.Items...)
		all.PrunedBeforeCount = page.PrunedBeforeCount
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// waitTaskStatus 轮询任务到期望状态（终态任一则提前失败面返回）。
func waitTaskStatus(ctx context.Context, app syncapp.Application, taskID, want string, timeout time.Duration) (view.TaskView, error) {
	deadline := time.Now().Add(timeout)
	lastPhase := ""
	for {
		tv, err := app.GetTask(ctx, taskID)
		if err != nil {
			return view.TaskView{}, err
		}
		if tv.Phase != lastPhase {
			fmt.Printf("  [gc poll] status=%s phase=%s msg=%s\n", tv.Status, tv.Phase, tv.MessageKey)
			lastPhase = tv.Phase
		}
		if tv.Status == want {
			return tv, nil
		}
		switch tv.Status {
		case model.TaskStatusFailed, model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
			return tv, fmt.Errorf("gc 任务终态 %s（期望 %s）problem=%s", tv.Status, want, problemText(tv.Problem))
		}
		if time.Now().After(deadline) {
			return tv, fmt.Errorf("gc 任务超时未达 %s（当前 %s/%s）", want, tv.Status, tv.Phase)
		}
		time.Sleep(gcPollInterval)
	}
}

// gcObjectState 查对象行状态（"" = 无行）。
func gcObjectState(db *sql.DB, digest string) (string, error) {
	var state string
	err := db.QueryRow("SELECT state FROM objects WHERE algorithm='sha256' AND digest=?", digest).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return state, err
}

// planBaseHitDigests 活跃计划 base 基线命中的 CAS 对象 digest（正例①观察面）。
func planBaseHitDigests(db *sql.DB, baselineID string) ([]string, error) {
	rows, err := db.Query(`
SELECT br.logical_digest FROM baseline_resources br
JOIN objects o ON o.algorithm='sha256' AND o.digest = br.logical_digest
WHERE br.baseline_id=?`, baselineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// commitExclusiveDigest 返回提交 object_refs 中第一个 digest（观察对象；
// 链首区提交必然被裁，其独占对象无保护时必成候选）。
func commitExclusiveDigest(db *sql.DB, relationID, commitID string) (string, error) {
	var d string
	err := db.QueryRow(
		"SELECT digest FROM object_refs WHERE owner_type='commit' AND owner_id=? LIMIT 1", commitID).Scan(&d)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("提交 %s 无对象引用", commitID)
	}
	return d, err
}

// commitReachableDigests 提交的可达闭包：object_refs 全部 digest ∪ 结果基线
// logical_digest 命中 objects 表的部分（logical_digest 是逻辑状态指纹，仅当
// 恰为 CAS 对象时才可做逐字节复验）。
func commitReachableDigests(db *sql.DB, commitID string) ([]string, error) {
	rows, err := db.Query(`
SELECT digest FROM object_refs WHERE owner_type='commit' AND owner_id=?
UNION
SELECT br.logical_digest FROM sync_commits c
JOIN baseline_resources br ON br.baseline_id = c.result_baseline_id
JOIN objects o ON o.algorithm='sha256' AND o.digest = br.logical_digest
WHERE c.id=?`, commitID, commitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// insertProbeRun 注入夹具运行（验收正例的可控注入面）：tasks 行满足
// apply_runs.task_id 外键，run.state 与恢复引用按场景给定。
func insertProbeRun(db *sql.DB, relationID, planID, state, digest string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	taskID := fmt.Sprintf("task_probe_%s_%d", state, time.Now().UnixNano())
	var planDigest string
	if err := db.QueryRow("SELECT plan_digest FROM sync_plans WHERE id=?", planID).Scan(&planDigest); err != nil {
		return fmt.Errorf("读屏障计划 digest: %w", err)
	}
	if _, err := db.Exec(`
INSERT INTO tasks(id, relation_id, kind, status, phase, can_cancel, message_key, created_at, updated_at)
VALUES(?, ?, 'apply', 'running', 'probe', 0, 'msg.task.apply.staging', ?, ?)`,
		taskID, relationID, now, now); err != nil {
		return fmt.Errorf("插夹具任务: %w", err)
	}
	refs := fmt.Sprintf(`[{"operation_id":"probe_op","kind":"cas","algorithm":"sha256","digest":%q,"purpose":"before_preservation"}]`, digest)
	if _, err := db.Exec(`
INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest, relation_revision, state,
	preconditions_json, recovery_refs_json, operation_count, created_at, updated_at)
VALUES(?, ?, ?, ?, 1, ?, '[]', ?, 1, ?, ?)`,
		taskID, relationID, planID, planDigest, state, refs, now, now); err != nil {
		return fmt.Errorf("插夹具运行: %w", err)
	}
	return nil
}

// setKeys map 键列表（断言器入参）。
func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
