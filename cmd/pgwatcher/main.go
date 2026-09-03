// pgwatcher 是 P4 watcher 验收链的编排 harness（票 #96；验收规格 §4.2/§9.1，
// 沿 cmd/pgrecovery「外部进程 + 断言 + 记录」先例）：
//
//	起 pgheadless -watch 常驻监听子进程（生产装配同链路），编排进程在外部写
//	真文件驱动六场景，只断不变式与轮数上界、不卡毫秒时序；断言数据面 =
//	pgheadless -record 的 p4-watcher-run/1 时间线（事件流/状态快照/链摘要）。
//
// 六场景（验收规格 §4.2，逐场景独立夹具与数据目录）：
//  1. 触发与收敛：外部写管辖目录 → 静默期后自动链 committed → 写盘自触发重扫
//     no_diff 收敛（轮数有界）；写 `mods/.index` → 不触发（红线④）；
//  2. 去抖上界：<1.5s 间隔持续写 ≥30s → 风暴窗口扫描轮数 ≤10s 上限量级；
//  3. 停靠待确认：授权关态 → awaiting_confirmation + pending_plan_id 就绪 +
//     收口点 relation_invalidated；双侧同段冲突差异 → 必停（红线①）；
//  4. 连败暂停与复位：假 CDN 全场 5xx 注入 ×2 → watch_status=paused + 无第三
//     次自动执行 + 监听保持 → 手动快速更新（CDN 恢复后经控制面）成功 → 复位
//     active（P3 cdnproc Script 能力注入，零真网）；
//  5. 恢复期只标脏：造数手术构造 recovery_required（GC probes 同款 SQL 注入，
//     终态任务不走启动恢复裁决）→ 触发文件变化 → 无自动物化（零新链零提交，
//     挂载保持 active）；
//  6. 并发 join：同 relation 双 goroutine 并发调 QuickUpdate → 等待并返回同一
//     结果（且链内只建一个扫描任务）；其他来源活跃任务（StartScan）照常互斥
//     （err.scan.already_running 透传）。
//
// 全链事件集断言（红线⑤）：各场景时间线事件类型 ⊆ {task_updated,
// relation_invalidated}（watch_failed 沿契约 04 §2.5 预留，本链不注入不可用
// 面，出现即违规）。
//
// 用法（Taskfile acceptance:watcher）：
//
//	pgwatcher [-work build/watcher] [-record <path>] [-pgheadless bin/pgheadless.exe]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// scenarioTimeout 是单场景整体看门狗（负向等待最多 ~15s，正向上界 90s）。
const scenarioTimeout = 4 * time.Minute

