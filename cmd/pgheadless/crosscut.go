package main

// pgheadless -crosscut（P4 票 #98；验收规格 §5.2）：横切真重启清理链三段。
// 惰性清理挂「启动时 + 任务终态」两个时机（ADR-0011 §2/§3，票 #89/#91），
// 单测够不着真实启动路径，必须真跑：
//
//	段① 启动通道：超量造数（>20 会话日志目录含超 100MB——按 #91 的清理顺序
//	     语义先分层压缩后硬顶计量，造数用不可压缩内容与已压缩 .gz 两种形态
//	     才能触发硬顶删；>10k task_events、>200 终态 tasks、过期
//	     sync_plans/preparations）→ 新起 pgheadless -crosscut-restart 子进程
//	     走生产启动通道（会话日志双轴保留清理 + bootstrap 启动惰性清理）→
//	     断言清理生效（窗口内/硬顶内/造数行消失/活跃行零触碰）。
//	段② 任务终态通道：再次超量造数 → 驱动一次扫描任务收口 → 断言终态后
//	     惰性清理异步触发（轮询账面收敛，不卡具体时长）。
//	段③ 脱敏断言（R1/R2）：坏 metafile 造含绝对路径的端点错误 → 断言新写
//	     diag.scan.modmeta_unreadable 的 Detail 为 <project> 别名路径、无
//	     用户名（历史行不追溯，不断言）；-metrics 输出无 Host 键（OS/Arch/
//	     GoVersion/CPUs 保留）。R3 凭据复核归验收报告（L0 断言面 =
//	     TestR3CredentialNoLeakOnFailure，task test 硬门槛）。
//
// 记录归 docs/acceptance/records/p4-crosscut-<date>-<host>.json（p4-crosscut/1，
// -record 空时自动命名，沿 -download/-watch 先例）。

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/appconfig"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/core/model"
	"packgradle/internal/download"
	"packgradle/internal/sessionlog"
	"packgradle/internal/store"
)

// 造数规模（编译期常量，验收规格 §5.2 的「>」下界取整档）。
const (
	// 会话日志：25 个目录 × 8MB ≈ 200MB（>20 份 ∧ >100MB 硬顶）。
	xcSeedLogDirs      = 25
	xcSeedLogBytesEach = 8 << 20
	// task_events 一次超出 10,000 窗口 500 条；终态 tasks 超出 200 窗口 40 条。
	xcSeedEvents = 10500
	xcSeedTasks  = 240
	// 段②的二次造数（在段①收敛后的账面上再次超出窗口）。
	xcSeed2Events = 500
	xcSeed2Tasks  = 60
	// 过期历史行：sync_plans / preparations 各 3 条。
	xcSeedPlans = 3
	xcSeedPreps = 3
	// xcExpiredAt 是造数历史行的过期时点（RFC3339 UTC，串比较即时间比较）。
	xcExpiredAt = "2000-01-01T00:00:00Z"
	// xcPollTimeout 是段②终态钩子异步清理的轮询上界（不卡时长，只断收敛）。
	xcPollTimeout = 30 * time.Second
)

// crosscutRestartReport 是 -crosscut-restart 子进程的启动通道计数报告
//（p4-crosscut-restart/1）：只走生产启动通道（会话日志保留清理 + bootstrap
// 启动惰性清理），不触碰关系/扫描面——报告即「重启后账面」的直接观测。
type crosscutRestartReport struct {
	Schema           string `json:"schema"`
	CapturedAt       string `json:"captured_at"`
	DataRoot         string `json:"data_root"`
	LogsDirCount     int    `json:"logs_dir_count"`
	LogsTotalBytes   int64  `json:"logs_total_bytes"`
	CurrentSessionDir string `json:"current_session_dir,omitempty"`
	TaskEventsCount  int64  `json:"task_events_count"`
	TaskEventsMinSeq int64  `json:"task_events_min_seq"`
	TaskEventsMaxSeq int64  `json:"task_events_max_seq"`
	TerminalTasks    int64  `json:"terminal_tasks_count"`
	ActiveTasks      int64  `json:"active_tasks_count"`
	// ExpiredDockedPlansRemaining 是残留的「expired ∧ draft/resolved」计划行
	//（清理守卫同款判定：无提交/运行/子计划引用——链上造数行满足无引用，
	// 残留即未清）。
	ExpiredDockedPlansRemaining int64 `json:"expired_docked_plans_remaining"`
	// ExpiredOrConsumedPreparationsRemaining 是残留的过期/已消费预检行。
	ExpiredOrConsumedPreparationsRemaining int64 `json:"expired_or_consumed_preparations_remaining"`
}

