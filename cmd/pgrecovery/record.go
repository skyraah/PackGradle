package main

// 逐轮记录形态（p2-recovery/1）：沿 P1/P2 records 先例（p2-perf-baseline/1），
// 携带随机种子、强杀时机（目标相位+延迟+实测标记时刻）、裁决结果、收口路径与
// 四不变式逐项结论——随机时机下裁决本就不定，记录透明化、不变式才是硬门槛
//（验收规格 §2.1「不做逐轮裁决断言」）。

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"time"
)

// recoveryRecord 是 acceptance:recovery 的顶层记录（p2-recovery/1）。
type recoveryRecord struct {
	Schema      string          `json:"schema"`
	Ticket      string          `json:"ticket"`
	Spec        string          `json:"spec"`
	Date        string          `json:"date"`
	Machine     machineInfo     `json:"machine"`
	Seed        int64           `json:"seed"` // 强杀调度种子（目标相位+延迟；同种子可重放调度序列）
	Fixture     fixtureInfo     `json:"fixture"`
	RoundsFixed int             `json:"rounds_fixed"`
	Procedure   string          `json:"procedure"`
	Rounds      []roundRecord   `json:"rounds"`
	Verdict     topLevelVerdict `json:"verdict"`

	rng *rand.Rand // 非序列化：调度随机源
}

type machineInfo struct {
	Host      string `json:"host"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`
	CPUs      int    `json:"cpus"`
}

type fixtureInfo struct {
	Seed         int64  `json:"seed"`
	Mods         int    `json:"mods"`
	TextFiles    int    `json:"text_files"`
	ManagedFiles int    `json:"managed_files"`
	Note         string `json:"note"`
}

type topLevelVerdict struct {
	AllPass    bool     `json:"all_pass"`
	Violations []string `json:"violations,omitempty"`
}

// roundRecord 是单轮逐项记录。
type roundRecord struct {
	Round        int            `json:"round"`
	Schedule     string         `json:"schedule"` // 种子调度的目标相位（staging/applying/verifying）
	Attempts     []attemptRec   `json:"attempts"`
	AttemptCount int            `json:"attempt_count"`
	Verdict      string         `json:"verdict"`      // committed | recovery_required
	ClosurePath  string         `json:"closure_path"` // auto_committed（probe 裁决收口或 committed 崩溃窗口簿记重建） | acknowledge_reapply_committed
	Kill         killFacts      `json:"kill"`
	OpTally      map[string]int `json:"op_status_tally,omitempty"`

	// recovery_required 轮的收口证据：acknowledge 后剩余 diff（不设门槛——
	// 补偿/诚实部分完成本就允许剩余，最终以复跑收口后的 diff 归零为准）。
	RemainingDiffAfterAck *changesSummaryRec `json:"remaining_diff_after_ack,omitempty"`

	Invariant1 bool     `json:"i1_no_partial_illusion"`
	Invariant2 bool     `json:"i2_rescan_diff_zero_after_closure"`
	Invariant3 bool     `json:"i3_apply_gate_and_rerun"`
	Invariant4 bool     `json:"i4_repeat_recovery_idempotent"`
	Violations []string `json:"violations,omitempty"`
}

// Passed 报告该轮是否全过（四不变式 + 无违规）。
func (r *roundRecord) Passed() bool {
	return r.Invariant1 && r.Invariant2 && r.Invariant3 && r.Invariant4 && len(r.Violations) == 0
}

// attemptRec 是单次强杀尝试的记录（击杀未验证/未过 ConfirmPlan 时重试）。
type attemptRec struct {
	Index       int       `json:"index"`
	TargetPhase string    `json:"target_phase"`
	DelayMS     int       `json:"kill_delay_ms"`
	Outcome     string    `json:"outcome"` // killed | killed_before_confirm | missed_child_exit | missed_chain_done
	Kill        killFacts `json:"kill"`
	StderrTail  string    `json:"stderr_tail,omitempty"`
}

// killFacts 是强杀事实：taskkill 输出、实际落点相位与观察到的相位标记时刻
// （相对子进程启动，毫秒）。
type killFacts struct {
	Verified       bool         `json:"verified"`
	TaskkillOutput string       `json:"taskkill_output,omitempty"`
	LandedPhase    string       `json:"landed_phase,omitempty"`
	Markers        []markerTime `json:"markers,omitempty"`
}

type markerTime struct {
	Marker string `json:"marker"`
	MS     int64  `json:"ms"`
}

// changesSummaryRec 是 GetChanges summary 的记录投影（五个待同步计数 + 总数）。
type changesSummaryRec struct {
	Total           int `json:"total"`
	CreateCount     int `json:"create_count"`
	ModifyCount     int `json:"modify_count"`
	DeleteCount     int `json:"delete_count"`
	ConflictCount   int `json:"conflict_count"`
	InitChoiceCount int `json:"init_choice_count"`
}

// pendingCounts 报告五个待同步计数之和（diff 归零断言用）。
func (s changesSummaryRec) pendingCounts() int {
	return s.CreateCount + s.ModifyCount + s.DeleteCount + s.ConflictCount + s.InitChoiceCount
}

func newRecord(seed, fixtureSeed int64, mods, textFiles int) *recoveryRecord {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return &recoveryRecord{
		Schema:  "p2-recovery/1",
		Ticket:  "skyraah/PackGradle#41",
		Spec:    "docs/acceptance/p2-acceptance-spec.md §2.1 强杀注入全口径 / §2.4 人工确认路径 L0 唯一自动化例外",
		Date:    time.Now().Format("2006-01-02"),
		Machine: machineInfo{Host: host, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), CPUs: runtime.NumCPU()},
		Seed:    seed,
		Fixture: fixtureInfo{
			Seed: fixtureSeed, Mods: mods, TextFiles: textFiles, ManagedFiles: mods + textFiles,
			Note: "internal/perffixture 确定性生成器；每轮全新 fixture+数据目录（互不污染），120 文本文件为展宽引擎相位窗口的 harness 规模",
		},
		RoundsFixed: 5,
		Procedure:   "每轮：全新 fixture+数据目录 → pgheadless -apply 子进程（stdout 相位标记实时观察）→ 种子调度目标相位（轮 1-3 洗牌保证 staging/applying/verifying 全覆盖，4-5 随机补采）标记出现后随机延迟 taskkill /F 真强杀（相位推进过目标即提前击杀）→ 重启（bootstrap.Build 共享启动路径，RecoverInterruptedTasks 同步恢复裁决）→ 再重启两次核对幂等（文件树 sha256 清单+运行/任务/操作状态+提交数不变）→ 四不变式断言；recovery_required 轮以 AcknowledgeRecovery 收口（§2.4 授权的 L0 唯一人工路径自动化）后复跑 pgheadless -apply 至 committed。击杀未验证或未过 ConfirmPlan 的尝试按同目标相位减半延迟重试。",
		rng:         rand.New(rand.NewSource(seed)),
	}
}

func (r *recoveryRecord) finish(allPass bool) {
	r.Verdict = topLevelVerdict{AllPass: allPass}
	for i := range r.Rounds {
		if !r.Rounds[i].Passed() {
			r.Verdict.Violations = append(r.Verdict.Violations,
				fmt.Sprintf("第 %d 轮: %v", r.Rounds[i].Round, r.Rounds[i].Violations))
		}
	}
}
