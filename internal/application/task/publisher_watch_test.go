package task

import (
	"context"
	"fmt"
	"testing"

	"packgradle/internal/core/model"
)

// TestPublisherWatchFailed watch_failed 预留形状原样启用（契约 04 §2.5，票 #92）：
// envelope 带 relation_id、payload {}；前端按 invalidation 处理 + 一次性
// 「监听不可用」提示。仅监听引擎重建仍败时发出（ADR-0010 §7）。
func TestPublisherWatchFailed(t *testing.T) {
	_, pub, _, sink := newFixture()
	if err := pub.PublishWatchFailed(context.Background(), "rel_x"); err != nil {
		t.Fatal(err)
	}
	if len(sink.items) != 1 || sink.items[0].EventType != model.EventWatchFailed {
		t.Fatalf("事件: %+v", sink.items)
	}
	if sink.items[0].RelationID != "rel_x" {
		t.Fatalf("relation_id: %q", sink.items[0].RelationID)
	}
	if fmt.Sprintf("%s", sink.items[0].Payload) != "{}" {
		t.Fatalf("payload: %s", sink.items[0].Payload)
	}
}