// crosscutStartupSegment 是段①的记录面（造数账面 + 重启前后对比 + 断言清单）。
type crosscutStartupSegment struct {
	SeedLogDirs       int     `json:"seed_log_dirs"`
	SeedLogBytesEach  int64   `json:"seed_log_bytes_each"`
	SeedTaskEvents    int64   `json:"seed_task_events"`
	SeedTerminalTasks int64   `json:"seed_terminal_tasks"`
	SeedPlans         int64   `json:"seed_plans"`
	SeedPreparations  int64   `json:"seed_preparations"`
	SeedPlanIDs       []string `json:"seed_plan_ids,omitempty"`
	SeedPreparationIDs []string `json:"seed_preparation_ids,omitempty"`
	SeedTaskIDs       []string `json:"seed_task_ids,omitempty"`
	SeedLogDirNames   []string `json:"seed_log_dir_names,omitempty"`
	// PreRestart 是子进程重启前的账面（>20 份 ∧ >100MB 前置条件的证据）。
	PreRestart struct {
		LogsDirCount   int   `json:"logs_dir_count"`
		LogsTotalBytes int64 `json:"logs_total_bytes"`
	} `json:"pre_restart"`
	// RestartReport 是重启子进程的启动通道观测（p4-crosscut-restart/1）。
	RestartReport *crosscutRestartReport `json:"restart_report"`
	// Assertions 是逐条断言清单（全部 "pass" 前缀；链失败时链直接非零退出，
	// 记录只在全过时落盘）。
	Assertions []string `json:"assertions"`
}

// crosscutTerminalSegment 是段②的记录面。
type crosscutTerminalSegment struct {
	SeedTaskEvents    int64 `json:"seed_task_events"`
	SeedTerminalTasks int64 `json:"seed_terminal_tasks"`
	AfterTaskEvents   int64 `json:"after_task_events"`
	AfterTerminal     int64 `json:"after_terminal_tasks"`
	SettleMS          int64 `json:"settle_ms"`
	Assertions        []string `json:"assertions"`
}

// crosscutRedactionSegment 是段③的记录面（R1/R2）。
type crosscutRedactionSegment struct {
	R1DiagCode       string `json:"r1_diag_code"`
	R1DetailAliased  bool   `json:"r1_detail_aliased"`
	R1Assertions     []string `json:"r1_assertions"`
	R2MetricsNoHost  bool   `json:"r2_metrics_no_host"`
	R2Assertions     []string `json:"r2_assertions"`
}

// crosscutRecord 是 -crosscut 链的验收记录（p4-crosscut/1）。
type crosscutRecord struct {
	Schema       string                   `json:"schema"`
	Ticket       string                   `json:"ticket"`
	Spec         string                   `json:"spec"`
	CapturedAt   string                   `json:"captured_at"`
	Machine      machineInfo              `json:"machine"`
	RelationID   string                   `json:"relation_id"`
	DataRoot     string                   `json:"data_root"`
	Startup      crosscutStartupSegment   `json:"startup"`
	Terminal     crosscutTerminalSegment  `json:"terminal"`
	Redaction    crosscutRedactionSegment `json:"redaction"`
	ChainTotalMS int64                    `json:"chain_total_ms"`
}

// crosscutChainStats 是 -crosscut 的 -metrics 段（段耗时只记录不设门槛）。
type crosscutChainStats struct {
	Kind               string `json:"kind"`
	StartupSegmentMS   int64  `json:"startup_segment_ms"`
	TerminalSegmentMS  int64  `json:"terminal_segment_ms"`
	RedactionSegmentMS int64  `json:"redaction_segment_ms"`
	ChainTotalMS       int64  `json:"chain_total_ms"`
	R1DetailAliased    bool   `json:"r1_detail_aliased"`
	R2MetricsNoHost    bool   `json:"r2_metrics_no_host"`
}

