package store

// paths.go 定义用户数据目录布局（架构文档 §5.1）：
// 本机权威状态（packgradle.db + objects/staging/logs/exports）只落在用户数据目录，
// 永不写入 Project 工作树（ADR-005/ADR-009）。

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultRoot 返回默认用户数据根目录：
//   - Windows: %APPDATA%/PackGradle
//   - Linux:   $XDG_DATA_HOME/PackGradle（未设置回退 ~/.local/share/PackGradle）
//   - macOS:   ~/Library/Application Support/PackGradle
func DefaultRoot() (string, error) {
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("store: %APPDATA% 未设置，无法定位用户数据目录")
		}
		return filepath.Join(appData, "PackGradle"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "PackGradle"), nil
	default:
		// Linux 及其余类 Unix：遵循 XDG 规范。
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "PackGradle"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", "PackGradle"), nil
	}
}

// Layout 描述根目录下的子目录布局。
type Layout struct {
	Root       string // 用户数据根目录
	DBPath     string // <root>/packgradle.db，唯一元数据权威
	ObjectsDir string // <root>/objects，全局 CAS
	StagingDir string // <root>/staging，文件系统事务日志与临时内容
	LogsDir    string // <root>/logs，结构化本地日志
	ExportsDir string // <root>/exports，用户显式导出的诊断或策略文件
}

// EnsureLayout 创建 root 及全部子目录（幂等，可重复调用），DBPath = <root>/packgradle.db。
// 只创建目录，不创建数据库文件本身（数据库由 sqlite.Open 创建）。
func EnsureLayout(root string) (Layout, error) {
	if root == "" {
		return Layout{}, errors.New("store: root 不能为空")
	}
	layout := Layout{
		Root:       root,
		DBPath:     filepath.Join(root, "packgradle.db"),
		ObjectsDir: filepath.Join(root, "objects"),
		StagingDir: filepath.Join(root, "staging"),
		LogsDir:    filepath.Join(root, "logs"),
		ExportsDir: filepath.Join(root, "exports"),
	}
	for _, dir := range []string{layout.Root, layout.ObjectsDir, layout.StagingDir, layout.LogsDir, layout.ExportsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Layout{}, err
		}
	}
	return layout, nil
}
