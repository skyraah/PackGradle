package packwiz

import (
	"os/exec"
	"strings"
	"syscall"
)

// newHiddenCmd 创建 packwiz 子进程命令，并确保 GUI 程序下不弹出控制台窗口：
// CREATE_NO_WINDOW 让 Windows 完全不创建控制台（仅隐藏不够，窗口仍会短暂出现）
func newHiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,       // 隐藏控制台窗口
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW：完全不创建控制台
	}
	return cmd
}

// RunRefresh 在项目目录执行 `packwiz refresh` 并返回输出
func RunRefresh(packwizPath, projectDir string) RefreshResult {
	cmd := newHiddenCmd(packwizPath, "refresh")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	return RefreshResult{OK: err == nil, Output: strings.TrimSpace(string(out))}
}

// RunCheckUpdates 通过 packwiz 官方 update 命令检查项目更新（不实际应用）：
// 运行 `packwiz update --all` 并向确认提示喂入 "n"，使其打印更新列表后取消
func RunCheckUpdates(packwizPath, projectDir string) (UpdateCheckResult, error) {
	cmd := newHiddenCmd(packwizPath, "update", "--all")
	cmd.Dir = projectDir
	cmd.Stdin = strings.NewReader("n\n") // 确认输入为 n：只打印更新列表，不应用
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
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
	cmd := newHiddenCmd(packwizPath, args...)
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	return RefreshResult{OK: err == nil, Output: strings.TrimSpace(string(out))}
}
