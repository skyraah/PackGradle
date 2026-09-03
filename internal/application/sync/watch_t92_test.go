package sync_test

// watch_status 投影与监听引擎接线测试（契约 07 §3.2，票 #92）：GetWorkspace 经
// AttachWatch 投影监听引擎状态（会话内存态，零持久化）；无监听面投影空值
//（headless 语境）；relation 生命周期尾点经 kick 通知引擎动态挂卸。缝②既有
// 风格：真实 store + 真实用例，只断言外部行为。

import (
	"context"
	"testing"

	"packgradle/internal/application/view"
	"packgradle/internal/application/watch"
)

// stubWatchStatus 是监听引擎状态投影假实现。
type stubWatchStatus map[string]string

func (s stubWatchStatus) WatchStatus(relationID string) string { return s[relationID] }

// TestWorkspaceWatchStatusProjection watch_status 投影（契约 07 §3.2，票 #92）：
// 无监听面 → 空值（未挂载语义）；AttachWatch 后随引擎状态透传（空/active/
// paused/unavailable 四值）。
func TestWorkspaceWatchStatusProjection(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	// 无监听面：投影空值
	if w := mustWorkspace(t, app, rel.RelationID); w.State.WatchStatus != "" {
		t.Fatalf("无监听面 watch_status = %q, 期望空", w.State.WatchStatus)
	}

	stub := stubWatchStatus{}
	app.AttachWatch(stub, nil)

	// 未挂载关系 → 空值
	if w := mustWorkspace(t, app, rel.RelationID); w.State.WatchStatus != "" {
		t.Fatalf("未挂载关系 watch_status = %q, 期望空", w.State.WatchStatus)
	}

	// active / paused / unavailable 三态透传（表驱动；状态按 relation id 查询）
	for _, tc := range []string{watch.StatusActive, watch.StatusPaused, watch.StatusUnavailable} {
		stub[rel.RelationID] = tc
		if w := mustWorkspace(t, app, rel.RelationID); w.State.WatchStatus != tc {
			t.Fatalf("watch_status = %q, 期望 %q", w.State.WatchStatus, tc)
		}
	}
}

// TestPolicyChangeKicksWatch 监听面是 policy 的函数（ADR-0010 §3）：policy 修改
// 尾点 kick 监听引擎重挂（kick 缝联动断言；引擎重挂本体归 watch 包测试）。
func TestPolicyChangeKicksWatch(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, _ := newStack(t, dataRoot)
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	kicks := make(chan string, 8)
	app.AttachWatch(stubWatchStatus{rel.RelationID: watch.StatusActive}, func() { kicks <- rel.RelationID })

	pol, err := app.GetMappingPolicy(context.Background(), rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.UpdateMappingPolicy(context.Background(), view.UpdateMappingPolicyInput{
		RelationID:       rel.RelationID,
		ExpectedRevision: rel.Revision,
		Rules:            pol.Rules,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-kicks:
		if got != rel.RelationID {
			t.Fatalf("kick relation = %q, 期望 %q", got, rel.RelationID)
		}
	default:
		t.Fatal("policy 修改后未通知监听引擎重挂")
	}
}
