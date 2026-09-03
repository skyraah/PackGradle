package notify

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeSender 是 Sender 假实现（单测注入；err 非 nil 时模拟 toast 被系统拒绝/
// 推送失败——降级错误注入缝）。
type fakeSender struct {
	mu   sync.Mutex
	sent []Notification
	err  error
}

var _ Sender = (*fakeSender)(nil)

func (s *fakeSender) Send(n Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, n)
	return s.err
}

func (s *fakeSender) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// focusNav 记录窗口前置与导航事件（点击回调分发的假 GUI 面）。
type focusNav struct {
	mu    sync.Mutex
	focus int
	navs  [][2]string
}

func (f *focusNav) onFocus() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focus++
}

func (f *focusNav) onNavigate(relationID, planID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.navs = append(f.navs, [2]string{relationID, planID})
}

func (f *focusNav) snapshot() (int, [][2]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.focus, append([][2]string(nil), f.navs...)
}

// newTestGate 组装一个已装配的 gate（platform=true、文案=DefaultCopy）。
func newTestGate(sender Sender, foreground *bool, workspaceName string, fn *focusNav) *Gate {
	g := NewGate()
	g.Attach(Ports{
		Platform:   true,
		Sender:     sender,
		Foreground: func() bool { return *foreground },
		WorkspaceName: func(string) string {
			return workspaceName
		},
		Copy:     DefaultCopy(),
		Focus:    fn.onFocus,
		Navigate: fn.onNavigate,
	})
	return g
}

// TestGateAutoDockFiresToast 三条件满足 → 恰好一次 toast，文案填充 {0} 槽位、
// 直达载荷（relation/plan）随通知走。
func TestGateAutoDockFiresToast(t *testing.T) {
	sender := &fakeSender{}
	fg := false
	fn := &focusNav{}
	g := newTestGate(sender, &fg, "工作区甲", fn)

	g.AutoChainDocked("rel-1", "plan-A")

	if got := sender.calls(); got != 1 {
		t.Fatalf("toast 发送次数 = %d, 期望 1", got)
	}
	n := sender.sent[0]
	if n.RelationID != "rel-1" || n.PlanID != "plan-A" {
		t.Fatalf("通知载荷 = %+v, 期望 relation=rel-1 plan=plan-A", n)
	}
	if strings.Contains(n.Title, "{0}") || !strings.Contains(n.Title, "工作区甲") {
		t.Fatalf("标题槽位未填充: %q", n.Title)
	}
	if strings.Contains(n.Body, "{0}") {
		t.Fatalf("正文残留未填充槽位: %q", n.Body)
	}
}

// TestGateForegroundSuppressThenNewPlan 条件②不满足（前台）不弹，但去重基线
// 照常记账：同一张计划转后台后重复停靠仍不弹，换新计划才弹（无→有/换新语义）。
func TestGateForegroundSuppressThenNewPlan(t *testing.T) {
	sender := &fakeSender{}
	fg := true
	fn := &focusNav{}
	g := newTestGate(sender, &fg, "工作区乙", fn)

	g.AutoChainDocked("rel-1", "plan-A")
	if got := sender.calls(); got != 0 {
		t.Fatalf("前台停靠弹了 toast（次数=%d）, 期望 0", got)
	}

	fg = false
	g.AutoChainDocked("rel-1", "plan-A") // 同一张计划重复停靠：不重弹
	if got := sender.calls(); got != 0 {
		t.Fatalf("同计划重复停靠弹了 toast（次数=%d）, 期望 0", got)
	}

	g.AutoChainDocked("rel-1", "plan-B") // 换新计划：弹
	if got := sender.calls(); got != 1 {
		t.Fatalf("换新计划未弹 toast（次数=%d）, 期望 1", got)
	}
	if sender.sent[0].PlanID != "plan-B" {
		t.Fatalf("通知计划 = %q, 期望 plan-B", sender.sent[0].PlanID)
	}
}

// TestGateSendRejectedSilentNoRetry 降级错误注入（验收强制项）：toast 被系统
// 拒绝 → 不报错不 panic、该事件只发送一次（不重试）；去重基线照常记账，同
// 计划重复停靠不再尝试；换新计划的停靠是全新事件、照常恰好一次。
func TestGateSendRejectedSilentNoRetry(t *testing.T) {
	sender := &fakeSender{err: errors.New("toast 被系统拒绝（通知关闭/勿扰）")}
	fg := false
	fn := &focusNav{}
	g := newTestGate(sender, &fg, "工作区丙", fn)

	g.AutoChainDocked("rel-1", "plan-A")
	if got := sender.calls(); got != 1 {
		t.Fatalf("首次停靠发送次数 = %d, 期望恰好 1（不重试）", got)
	}
	g.AutoChainDocked("rel-1", "plan-A")
	if got := sender.calls(); got != 1 {
		t.Fatalf("同计划重复停靠又发送（次数=%d）, 期望仍为 1", got)
	}
	g.AutoChainDocked("rel-1", "plan-B")
	if got := sender.calls(); got != 2 {
		t.Fatalf("换新计划停靠发送次数 = %d, 期望 2（新事件恰好一次）", got)
	}
}

