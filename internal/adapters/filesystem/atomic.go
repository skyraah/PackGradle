package filesystem

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var errIsDir = errors.New("目标是目录而非文件")

// WriteFileAtomic 以「临时文件 + fsync + 原子 rename」写入 dest；
// 任何失败都清理临时文件，不留半成品。
func WriteFileAtomic(dest string, r io.Reader) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomic write: 创建父目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pgtmp-*")
	if err != nil {
		return fmt.Errorf("atomic write: 创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := io.Copy(tmp, r); err != nil {
		cleanup()
		return fmt.Errorf("atomic write: 写入内容: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("atomic write: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("atomic write: 关闭临时文件: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("atomic write: 原子替换: %w", err)
	}
	return nil
}