func main() {
	var (
		mods        = flag.Int("mods", 4, "夹具 mod 数（小型确定性夹具）")
		textFiles   = flag.Int("text-files", 8, "夹具受管文本文件数")
		fixtureSeed = flag.Int64("seed", 20260903, "夹具生成种子")
		headlessBin = flag.String("pgheadless", filepath.Join("bin", "pgheadless.exe"), "pgheadless 可执行文件（常驻子进程与预跑）")
		pgfixtureBn = flag.String("pgfixture", filepath.Join("bin", "pgfixture.exe"), "pgfixture 可执行文件（场景 4 拉起假 CDN 进程）")
		work        = flag.String("work", filepath.Join("build", "watcher"), "harness 工作目录（逐场景独立夹具/数据目录）")
		recordPath  = flag.String("record", "", "验收记录 JSON 输出路径（空=自动 docs/acceptance/records/p4-watcher-<date>-<host>.json；\"-\"=不落盘）")
	)
	flag.Parse()

	env := &harnessEnv{
		mods: *mods, textFiles: *textFiles, seed: *fixtureSeed,
		headlessBin: *headlessBin, pgfixtureBin: *pgfixtureBn, work: *work,
	}
	rec := &acceptanceRecord{
		Schema: "p4-watcher/1", Ticket: "skyraah/PackGradle#96",
		Spec:    "docs/acceptance/p4-acceptance-spec.md §4.2 六场景监听不变式链（真 fsnotify 真文件写入）",
		Date:    time.Now().Format("2006-01-02"),
		Machine: newMachineInfo(),
	}

	runners := []struct {
		name string
		spec string
		fn   func(*wScenario)
	}{
		{"触发与收敛（授权开态 committed → no_diff 收敛；mods/.index 不触发）", "§4.2 场景1", scenarioTriggerConverge},
		{"去抖上界（<1.5s 间隔持续写 ≥30s → 轮数 ≤10s 上限量级）", "§4.2 场景2", scenarioDebounceBound},
		{"停靠待确认（授权关态/冲突必停 + 收口点失效事件）", "§4.2 场景3", scenarioDockAwaiting},
		{"连败暂停与复位（假 CDN 全挂 ×2 → paused → 手动成功复位）", "§4.2 场景4", scenarioFailPauseReset},
		{"恢复期只标脏（recovery_required 触发无自动物化）", "§4.2 场景5", scenarioRecoveryDirtyOnly},
		{"并发 join（双调同结果 + 其他来源任务互斥）", "§4.2 场景6", scenarioConcurrentJoin},
	}
	allPass := true
	for _, r := range runners {
		fmt.Printf("\n===== 场景：%s =====\n", r.name)
		s := &wScenario{name: r.name, spec: r.spec, env: env, passed: true}
		// 场景体在子 goroutine 运行（panic 就地 recover 上报）；主 goroutine
		// 裁决结果与整体看门狗（超时先停常驻子进程解除阻塞，再判失败）。
		type outcome struct {
			aborted  *wAbort
			panicked any
		}
		ch := make(chan outcome, 1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					if ab, ok := p.(wAbort); ok {
						ch <- outcome{aborted: &ab}
						return
					}
					ch <- outcome{panicked: p}
					return
				}
				ch <- outcome{}
			}()
			r.fn(s)
		}()
		timer := time.NewTimer(scenarioTimeout)
		select {
		case out := <-ch:
			timer.Stop()
			if out.aborted != nil {
				s.passed = false
				s.failedAt = out.aborted.msg
			}
			if out.panicked != nil {
				panic(out.panicked) // 非断言 panic 不吞
			}
		case <-timer.C:
			s.teardown()
			s.passed = false
			s.failedAt = fmt.Sprintf("场景整体看门狗超时 %s", scenarioTimeout)
			<-ch // 场景体随 teardown 解除阻塞后收尾
		}
		s.teardown()
		rec.Scenarios = append(rec.Scenarios, s.record())
		allPass = allPass && s.passed
		fmt.Printf("== 场景结论 == %s → passed=%v（断言 %d 条）\n", r.name, s.passed, len(s.assertions))
		for _, a := range s.assertions {
			fmt.Println("   ✓", a)
		}
		if !s.passed {
			fmt.Println("   ✗ FAIL:", s.failedAt)
		}
	}

	rec.Verdict.AllPass = allPass
	if !allPass {
		for _, sc := range rec.Scenarios {
			if !sc.Passed {
				rec.Verdict.Violations = append(rec.Verdict.Violations, sc.Name+": "+sc.FailedAt)
			}
		}
	}
	rec.Note = "六场景零真网（连败注入=假 CDN 控制面全场 5xx 脚本，pgfixture -serve）；" +
		"断言面=pgheadless -watch -record 的 p4-watcher-run/1 时间线（事件流/状态快照/链摘要，逐场景内嵌 resident 字段）；" +
		"只断不变式与轮数上界（验收规格 §8.4：不卡毫秒时序）"

	b, err := json.MarshalIndent(rec, "", "  ")
	fatalOn(err, "记录序列化")
	if *recordPath != "-" {
		path := *recordPath
		if path == "" {
			path = defaultRecordPath()
		}
		fatalOn(os.MkdirAll(filepath.Dir(path), 0o755), "创建记录目录")
		fatalOn(os.WriteFile(path, b, 0o644), "写入记录")
		fmt.Printf("\n== 记录 == %s\n", path)
	}
	fmt.Printf("\n== acceptance:watcher 总结论 == 全部通过=%v（%d 场景）\n", allPass, len(rec.Scenarios))
	if !allPass {
		os.Exit(1)
	}
}

