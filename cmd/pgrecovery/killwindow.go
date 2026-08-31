package main

// 强杀窗口控制器：spawn pgheadless -apply 子进程，实时观察 stdout 相位标记
//（引擎任务相位经 pgheadless 轮询行打印：phase=staging/applying/verifying/done），
// 在种子调度的目标相位标记出现后随机延迟 taskkill /F 真强杀。
//
// 时序保障：
//   - 目标相位之后任一标记（含 done）先出现 → 立即击杀（相位已推进，剩余延迟
//     作废，落点如实记录）——把击杀压在运行收尾之前，杜绝「杀了个空」；
//   - 子进程先行干净退出（打印链路完成行/退出码 0）→ 强杀未生效，按 miss 处理
//     由调用方重试；
//   - taskkill 成功且击杀前未见链路完成行 → 强杀真实生效（非优雅退出）。
//
// 注意：击杀后管道内仍有已缓冲行可读，相位/链路完成等判定一律以击杀发放时刻
// 的快照为准（confirmedAtKill/chainDoneAtKill），避免缓冲行污染分类。

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// phaseRank 是引擎相位序（done = 引擎收尾，之后仅剩 pgheadless 断言/摘要输出）。
var phaseRank = map[string]int{"staging": 0, "applying": 1, "verifying": 2, "done": 3}

// attempt 是一次强杀尝试的运行时结果（对应记录里的 attemptRec）。
type attempt struct {
	index       int
	targetPhase string
	delayMS     int

	confirmed       bool   // 见到 "== ConfirmPlan =="（运行已落库）
	chainDone       bool   // 见到链路完成行（子进程将要干净退出）
	confirmedAtKill bool   // 击杀发放时刻的 confirmed 快照
	chainDoneAtKill bool   // 击杀发放时刻的 chainDone 快照
	landedPhase     string // 击杀时刻前最后观察到的相位标记
	markers         []markerTime
	taskkillOut     string
	taskkillOK      bool
	killed          bool // 已执行击杀
	childErr        error
	outcome         string
}

const (
	outcomeKilled              = "killed"
	outcomeKilledBeforeConfirm = "killed_before_confirm"
	outcomeMissedChildExit     = "missed_child_exit"
	outcomeMissedChainDone     = "missed_chain_done"
)

// childProcess 是已启动的 apply 子进程句柄。
type childProcess struct {
	cmd    *exec.Cmd
	events <-chan string // stdout 行
	wait   <-chan error
	stderr *bytes.Buffer
}

// spawnApplyChild 启动 pgheadless -apply 子进程（必须是直接 exec 的二进制：
// 强杀目标 PID 即 pgheadless 进程本身，go run 的包装进程不可用）。
func spawnApplyChild(headlessBin, project, instance, dataDir string) (*childProcess, error) {
	cmd := exec.Command(headlessBin, "-project", project, "-instance", instance, "-data", dataDir, "-apply")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	events := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			events <- sc.Text()
		}
		close(events)
	}()
	var stderrBuf bytes.Buffer
	stderr := &stderrBuf
	go func() { _, _ = io.Copy(stderr, stderrPipe) }()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 pgheadless 子进程: %w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	return &childProcess{cmd: cmd, events: events, wait: wait, stderr: stderr}, nil
}

// pid 返回子进程 PID（taskkill 目标）。
func (c *childProcess) pid() int { return c.cmd.Process.Pid }

// forceKill 执行 taskkill /F /PID（真强杀，非优雅退出），返回是否成功与输出。
func (c *childProcess) forceKill() (bool, string) {
	out, err := exec.Command("taskkill", "/F", "/PID", fmt.Sprint(c.pid())).CombinedOutput()
	return err == nil, strings.TrimSpace(consoleDecode(out))
}

var procMultiByteToWideChar = syscall.NewLazyDLL("kernel32.dll").NewProc("MultiByteToWideChar")

