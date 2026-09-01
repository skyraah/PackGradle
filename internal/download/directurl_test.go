package download

import (
	"fmt"
	"strings"
	"testing"
)

// 直链构造黄金向量（ADR-0008 §2；出处=研究笔记 §1.2，全部实测钉死）：
// 整数除法、不补零——8778011 → `8778/11` 实测 206，`8778/011` 补零实测 403。
func TestDirectURLGolden(t *testing.T) {
	cases := []struct {
		name     string
		fileID   int64
		filename string
		want     string
	}{
		{
			name:     "7位常规例（实测206向量）",
			fileID:   7270446,
			filename: "jei-1.20.1-forge-15.20.0.127.jar",
			want:     cfFileBase + "/7270/446/jei-1.20.1-forge-15.20.0.127.jar",
		},
		{
			name:     "7位余数<100例（实测206向量，不补零）",
			fileID:   8778011,
			filename: "jei-1.20.1-forge-15.56.0.205.jar",
			want:     cfFileBase + "/8778/11/jei-1.20.1-forge-15.56.0.205.jar",
		},
		{
			name:     "野外6位例（实测206向量）",
			fileID:   2252518,
			filename: "AIES_Aerospace161.zip",
			want:     cfFileBase + "/2252/518/AIES_Aerospace161.zip",
		},
		{
			name:     "余数两位99不补零",
			fileID:   1234099,
			filename: "a.jar",
			want:     cfFileBase + "/1234/99/a.jar",
		},
		{
			name:     "小编号<1000归零段",
			fileID:   999,
			filename: "b.jar",
			want:     cfFileBase + "/0/999/b.jar",
		},
		{
			name:     "极小编号",
			fileID:   7,
			filename: "c.jar",
			want:     cfFileBase + "/0/7/c.jar",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DirectURL(tc.fileID, tc.filename)
			if got != tc.want {
				t.Fatalf("DirectURL(%d, %q) = %s, 期望 %s", tc.fileID, tc.filename, got, tc.want)
			}
		})
	}

	// 补零对照钉死：任何向量都不得出现前导零分段（`8778/011` 实测 403）
	got := DirectURL(8778011, "x.jar")
	if strings.Contains(got, "/011/") {
		t.Fatalf("补零分段出现（实测 403 的错误口径）: %s", got)
	}
}

// fileID ≥ 10^7（8 位）：记日志、不换口径（仍按整数除法构造，ADR-0008 §2）
func TestDirectURLLegacyFileIDLogs(t *testing.T) {
	old := URLLog
	defer func() { URLLog = old }()

	var logs []string
	URLLog = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	got := DirectURL(12345678, "big.jar")
	want := cfFileBase + "/12345/678/big.jar"
	if got != want {
		t.Fatalf("8 位 fileID 应不换口径（整数除法），got %s, 期望 %s", got, want)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "12345678") {
		t.Fatalf("8 位 fileID 应记越界日志且含 fileID，实际 %v", logs)
	}

	// 7 位不记越界日志
	logs = nil
	DirectURL(8778011, "x.jar")
	if len(logs) != 0 {
		t.Fatalf("7 位 fileID 不应记越界日志，实际 %v", logs)
	}
}
