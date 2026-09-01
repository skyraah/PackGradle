package model

import "testing"

// 大文件保全阈值判定单测（ADR-0007 §7，票 #64）：判定口径的唯一权威函数，
// sync 计划构建（core/plan）与 restore 计划构建（票 #60）共用。
func TestShouldSkipPreserve(t *testing.T) {
	const miB = int64(1) << 20
	tests := []struct {
		name       string
		kind       ResourceKind
		size       int64
		threshold  int64
		want       bool
	}{
		{"非 mod 超阈值", ResourceTextFile, 33 * miB, 32 * miB, true},
		{"非 mod 恰等阈值不跳", ResourceBinaryFile, 32 * miB, 32 * miB, false},
		{"非 mod 低于阈值", ResourceTextFile, 1 * miB, 32 * miB, false},
		{"mod 恒不跳（走重取通道）", ResourceMod, 999 * miB, 32 * miB, false},
		{"显式 0=不限", ResourceTextFile, 999 * miB, PreserveMaxUnlimited, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldSkipPreserve(tc.kind, tc.size, tc.threshold); got != tc.want {
				t.Fatalf("ShouldSkipPreserve(%v, %d, %d) = %v，期望 %v",
					tc.kind, tc.size, tc.threshold, got, tc.want)
			}
		})
	}
}
