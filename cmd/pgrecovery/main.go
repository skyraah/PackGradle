// pgrecovery 是 P2 验收恢复强杀注入 harness（票 #41；验收规格 §2.1/§2.4，
// ADR-0004 §4 恢复协议的 L0 硬门槛）：
//
//	每轮（固定 5 轮）：全新 fixture + 全新用户数据目录 → 起 pgheadless -apply
//	子进程 → 按种子调度的目标相位（staging/applying/verifying，五轮全覆盖）
//	观察到相位标记后随机延迟 taskkill /F 真强杀 → 重启（bootstrap.Build 共享
//	启动路径，RecoverInterruptedTasks 同步执行恢复管线）→ 四不变式逐轮断言 →
//	逐轮记录 JSON。
//
// 四不变式（验收规格 §2.1）：
//  1. 无「部分完成」假象：终局要么 committed 要么 recovery_required；
//     committed 必须逐操作 verified 且复扫 diff 归零；recovery_required 必须
//     零提交（基线未推进）且 apply 不可用（err.recovery.in_progress）；
//  2. 收口后重扫 diff 归零（committed 轮恢复收口后；recovery_required 轮
//     acknowledge → 复跑 -apply 收口后）；
//  3. recovery_required 期间 apply 不可用 + 收口后 apply 可重跑成功
//     （真实 pgheadless -apply 子进程退出码 0）；
//  4. 重复恢复幂等：再重启两次（共三次启动），fixture 文件树逐字节清单、
//     运行/任务/操作日志状态、提交数全部不变——无重复补偿/删除/二次破坏。
//
// 含糊轮次（probe 保持 recovery_required）：以 AcknowledgeRecovery 收口后
// 继续断言——这是 §2.4 授权的唯一 L0 自动化人工路径；含糊判定逻辑本身由
// T05 单测覆盖，harness 不构造含糊夹具、不逐轮断言裁决结果（随机时机下
// 裁决本就不定，不变式才是硬门槛）。
//
// 用法（Taskfile acceptance:recovery）：
//
//	pgrecovery [-rounds 5] [-seed N] [-pgheadless bin/pgheadless.exe]
//	           [-work build/recovery] [-record <path>]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// recovery 错误码（internal/application/sync 拆码；harness 侧只比对字面值）。
const codeRecoveryInProgress = "err.recovery.in_progress"

func main() {
	var (
		rounds      = flag.Int("rounds", 5, "强杀轮数（验收规格 §2.1 固定 5）")
		seed        = flag.Int64("seed", 20260831, "强杀调度随机种子（目标相位+延迟，入记录可重放）")
		mods        = flag.Int("mods", 6, "fixture mod 数")
		textFiles   = flag.Int("text-files", 120, "fixture 受管文本文件数（加大以展宽相位窗口）")
		fixtureSeed = flag.Int64("fixture-seed", 20260830, "fixture 生成种子（确定性生成器）")
		headlessBin = flag.String("pgheadless", filepath.Join("bin", "pgheadless.exe"), "pgheadless 可执行文件（apply 子进程与复跑）")
		work        = flag.String("work", filepath.Join("build", "recovery"), "harness 工作目录（逐轮逐次尝试新建 fixture/数据目录）")
		recordPath  = flag.String("record", "", "逐轮记录 JSON 输出路径（空=自动 docs/acceptance/records/p2-recovery-<date>-<host>.json；\"-\"=不落盘）")
		killWindow  = flag.Duration("kill-window", 120*time.Second, "等待目标相位标记的总时限（超时仍强杀）")
	)
	flag.Parse()
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "acceptance:recovery 是 Windows 强杀注入（taskkill /F），仅支持 Windows 执行")
		os.Exit(2)
	}
	if *rounds != 5 {
		fmt.Fprintf(os.Stderr, "警告：验收规格 §2.1 固定 5 轮，当前 -rounds=%d（仅调试用）\n", *rounds)
	}

	rec := newRecord(*seed, *fixtureSeed, *mods, *textFiles)
	allPass := true

	// 强杀相位调度（种子驱动，一次抽取入记录）：轮 1-3 洗牌保证 staging/
	// applying/verifying 三相位全覆盖，轮 4-5 随机补采。
	schedule := make([]string, *rounds)
	perm := rec.rng.Perm(len(schedulePhases))
	for i := range schedule {
		if i < len(schedulePhases) {
			schedule[i] = schedulePhases[perm[i]]
		} else {
			schedule[i] = schedulePhases[rec.rng.Intn(len(schedulePhases))]
		}
	}

	for r := 1; r <= *rounds; r++ {
		fmt.Printf("\n===== 第 %d/%d 轮 =====\n", r, *rounds)
		rr := runRound(roundContext{
			index:       r,
			targetPhase: schedule[r-1],
			rng:         rec.rng,
			mods:        *mods,
			textFiles:   *textFiles,
			fixtureSeed: *fixtureSeed,
			headlessBin: *headlessBin,
			workRoot:    *work,
			killWindow:  *killWindow,
		})
		rec.Rounds = append(rec.Rounds, rr)
		allPass = allPass && rr.Passed()
		fmt.Printf("== 第 %d 轮结论 == verdict=%s 收口=%s 不变式 I1=%v I2=%v I3=%v I4=%v 违规=%d\n",
			r, rr.Verdict, rr.ClosurePath,
			rr.Invariant1, rr.Invariant2, rr.Invariant3, rr.Invariant4, len(rr.Violations))
		for _, v := range rr.Violations {
			fmt.Printf("  [违规] %s\n", v)
		}
	}

	rec.finish(allPass)
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
	fmt.Printf("\n== acceptance:recovery 总结论 == 全部通过=%v（%d 轮）\n", allPass, rec.RoundsFixed)
	if !allPass {
		os.Exit(1)
	}
}

// defaultRecordPath 沿 P1/P2 records 先例自动命名：p2-recovery-<date>-<host>.json。
func defaultRecordPath() string {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return filepath.Join("docs", "acceptance", "records",
		fmt.Sprintf("p2-recovery-%s-%s.json", time.Now().Format("2006-01-02"), host))
}

func fatalOn(err error, stage string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 失败: %v\n", stage, err)
		os.Exit(2)
	}
}
