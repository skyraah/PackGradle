package sync_test

// T14（票 #24）验收面：扫描分相耗时（Project/Runtime/Normalize/Persist/总）
// 经 LastScanTiming 可读；hash cache 命中计数以冷/热两轮 delta 供数
//（冷全 miss、热全 hit——同内容未变时命中率为 1）。

import (
	"context"
	"testing"
)

func TestHeadlessScanTimingAndHashCacheDelta(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, _ := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)

	before, err := app.GetHashCacheStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Hits != 0 || before.Misses != 0 {
		t.Fatalf("扫描前计数应为零: %+v", before)
	}

	tv, err := app.StartScan(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, app, tv.TaskID)

	afterCold, err := app.GetHashCacheStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 冷扫描：3 个 jar 全部真实哈希；3 个项目侧 metafile 自 #88 起同闭包实测
	// 捕获 Content（hash cache 顺路复用，零新扫描成本维度）→ 6 miss。
	if afterCold.Hits != 0 || afterCold.Misses != 6 {
		t.Fatalf("冷扫描 delta 应为 0 hit/6 miss: %+v", afterCold)
	}
	if afterCold.HitRatio != 0 {
		t.Fatalf("冷扫描命中率应为 0: %+v", afterCold)
	}

	timing := app.LastScanTiming()
	if timing.RelationID != rel.RelationID {
		t.Fatalf("计时 relation = %q, 期望 %q", timing.RelationID, rel.RelationID)
	}
	if timing.TotalMs <= 0 {
		t.Fatalf("总耗时应为正: %+v", timing)
	}
	phases := []int64{timing.ProjectScanMs, timing.RuntimeScanMs, timing.NormalizeMs, timing.PersistMs}
	for i, ms := range phases {
		if ms < 0 {
			t.Fatalf("分相 %d 耗时为负: %+v", i, timing)
		}
	}
	var sum int64
	for _, ms := range phases {
		sum += ms
	}
	if sum > timing.TotalMs {
		t.Fatalf("分相之和 %d 超过总耗时 %d: %+v", sum, timing.TotalMs, timing)
	}

	// 热扫描（同目录、未变、不删缓存）：全部命中
	tv2, err := app.StartScan(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, app, tv2.TaskID)
	afterWarm, err := app.GetHashCacheStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	warmHits := afterWarm.Hits - afterCold.Hits
	warmMisses := afterWarm.Misses - afterCold.Misses
	if warmHits != 6 || warmMisses != 0 {
		t.Fatalf("热扫描 delta 应为 6 hit/0 miss: hits=%d misses=%d", warmHits, warmMisses)
	}

	timing2 := app.LastScanTiming()
	if timing2.RelationID != rel.RelationID || timing2.TotalMs <= 0 {
		t.Fatalf("热扫描计时未刷新: %+v", timing2)
	}
}