// runCrosscutRestart 是 -crosscut-restart 探测子进程：只走生产启动通道
//（sessionlog.Open 的启动双轴保留清理 + bootstrap 装配内的启动惰性清理），
// 不建关系不扫描，写启动后计数报告后退出。父进程（-crosscut）据此断言。
func runCrosscutRestart(dataRoot, reportPath string) {
	root := dataRoot
	if root == "" {
		var err error
		root, err = store.DefaultRoot()
		if err != nil {
			log.Fatalf("定位用户数据目录失败: %v", err)
		}
	}
	// 启动通道①：会话日志（保留清理在 Open 内同步执行）。
	sess, serr := sessionlog.Open(filepath.Join(root, "logs"), sessionlog.Options{})
	if serr != nil {
		log.Printf("会话日志初始化失败（不阻断启动）: %v", serr)
	} else {
		defer sess.Close()
	}
	// 启动通道②：bootstrap 装配（迁移门禁 + 启动恢复 + RunLazyCleanup）。
	retentionMgr, err := appconfig.NewConfigManagerAtLoaded(filepath.Join(root, "config.toml"))
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}
	stack, err := bootstrap.BuildWithDownloadOptions(root, retentionMgr, download.Options{})
	if err != nil {
		log.Fatalf("装配失败: %v", err)
	}
	defer stack.Close()

	rep := crosscutRestartReport{
		Schema: "p4-crosscut-restart/1", CapturedAt: time.Now().UTC().Format(time.RFC3339),
		DataRoot: root,
	}
	rep.LogsDirCount, rep.LogsTotalBytes, err = sessionlog.Stats(filepath.Join(root, "logs"))
	if err != nil {
		log.Fatalf("会话日志账面统计失败: %v", err)
	}
	if sess != nil {
		rep.CurrentSessionDir = sess.Dir
	}
	seqFace := xcEventSeqFace(stack.DB)
	rep.TaskEventsCount, rep.TaskEventsMinSeq, rep.TaskEventsMaxSeq = seqFace.count, seqFace.min, seqFace.max
	rep.TerminalTasks = xcCount(stack.DB,
		"SELECT COUNT(*) FROM tasks WHERE status IN ('succeeded','failed','cancelled','recovery_required')")
	rep.ActiveTasks = xcCount(stack.DB,
		"SELECT COUNT(*) FROM tasks WHERE status IN ('queued','running')")
	now := time.Now().UTC().Format(time.RFC3339)
	rep.ExpiredDockedPlansRemaining = xcCount(stack.DB,
		"SELECT COUNT(*) FROM sync_plans WHERE status IN ('draft','resolved') AND expires_at <= ?", now)
	rep.ExpiredOrConsumedPreparationsRemaining = xcCount(stack.DB,
		"SELECT COUNT(*) FROM preparations WHERE consumed_at IS NOT NULL OR expires_at <= ?", now)

	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		log.Fatalf("报告序列化失败: %v", err)
	}
	if err := os.WriteFile(reportPath, b, 0o644); err != nil {
		log.Fatalf("写启动通道报告失败: %v", err)
	}
	fmt.Printf("== -crosscut-restart == 启动通道完成（logs=%d 份/%dB task_events=%d 终态tasks=%d 残留过期计划=%d 预检=%d）\n",
		rep.LogsDirCount, rep.LogsTotalBytes, rep.TaskEventsCount, rep.TerminalTasks,
		rep.ExpiredDockedPlansRemaining, rep.ExpiredOrConsumedPreparationsRemaining)
}

