package main

// wait.go 收敛 pgheadless 包内「轮询任务至终态」的重复循环（评审 T3）：apply/
// restore/restorecold/restoretarget/download/gc/merge 各链原本各持一份 GetTask
// 轮询循环（waitApplyTask/rstWaitTask/waitTaskStatus/mrgWaitTask），语义仅四点
// 差异：轮询节奏、总超时、峰值内存采样、相位进度行与终态判定口径。统一为一个
// 核心循环 + 选项结构；各链保留自身超时/节奏常量与 stdout 进度行形态（验收
// 断言形态零变化）。

import (
	"context"
	"fmt"
	"time"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
)

// taskWait 是轮询任务的选项（interval/timeout 必填，其余零值 = 缺省行为）。
type taskWait struct {
	// interval 是轮询节奏（沿各链既有常量：100ms/200ms）。
	interval time.Duration
	// timeout 是总超时。
	timeout time.Duration
	// mem 非 nil 时每周期采样进程内存峰值（apply 度量面，票 #46）。
	mem *memPeakSampler
	// want 非空 = 「轮询到期望状态」语义（GC 面）：仅该状态返回成功，其余终态
	// 提前失败返回。空 = 任一合法终态（succeeded/failed/cancelled/
	// recovery_required）都返回，成败交调用方按非成功收口（不给假绿）。
	want string
	// onPhase 是相位变化回调（nil = 静默；stdout 进度行形态由各链自持）。
	onPhase func(tv view.TaskView)
}

// waitTask 轮询 GetTask 至终态：want 命中（或 want 空时任一合法终态）返回任务
// 快照；GetTask 失败、其余终态先于 want 出现（want 模式）或超时返回错误。
func waitTask(ctx context.Context, app syncapp.Application, taskID string, w taskWait) (view.TaskView, error) {
	deadline := time.Now().Add(w.timeout)
	lastPhase := ""
	for {
		tv, err := app.GetTask(ctx, taskID)
		if err != nil {
			return view.TaskView{}, fmt.Errorf("GetTask: %w", err)
		}
		if w.mem != nil {
			w.mem.sample()
		}
		if tv.Phase != lastPhase {
			if w.onPhase != nil {
				w.onPhase(tv)
			}
			lastPhase = tv.Phase
		}
		if w.want == "" {
			switch tv.Status {
			case model.TaskStatusSucceeded, model.TaskStatusFailed,
				model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
				return tv, nil
			}
		} else {
			if tv.Status == w.want {
				return tv, nil
			}
			switch tv.Status {
			case model.TaskStatusFailed, model.TaskStatusCancelled, model.TaskStatusRecoveryRequired:
				return tv, fmt.Errorf("任务终态 %s（期望 %s）problem=%s",
					tv.Status, w.want, problemText(tv.Problem))
			}
		}
		if time.Now().After(deadline) {
			return view.TaskView{}, fmt.Errorf("任务 %s 超时未至终态（当前 %s/%s，超时 %v）",
				taskID, tv.Status, tv.Phase, w.timeout)
		}
		time.Sleep(w.interval)
	}
}