// consoleDecode 把控制台工具输出（系统 ANSI 代码页；zh-CN 机器为 GBK）转 UTF-8，
// 使 taskkill 输出可入记录。转换失败回退原始字节。
func consoleDecode(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	const cpACP = 0 // 系统 ANSI 代码页
	p := unsafe.Pointer(&b[0])
	r, _, _ := procMultiByteToWideChar.Call(uintptr(cpACP), 0, uintptr(p), uintptr(len(b)), 0, 0)
	if r == 0 {
		return string(b)
	}
	buf := make([]uint16, r)
	r2, _, _ := procMultiByteToWideChar.Call(uintptr(cpACP), 0, uintptr(p), uintptr(len(b)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(r))
	if r2 == 0 {
		return string(b)
	}
	return string(utf16.Decode(buf[:r2]))
}

// runKillWindow 观察子进程直至按调度完成强杀或判定 miss。
func (c *childProcess) runKillWindow(a *attempt, window time.Duration) {
	start := time.Now()
	deadline := time.After(window)
	var timer <-chan time.Time
	for {
		select {
		case line, ok := <-c.events:
			if !ok {
				c.events = nil
				continue
			}
			ms := time.Since(start).Milliseconds()
			if !a.killed {
				if p, isPhase := phaseOf(line); isPhase {
					a.markers = append(a.markers, markerTime{Marker: "phase=" + p, MS: ms})
					a.landedPhase = p
					switch {
					case p == a.targetPhase:
						// 目标相位开跑：武装随机延迟击杀。
						if timer == nil {
							timer = time.After(time.Duration(a.delayMS) * time.Millisecond)
						}
					case phaseRank[p] > phaseRank[a.targetPhase]:
						// 相位已推进过目标（目标标记在轮询间隔内一闪而过，或延迟
						// 等待期间相位走完）：立即击杀，落点=当前相位。
						a.fireKill(c)
					}
				}
				if strings.Contains(line, "== ConfirmPlan ==") {
					a.confirmed = true
				}
				if strings.Contains(line, "链路完成") {
					a.chainDone = true
					if !a.killed {
						a.outcome = outcomeMissedChainDone
						return
					}
				}
			}
		case <-timer:
			timer = nil
			if !a.killed {
				a.fireKill(c)
			}
		case err := <-c.wait:
			a.childErr = err
			if !a.killed {
				a.outcome = outcomeMissedChildExit
				return
			}
			a.classify()
			return
		case <-deadline:
			if !a.killed {
				// 兜底击杀：目标相位标记迟迟未现（进程卡死同样要交给恢复管线）。
				a.fireKill(c)
			}
			deadline = make(chan time.Time) // 只兜底一次，之后专心等子进程退出
		}
	}
}

// fireKill 执行强杀并固化发放时刻的判定快照。
func (a *attempt) fireKill(c *childProcess) {
	a.confirmedAtKill, a.chainDoneAtKill = a.confirmed, a.chainDone
	a.killed = true
	a.taskkillOK, a.taskkillOut = c.forceKill()
	fmt.Printf("  [kill] taskkill /F /PID %d → ok=%v 落点相位=%s（延迟 %dms）\n",
		c.pid(), a.taskkillOK, a.landedPhase, a.delayMS)
	if !a.taskkillOK {
		fmt.Printf("  [kill] taskkill 输出: %s\n", a.taskkillOut)
	}
}

// classify 击杀后按观察事实定性尝试结果。判定以击杀发放时刻的快照为准；
// 击杀已发放即认为强杀生效（进程被 TerminateProcess 终止，非优雅退出），
// 链路完成行若在发放前已打印则如实视为 crash-window 之外的 miss 以外情形——
// 仍算 killed（提交事务本身完成与否由恢复收口与不变式裁决，见记录 landed_phase）。
func (a *attempt) classify() {
	switch {
	case !a.killed || !a.taskkillOK:
		a.outcome = outcomeMissedChildExit // 进程已自行退出或已不在，taskkill 落空
	case a.chainDoneAtKill:
		a.outcome = outcomeMissedChainDone // 链路已完成才杀：无中断语义，交重试
	case !a.confirmedAtKill:
		a.outcome = outcomeKilledBeforeConfirm // 未过 ConfirmPlan：无运行可恢复，交重试
	default:
		a.outcome = outcomeKilled
	}
}

// phaseOf 从 stdout 行提取相位名（pgheadless 轮询行形如
// "  [poll] status=running phase=applying progress=3/120"）。
func phaseOf(line string) (string, bool) {
	const key = "phase="
	i := strings.Index(line, key)
	if i < 0 {
		return "", false
	}
	rest := line[i+len(key):]
	for p := range phaseRank {
		if strings.HasPrefix(rest, p) {
			return p, true
		}
	}
	return "", false
}

// runApplyToCompletion 运行 pgheadless -apply 至自然退出（复跑收口用），
// 返回退出码与输出尾部。
func runApplyToCompletion(ctx context.Context, headlessBin, project, instance, dataDir string) (int, string, error) {
	cmd := exec.CommandContext(ctx, headlessBin, "-project", project, "-instance", instance, "-data", dataDir, "-apply")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		return -1, out.String(), err
	}
	return code, tail(out.String(), 4000), nil
}

// tail 取字符串末尾至多 n 字节（按行边界起头，便于记录可读）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[len(s)-n:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 {
		cut = cut[i+1:]
	}
	return "…(截断)…\n" + cut
}
