package transport

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// CoreEvent 是新架构统一事件 topic：payload 恒为 EventEnvelope。
// 前端订阅后按 stream_sequence 检测漏包，并经查询 API 恢复权威状态。
const CoreEvent = "packgradle://event"

func init() {
	// 注册事件数据类型：Wails 生成 TS 绑定时把该 topic 的数据映射为 model.EventEnvelope。
	application.RegisterEvent[model.EventEnvelope](CoreEvent)
}

// EventBridge 把 application 事件桥接到 Wails Events；
// headless 场景（无 Wails 应用）静默丢弃——事件不是事实源，查询 API 仍可恢复状态。
type EventBridge struct{}

// NewEventBridge 构造事件桥。
func NewEventBridge() *EventBridge { return &EventBridge{} }

// Publish 实现 ports.EventPublisher。
func (b *EventBridge) Publish(_ context.Context, env model.EventEnvelope) error {
	if app := application.Get(); app != nil && app.Event != nil {
		app.Event.Emit(CoreEvent, env)
	}
	return nil
}

var _ ports.EventPublisher = (*EventBridge)(nil)
