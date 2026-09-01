package main

// restore 强杀记录形态（p3-recovery-restore/1，票 #66；验收规格 §4）：沿
// p2-recovery/1 先例，逐轮携带随机种子、强杀时机、裁决终局、收口路径与
// 不变式结论——P2 四不变式照用 + restore 特有两条（R5 绝不假 committed /
// R6 kind=restore 终局后历史不改写）。

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// restoreRecoveryRecord 是 acceptance:recovery:restore 的顶层记录。
type restoreRecoveryRecord struct {
	Schema      string               `json:"schema"` // p3-recovery-restore/1
	Ticket      string               `json:"ticket"`
	Spec        string               `json:"spec"`
	Date        string               `json:"date"`
	Machine     machineInfo          `json:"machine"`
	Seed        int64                `json:"seed"` // 强杀调度种子（同种子可重放调度序列）
	CDN         cdnInfo              `json:"cdn"`
	Fixture     fixtureInfo          `json:"fixture"`
	RoundsFixed int                  `json:"rounds_fixed"`
	Procedure   string               `json:"procedure"`
	Rounds      []restoreRoundRecord `json:"rounds"`
	Verdict     topLevelVerdict      `json:"verdict"`

	rng *rand.Rand // 非序列化：调度随机源
}

type cdnInfo struct {
	Mode    string `json:"mode"` // managed（harness 拉起 pgfixture -serve）
	BaseURL string `json:"base_url"`
}

// restoreRoundRecord 是 restore 强杀单轮记录。
type restoreRoundRecord struct {
	Round        int            `json:"round"`
	Schedule     string         `json:"schedule"` // 种子调度的目标相位（staging 下载/applying/verifying）
	Attempts     []attemptRec   `json:"attempts"`
	AttemptCount int            `json:"attempt_count"`
	Chain        []string       `json:"chain"`        // 子进程透出的提交链（armed 行；历史不改写断言基准）
	Verdict      string         `json:"verdict"`      // committed | recovery_required
	ClosurePath  string         `json:"closure_path"` // auto_committed | acknowledge_rerestore_committed
	Kill         killFacts      `json:"kill"`
	OpTally      map[string]int `json:"op_status_tally,omitempty"`
	NewCommit    string         `json:"new_commit,omitempty"` // restore 新提交（committed 轮）
	StagingParts int            `json:"staging_part_files"`   // 终局后 staging 根下 .part 残留数

	// recovery_required 轮的收口证据：acknowledge 后剩余 diff（不设门槛）。
	RemainingDiffAfterAck *changesSummaryRec `json:"remaining_diff_after_ack,omitempty"`

	Invariant1 bool     `json:"i1_no_partial_illusion"`
	Invariant2 bool     `json:"i2_rescan_diff_zero_after_closure"`
	Invariant3 bool     `json:"i3_gate_and_rerestore"`
	Invariant4 bool     `json:"i4_repeat_recovery_idempotent"`
	R5         bool     `json:"r5_no_false_committed"`  // restore 特有：绝不假 committed
	R6         bool     `json:"r6_history_append_only"` // restore 特有：终局后历史不改写
	Violations []string `json:"violations,omitempty"`
}

// Passed 报告该轮是否全过（四不变式 + restore 特有两条 + 无违规）。
func (r *restoreRoundRecord) Passed() bool {
	return r.Invariant1 && r.Invariant2 && r.Invariant3 && r.Invariant4 && r.R5 && r.R6 &&
		len(r.Violations) == 0
}

func newRestoreRecord(seed, fixtureSeed int64, mods, textFiles int) *restoreRecoveryRecord {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return &restoreRecoveryRecord{
		Schema:  "p3-recovery-restore/1",
		Ticket:  "skyraah/PackGradle#66",
		Spec:    "docs/acceptance/p3-acceptance-spec.md §4 回滚中断强杀（P2 §2.1 四不变式照用 + restore 特有两条）",
		Date:    time.Now().Format("2006-01-02"),
		Machine: machineInfo{Host: host, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), CPUs: runtime.NumCPU()},
		Seed:    seed,
		Fixture: fixtureInfo{
			Seed: fixtureSeed, Mods: mods, TextFiles: textFiles, ManagedFiles: mods + textFiles,
			Note: "pgheadless -restore-target 自含夹具（双侧 120 受管文本 + 6 个 CF 声明 mod 经假 CDN 下载入基线）；restore 前删 3 个运行端 jar 作 redownload 行漂移",
		},
		RoundsFixed: 5,
		Procedure:   "每轮：全新轮目录 → pgheadless -restore-target 子进程（自建夹具历史 c1/c2 → PrepareRestore(最老提交) → == ConfirmRestorePlan == armed 行）→ armed 后种子调度目标相位（轮 1-3 洗牌保证 staging 下载/applying/verifying 全覆盖，4-5 随机补采）标记出现后随机延迟 taskkill /F 真强杀 → 重启（bootstrap.Build 共享启动路径，RecoverInterruptedTasks 同步恢复裁决）→ 再重启两次核对幂等 → 四不变式 + restore 特有两条逐轮断言；recovery_required 轮以 AcknowledgeRecovery 收口后复跑 -restore-target 至 committed。armed 前击杀（killed_before_confirm）按同目标相位减半延迟重试。",
		rng:         rand.New(rand.NewSource(seed)),
	}
}

func (r *restoreRecoveryRecord) finish(allPass bool) {
	r.Verdict.AllPass = allPass
	for i := range r.Rounds {
		if !r.Rounds[i].Passed() {
			r.Verdict.Violations = append(r.Verdict.Violations,
				fmt.Sprintf("第 %d 轮: %v", r.Rounds[i].Round, r.Rounds[i].Violations))
		}
	}
}

// defaultRestoreRecordPath 沿 records 先例自动命名：p3-recovery-restore-<date>-<host>.json。
func defaultRestoreRecordPath() string {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return filepath.Join("docs", "acceptance", "records", fmt.Sprintf("p3-recovery-restore-%s-%s.json", time.Now().Format("2006-01-02"), host))
}
