package main

// pgheadless -watch（P4 票 #96；验收规格 §4.2/§9.1，ADR-0010）：常驻监听模式。
// 挂 watcher + 自动链常驻不退出（生产装配同一条 bootstrap 链路：fsnotify 事件
// 源 + 触发器状态机 + QuickUpdate 自动链），直至约定退出条件（哨兵文件 / 超时）。
// 运行期把事件流与扫描轮数时间线写入 -record 记录（p4-watcher-run/1，供
// acceptance:watcher 编排进程断言与报告归档 docs/acceptance/records/p4-watcher-*）：
//   - timeline：task_events 事件流（task_updated/relation_invalidated/...，带
//     任务快照展开）+ 工作区状态快照（health/watch_status/pending_plan_id/
//     diff_state/提交数）+ 扫描/apply 分相耗时（LastScanTiming/LastApplyTiming
//     变化沿）；
//   - chains：按扫描任务分组的自动链摘要（outcome/apply 终态/墙钟——#96
//     -metrics 增量「watcher 触发→链收口墙钟、快速更新链相位」的记录面）；
//   - 手动快速更新控制通道：-watch-control 目录出现 quickupdate 命令文件时经
//     transport.SyncService.QuickUpdate 执行（与前端同一条 wire 面，收口触发
//     AttachQuickUpdateResult → paused 复位 active 的真实接线）。
//
// 既有 stderr/stdout 断言形态不变；本模式全部输出走 "== -watch ==" 前缀新行。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/application/watch"
	"packgradle/internal/bootstrap"
	"packgradle/internal/transport"
)

// watchRecordTS 是时间线时间戳格式（毫秒精度——编排进程按时间窗关联外部写入
// 与链触发，秒级粒度不够）。
const watchRecordTS = "2006-01-02T15:04:05.000Z07:00"

// watchPollInterval 是观察轮询间隔（事件/状态/控制面/哨兵共用一个循环）。
const watchPollInterval = 200 * time.Millisecond

// watchEnv 是 -watch 常驻模式的运行环境（main 装配后传入）。
type watchEnv struct {
	stack        *bootstrap.Stack
	app          syncapp.Application // 用例面（GetWorkspace 投影；具体类型带计时面）
	svc          *transport.SyncService
	db           *sql.DB
	relationID   string
	projectRoot  string
	instanceDir  string
	dataRoot     string
	recordPath   string // p4-watcher-run/1 记录（空=仅 stdout 摘要）
	metricsPath  string // -metrics（watcher 段增量）
	exitSentinel string // 退出哨兵文件（存在即收敛退出；空=只由超时收敛）
	controlDir   string // 手动快速更新命令目录（空=无控制面）
	timeout      time.Duration
	authorized   bool
}

// watchTimelineEntry 是时间线一行（kind=event|state|timing|note；字段按 kind
// 取用，omitempty 收窄体积）。
type watchTimelineEntry struct {
	At   string `json:"at"`
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
	// event 面
	Seq        int64  `json:"seq,omitempty"`
	EventType  string `json:"event_type,omitempty"`
	EmittedAt  string `json:"emitted_at,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	TaskKind   string `json:"task_kind,omitempty"`   // task_updated payload 展开
	TaskStatus string `json:"task_status,omitempty"` // 最近的持久化任务状态
	// state 面（工作区投影 + 提交数）
	Health        string `json:"health,omitempty"`
	WatchStatus   string `json:"watch_status,omitempty"`
	PendingPlanID string `json:"pending_plan_id,omitempty"`
	DiffState     string `json:"diff_state,omitempty"`
	Commits       int    `json:"commits,omitempty"`
	// timing 面（快速更新链相位：扫描四相 / apply 分相）
	ScanPhasesMS  *scanPhasesMS  `json:"scan_phases_ms,omitempty"`
	ApplyPhasesMS *applyTimingMS `json:"apply_phases_ms,omitempty"`
}

// applyTimingMS 是 apply 分相记录形态（view.ApplyTimingView 的本地投影）。
type applyTimingMS struct {
	OperationCount int   `json:"operation_count"`
	Staging        int64 `json:"staging"`
	Applying       int64 `json:"applying"`
	Verifying      int64 `json:"verifying"`
	Total          int64 `json:"total"`
}

// watchChain 是自动链摘要（按扫描任务分组派生；时间线事实的只读投影）。
type watchChain struct {
	Index      int    `json:"index"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at"`
	DurationMS int64  `json:"duration_ms"`
	// Outcome 是链收口分类：apply_succeeded|apply_failed|apply_recovery_required|
	// no_apply_task（无 apply 任务 = no_diff 或停待确认，编排进程结合状态
	// 时间线的 pending_plan_id 演进区分）。
	Outcome     string `json:"outcome"`
	ScanTaskID  string `json:"scan_task_id"`
	ApplyTaskID string `json:"apply_task_id,omitempty"`
	ApplyStatus string `json:"apply_status,omitempty"`
	Manual      bool   `json:"manual,omitempty"`
}