// TestGatePlatformDisabledNeverFires 平台面：非 Windows 恒不弹（判定层拦截，
// 横幅/角标照常——前端不受影响）。
func TestGatePlatformDisabledNeverFires(t *testing.T) {
	sender := &fakeSender{}
	fg := false
	fn := &focusNav{}
	g := newTestGate(sender, &fg, "工作区丁", fn)
	g.mu.Lock()
	g.ports.Platform = false
	g.mu.Unlock()

	g.AutoChainDocked("rel-1", "plan-A")
	if got := sender.calls(); got != 0 {
		t.Fatalf("非 Windows 弹了 toast（次数=%d）, 期望 0", got)
	}
}

// TestGateConservativeForegroundWhenWindowUnknown 窗口前台态取不到（Foreground
// 缝为 nil）→ 保守视为在前台，宁可不弹不可误弹（契约 07 §3.5 取向）。
func TestGateConservativeForegroundWhenWindowUnknown(t *testing.T) {
	sender := &fakeSender{}
	fn := &focusNav{}
	g := NewGate()
	g.Attach(Ports{
		Platform:   true,
		Sender:     sender,
		Foreground: nil, // 取不到窗口态
		Copy:       DefaultCopy(),
		Focus:      fn.onFocus,
		Navigate:   fn.onNavigate,
	})

	g.AutoChainDocked("rel-1", "plan-A")
	if got := sender.calls(); got != 0 {
		t.Fatalf("窗口态取不到时弹了 toast（次数=%d）, 期望 0（保守不弹）", got)
	}
}

// TestGateNotAttachedInert 未装配平台面（headless 工具共用 bootstrap 装配）→
// 全部入口 no-op，零 panic 零发送。
func TestGateNotAttachedInert(t *testing.T) {
	g := NewGate()
	g.AutoChainDocked("rel-1", "plan-A")   // 不得 panic
	g.HandleNotificationResponse("r", "p") // 不得 panic
}

// TestGateNilSenderSilent 平台支持但无发送面 → 等价恒降级（静默、零 panic）。
func TestGateNilSenderSilent(t *testing.T) {
	fg := false
	fn := &focusNav{}
	g := NewGate()
	g.Attach(Ports{
		Platform:   true,
		Sender:     nil,
		Foreground: func() bool { return fg },
		Copy:       DefaultCopy(),
		Focus:      fn.onFocus,
		Navigate:   fn.onNavigate,
	})

	g.AutoChainDocked("rel-1", "plan-A") // 不得 panic
}

// TestGateWorkspaceNameFallback 文案 {0} 槽：WorkspaceName 缝为 nil 或返回空
// → 回退 relation id，绝不留下未填充槽位或空标题。
func TestGateWorkspaceNameFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		ws   func(string) string
	}{
		{"缝为nil", nil},
		{"返回空", func(string) string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeSender{}
			fg := false
			g := NewGate()
			g.Attach(Ports{
				Platform:      true,
				Sender:        sender,
				Foreground:    func() bool { return fg },
				WorkspaceName: tc.ws,
				Copy:          DefaultCopy(),
			})

			g.AutoChainDocked("rel-9", "plan-A")
			if got := sender.calls(); got != 1 {
				t.Fatalf("发送次数 = %d, 期望 1", got)
			}
			n := sender.sent[0]
			if strings.Contains(n.Title, "{0}") || !strings.Contains(n.Title, "rel-9") {
				t.Fatalf("标题未回退 relation id: %q", n.Title)
			}
		})
	}
}

// TestGateEmptyCopyFallsBackToDefault locale 缺键（Copy 空项）→ 退内置同文
// 缺省，缺键不静默丢通知。
func TestGateEmptyCopyFallsBackToDefault(t *testing.T) {
	sender := &fakeSender{}
	fg := false
	g := NewGate()
	g.Attach(Ports{
		Platform:   true,
		Sender:     sender,
		Foreground: func() bool { return fg },
		Copy:       Copy{}, // 模拟 locale 键缺失
	})

	g.AutoChainDocked("rel-1", "plan-A")
	if got := sender.calls(); got != 1 {
		t.Fatalf("发送次数 = %d, 期望 1", got)
	}
	d := DefaultCopy()
	if sender.sent[0].Title != strings.ReplaceAll(d.Title, "{0}", "rel-1") {
		t.Fatalf("标题 = %q, 期望缺省 %q", sender.sent[0].Title, d.Title)
	}
}

// TestGateClickResponseDispatch 点击回调分发：合法载荷 → 窗口前置恰好一次 +
// 导航事件带原载荷；载荷不完整 → 不动作。
func TestGateClickResponseDispatch(t *testing.T) {
	fg := false
	fn := &focusNav{}
	g := newTestGate(&fakeSender{}, &fg, "工作区", fn)

	g.HandleNotificationResponse("rel-1", "plan-A")
	focus, navs := fn.snapshot()
	if focus != 1 || len(navs) != 1 || navs[0] != [2]string{"rel-1", "plan-A"} {
		t.Fatalf("点击分发 = focus %d 次 navs %v, 期望各 1 次且载荷原样", focus, navs)
	}

	g.HandleNotificationResponse("", "plan-A")
	g.HandleNotificationResponse("rel-1", "")
	focus, navs = fn.snapshot()
	if focus != 1 || len(navs) != 1 {
		t.Fatalf("载荷不完整仍动作: focus %d navs %v, 期望无增量", focus, navs)
	}
}
