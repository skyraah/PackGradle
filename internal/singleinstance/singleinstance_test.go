//go:build windows

package singleinstance

import (
	"fmt"
	"os"
	"testing"
)

// 互斥体双获取：第一次成功，第二次失败（模拟第二个实例）
func TestAcquireTwice(t *testing.T) {
	name := fmt.Sprintf(`Local\PackGradle_Test_%d`, os.Getpid())
	if !Acquire(name) {
		t.Fatal("第一次获取互斥体应成功")
	}
	if Acquire(name) {
		t.Error("第二次获取应失败（互斥体已被持有）")
	}
}