// defaultRecordPath 沿 records 先例自动命名：p4-watcher-<date>-<host>.json。
func defaultRecordPath() string {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return filepath.Join("docs", "acceptance", "records",
		fmt.Sprintf("p4-watcher-%s-%s.json", time.Now().Format("2006-01-02"), host))
}

func fatalOn(err error, stage string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 失败: %v\n", stage, err)
		os.Exit(2)
	}
}

// machineInfo 机器规格四元组（R2 脱敏同口径：不采集机器名）。
func newMachineInfo() machineInfo {
	return machineInfo{OS: runtime.GOOS, Arch: runtime.GOARCH,
		GoVersion: runtime.Version(), CPUs: runtime.NumCPU()}
}

type machineInfo struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`
	CPUs      int    `json:"cpus"`
}

// ---- 验收记录形态（p4-watcher/1）----

type acceptanceRecord struct {
	Schema    string        `json:"schema"`
	Ticket    string        `json:"ticket"`
	Spec      string        `json:"spec"`
	Date      string        `json:"date"`
	Machine   machineInfo   `json:"machine"`
	Scenarios []scenarioRec `json:"scenarios"`
	Verdict   struct {
		AllPass    bool     `json:"all_pass"`
		Violations []string `json:"violations,omitempty"`
	} `json:"verdict"`
	Note string `json:"note"`
}

type scenarioRec struct {
	Name       string          `json:"name"`
	Spec       string          `json:"spec"`
	Passed     bool            `json:"passed"`
	Assertions []string        `json:"assertions"`
	FailedAt   string          `json:"failed_at,omitempty"`
	Evidence   any             `json:"evidence,omitempty"`
	Resident   *watchRecMirror `json:"resident,omitempty"` // 归档时间线（§7：含事件/扫描轮数时间线）
}

// ---- pgheadless -record 时间线的 harness 侧镜像（只取断言消费的字段）----

type watchRecMirror struct {
	Schema     string        `json:"schema"`
	RelationID string        `json:"relation_id"`
	Authorized bool          `json:"authorized"`
	EndReason  string        `json:"end_reason,omitempty"`
	ScanRounds int           `json:"scan_rounds"`
	EventTypes []string      `json:"event_types"`
	Chains     []chainMirror `json:"chains"`
	Timeline   []entryMirror `json:"timeline"`
}

type chainMirror struct {
	Index       int    `json:"index"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at"`
	DurationMS  int64  `json:"duration_ms"`
	Outcome     string `json:"outcome"`
	ScanTaskID  string `json:"scan_task_id"`
	ApplyTaskID string `json:"apply_task_id,omitempty"`
	ApplyStatus string `json:"apply_status,omitempty"`
	Manual      bool   `json:"manual,omitempty"`
}

