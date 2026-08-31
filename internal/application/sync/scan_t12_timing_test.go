package sync_test

// T12（票 #44）验收观察项回归：pgheadless waitScan 以「无活跃任务」为扫描
// 完成信号后立即读 LastScanTiming（-metrics 供数口）。此前计时在 runScan
// 返回时的 deferred recordScanTiming 落账，晚于成功终态落库——消费方在该
// 窗口内读到零值计时（-metrics scan_phases_ms 全 0 而 run_total_ms 为真值，
// T14 九轮两现、T12 acceptance:perf 验收轮复现）。修复后计时先于成功终态
// 写入，同一 goroutine 序 + 互斥保证「无活跃任务」即蕴含计时已可见，本断言
// 因此是确定性的。

import (
	"context"
	"testing"
	"time"

	"packgradle/internal/application/ports"
)

func TestScanTimingVisibleWhenNoActiveTasks(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	tv, err := app.StartScan(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	// 与 cmd/pgheadless waitScan 同一完成信号：活跃任务列表清空即返回。
	deadline := time.Now().Add(30 * time.Second)
	for {
		page, err := app.ListTasks(ctx, rel.RelationID, true, ports.PageRequest{Limit: 5})
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if len(page.Items) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("扫描任务超时未结束")
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitTask(t, app, tv.TaskID)

	timing := app.LastScanTiming()
	if timing.RelationID != rel.RelationID {
		t.Fatalf("计时 relation = %q, 期望 %q", timing.RelationID, rel.RelationID)
	}
	if timing.TotalMs <= 0 {
		t.Fatalf("活跃任务清空后计时仍为零值（竞态复现）: %+v", timing)
	}
	if timing.ProjectScanMs <= 0 || timing.RuntimeScanMs <= 0 {
		t.Fatalf("分相计时缺失: %+v", timing)
	}
}