// runCrosscutChain 执行三段链。stack 为 main 装配的生产栈（段①造数经同一
// DB；子进程重启窗口内父进程零 DB 活动——空闲连接不持 SQLite 锁，子进程
// 清理写不受阻）。rel 为已登记关系。
func runCrosscutChain(ctx context.Context, stack *bootstrap.Stack, app syncapp.Application,
	rel view.RelationView, projectAbs, root string, recordPath, metricsPath string) (*crosscutChainStats, error) {

	chainStart := time.Now()
	logsDir := filepath.Join(root, "logs")
	rec := crosscutRecord{
		Schema: "p4-crosscut/1", Ticket: "skyraah/PackGradle#98",
		Spec: "docs/acceptance/p4-acceptance-spec.md §5.2 横切真重启清理链三段",
		Machine: newMachineInfo(), RelationID: rel.RelationID, DataRoot: root,
	}
	fmt.Println("== -crosscut == 段① 启动通道（超量造数 → 真重启 → 断言清理生效）")

	// ---- 段① 前置：先扫一轮（快照面供造数计划行引用；产生基线账面）----
	if _, err := app.StartScan(ctx, rel.RelationID); err != nil {
		return nil, fmt.Errorf("段① 初始扫描: %w", err)
	}
	waitScan(ctx, app, rel.RelationID)

	seqBase := xcEventSeqFace(stack.DB).max
	seedPlanIDs, err := xcSeedExpiredPlans(ctx, stack.DB, rel.RelationID)
	if err != nil {
		return nil, fmt.Errorf("段① 造数过期计划: %w", err)
	}
	seedPrepIDs, err := xcSeedExpiredPreparations(ctx, stack.DB)
	if err != nil {
		return nil, fmt.Errorf("段① 造数过期预检: %w", err)
	}
	seedTaskIDs, err := xcSeedTerminalTasks(ctx, stack.DB, "xc-seed1-task", xcSeedTasks)
	if err != nil {
		return nil, fmt.Errorf("段① 造数终态任务: %w", err)
	}
	if err := xcSeedTaskEvents(ctx, stack.DB, seqBase, xcSeedEvents); err != nil {
		return nil, fmt.Errorf("段① 造数 task_events: %w", err)
	}
	seedLogNames, err := xcSeedSessionLogs(logsDir)
	if err != nil {
		return nil, fmt.Errorf("段① 造数会话日志: %w", err)
	}
	preCount, preBytes, err := sessionlog.Stats(logsDir)
	if err != nil {
		return nil, fmt.Errorf("段① 重启前账面: %w", err)
	}
	rec.Startup = crosscutStartupSegment{
		SeedLogDirs: xcSeedLogDirs, SeedLogBytesEach: xcSeedLogBytesEach,
		SeedTaskEvents: xcSeedEvents, SeedTerminalTasks: xcSeedTasks,
		SeedPlans: xcSeedPlans, SeedPreparations: xcSeedPreps,
		SeedPlanIDs: seedPlanIDs, SeedPreparationIDs: seedPrepIDs,
		SeedTaskIDs: seedTaskIDs, SeedLogDirNames: seedLogNames,
	}
	rec.Startup.PreRestart.LogsDirCount, rec.Startup.PreRestart.LogsTotalBytes = preCount, preBytes

	// ---- 真重启：新起 pgheadless -crosscut-restart 子进程走生产启动通道。
	// 父进程窗口内零 DB 活动；子进程退出码即启动通道成败。----
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	reportPath := filepath.Join(root, "crosscut-restart-report.json")
	fmt.Printf("== -crosscut == 真重启：%s -crosscut-restart %s -data %s\n", filepath.Base(self), reportPath, root)
	cmd := exec.Command(self, "-crosscut-restart", reportPath, "-data", root)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("段① 重启子进程失败: %w", err)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, fmt.Errorf("段① 读取启动通道报告: %w", err)
	}
	var rep crosscutRestartReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("段① 解析启动通道报告: %w", err)
	}
	rec.Startup.RestartReport = &rep

	// ---- 段① 断言（启动通道清理生效）----
	asserts := []string{}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("段① 断言失败: "+format, args...)
	}
	if preCount <= 20 || preBytes <= 100<<20 {
		return nil, fail("造数前置不成立：重启前 logs=%d 份/%dB，应 >20 份 ∧ >100MB", preCount, preBytes)
	}
	asserts = append(asserts, fmt.Sprintf("pass 造数前置：重启前 %d 份会话目录 / %dB（>20 ∧ >100MB）", preCount, preBytes))
	if rep.TaskEventsCount > 10000 {
		return nil, fail("task_events=%d 应收敛到 10,000 窗口内", rep.TaskEventsCount)
	}
	if rep.TaskEventsMinSeq <= seqBase {
		return nil, fail("task_events 尾部窗保留不成立：min_seq=%d 应 > 造数前基线 %d（窗口外旧行已截断）", rep.TaskEventsMinSeq, seqBase)
	}
	asserts = append(asserts, fmt.Sprintf("pass task_events 窗口：%d 条（≤10000），min_seq=%d > 造数基线 %d（留尾截断）",
		rep.TaskEventsCount, rep.TaskEventsMinSeq, seqBase))
	if rep.TerminalTasks > 200 {
		return nil, fail("终态任务=%d 应收敛到 200 窗口内", rep.TerminalTasks)
	}
	if rep.ActiveTasks != 0 {
		return nil, fail("活跃任务=%d 应零触碰（清理只动终态行）", rep.ActiveTasks)
	}
	asserts = append(asserts, fmt.Sprintf("pass 终态任务窗口：%d 条（≤200），活跃行零触碰（active=%d）",
		rep.TerminalTasks, rep.ActiveTasks))
	if rep.ExpiredDockedPlansRemaining != 0 {
		return nil, fail("过期历史计划残留 %d 行（造数 %d 行应物理删）", rep.ExpiredDockedPlansRemaining, xcSeedPlans)
	}
	if rep.ExpiredOrConsumedPreparationsRemaining != 0 {
		return nil, fail("过期/已消费预检残留 %d 行（造数 %d 行应物理删）", rep.ExpiredOrConsumedPreparationsRemaining, xcSeedPreps)
	}
	asserts = append(asserts, fmt.Sprintf("pass 旧数据行：造数过期计划 %d 行 + 过期预检 %d 行全部物理删（残留 0）", xcSeedPlans, xcSeedPreps))
	if rep.LogsDirCount > 20 || rep.LogsTotalBytes > 100<<20 {
		return nil, fail("会话日志清理不成立：重启后 %d 份/%dB，应 ≤20 份 ∧ ≤100MB", rep.LogsDirCount, rep.LogsTotalBytes)
	}
	asserts = append(asserts, fmt.Sprintf("pass 会话日志双轴：重启后 %d 份 / %dB（≤20 ∧ ≤100MB 硬顶，先分层压缩后硬顶删）",
		rep.LogsDirCount, rep.LogsTotalBytes))
	goneOldest := 0
	for i, name := range seedLogNames {
		if i >= 5 { // 最旧 5 个造数目录必须消失（硬顶从最旧删起的直接证据）
			break
		}
		if _, err := os.Stat(filepath.Join(logsDir, name)); os.IsNotExist(err) {
			goneOldest++
		}
	}
	if goneOldest < 5 {
		return nil, fail("最旧造数会话目录仅 %d/5 消失，硬顶「从最旧删起」不成立", goneOldest)
	}
	asserts = append(asserts, "pass 硬顶删除方向：最旧 5 个造数会话目录全部消失（从最旧删起）")
	rec.Startup.Assertions = asserts
	fmt.Printf("== 段① 启动通道 == 通过（%d 条断言；重启后 %d 份/%dB）\n", len(asserts), rep.LogsDirCount, rep.LogsTotalBytes)
	stats := &crosscutChainStats{}
	stats.StartupSegmentMS = time.Since(chainStart).Milliseconds()

	// ---- 段② 任务终态通道：再次超量造数 → 驱动一次扫描收口 → 终态钩子
	// 异步清理（轮询账面收敛）。----
	fmt.Println("== -crosscut == 段② 任务终态通道（再造数 → 扫描收口 → 断言惰性清理触发）")
	seg2Start := time.Now()
	seqBase2 := xcEventSeqFace(stack.DB).max
	if err := xcSeedTaskEvents(ctx, stack.DB, seqBase2, xcSeed2Events); err != nil {
		return nil, fmt.Errorf("段② 造数 task_events: %w", err)
	}
	seed2IDs, err := xcSeedTerminalTasks(ctx, stack.DB, "xc-seed2-task", xcSeed2Tasks)
	if err != nil {
		return nil, fmt.Errorf("段② 造数终态任务: %w", err)
	}
	if _, err := app.StartScan(ctx, rel.RelationID); err != nil {
		return nil, fmt.Errorf("段② 驱动扫描: %w", err)
	}
	waitScan(ctx, app, rel.RelationID)
	// 终态钩子是异步 goroutine（lazyCleanupAfterTask）：轮询账面至窗口收敛。
	settleStart := time.Now()
	var afterEvents, afterTerminal int64
	deadline := time.Now().Add(xcPollTimeout)
	for {
		afterEvents = xcCount(stack.DB, "SELECT COUNT(*) FROM task_events")
		afterTerminal = xcCount(stack.DB,
			"SELECT COUNT(*) FROM tasks WHERE status IN ('succeeded','failed','cancelled','recovery_required')")
		if afterEvents <= 10000 && afterTerminal <= 200 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("段② 断言失败: 终态后清理 %s 未收敛（task_events=%d 终态tasks=%d）",
				xcPollTimeout, afterEvents, afterTerminal)
		}
		time.Sleep(200 * time.Millisecond)
	}
	rec.Terminal = crosscutTerminalSegment{
		SeedTaskEvents: xcSeed2Events, SeedTerminalTasks: xcSeed2Tasks,
		AfterTaskEvents: afterEvents, AfterTerminal: afterTerminal,
		SettleMS: time.Since(settleStart).Milliseconds(),
	}
	seg2 := []string{
		fmt.Sprintf("pass task_events 终态后收敛：%d 条（≤10000 窗口）", afterEvents),
		fmt.Sprintf("pass 终态任务终态后收敛：%d 条（≤200 窗口）", afterTerminal),
	}
	// 段②造数行按 created_at 留尾被裁的直接证据：60 条里最早的一半消失。
	gone2 := 0
	for i, id := range seed2IDs {
		if i >= len(seed2IDs)/2 {
			break
		}
		var n int
		_ = stack.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE id=?", id).Scan(&n)
		if n == 0 {
			gone2++
		}
	}
	if gone2 < len(seed2IDs)/4 {
		return nil, fmt.Errorf("段② 断言失败: 二次造数终态任务仅 %d 条消失，窗口留尾不成立", gone2)
	}
	seg2 = append(seg2, fmt.Sprintf("pass 留尾方向：二次造数最旧半数 %d/%d 条消失（保最近 200 条）", gone2, len(seed2IDs)))
	rec.Terminal.Assertions = seg2
	stats.TerminalSegmentMS = time.Since(seg2Start).Milliseconds()
	fmt.Printf("== 段② 任务终态通道 == 通过（task_events=%d 终态tasks=%d 收敛耗时 %dms）\n",
		afterEvents, afterTerminal, rec.Terminal.SettleMS)

	// ---- 段③ 脱敏断言（R1/R2）----
	fmt.Println("== -crosscut == 段③ 脱敏断言（R1 新写 detail 别名路径 / R2 -metrics 无 Host）")
	seg3Start := time.Now()
	diagCode, r1Detail, r1Asserts, err := xcAssertRedaction(ctx, app, rel, projectAbs)
	if err != nil {
		return nil, err
	}
	rec.Redaction.R1DiagCode = diagCode
	rec.Redaction.R1DetailAliased = r1Detail != ""
	rec.Redaction.R1Assertions = r1Asserts
	stats.R1DetailAliased = rec.Redaction.R1DetailAliased

	// R2：真实 -metrics 记录形态断言（序列化后 machine 段无 host/hostname 键，
	// OS/Arch/GoVersion/CPUs 保留）。
	mrec := metricsRecord{
		Schema: "p4-perf-run/1", CapturedAt: time.Now().UTC().Format(time.RFC3339),
		ProjectRoot: projectAbs, DataRoot: root, Machine: newMachineInfo(), Crosscut: stats,
	}
	r2b, err := json.Marshal(mrec)
	if err != nil {
		return nil, err
	}
	r2Asserts := []string{}
	if strings.Contains(string(r2b), `"host"`) || strings.Contains(string(r2b), `"hostname"`) {
		return nil, fmt.Errorf("段③ 断言失败: -metrics 输出含机器名键（R2 违规）: %s", r2b)
	}
	r2Asserts = append(r2Asserts, "pass -metrics 全记录无 host/hostname 键")
	for _, want := range []string{`"os"`, `"arch"`, `"go_version"`, `"cpus"`} {
		if !strings.Contains(string(r2b), want) {
			return nil, fmt.Errorf("段③ 断言失败: -metrics 机器信息缺保留键 %s", want)
		}
	}
	r2Asserts = append(r2Asserts, "pass 机器规格保留 OS/Arch/GoVersion/CPUs 四键")
	rec.Redaction.R2MetricsNoHost = true
	rec.Redaction.R2Assertions = r2Asserts
	stats.R2MetricsNoHost = true
	stats.RedactionSegmentMS = time.Since(seg3Start).Milliseconds()
	fmt.Printf("== 段③ 脱敏断言 == 通过（R1: %s detail=%q；R2: 无 host 键）\n", diagCode, r1Detail)

	stats.ChainTotalMS = time.Since(chainStart).Milliseconds()
	rec.ChainTotalMS = stats.ChainTotalMS
	if err := writeCrosscutRecord(recordPath, &rec); err != nil {
		return nil, err
	}
	return stats, nil
}