type entryMirror struct {
	At            string `json:"at"`
	Kind          string `json:"kind"`
	Note          string `json:"note,omitempty"`
	Seq           int64  `json:"seq,omitempty"`
	EventType     string `json:"event_type,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	TaskKind      string `json:"task_kind,omitempty"`
	TaskStatus    string `json:"task_status,omitempty"`
	Health        string `json:"health,omitempty"`
	WatchStatus   string `json:"watch_status,omitempty"`
	PendingPlanID string `json:"pending_plan_id,omitempty"`
	DiffState     string `json:"diff_state,omitempty"`
	Commits       int    `json:"commits,omitempty"`
}

// readWatchRecord 读取并镜像解析常驻进程的记录（损坏/未落盘返回错误）。
func readWatchRecord(path string) (*watchRecMirror, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m watchRecMirror
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return &m, nil
}

// ---- 场景运行上下文 ----

// wAbort 是场景断言失败的控制流信号（recover 收为场景失败，链继续）。
type wAbort struct{ msg string }

func (a wAbort) Error() string { return a.msg }

// harnessEnv 是全链共享的执行环境。
type harnessEnv struct {
	mods, textFiles int
	seed            int64
	headlessBin     string
	pgfixtureBin    string
	work            string
}

// wScenario 是单场景上下文：断言（失败即中止本场景）+ 常驻子进程生命周期。
type wScenario struct {
	name, spec string
	env        *harnessEnv
	assertions []string
	failedAt   string
	passed     bool
	resident   *residentHandle
	evidence   any
}

func (s *wScenario) abort(format string, args ...any) {
	panic(wAbort{msg: fmt.Sprintf(format, args...)})
}

func (s *wScenario) want(got, want, label string) {
	if got != want {
		s.abort("%s：got=%q want=%q", label, got, want)
	}
	s.assertions = append(s.assertions, label+" ✓")
}

func (s *wScenario) wantTrue(cond bool, label string) {
	if !cond {
		s.abort("%s：条件不成立", label)
	}
	s.assertions = append(s.assertions, label+" ✓")
}

// record 收敛场景记录（teardown 后调用）。
func (s *wScenario) record() scenarioRec {
	out := scenarioRec{
		Name: s.name, Spec: s.spec, Passed: s.passed,
		Assertions: s.assertions, FailedAt: s.failedAt, Evidence: s.evidence,
	}
	if s.resident != nil {
		if m, err := readWatchRecord(s.resident.recordPath); err == nil {
			out.Resident = m
		}
	}
	return out
}

// teardown 停掉本场景常驻子进程（哨兵收敛；悬挂即强杀）。保留句柄供 record()
// 归档终态记录；stop 幂等可重复调用。
func (s *wScenario) teardown() {
	if s.resident == nil {
		return
	}
	s.resident.stop(30 * time.Second)
}

// ---- 常驻子进程句柄 ----

type residentHandle struct {
	cmd         *exec.Cmd
	recordPath  string
	metricsPath string // -metrics 输出（收敛后写 watcher 段）
	sentinel    string
	exited      chan struct{} // 退出广播（close 即终态；可重复观察不耗尽）
	exitErr     error         // exited 关闭后可读（close 提供 happens-before）
	stopOnce    sync.Once
	stderrCap   bytes.Buffer
}

// startResident 拉起 pgheadless -watch 常驻子进程（stderr 捕获截尾供证据）。
func (s *wScenario) startResident(args []string, recordPath, sentinel string) *residentHandle {
	cmd := exec.Command(s.env.headlessBin, args...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		s.abort("拉起常驻子进程: %v", err)
	}
	if err := cmd.Start(); err != nil {
		s.abort("拉起常驻子进程: %v", err)
	}
	rh := &residentHandle{cmd: cmd, recordPath: recordPath, sentinel: sentinel, exited: make(chan struct{})}
	go func() {
		b, _ := io.ReadAll(stderrPipe)
		if len(b) > 8<<10 { // 截尾：证据只留尾部
			b = b[len(b)-8<<10:]
		}
		rh.stderrCap.Write(b)
		rh.exitErr = cmd.Wait()
		close(rh.exited)
	}()
	s.resident = rh
	return rh
}

// alive 常驻进程是否仍在运行（记录读取等待中提前发现崩溃）。
func (r *residentHandle) alive() bool {
	select {
	case <-r.exited:
		return false
	default:
		return true
	}
}

// stop 写退出哨兵并等待收敛；超时强杀（验收进程清理）。幂等（场景内收敛 +
// main 收尾二次调用安全）。
func (r *residentHandle) stop(timeout time.Duration) {
	r.stopOnce.Do(func() {
		if r.sentinel != "" {
			_ = os.WriteFile(r.sentinel, []byte("stop"), 0o644)
		}
		select {
		case <-r.exited:
		case <-time.After(timeout):
			if r.alive() {
				_ = r.cmd.Process.Kill()
			}
			<-r.exited
		}
	})
}

// waitRecordCond 轮询常驻记录直至 cond 成立（编排进程断言的主等待面）。
func (s *wScenario) waitRecordCond(rh *residentHandle, timeout time.Duration, label string,
	cond func(*watchRecMirror) bool) *watchRecMirror {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !rh.alive() {
			s.abort("%s：常驻进程提前退出（stderr 尾部: %s）", label, tail(rh.stderrCap.String(), 600))
		}
		if m, err := readWatchRecord(rh.recordPath); err == nil && cond(m) {
			return m
		}
		time.Sleep(250 * time.Millisecond)
	}
	m, err := readWatchRecord(rh.recordPath)
	if err != nil {
		s.abort("%s：等待超时且记录不可读（%v）", label, err)
	}
	return m
}

// settleSeconds 负向等待（断言窗口内零变化），期间监测常驻进程存活。
func (s *wScenario) settleSeconds(rh *residentHandle, seconds int) {
	for i := 0; i < seconds*4; i++ {
		if !rh.alive() {
			s.abort("负向等待期间常驻进程退出（stderr 尾部: %s）", tail(rh.stderrCap.String(), 600))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// assertEventSet 断言时间线事件类型 ⊆ {task_updated, relation_invalidated}
// （红线⑤：监听零新事件类型；watch_failed 本链未注入不可用面，出现即违规）。
func (s *wScenario) assertEventSet(m *watchRecMirror) {
	for _, t := range m.EventTypes {
		if t != "task_updated" && t != "relation_invalidated" {
			s.abort("红线⑤：时间线出现事件类型 %q（期望 ⊆ {task_updated, relation_invalidated}）", t)
		}
	}
	s.assertions = append(s.assertions,
		fmt.Sprintf("全链事件集 ⊆ {task_updated, relation_invalidated}（实测 %v）✓", m.EventTypes))
}

// ---- 通用辅助 ----

// runHeadless 跑一次 pgheadless 子进程至自然退出（预跑面：-apply/-set-authorized），
// 返回退出码与输出尾部。
func (s *wScenario) runHeadless(timeout time.Duration, label string, args ...string) string {
	cmd := exec.Command(s.env.headlessBin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		s.abort("%s：启动 %s: %v", label, s.env.headlessBin, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			s.abort("%s：退出码非 0（%v，输出尾部: %s）", label, err, tail(out.String(), 800))
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		s.abort("%s：子进程超时 %s", label, timeout)
	}
	s.assertions = append(s.assertions, fmt.Sprintf("%s（pgheadless %s 退出码 0）✓", label, strings.Join(args, " ")))
	return tail(out.String(), 2000)
}

// chainsAfter 返回 StartedAt ≥ since（容差 margin）的链摘要。
func chainsAfter(m *watchRecMirror, since time.Time, margin time.Duration) []chainMirror {
	var out []chainMirror
	for _, c := range m.Chains {
		if st, err := time.Parse(watchTS, c.StartedAt); err == nil && !st.Before(since.Add(-margin)) {
			out = append(out, c)
		}
	}
	return out
}

// eventsAfter 统计 At ≥ since 的事件行数（可选按类型过滤）。
func eventsAfter(m *watchRecMirror, since time.Time, eventType string) int {
	n := 0
	for _, e := range m.Timeline {
		if e.Kind != "event" {
			continue
		}
		if eventType != "" && e.EventType != eventType {
			continue
		}
		if at, err := time.Parse(watchTS, e.At); err == nil && !at.Before(since) {
			n++
		}
	}
	return n
}

// lastState 取时间线最后一条状态快照。
func lastState(m *watchRecMirror) *entryMirror {
	for i := len(m.Timeline) - 1; i >= 0; i-- {
		if m.Timeline[i].Kind == "state" {
			return &m.Timeline[i]
		}
	}
	return nil
}

// watchTS 与 pgheadless -watch 记录同款毫秒时间戳格式。
const watchTS = "2006-01-02T15:04:05.000Z07:00"

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
