package packwiz

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"packgradle/internal/errs"
)

// packwiz CLI 超时：refresh 通常在本地工作，update/check 需要访问网络，
// 分别给 5 分钟与 15 分钟上限，避免网络挂起时前端永久 loading。
const (
	refreshTimeout = 5 * time.Minute
	updateTimeout  = 15 * time.Minute
)

// hiddenProcAttr 确保 GUI 程序下不弹出控制台窗口：
// CREATE_NO_WINDOW 让 Windows 完全不创建控制台（仅隐藏不够，窗口仍会短暂出现）
func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,       // 隐藏控制台窗口
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW：完全不创建控制台
	}
}

// newHiddenCmd 创建带超时的 packwiz 子进程命令。
// 超时触发时返回 (部分输出, context.DeadlineExceeded)，由调用方转为错误码文本。
func runHiddenCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = hiddenProcAttr()
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(string(out)), context.DeadlineExceeded
	}
	return strings.TrimSpace(string(out)), err
}

// timeoutText 返回超时的结构化错误码文本（前端按语言文件渲染）
func timeoutText() string {
	return errs.New("err.packwiz.timeout").Error()
}

// RunRefresh 在项目目录执行 `packwiz refresh` 并返回输出
func RunRefresh(packwizPath, projectDir string) RefreshResult {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	out, err := runHiddenCmd(ctx, projectDir, packwizPath, "refresh")
	if err == context.DeadlineExceeded {
		return RefreshResult{OK: false, Output: timeoutText()}
	}
	return RefreshResult{OK: err == nil, Output: out}
}

// RunCheckUpdates 通过 packwiz 官方 update 命令检查项目更新（不实际应用）：
// 运行 `packwiz update --all` 并向确认提示喂入 "n"，使其打印更新列表后取消
func RunCheckUpdates(packwizPath, projectDir string) (UpdateCheckResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, packwizPath, "update", "--all")
	cmd.SysProcAttr = hiddenProcAttr()
	cmd.Dir = projectDir
	cmd.Stdin = strings.NewReader("n\n") // 确认输入为 n：只打印更新列表，不应用
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return UpdateCheckResult{OK: false, Output: timeoutText()}, nil
	}
	updates, errors := ParseUpdateOutput(output)
	return UpdateCheckResult{OK: err == nil, Output: output, Updates: updates, Errors: errors}, nil
}

// RunUpdateMods 应用更新：modName 非空时更新单个（packwiz update <name>，无确认直接应用），
// 为空时更新全部（packwiz update --all -y）。
// name 为 .pw.toml 文件名（即 mod id）
func RunUpdateMods(packwizPath, projectDir, modName string) RefreshResult {
	args := []string{"update"}
	if modName != "" {
		args = append(args, modName)
	} else {
		args = append(args, "--all", "-y")
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	out, err := runHiddenCmd(ctx, projectDir, packwizPath, args...)
	if err == context.DeadlineExceeded {
		return RefreshResult{OK: false, Output: timeoutText()}
	}
	return RefreshResult{OK: err == nil, Output: out}
}
