package notify

// 通知判定纯逻辑（契约 07 §3.5；P4 验收规格 §4.1——「系统通知三条件判定」
// 与「降级」都以纯函数边界做表驱动全格覆盖：它们是 ADR 定义的确定函数，属
// 外部契约的一部分）。

// TriggerInput 是一次自动链停靠通知判定的输入（注入单测——验收核心缝）。
type TriggerInput struct {
	// Platform：v1 仅 Windows 弹 toast；其他平台判定恒不弹（横幅/角标照常）。
	Platform bool
	// AutoDocked：自动链停于 awaiting_confirmation（触发条件①；手动入口停靠
	// 不弹——人就在界面，事件源层已隔离）。
	AutoDocked bool
	// Foreground：应用窗口在前台（触发条件②的肯定面）。窗口前台态取不到时
	// 保守视为在前台——宁可不弹不可误弹（该取向由表驱动单测锁定）。
	Foreground bool
	// PreviousPlan 是上次停靠已判定的 pending_plan_id 去重基线（空=无）。
	PreviousPlan string
	// CurrentPlan 是本次停靠的 pending_plan_id（空=无计划，不弹）。
	CurrentPlan string
}

// ShouldToast 判定是否弹系统 toast：三条件同时满足才弹（契约 07 §3.5）——
// ① 自动链停靠 ∧ ② 窗口不在前台 ∧ ③ pending_plan_id 发生更新（无→有或换新；
// 同一张计划重复停靠不重弹）；外加平台面 v1 仅 Windows。
func ShouldToast(in TriggerInput) bool {
	return in.Platform && in.AutoDocked && !in.Foreground &&
		in.CurrentPlan != "" && in.CurrentPlan != in.PreviousPlan
}

// Outcome 是一次 toast 处置结果。
type Outcome int

const (
	// OutcomeToast：toast 已发送（Win11 通知中心弹出；应用内角标照常在）。
	OutcomeToast Outcome = iota
	// OutcomeBadgeSilent：静默退回应用内角标——toast 不可用（非 Windows、
	// 无发送面）或被系统拒绝（通知关闭/勿扰）；不报错不重试。
	OutcomeBadgeSilent
)

// Deliver 降级判定（纯逻辑）：平台不支持恒不弹；发送返回任何错误一律静默
// 降级。「不重试」由调用方保证（Gate 对同一停靠事件只发送一次）。
func Deliver(platform bool, sendErr error) Outcome {
	if !platform || sendErr != nil {
		return OutcomeBadgeSilent
	}
	return OutcomeToast
}
