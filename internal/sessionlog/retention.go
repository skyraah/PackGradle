// 会话日志双轴保留（ADR-0011 §1）：份数窗口（保最近 KeepSessions 个会话）、
// 明文/压缩分层（最近 KeepPlaintext 份明文 .log、更早原地压缩 .log.gz）、
// 总量硬顶（MaxTotalBytes，优先于份数——超顶从最旧会话目录删起，允许低于
// 份数窗口）。清理时机 = 启动时（Open 内调用），无定时器、无用户设置面。
package sessionlog

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// sessionDirRe 匹配会话目录名（启动时间戳，或同秒冲突的 -N 序号后缀）。
// 只清理匹配目录——logs/ 下将来若落他类产物不会被误删。
var sessionDirRe = regexp.MustCompile(`^\d{8}-\d{6}(-\d+)?$`)

// sweep 对 logsDir 执行启动时保留清理，返回聚合错误（个别条目失败不中断
// 整体：Windows 下偶发文件占用，删不掉的留给下次启动）。顺序与理由：
//
//  1. 份数窗口：超出 KeepSessions 的最旧会话整目录删除；
//  2. 明文/压缩分层：最近 KeepPlaintext 份保持明文，更早的 session.log
//     原地压缩为 session.log.gz（压缩本身服务磁盘保护，先省后删）；
//  3. 总量硬顶：压缩后实态仍超 MaxTotalBytes 时从最旧会话删起直至限内，
//     允许低于 KeepSessions（硬顶优先于份数，ADR-0011 §1 双事故教训）。
//
// keepDir 是本次启动的会话目录，任何一步都永不自删。
func sweep(logsDir string, policy Policy, keepDir string) error {
	sessions, err := listSessions(logsDir)
	if err != nil {
		return fmt.Errorf("sessionlog: 列会话目录: %w", err)
	}
	var errs []error

	// ① 份数窗口：从最旧删起；最旧即 keepDir 时保序停手。
	for len(sessions) > policy.KeepSessions {
		if sessions[0] == keepDir {
			break
		}
		if err := os.RemoveAll(sessions[0]); err != nil {
			errs = append(errs, fmt.Errorf("sessionlog: 删超窗会话 %s: %w", sessions[0], err))
			break // 最旧删不掉则停手，避免对同一条目反复失败
		}
		sessions = sessions[1:]
	}

	// ② 明文/压缩分层：非最近 KeepPlaintext 份的明文原地压缩。
	plaintextFrom := len(sessions) - policy.KeepPlaintext
	if plaintextFrom < 0 {
		plaintextFrom = 0
	}
	for i := 0; i < plaintextFrom; i++ {
		if err := compressSessionLog(sessions[i]); err != nil {
			errs = append(errs, fmt.Errorf("sessionlog: 压缩会话 %s: %w", sessions[i], err))
		}
	}

	// ③ 总量硬顶优先于份数：从最旧会话目录删起直至回到限内；
	// 仅剩 keepDir（当次会话）时停手——打开中的日志文件不可自删。
	total := totalSize(sessions)
	for total > policy.MaxTotalBytes && len(sessions) > 0 && sessions[0] != keepDir {
		size := dirSize(sessions[0])
		if err := os.RemoveAll(sessions[0]); err != nil {
			errs = append(errs, fmt.Errorf("sessionlog: 删超顶会话 %s: %w", sessions[0], err))
			break
		}
		total -= size
		sessions = sessions[1:]
	}
	return errors.Join(errs...)
}

// Stats 汇总 logsDir 的会话日志账面（票 #98 验收链观测面）：会话目录数与
// 总字节。口径与 sweep 的份数窗口/总量硬顶计量一致（仅匹配会话目录名的
// 目录；目录整体递归字节）。
func Stats(logsDir string) (count int, totalBytes int64, err error) {
	sessions, err := listSessions(logsDir)
	if err != nil {
		return 0, 0, fmt.Errorf("sessionlog: 列会话目录: %w", err)
	}
	return len(sessions), totalSize(sessions), nil
}

// listSessions 列出 logsDir 下的会话目录（按名升序 = 时间升序）。
func listSessions(logsDir string) ([]string, error) {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && sessionDirRe.MatchString(e.Name()) {
			out = append(out, filepath.Join(logsDir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// compressSessionLog 把会话目录内的明文 session.log 原地压缩为
// session.log.gz（先写 .tmp 再改名，压缩中断不冒充完成态；无明文时幂等
// 无操作）。Windows 的 Rename 不覆盖已存在目标，先移走旧 .gz。
func compressSessionLog(dir string) error {
	src := filepath.Join(dir, sessionFile)
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // 已压缩（或本无明文）：幂等
		}
		return err
	}
	dst := filepath.Join(dir, sessionGzFile)
	tmp := dst + ".tmp"
	if err := writeGzip(tmp, src); err != nil {
		os.Remove(tmp)
		return err
	}
	// 压缩中断可能遗留旧 .gz（与 .log 并存）：Windows Rename 不覆盖目标，
	// 先移走旧产物，以本次重压产物为准。
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

// writeGzip 把 src 压缩为 gzip 写入 dst（默认压缩级别；stderr 现状与验收
// 面零关系，纯落盘产物）。
func writeGzip(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	gw := gzip.NewWriter(out)
	if _, err := io.Copy(gw, in); err != nil {
		gw.Close()
		out.Close()
		return err
	}
	if err := gw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// totalSize 汇总全部会话目录的磁盘占用（硬顶的计量口径：会话目录整体）。
func totalSize(sessions []string) int64 {
	var total int64
	for _, dir := range sessions {
		total += dirSize(dir)
	}
	return total
}

// dirSize 递归汇总单目录字节数；个别条目读不到按 0 计（清理尽力而为）。
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
