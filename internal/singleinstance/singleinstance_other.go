//go:build !windows

package singleinstance

import "os"

// 非 Windows 平台：不限制多实例（项目主目标为 Windows）
func Acquire(name string) bool {
	return true
}

func NotifyAlreadyRunning() {
	os.Exit(1)
}