// watchRecord 是 -record 记录形态（p4-watcher-run/1；验收规格 §7：
// p4-watcher-<date>-<host>.json 的常驻运行面，含事件/扫描轮数时间线）。
type watchRecord struct {
	Schema      string `json:"schema"` // p4-watcher-run/1
	Ticket      string `json:"ticket"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at,omitempty"`
	EndReason   string `json:"end_reason,omitempty"` // sentinel|timeout|error
	RelationID  string `json:"relation_id"`
	Authorized  bool   `json:"authorized"`
	ProjectRoot string `json:"project_root"`
	InstanceDir string `json:"instance_dir"`
	DataRoot    string `json:"data_root"`
	QuiesceMS   int64  `json:"quiesce_ms"`
	MaxWaitMS   int64  `json:"max_wait_ms"`
	// Timeline 是事件流 + 状态快照 + 相位耗时 + 编排注记的统一时间线。
	Timeline []watchTimelineEntry `json:"timeline"`
	// Chains 是按扫描任务分组的链摘要；ScanRounds = len(Chains)（扫描轮数）。
	Chains     []watchChain `json:"chains"`
	ScanRounds int          `json:"scan_rounds"`
	EventTypes []string     `json:"event_types"`
}

// watcherMetrics 是 -metrics 的 watcher 增量段（验收规格 §6：watcher 触发→链
// 收口墙钟、快速更新链相位——只记录不设门槛）。
type watcherMetrics struct {
	ScanRounds       int            `json:"scan_rounds"`
	ChainWallClockMS []int64        `json:"chain_wall_clock_ms"`
	LastScanPhases   *scanPhasesMS  `json:"last_scan_phases_ms,omitempty"`
	LastApplyPhases  *applyTimingMS `json:"last_apply_phases_ms,omitempty"`
}

