//go:build windows

package notify

// Windows 平台适配层（票 #97，契约 07 §3.5）：toast 走 wails v3
// pkg/services/notifications 的 wintoast 子包（随已 pin 的 wails 模块，零新
// 顶层依赖、零 CGO）。点击回调=进程内 OnNotificationResponse（正文点击
// DefaultActionIdentifier）→ 窗口前置 + 发前端导航事件直达计划页；不注册系统
// 协议、不改单实例。服务本体须注册进 Wails Services 列表（main.go）以获得
// ServiceStartup（wintoast AppData/GUID 注册与激活回调接线）。

import (
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// NavTopic 是点击直达的前端导航事件 topic（packgradle://notify）。它独立于
// 契约 04 核心事件流 packgradle://event（零新事件类型红线不受影响），纯 UI
// 导航信号、零状态语义；前端订阅点 frontend/src/api/notificationNav.ts。
const NavTopic = "packgradle://notify"

// wailsSender 把 Sender 缝接到 wails notifications 服务。RelationID/PlanID
// 放进 Data：点击激活时原样回传（NotificationResponse.UserInfo），直达载荷
// 不依赖通知文案。
type wailsSender struct{ svc *notifications.NotificationService }

// Send 发一次 toast（恰好一次；失败原样返回错误 → gate 静默降级不重试）。
func (s wailsSender) Send(n Notification) error {
	return s.svc.SendNotification(notifications.NotificationOptions{
		ID:    "pending-plan:" + n.RelationID,
		Title: n.Title,
		Body:  n.Body,
		Data: map[string]any{
			"relation_id": n.RelationID,
			"plan_id":     n.PlanID,
		},
	})
}

// AttachWails 把 gate 接到 Wails 应用面（GUI main 在应用创建前调用一次；
// 窗口/事件经 application.Get() 在使用点惰性解析）。workspaceName 供文案
// {0} 槽取工作区显示名（可 nil/返回空 → toast 回退 relation id）。非 Windows
// 构建该符号为 no-op（wails_other.go）。
func AttachWails(g *Gate, copy Copy, workspaceName func(relationID string) string) {
	svc := notifications.New()
	g.Attach(Ports{
		Platform:      true, // v1 仅 Windows 弹 toast
		Sender:        wailsSender{svc: svc},
		Foreground:    currentWindowFocused,
		WorkspaceName: workspaceName,
		Copy:          copy,
		Focus:         bringWindowToFront,
		Navigate:      emitNavigate,
	})
	// 进程内点击回调（正文点击=DefaultActionIdentifier；本票未注册任何按钮
	// 分类，其他 ActionIdentifier 一律忽略）。回调错误只记诊断日志（会话日志）。
	svc.OnNotificationResponse(func(result notifications.NotificationResult) {
		if result.Error != nil {
			slog.Warn("notify: 通知激活回调返回错误", "err", result.Error)
			return
		}
		if result.Response.ActionIdentifier != notifications.DefaultActionIdentifier {
			return
		}
		relationID, _ := result.Response.UserInfo["relation_id"].(string)
		planID, _ := result.Response.UserInfo["plan_id"].(string)
		g.HandleNotificationResponse(relationID, planID)
	})
}

// emitNavigate 发前端导航事件（点击直达第二步；直达 /workspaces/:id/plans/
// :pending_plan_id，与角标同落点）。应用未起（headless/窗口未建）静默丢弃。
func emitNavigate(relationID, planID string) {
	app := application.Get()
	if app == nil || app.Event == nil {
		return
	}
	app.Event.Emit(NavTopic, map[string]string{
		"relation_id": relationID,
		"plan_id":     planID,
	})
}

// currentWindowFocused 报告应用窗口是否在前台（触发条件②的肯定面）。取不到
// 应用/窗口（headless、窗口尚未创建）时保守返回 true=视为在前台——宁可不弹
// 不可误弹（契约 07 §3.5；该取向由 gate 表驱动单测锁定）。
func currentWindowFocused() bool {
	app := application.Get()
	if app == nil || app.Window == nil {
		return true
	}
	w := app.Window.Current()
	if w == nil {
		return true
	}
	return w.IsFocused()
}

// bringWindowToFront 窗口前置（点击直达第一步）：最小化先还原，再 Show+Focus。
func bringWindowToFront() {
	app := application.Get()
	if app == nil || app.Window == nil {
		return
	}
	w := app.Window.Current()
	if w == nil {
		return
	}
	if w.IsMinimised() {
		w.Restore()
	}
	w.Show()
	w.Focus()
}
