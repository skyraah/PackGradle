package notify

import (
	"errors"
	"testing"
)

// ti 是 TriggerInput 的短构造（保持表行单行可读）。
func ti(platform, autoDocked, foreground bool, prev, cur string) TriggerInput {
	return TriggerInput{
		Platform: platform, AutoDocked: autoDocked, Foreground: foreground,
		PreviousPlan: prev, CurrentPlan: cur,
	}
}

// TestShouldToastFullGrid 三条件+平台全格表驱动（2×2×2×4 计划过渡类 = 32 格）。
// 计划过渡四类：无→有（""→P）、换新（P0→P）、同一张重复（P0→P0）、本次无计划
//（P0→""，防御类）。契约 07 §3.5：平台 ∧ 自动链停靠 ∧ 窗口不在前台 ∧ 计划
// 更新才弹；重复停靠不重弹；Foreground=true 的行同时锁定「窗口态取不到→保守
// 视为在前台→宁可不弹不可误弹」取向。
func TestShouldToastFullGrid(t *testing.T) {
	const (
		P  = "plan-01HNEW"  // 本次停靠计划（新）
		P0 = "plan-01HOLD"  // 上次已判定计划（旧）
	)
	tests := []struct {
		name string
		in   TriggerInput
		want bool
	}{
		// ---- Platform=false（非 Windows）：16 格恒不弹（横幅/角标照常）----
		{"非Windows+未停靠+前台+无→有", ti(false, false, true, "", P), false},
		{"非Windows+未停靠+前台+换新", ti(false, false, true, P0, P), false},
		{"非Windows+未停靠+前台+重复", ti(false, false, true, P0, P0), false},
		{"非Windows+未停靠+前台+本次无", ti(false, false, true, P0, ""), false},
		{"非Windows+未停靠+后台+无→有", ti(false, false, false, "", P), false},
		{"非Windows+未停靠+后台+换新", ti(false, false, false, P0, P), false},
		{"非Windows+未停靠+后台+重复", ti(false, false, false, P0, P0), false},
		{"非Windows+未停靠+后台+本次无", ti(false, false, false, P0, ""), false},
		{"非Windows+停靠+前台+无→有", ti(false, true, true, "", P), false},
		{"非Windows+停靠+前台+换新", ti(false, true, true, P0, P), false},
		{"非Windows+停靠+前台+重复", ti(false, true, true, P0, P0), false},
		{"非Windows+停靠+前台+本次无", ti(false, true, true, P0, ""), false},
		{"非Windows+停靠+后台+无→有", ti(false, true, false, "", P), false},
		{"非Windows+停靠+后台+换新", ti(false, true, false, P0, P), false},
		{"非Windows+停靠+后台+重复", ti(false, true, false, P0, P0), false},
		{"非Windows+停靠+后台+本次无", ti(false, true, false, P0, ""), false},

		// ---- Platform=true + AutoDocked=false：8 格恒不弹（手动入口停靠不弹
		//——人就在界面；事件源层隔离之外的第二道判定）----
		{"Windows+未停靠+前台+无→有", ti(true, false, true, "", P), false},
		{"Windows+未停靠+前台+换新", ti(true, false, true, P0, P), false},
		{"Windows+未停靠+前台+重复", ti(true, false, true, P0, P0), false},
		{"Windows+未停靠+前台+本次无", ti(true, false, true, P0, ""), false},
		{"Windows+未停靠+后台+无→有", ti(true, false, false, "", P), false},
		{"Windows+未停靠+后台+换新", ti(true, false, false, P0, P), false},
		{"Windows+未停靠+后台+重复", ti(true, false, false, P0, P0), false},
		{"Windows+未停靠+后台+本次无", ti(true, false, false, P0, ""), false},

		// ---- Platform=true + AutoDocked=true + Foreground=true：4 格恒不弹
		//（正开着看时界面角标已可见；窗口态取不到时保守视为在前台同此列——
		//宁可不弹不可误弹）----
		{"Windows+停靠+前台+无→有", ti(true, true, true, "", P), false},
		{"Windows+停靠+前台+换新", ti(true, true, true, P0, P), false},
		{"Windows+停靠+前台+重复", ti(true, true, true, P0, P0), false},
		{"Windows+停靠+前台+本次无", ti(true, true, true, P0, ""), false},

		// ---- Platform=true + AutoDocked=true + Foreground=false：4 格按计划
		//过渡判定——无→有、换新弹；同一张重复、本次无计划不弹----
		{"Windows+停靠+后台+无→有", ti(true, true, false, "", P), true},
		{"Windows+停靠+后台+换新", ti(true, true, false, P0, P), true},
		{"Windows+停靠+后台+重复（同计划不重弹）", ti(true, true, false, P0, P0), false},
		{"Windows+停靠+后台+本次无", ti(true, true, false, P0, ""), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldToast(tc.in); got != tc.want {
				t.Fatalf("ShouldToast(%+v) = %v, 期望 %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestDeliverFullGrid 降级判定全格（2 平台 × 3 发送结果 = 6 格）：toast 不可用
//（非 Windows）或被系统拒绝（通知关闭/勿扰，错误注入）→ 一律静默退应用内角标；
// 仅平台支持且发送无错才计 toast。
func TestDeliverFullGrid(t *testing.T) {
	errDenied := errors.New("toast 被系统拒绝（通知关闭/勿扰）")
	errOther := errors.New("toast 推送失败（COM 不可用）")
	tests := []struct {
		name     string
		platform bool
		sendErr  error
		want     Outcome
	}{
		{"非Windows+发送成功", false, nil, OutcomeBadgeSilent},
		{"非Windows+被拒", false, errDenied, OutcomeBadgeSilent},
		{"非Windows+推送失败", false, errOther, OutcomeBadgeSilent},
		{"Windows+发送成功", true, nil, OutcomeToast},
		{"Windows+被拒（静默降级强制项）", true, errDenied, OutcomeBadgeSilent},
		{"Windows+推送失败", true, errOther, OutcomeBadgeSilent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Deliver(tc.platform, tc.sendErr); got != tc.want {
				t.Fatalf("Deliver(%v, %v) = %v, 期望 %v", tc.platform, tc.sendErr, got, tc.want)
			}
		})
	}
}