// xcAssertRedaction 构造含绝对路径的端点错误并断言新写诊断为别名路径（R1）：
// 坏 metafile（非法 TOML）→ parseModMeta 错误串内嵌端点内绝对路径 →
// diag.scan.modmeta_unreadable 的 Detail 经 AliasDetail 别名化（ADR-0011 §7；
// #90 构造点）。返回命中的诊断码、别名化 detail（空=未命中即失败）与断言清单。
func xcAssertRedaction(ctx context.Context, app syncapp.Application, rel view.RelationView,
	projectRoot string) (string, string, []string, error) {

	// 造端点错误：index.toml 追加坏 metafile 条目 + 非法 TOML 文件落位。
	indexPath := filepath.Join(projectRoot, "index.toml")
	rawIdx, err := os.ReadFile(indexPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("段③ 读 index.toml: %w", err)
	}
	brokenEntry := "\n[[files]]\nfile = \"mods/broken.pw.toml\"\nhash = \"1\"\nmetafile = true\n"
	if err := os.WriteFile(indexPath, append(rawIdx, []byte(brokenEntry)...), 0o644); err != nil {
		return "", "", nil, err
	}
	brokenPath := filepath.Join(projectRoot, "mods", "broken.pw.toml")
	if err := os.WriteFile(brokenPath, []byte("this is [ not toml"), 0o644); err != nil {
		return "", "", nil, err
	}
	if _, err := app.StartScan(ctx, rel.RelationID); err != nil {
		return "", "", nil, fmt.Errorf("段③ 带坏 metafile 扫描: %w", err)
	}
	waitScan(ctx, app, rel.RelationID)
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return "", "", nil, err
	}
	if ws.LatestProjectSnapshot == nil {
		return "", "", nil, fmt.Errorf("段③ 扫描后无项目侧快照")
	}
	diags, err := app.GetSnapshotDiagnostics(ctx, rel.RelationID, ws.LatestProjectSnapshot.SnapshotID)
	if err != nil {
		return "", "", nil, fmt.Errorf("段③ 读快照诊断: %w", err)
	}
	var detail string
	for _, d := range diags {
		if d.Code == "diag.scan.modmeta_unreadable" {
			detail = d.Detail
			break
		}
	}
	if detail == "" {
		return "", "", nil, fmt.Errorf("段③ 断言失败: 坏 metafile 未产生 diag.scan.modmeta_unreadable 诊断")
	}
	asserts := []string{}
	if !strings.Contains(detail, model.AliasProject) {
		return "", "", nil, fmt.Errorf("段③ 断言失败: 新写诊断 detail 非别名路径（R1）: %q", detail)
	}
	asserts = append(asserts, fmt.Sprintf("pass detail 含 %s 角色前缀", model.AliasProject))
	if strings.Contains(detail, projectRoot) {
		return "", "", nil, fmt.Errorf("段③ 断言失败: 新写诊断 detail 泄漏端点绝对路径（R1）: %q", detail)
	}
	asserts = append(asserts, "pass detail 无端点绝对路径")
	if user := os.Getenv("USERNAME"); user != "" && strings.Contains(detail, user) {
		return "", "", nil, fmt.Errorf("段③ 断言失败: 新写诊断 detail 含用户名 %q（R1）", user)
	}
	asserts = append(asserts, "pass detail 无用户名（历史行不追溯，不断言）")
	return "diag.scan.modmeta_unreadable", detail, asserts, nil
}

