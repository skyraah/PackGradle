//go:build windows

package singleinstance

import (
	"os"

	"golang.org/x/sys/windows"
)

var mutex windows.Handle // 进程生命周期内持有，进程退出时系统自动释放

// Acquire 尝试获取命名互斥体（Local\ 会话级命名空间，同用户会话内互斥）
func Acquire(name string) bool {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return true // 参数异常不阻塞启动
	}
	h, err := windows.CreateMutex(nil, false, ptr)
	if err == windows.ERROR_ALREADY_EXISTS {
		return false // 已有实例持有该互斥体
	}
	if err != nil {
		return true // 其他错误不阻塞启动
	}
	mutex = h
	return true
}

// NotifyAlreadyRunning 弹窗提示已有实例运行后退出
func NotifyAlreadyRunning() {
	title, _ := windows.UTF16PtrFromString("PackGradle")
	text, _ := windows.UTF16PtrFromString(
		"PackGradle 已在运行。\n\n多个实例同时运行会互相覆盖配置数据（如删除项目后配置复活）。\n请关闭现有实例后重试。")
	_, _ = windows.MessageBox(0, text, title, 0) // MB_OK
	os.Exit(1)
}