// taskEventPayload 是 task_updated 事件 payload（model.Task JSON）的链派生
// 字段子集。
type taskEventPayload struct {
	TaskID   string `json:"task_id"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Outcome  string `json:"outcome"`
	PlanID   string `json:"plan_id"`
	CommitID string `json:"commit_id"`
}

// runWatchMode 进入常驻监听（不返回，直至哨兵/超时/致命错误；timeout 退出码 3）。
func runWatchMode(env watchEnv) {
	rec := &watchRecord{
		Schema:      "p4-watcher-run/1",
		Ticket:      "skyraah/PackGradle#96",
		StartedAt:   time.Now().UTC().Format(watchRecordTS),
		RelationID:  env.relationID,
		Authorized:  env.authorized,
		ProjectRoot: env.projectRoot,
		InstanceDir: env.instanceDir,
		DataRoot:    env.dataRoot,
		QuiesceMS:   watch.QuiescePeriod.Milliseconds(),
		MaxWaitMS:   watch.MaxWaitPeriod.Milliseconds(),
		Timeline:    []watchTimelineEntry{},
		Chains:      []watchChain{},
		EventTypes:  []string{},
	}

	// 生产装配同一条链路：watcher 引擎已随 bootstrap 装配，这里启动常驻 loop。
	env.stack.StartWatcher()
	fmt.Printf("== -watch == relation=%s authorized=%v（quiesce=%s max_wait=%s）\n",
		env.relationID, env.authorized, watch.QuiescePeriod, watch.MaxWaitPeriod)
	fmt.Printf("== -watch == 常驻监听已启动，退出条件：哨兵 %q 或超时 %s\n", env.exitSentinel, env.timeout)

	// 单 goroutine 顺序循环：观察 → 控制面 → 退出条件 → 落盘。手动快速更新
	// 阻塞本循环数秒可接受（哨兵检查随之延后；事件在 DB 持久化，下一轮补齐）。
	// 时间线基线 = 启动时事件流尾部（预跑进程的 task_events 不入本记录——
	// 时间线只覆盖本常驻窗口）。
	lastSeq := int64(0)
	_ = env.db.QueryRow("SELECT COALESCE(MAX(stream_sequence),0) FROM task_events").Scan(&lastSeq)
	lastStateKey := ""
	lastScan, lastApply := view.ScanTimingView{}, view.ApplyTimingView{}
	var manualWindowStart, manualWindowEnd time.Time
	lastHeartbeat := time.Now()
	endReason := "error"

	deadline := time.Now().Add(env.timeout)
	for {
		now := time.Now()
		if now.After(deadline) {
			endReason = "timeout"
			fmt.Fprintln(os.Stderr, "== -watch == 超时未收到退出哨兵，强制收敛（exit 3）")
			break
		}

		// ① 事件流：新 task_events 逐行入时间线（持久化事件是事实源）。
		env.pollEvents(rec, &lastSeq, now)

		// ② 状态快照：变化即记 + 周期心跳（编排进程断言 pending_plan_id /
		// watch_status / 提交数演进的数据面）。
		env.pollState(rec, &lastStateKey, &lastHeartbeat, now)

		// ③ 快速更新链相位（LastScanTiming/LastApplyTiming 变化沿——-metrics
		// 增量的记录面；不入 transport 契约，接口断言沿 main.go 先例）。
		if ts, ok := env.app.(interface {
			LastScanTiming() view.ScanTimingView
		}); ok {
			if st := ts.LastScanTiming(); st.TotalMs != lastScan.TotalMs {
				lastScan = st
				rec.Timeline = append(rec.Timeline, watchTimelineEntry{
					At: now.UTC().Format(watchRecordTS), Kind: "timing",
					ScanPhasesMS: &scanPhasesMS{
						ProjectScan: st.ProjectScanMs, RuntimeScan: st.RuntimeScanMs,
						Normalize: st.NormalizeMs, Persist: st.PersistMs,
					},
				})
			}
		}
		if ta, ok := env.app.(interface {
			LastApplyTiming() view.ApplyTimingView
		}); ok {
			if at := ta.LastApplyTiming(); at.TotalMs != lastApply.TotalMs {
				lastApply = at
				rec.Timeline = append(rec.Timeline, watchTimelineEntry{
					At: now.UTC().Format(watchRecordTS), Kind: "timing",
					ApplyPhasesMS: &applyTimingMS{
						OperationCount: at.OperationCount, Staging: at.StagingMs,
						Applying: at.ApplyingMs, Verifying: at.VerifyingMs, Total: at.TotalMs,
					},
				})
			}
		}

		// ④ 控制面：命令文件消费（quickupdate = 手动快速更新，与前端同一
		// transport 服务，收口触发 paused 复位接线的真实路径）。
		if env.controlDir != "" {
			for _, cmd := range consumeControlFiles(env.controlDir) {
				rec.Timeline = append(rec.Timeline, watchTimelineEntry{
					At: time.Now().UTC().Format(watchRecordTS), Kind: "note",
					Note: "控制命令 " + cmd,
				})
				if strings.TrimSuffix(cmd, filepath.Ext(cmd)) != "quickupdate" {
					continue
				}
				manualWindowStart = time.Now()
				res, qerr := env.svc.QuickUpdate(env.relationID)
				manualWindowEnd = time.Now()
				note := fmt.Sprintf("手动快速更新收口 outcome=%s plan=%s task=%s err=%v",
					res.Outcome, res.PlanID, res.ApplyTaskID, qerr)
				rec.Timeline = append(rec.Timeline, watchTimelineEntry{
					At: manualWindowEnd.UTC().Format(watchRecordTS), Kind: "note", Note: note,
				})
				fmt.Printf("== -watch == %s\n", note)
			}
		}

		// ⑤ 派生链摘要并原子落盘（编排进程轮询读取断言的数据面）。
		refreshDerived(env, rec, manualWindowStart, manualWindowEnd)

		// ⑥ 退出哨兵。
		if env.exitSentinel != "" {
			if _, err := os.Stat(env.exitSentinel); err == nil {
				endReason = "sentinel"
				break
			}
		}
		time.Sleep(watchPollInterval)
	}

	// 收敛：终态记录 + -metrics watcher 段。
	rec.EndedAt = time.Now().UTC().Format(watchRecordTS)
	rec.EndReason = endReason
	refreshDerived(env, rec, manualWindowStart, manualWindowEnd)
	flushWatchRecord(env.recordPath, rec)
	fmt.Printf("== -watch == 收敛（reason=%s 扫描轮数=%d 事件类型=%v）\n",
		endReason, rec.ScanRounds, rec.EventTypes)
	if env.metricsPath != "" {
		writeMetrics(env.metricsPath, watchMetricsRecord(env, rec))
	}
	if endReason != "sentinel" {
		os.Exit(3)
	}
}

// pollEvents 追加新持久化事件到时间线（task_updated 展开任务快照字段）。
func (env watchEnv) pollEvents(rec *watchRecord, lastSeq *int64, now time.Time) {
	rows, err := env.db.Query(
		`SELECT stream_sequence, event_type, emitted_at, task_id, payload_json FROM task_events
		 WHERE stream_sequence > ? ORDER BY stream_sequence ASC`, *lastSeq)
	if err != nil {
		rec.Timeline = append(rec.Timeline, watchTimelineEntry{
			At: now.UTC().Format(watchRecordTS), Kind: "note",
			Note: "task_events 查询失败: " + err.Error(),
		})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var evType, emittedAt, payload string
		var taskID sql.NullString
		if serr := rows.Scan(&seq, &evType, &emittedAt, &taskID, &payload); serr != nil {
			return
		}
		if seq <= *lastSeq {
			continue
		}
		*lastSeq = seq
		e := watchTimelineEntry{
			At: now.UTC().Format(watchRecordTS), Kind: "event",
			Seq: seq, EventType: evType, EmittedAt: emittedAt, TaskID: taskID.String,
		}
		if evType == "task_updated" {
			var tp taskEventPayload
			if json.Unmarshal([]byte(payload), &tp) == nil {
				e.TaskID, e.TaskKind, e.TaskStatus = tp.TaskID, tp.Kind, tp.Status
			}
		}
		rec.Timeline = append(rec.Timeline, e)
		if !containsStr(rec.EventTypes, evType) {
			rec.EventTypes = append(rec.EventTypes, evType)
		}
	}
}

// pollState 追加工作区状态快照（变化即记 + 2s 心跳）。
func (env watchEnv) pollState(rec *watchRecord, lastStateKey *string, lastHeartbeat *time.Time, now time.Time) {
	ws, err := env.app.GetWorkspace(context.Background(), env.relationID)
	if err != nil {
		return
	}
	commits := countCommits(env.db, env.relationID)
	key := strings.Join([]string{ws.Relation.Health, ws.State.WatchStatus,
		ws.State.PendingPlanID, ws.State.DiffState, fmt.Sprint(commits)}, "|")
	if key == *lastStateKey && now.Sub(*lastHeartbeat) < 2*time.Second {
		return
	}
	rec.Timeline = append(rec.Timeline, watchTimelineEntry{
		At: now.UTC().Format(watchRecordTS), Kind: "state",
		Health: ws.Relation.Health, WatchStatus: ws.State.WatchStatus,
		PendingPlanID: ws.State.PendingPlanID, DiffState: ws.State.DiffState,
		Commits: commits,
	})
	*lastStateKey = key
	*lastHeartbeat = now
}

// refreshDerived 重算链摘要与扫描轮数并原子落盘记录。
func refreshDerived(env watchEnv, rec *watchRecord, manualStart, manualEnd time.Time) {
	rec.Chains = deriveChains(rec.Timeline, manualStart, manualEnd)
	rec.ScanRounds = len(rec.Chains)
	flushWatchRecord(env.recordPath, rec)
}

// deriveChains 按扫描任务把事件时间线分组成链摘要：一条链 = 一个 scan 任务的
// 事件窗（scan 任务首事件 → 下一 scan 任务首事件前），窗内 apply 任务终态决定
// outcome（自动链与手动链共用 QuickUpdate，都是「扫描任务先行」）。
func deriveChains(timeline []watchTimelineEntry, manualStart, manualEnd time.Time) []watchChain {
	var chains []watchChain
	var cur *watchChain
	closeCur := func(endAt string) {
		if cur == nil {
			return
		}
		cur.EndedAt = endAt
		if st, e1 := time.Parse(watchRecordTS, cur.StartedAt); e1 == nil {
			if en, e2 := time.Parse(watchRecordTS, cur.EndedAt); e2 == nil {
				cur.DurationMS = en.Sub(st).Milliseconds()
			}
		}
		switch {
		case cur.ApplyTaskID == "":
			cur.Outcome = "no_apply_task"
		case cur.ApplyStatus == "succeeded":
			cur.Outcome = "apply_succeeded"
		case cur.ApplyStatus == "failed":
			cur.Outcome = "apply_failed"
		case cur.ApplyStatus == "recovery_required":
			cur.Outcome = "apply_recovery_required"
		default:
			cur.Outcome = "apply_" + cur.ApplyStatus
		}
		if st, e1 := time.Parse(watchRecordTS, cur.StartedAt); e1 == nil {
			if manualEnd.After(manualStart) &&
				!st.Before(manualStart.Add(-2*time.Second)) && !st.After(manualEnd.Add(2*time.Second)) {
				cur.Manual = true
			}
		}
		chains = append(chains, *cur)
		cur = nil
	}
	for _, e := range timeline {
		if e.Kind != "event" {
			continue
		}
		if e.EventType == "task_updated" && e.TaskKind == "scan" && e.TaskID != "" &&
			(cur == nil || cur.ScanTaskID != e.TaskID) {
			// 已见过的 scan 任务更新（终态等）不重开链：cur 归属判断靠任务 id。
			seen := false
			for i := range chains {
				if chains[i].ScanTaskID == e.TaskID {
					seen = true
					break
				}
			}
			if cur != nil && !seen {
				closeCur(e.At)
			}
			if !seen {
				cur = &watchChain{Index: len(chains) + 1, StartedAt: e.At, ScanTaskID: e.TaskID}
			}
		}
		if cur == nil {
			continue
		}
		if e.EventType == "task_updated" && e.TaskKind == "apply" && e.TaskID != "" {
			cur.ApplyTaskID = e.TaskID
			if e.TaskStatus == "succeeded" || e.TaskStatus == "failed" || e.TaskStatus == "recovery_required" {
				cur.ApplyStatus = e.TaskStatus
			}
		}
		cur.EndedAt = e.At
	}
	closeCur(lastEventAt(timeline))
	return chains
}

// lastEventAt 取时间线最后一个事件行时间（尾链收口默认值）。
func lastEventAt(timeline []watchTimelineEntry) string {
	for i := len(timeline) - 1; i >= 0; i-- {
		if timeline[i].Kind == "event" {
			return timeline[i].At
		}
	}
	return ""
}

// consumeControlFiles 消费控制目录中的命令文件（文件名即命令；读到的文件一律
// 删除——一次性命令面，编排进程写真文件驱动）。
func consumeControlFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			out = append(out, name)
		}
	}
	return out
}

// containsStr 列表包含判定（事件类型去重）。
func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// countCommits 读提交数（记录观察面：编排进程断言「无自动物化」的账面）。
func countCommits(db *sql.DB, relationID string) int {
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sync_commits WHERE relation_id=?", relationID).Scan(&n); err != nil {
		return -1
	}
	return n
}

// flushWatchRecord 原子落盘记录（临时文件 + rename，编排进程随时可读）。
func flushWatchRecord(path string, rec *watchRecord) {
	if path == "" || path == "-" {
		return
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// watchMetricsRecord 把 watcher 段并入既有 metrics 记录形态（schema 升
// p4-perf-run/1；既有键零改动，pgfixture -eval 只消费既有键不受影响）。
func watchMetricsRecord(env watchEnv, rec *watchRecord) metricsRecord {
	wm := watcherMetrics{ScanRounds: rec.ScanRounds, ChainWallClockMS: []int64{}}
	for _, c := range rec.Chains {
		wm.ChainWallClockMS = append(wm.ChainWallClockMS, c.DurationMS)
	}
	for i := len(rec.Timeline) - 1; i >= 0; i-- {
		e := rec.Timeline[i]
		if wm.LastScanPhases == nil && e.ScanPhasesMS != nil {
			wm.LastScanPhases = e.ScanPhasesMS
		}
		if wm.LastApplyPhases == nil && e.ApplyPhasesMS != nil {
			wm.LastApplyPhases = e.ApplyPhasesMS
		}
		if wm.LastScanPhases != nil && wm.LastApplyPhases != nil {
			break
		}
	}
	return metricsRecord{
		Schema: "p4-perf-run/1", CapturedAt: time.Now().UTC().Format(time.RFC3339),
		ProjectRoot: env.projectRoot, InstanceDir: env.instanceDir, DataRoot: env.dataRoot,
		Machine: newMachineInfo(), Watcher: &wm,
	}
}
