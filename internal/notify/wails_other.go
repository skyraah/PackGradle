//go:build !windows

package notify

// 非 Windows 平台适配层（票 #97，契约 07 §3.5 平台面）：v1 仅 Windows 弹
// toast，其他平台判定恒不弹——不装配发送面，gate 保持未 Attach（全部入口
// no-op）；横幅/角标照常（前端不受影响）。

// AttachWails 非 Windows 为 no-op（签名与 wails_windows.go 对称）。
func AttachWails(g *Gate, copy Copy, workspaceName func(relationID string) string) {}
