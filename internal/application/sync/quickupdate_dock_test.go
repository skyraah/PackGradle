package sync

// 停靠判定纯函数的表驱动全格测试（票 #86；判定函数按测试决议做全格覆盖，
// 内部测试包直呼 quickUpdateDock，restore_matrix_test.go 同包先例）。

import "testing"

func TestQuickUpdateDockTruthTable(t *testing.T) {
	cases := []struct {
		name                 string
		draftHasConflicts    bool
		requirementsNonEmpty bool
		authorized           bool
		want                 string
	}{
		{"要求空_授权开启_免确认直达", false, false, true, QuickUpdateApplyStarted},
		{"draft含冲突_无决议输入_恒停靠", true, false, true, QuickUpdateAwaitingConfirmation},
		{"draft含冲突_要求也非空_停靠", true, true, true, QuickUpdateAwaitingConfirmation},
		{"draft含冲突_授权关闭_停靠", true, false, false, QuickUpdateAwaitingConfirmation},
		{"draft含冲突_要求非空_授权关闭_停靠", true, true, false, QuickUpdateAwaitingConfirmation},
		{"要求非空_授权开启仍停靠_删除损失面人工", false, true, true, QuickUpdateAwaitingConfirmation},
		{"要求非空_授权关闭_停靠", false, true, false, QuickUpdateAwaitingConfirmation},
		{"要求空_授权关闭_停靠_计划停留既有流", false, false, false, QuickUpdateAwaitingConfirmation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quickUpdateDock(tc.draftHasConflicts, tc.requirementsNonEmpty, tc.authorized); got != tc.want {
				t.Fatalf("quickUpdateDock(%v,%v,%v) = %q, 期望 %q",
					tc.draftHasConflicts, tc.requirementsNonEmpty, tc.authorized, got, tc.want)
			}
		})
	}
}
