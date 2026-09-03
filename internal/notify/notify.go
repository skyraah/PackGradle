// Package notify 实现系统通知（票 #97；契约 07 §3.5；规格 §D）：自动链停靠
// 待确认时的 Windows 11 toast 三条件触发 + 点击直达 + 静默降级。
//
// 结构分层：
//   - decision.go：三条件判定 ShouldToast + 降级判定 Deliver（纯逻辑，表驱动
//     全格单测——ADR 定义的确定函数，属外部契约的一部分，验收核心缝）；
//   - Gate：有状态编排（跨停靠的 pending_plan_id 去重账本 + 一次发送 + 点击
//     回调分发）；「通知发送」抽成 Sender 接口缝：Windows 生产实现包 wails
//     notifications 服务，单测注入假实现（错误注入）；
//   - wails_windows.go / wails_other.go：平台缝——Windows 用 wails v3
//     pkg/services/notifications（wintoast 子包，随已 pin 的 wails 模块，
//     零新顶层依赖）；非 Windows 恒不弹（v1 平台面，横幅/角标照常）。
//
// 线程安全：AutoChainDocked 由监听引擎的链 goroutine 调用（同 relation 单飞、
// 不同 relation 并发），Gate 内部互斥。
package notify

import (
	"log/slog"
	"strings"
	"sync"
)

// Sender 是「通知发送」接口缝（票面要求）：Windows 生产实现包 wails
// notifications 服务；单测注入假实现。返回错误 = toast 不可用或被系统拒绝
//（通知关闭/勿扰等）→ 调用方静默降级，不报错不重试。
type Sender interface {
	Send(n Notification) error
}

// Notification 是一次 toast 的内容。RelationID/PlanID 同时是点击回调回传的
// 直达载荷（wire 面由平台适配层装配进 Data）。
type Notification struct {
	RelationID string
	PlanID     string
	Title      string
	Body       string
}

// Copy 是通知文案（唯一文案源 = zh-CN locale，main 嵌入注入；{0}=工作区
// 显示名槽位）。
type Copy struct {
	Title string // locale 键 notify.pendingPlan.title
	Body  string // locale 键 notify.pendingPlan.body
}

// DefaultCopy 是 locale 加载失败时的同文缺省（与 zh-CN.json 键值保持一致；
// 正常路径永远不经过这里）。缺键不静默丢通知。
func DefaultCopy() Copy {
	return Copy{
		Title: "「{0}」有待确认计划",
		Body:  "自动同步已停靠待人工确认，点击直达计划页处理。",
	}
}

// orDefault 缺键项退内置同文缺省。
func (c Copy) orDefault() Copy {
	d := DefaultCopy()
	if c.Title == "" {
		c.Title = d.Title
	}
	if c.Body == "" {
		c.Body = d.Body
	}
	return c
}

// NewCopy 用 locale 取得的文案构造 Copy，缺键项退内置同文缺省（跨包装配入口，
// main 的 locale 加载面使用；缺键不静默丢通知）。
func NewCopy(title, body string) Copy {
	return Copy{Title: title, Body: body}.orDefault()
}

// formatCopy 用工作区显示名填充 {0} 槽位。
func formatCopy(tmpl, workspace string) string {
	return strings.ReplaceAll(tmpl, "{0}", workspace)
}

// Ports 是平台缝依赖（Attach 后生效）。字段注释同时是契约：
//   - Platform=false（非 Windows）判定恒不弹；
//   - Foreground 取不到窗口时必须保守返回 true（视为在前台，宁可不弹不可误弹）；
//   - Sender 返回错误一律静默降级。
type Ports struct {
	// Platform：v1 仅 Windows 弹 toast；其他平台判定恒不弹（契约 07 §3.5）。
	Platform bool
	// Sender 是 toast 发送缝；nil = 无发送面（等价恒降级，不 panic）。
	Sender Sender
	// Foreground 报告应用窗口是否在前台（触发条件②的肯定面）；nil = 保守视为
	// 在前台（宁可不弹不可误弹——该取向有表驱动单测锁定）。
	Foreground func() bool
	// WorkspaceName 供文案 {0} 槽取工作区显示名；nil 或返回空回退 relation id。
	WorkspaceName func(relationID string) string
	// Copy 是通知文案（zh-CN locale 注入；空项退 DefaultCopy）。
	Copy Copy
	// Focus 窗口前置（点击回调第一步）；Navigate 发前端导航事件（直达
	// /workspaces/:id/plans/:pending_plan_id，与角标同落点）。headless 面两者
	// 可为 nil（回调缺载荷时本来就不动作）。
	Focus    func()
	Navigate func(relationID, planID string)
}

