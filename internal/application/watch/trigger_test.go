package watch

import (
	"testing"
	"time"
)

// 触发器状态机单测（P4 验收规格 §4.1，票 #92）：纯逻辑、时间全参数注入
//（假时钟=显式时间戳），断言行为不卡毫秒。时序取编译期常量口径的注入值。

var (
	tq = 1500 * time.Millisecond // 静默期
	tm = 10 * time.Second        // 上限
)

// TestTriggerQuiescenceAggregates 静默期聚合：无新事件持续 1.5s 才触发；
// 期间新事件刷新静默期锚点（上限锚点保持本轮首个事件）。
func TestTriggerQuiescenceAggregates(t *testing.T) {
	t0 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	tr := newTrigger(tq, tm)

	// idle：无可触发时刻
	if _, ok := tr.deadline(); ok {
		t.Fatal("idle 相位不应有待决 deadline")
	}
	if tr.fireable(t0) {
		t.Fatal("idle 相位不可触发")
	}

	tr.invalidate(t0)
	dl, ok := tr.deadline()
	if !ok || !dl.Equal(t0.Add(tq)) {
		t.Fatalf("deadline = %v, 期望 %v", dl, t0.Add(tq))
	}
	if tr.fireable(t0.Add(tq - time.Millisecond)) {
		t.Fatal("静默期未满不应触发")
	}
	if !tr.fireable(t0.Add(tq)) {
		t.Fatal("静默期满应可触发")
	}

	// 事件风暴：每 100ms 一个新事件，静默期锚点持续后移，永不满 1.5s
	late := t0
	for i := 0; i < 20; i++ {
		late = late.Add(100 * time.Millisecond)
		tr.invalidate(late)
		if tr.fireable(late.Add(tq - time.Millisecond)) {
			t.Fatalf("第 %d 个事件后静默期不应已满", i)
		}
	}
	if dl, _ := tr.deadline(); !dl.Equal(late.Add(tq)) {
		t.Fatalf("静默期锚点应随新事件后移: %v", dl)
	}
	// 停笔：1.5s 后触发
	if !tr.fireable(late.Add(tq)) {
		t.Fatal("静默期满应可触发")
	}
}

// TestTriggerMaxWaitForces 上限强制：持续写入风暴（<1.5s 间隔）下，本轮首个
// 事件起 10s 内必触发（时间线与验收场景 2「持续写 ≥30s 轮数有上界」同型）。
func TestTriggerMaxWaitForces(t *testing.T) {
	t0 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	tr := newTrigger(tq, tm)
	tr.invalidate(t0)

	// 以 1s 间隔持续写 30s：静默期永不满，但 10s 上限到点必触发
	now := t0
	fired := false
	for i := 0; i < 30; i++ {
		now = now.Add(time.Second)
		if tr.fireable(now) {
			fired = true
			break
		}
		tr.invalidate(now)
	}
	if !fired {
		t.Fatal("上限期内未强制触发")
	}
	if !now.Equal(t0.Add(MaxWaitPeriod)) {
		t.Fatalf("强制触发点 = %v, 期望上限 %v", now, t0.Add(MaxWaitPeriod))
	}
	// deadline 取 min(静默期, 上限)：静默期锚点越界（last+1.5s > first+10s）
	// 后 deadline=上限锚点
	tr2 := newTrigger(tq, tm)
	tr2.invalidate(t0)
	tr2.invalidate(t0.Add(500 * time.Millisecond))
	if dl, _ := tr2.deadline(); !dl.Equal(t0.Add(2 * time.Second)) {
		t.Fatalf("早期 deadline 应=静默期锚: %v", dl)
	}
	tr2.invalidate(t0.Add(9 * time.Second))
	if dl, _ := tr2.deadline(); !dl.Equal(t0.Add(tm)) {
		t.Fatalf("风暴中 deadline 应=上限: %v", dl)
	}
}

// TestTriggerInflightDirtySupplement 单飞 + dirty 补轮（ADR-0010 §5）：链进行中
// 新失效只标 dirty；链收口立即重开一轮 pending（补一轮至干净）；干净收口回 idle。
func TestTriggerInflightDirtySupplement(t *testing.T) {
	t0 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	tr := newTrigger(tq, tm)

	tr.invalidate(t0)
	tr.start()
	if _, ok := tr.deadline(); ok {
		t.Fatal("inflight 相位不应有待决 deadline")
	}
	// 链进行中新失效：只标 dirty，不开新轮
	tr.invalidate(t0.Add(200 * time.Millisecond))
	tr.invalidate(t0.Add(400 * time.Millisecond))
	if _, ok := tr.deadline(); ok {
		t.Fatal("inflight 期间新失效不应产生待决 deadline")
	}

	// 收口 → dirty 重开 pending（补一轮），静默期从收口时刻重算
	settleAt := t0.Add(time.Second)
	tr.settle(settleAt)
	dl, ok := tr.deadline()
	if !ok || !dl.Equal(settleAt.Add(tq)) {
		t.Fatalf("补轮 deadline = %v, 期望 %v", dl, settleAt.Add(tq))
	}

	// 第二轮干净收口 → idle（风暴两轮收敛）
	tr.start()
	tr.settle(settleAt.Add(2 * tq))
	if _, ok := tr.deadline(); ok {
		t.Fatal("干净收口后不应有待决 deadline")
	}
}

// TestTriggerClear 手动快速更新成功复位语境：挂起失效整体清空（链刚做过
// 全量扫描，失效事实已随消化）。
func TestTriggerClear(t *testing.T) {
	t0 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	tr := newTrigger(tq, tm)
	tr.invalidate(t0)
	tr.invalidate(t0.Add(100 * time.Millisecond))
	tr.clear()
	if _, ok := tr.deadline(); ok {
		t.Fatal("clear 后不应有待决 deadline")
	}
	if tr.fireable(t0.Add(10 * time.Second)) {
		t.Fatal("clear 后不可触发")
	}
	// clear 后新一轮从 idle 正常开启
	tr.invalidate(t0.Add(time.Second))
	if _, ok := tr.deadline(); !ok {
		t.Fatal("clear 后新事件应重新开启 pending")
	}
}
