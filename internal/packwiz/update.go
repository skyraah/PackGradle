package packwiz

import (
	"regexp"
	"strings"
)

// 解析 `packwiz update --all` 输出（对应 packwiz 源码 cmd/update.go 的打印格式）。
// 注意：检查顺序为 固定跳过 → 无更新源 → 检查失败 → 有更新，避免错误信息误匹配更新行。
// 事件标签返回错误码（err.update.*），由前端翻译；检查失败时透传 packwiz 原文。
var (
	pinnedSkipRe  = regexp.MustCompile(`^Update skipped for pinned mod (.+)$`)
	noUpdaterRe   = regexp.MustCompile(`^A supported update system for "(.+)" cannot be found\.$`)
	failedCheckRe = regexp.MustCompile(`^Failed to check updates for (.+?): (.+)$`)
	updateLineRe  = regexp.MustCompile(`^(.+): (.+) -> (.+)$`) // <Name>: <旧文件> -> <新文件>
)

// ParseUpdateOutput 解析 `packwiz update --all` 的文本输出，提取有更新的 mod 与失败/跳过的 mod
func ParseUpdateOutput(output string) (updates, errors []ModUpdateInfo) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := pinnedSkipRe.FindStringSubmatch(line); m != nil {
			errors = append(errors, ModUpdateInfo{Name: m[1], Error: "err.update.pinned"})
			continue
		}
		if m := noUpdaterRe.FindStringSubmatch(line); m != nil {
			errors = append(errors, ModUpdateInfo{Name: m[1], Error: "err.update.no_updater"})
			continue
		}
		if m := failedCheckRe.FindStringSubmatch(line); m != nil {
			errors = append(errors, ModUpdateInfo{Name: m[1], Error: m[2]})
			continue
		}
		if m := updateLineRe.FindStringSubmatch(line); m != nil {
			updates = append(updates, ModUpdateInfo{
				Name:        m[1],
				HasUpdate:   true,
				CurrentFile: m[2],
				LatestFile:  m[3],
			})
		}
	}
	return
}