// Gate 是通知编排器（有状态：跨停靠的 pending_plan_id 去重账本）。未 Attach
// 时恒惰——headless 工具共用 bootstrap 装配但无 GUI 面，入口全部 no-op。
type Gate struct {
	mu    sync.Mutex
	ports Ports
	set   bool
	// last 是每 relation 最近一次自动停靠已判定的 pending_plan_id（条件③的
	// 去重基线：无→有或换新才可能弹；同一张计划重复停靠不重弹）。
	last map[string]string
}

// NewGate 构造未装配的惰性 gate（bootstrap 装配；GUI main AttachWails 后生效）。
func NewGate() *Gate { return &Gate{last: map[string]string{}} }

// Attach 装配平台面（GUI main 经平台适配层调用一次；重复调用覆盖）。
func (g *Gate) Attach(p Ports) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p.Copy = p.Copy.orDefault()
	g.ports, g.set = p, true
}

// AutoChainDocked 是触发事件源入口：自动链停于 awaiting_confirmation
//（bootstrap 的 AutoChain 缝喂入；手动入口不经此处——人就在界面，条件①在
// 事件源层已隔离，见 transport SyncService.QuickUpdate 的 paused 复位缝）。
// planID 是本次停靠的待确认计划，即此刻 WorkspaceStateDTO.pending_plan_id
// 投影（刚停靠的计划就是最新待人工计划）。
func (g *Gate) AutoChainDocked(relationID, planID string) {
	if relationID == "" || planID == "" {
		return
	}
	g.mu.Lock()
	prev := g.last[relationID]
	g.last[relationID] = planID // 去重基线在判定时点记账：同一张计划重复停靠不重弹
	p := g.ports
	attached := g.set
	g.mu.Unlock()
	if !attached {
		return
	}

	foreground := true
	if p.Foreground != nil {
		foreground = p.Foreground()
	}
	if !ShouldToast(TriggerInput{
		Platform:     p.Platform,
		AutoDocked:   true, // 事件源即自动链停靠，恒真
		Foreground:   foreground,
		PreviousPlan: prev,
		CurrentPlan:  planID,
	}) {
		return
	}

	// 三条件满足 → 组文案发送恰好一次；发送失败/被系统拒绝 → 静默退回应用内
	// 角标（角标是 pending_plan_id 投影的常在 UI 面），不报错不重试。
	name := relationID
	if p.WorkspaceName != nil {
		if n := p.WorkspaceName(relationID); n != "" {
			name = n
		}
	}
	var sendErr error
	if p.Sender != nil {
		sendErr = p.Sender.Send(Notification{
			RelationID: relationID,
			PlanID:     planID,
			Title:      formatCopy(p.Copy.Title, name),
			Body:       formatCopy(p.Copy.Body, name),
		})
	}
	if Deliver(p.Platform, sendErr) == OutcomeBadgeSilent {
		slog.Warn("notify: toast 不可用或被系统拒绝，静默退回应用内角标（不报错不重试）",
			"relation", relationID, "err", sendErr)
	}
}

// HandleNotificationResponse 是点击回调分发（平台适配层把正文点击
// DefaultActionIdentifier 转发进来）：窗口前置 + 发导航事件直达
// /workspaces/:id/plans/:pending_plan_id（与角标同落点）。不注册系统协议、
// 不改单实例（契约 07 §3.5）。载荷不完整（异常回调）只记日志不动作。
func (g *Gate) HandleNotificationResponse(relationID, planID string) {
	if relationID == "" || planID == "" {
		slog.Warn("notify: 通知点击回调载荷不完整，忽略", "relation", relationID, "plan", planID)
		return
	}
	g.mu.Lock()
	p := g.ports
	attached := g.set
	g.mu.Unlock()
	if !attached {
		return
	}
	if p.Focus != nil {
		p.Focus()
	}
	if p.Navigate != nil {
		p.Navigate(relationID, planID)
	}
}