// ---- 造数与账面（直接 SQL/文件系统手术，沿 pgrecovery/pgwatcher 造数先例）----

// xcEventSeqFace 返回 task_events 账面（count/min/max stream_sequence）。
type xcSeqFace struct{ count, min, max int64 }

func xcEventSeqFace(db *sql.DB) xcSeqFace {
	var f xcSeqFace
	if err := db.QueryRow(
		"SELECT COUNT(*), COALESCE(MIN(stream_sequence),0), COALESCE(MAX(stream_sequence),0) FROM task_events",
	).Scan(&f.count, &f.min, &f.max); err != nil {
		log.Fatalf("task_events 账面读取失败: %v", err)
	}
	return f
}

// xcCount 单值计数（账面观测）。
func xcCount(db *sql.DB, query string, args ...any) int64 {
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		log.Fatalf("账面计数失败: %v", err)
	}
	return n
}

// xcSeedTaskEvents 造 n 条 task_events（stream_sequence 从 base+1 连续递增；
// 截断窗口按 stream_sequence 留尾，造数行占位高位即可参与窗口判定）。
func xcSeedTaskEvents(ctx context.Context, db *sql.DB, base int64, n int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		"INSERT INTO task_events (event_id, stream_sequence, event_type, emitted_at, relation_id, task_id, payload_json) VALUES (?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for i := int64(1); i <= n; i++ {
		if _, err := stmt.ExecContext(ctx,
			fmt.Sprintf("xc-seed-ev-%d-%06d", base, i), base+i, "task_updated", now, nil, nil, "{}"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// xcSeedTerminalTasks 造 n 条终态任务（succeeded；relation_id NULL 不触碰
// 关系账面；created_at 按序错开——修剪窗口按 created_at DESC, id DESC 留尾，
// 造数行整体最旧、优先被裁）。返回造数 id 清单（升序=最旧在前）。
func xcSeedTerminalTasks(ctx context.Context, db *sql.DB, prefix string, n int) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO tasks (id, relation_id, kind, status, phase, can_cancel, message_key, message_args_json, created_at, updated_at)
VALUES (?,?,?,?,'done',0,'','[]',?,?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%03d", prefix, i)
		at := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		if _, err := stmt.ExecContext(ctx, id, nil, "scan", "succeeded", at, at); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

// xcSeedExpiredPlans 造 n 条过期 draft 计划行（输入快照引用该关系真实最新
// 快照——FK 合法；无提交/运行/子计划引用，落在 DeleteExpiredPlans 判定域内）。
func xcSeedExpiredPlans(ctx context.Context, db *sql.DB, relationID string) ([]string, error) {
	var projSnap, rtSnap string
	if err := db.QueryRowContext(ctx, `
SELECT id FROM observed_snapshots WHERE relation_id=? AND side='project' ORDER BY captured_at DESC, id DESC LIMIT 1`,
		relationID).Scan(&projSnap); err != nil {
		return nil, fmt.Errorf("定位项目侧快照: %w", err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT id FROM observed_snapshots WHERE relation_id=? AND side='runtime' ORDER BY captured_at DESC, id DESC LIMIT 1`,
		relationID).Scan(&rtSnap); err != nil {
		return nil, fmt.Errorf("定位运行侧快照: %w", err)
	}
	var revision int
	if err := db.QueryRowContext(ctx, "SELECT revision FROM relations WHERE id=?", relationID).Scan(&revision); err != nil {
		return nil, fmt.Errorf("读关系修订号: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO sync_plans (id, relation_id, kind, input_project_snapshot_id, input_runtime_snapshot_id,
  relation_revision, plan_digest, status, expires_at, normalization_version, plan_json)
VALUES (?,?,?,?,?,?,?,?,'2000-01-01T00:00:00Z',1,'{}')`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	ids := []string{}
	for i := 0; i < xcSeedPlans; i++ {
		id := fmt.Sprintf("xc-seed-plan-%d", i)
		if _, err := stmt.ExecContext(ctx, id, relationID, "sync", projSnap, rtSnap, revision, "xc-seed", "draft"); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

// xcSeedExpiredPreparations 造 n 条过期预检行（preparations 表无外键引用）。
func xcSeedExpiredPreparations(ctx context.Context, db *sql.DB) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO preparations (preparation_id, created_at, expires_at, consumed_at, input_json, policy_json, checks_json)
VALUES (?,?,?,NULL,'{}','{}','{}')`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	ids := []string{}
	for i := 0; i < xcSeedPreps; i++ {
		id := fmt.Sprintf("xc-seed-prep-%d", i)
		if _, err := stmt.ExecContext(ctx, id, xcExpiredAt, xcExpiredAt); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

// xcSeedSessionLogs 造 25 个会话日志目录（>20 份），每目录 8MB 不可压缩
// 内容：奇数序用明文 session.log（随机字节，gzip 不缩——压缩层后仍超顶），
// 偶数序用已压缩 session.log.gz（#91 清理顺序语义：先分层压缩后硬顶计量，
// 可压缩内容会在压缩层缩水而不触发硬顶删）。目录名取当前时刻之前的逐分钟
// 时间戳（字典序即时间序，整体早于父/子进程的真实会话目录）。
func xcSeedSessionLogs(logsDir string) ([]string, error) {
	names := []string{}
	base := time.Now().Add(-time.Duration(xcSeedLogDirs+2) * time.Minute)
	rng := rand.New(rand.NewSource(20260903)) //nolint:gosec // 固定种子的验收造数
	buf := make([]byte, 1<<20)
	for i := 0; i < xcSeedLogDirs; i++ {
		name := base.Add(time.Duration(i) * time.Minute).Format("20060102-150405")
		dir := filepath.Join(logsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, "session.log")
		if i%2 == 0 {
			if err := xcWriteRandom(path, rng, buf, xcSeedLogBytesEach); err != nil {
				return nil, err
			}
		} else if err := xcWriteRandomGz(path+".gz", rng, buf, xcSeedLogBytesEach); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// xcWriteRandom 写 n 字节伪随机内容（不可压缩——硬顶删触发前提）。
func xcWriteRandom(path string, rng *rand.Rand, buf []byte, n int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for written := int64(0); written < n; {
		chunk := int64(len(buf))
		if remain := n - written; remain < chunk {
			chunk = remain
		}
		if _, err := rng.Read(buf[:chunk]); err != nil {
			return err
		}
		if _, err := f.Write(buf[:chunk]); err != nil {
			return err
		}
		written += chunk
	}
	return nil
}

// xcWriteRandomGz 把 n 字节伪随机内容经 gzip 落盘（预压缩形态——压缩层对其
// 幂等，硬顶计量按磁盘字节）。
func xcWriteRandomGz(path string, rng *rand.Rand, buf []byte, n int64) error {
	gz, err := os.Create(path)
	if err != nil {
		return err
	}
	defer gz.Close()
	gw := gzip.NewWriter(gz)
	defer gw.Close()
	for written := int64(0); written < n; {
		chunk := int64(len(buf))
		if remain := n - written; remain < chunk {
			chunk = remain
		}
		if _, err := rng.Read(buf[:chunk]); err != nil {
			return err
		}
		if _, err := gw.Write(buf[:chunk]); err != nil {
			return err
		}
		written += chunk
	}
	return gw.Flush()
}

// writeCrosscutRecord 原子落盘 p4-crosscut/1 记录（"-" 不落盘）。
func writeCrosscutRecord(path string, rec *crosscutRecord) error {
	if path == "" || path == "-" {
		return nil
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("== -crosscut 记录 == %s\n", path)
	return nil
}

// defaultCrosscutRecordPath 沿 records 先例自动命名：p4-crosscut-<date>-<host>.json。
func defaultCrosscutRecordPath() string {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return filepath.Join("docs", "acceptance", "records",
		fmt.Sprintf("p4-crosscut-%s-%s.json", time.Now().Format("2006-01-02"), host))
}
