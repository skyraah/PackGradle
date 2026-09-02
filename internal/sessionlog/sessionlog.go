// Package sessionlog 落实 ADR-0011 §1 的会话日志形态：每次应用启动在
// <LogsDir>/<启动时间戳>/session.log 落一份 slog 结构化 JSON 会话日志
// （启用既有预留 LogsDir；Windows GUI 子系统下 stderr 无处落地，桌面形态
// 运行期日志此前事实丢失），并在启动时执行双轴保留清理（retention.go）——
// 保最近 20 个会话（最近 3 份明文 .log、更早原地压缩 .log.gz）× 100MB
// 总量硬顶优先于份数（超顶从最旧会话目录删起，允许低于 20 份）。
//
// 本包只由 GUI 进程装配（main.go 调 Open 后 slog.SetDefault）；headless
// 三工具（pgheadless/pgfixture/pgrecovery）保持 stderr 现状不接（验收断言
// 依赖其输出形态）。保留参数为编译期常量（数值施工可调），无用户设置面、
// 无日志查看 UI。
package sessionlog

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// 保留策略编译期常量（ADR-0011 §1，数值施工可调，数量级不变）。
const (
	// KeepSessions 常态保留的会话总数（份数轴）。
	KeepSessions = 20
	// KeepPlaintext 最近 N 份会话保持明文 .log，更早原地压缩为 .log.gz。
	KeepPlaintext = 3
	// MaxTotalBytes 全部会话目录占用总量硬顶（字节）。硬顶优先于份数：
	// 超顶从最旧会话删起直至回到限内，允许低于 KeepSessions。
	MaxTotalBytes = 100 << 20 // 100MB
)

// sessionDirLayout 是启动时间戳的目录名格式：字典序即时间序，保序清理
// （删最旧）直接按名排序。
const sessionDirLayout = "20060102-150405"

// 会话日志文件名（明文 / 原地压缩产物）。
const (
	sessionFile   = "session.log"
	sessionGzFile = "session.log.gz"
)

// Policy 是保留策略参数（测试缝：单测用小数值全格三轴；生产用零值 Options
// 归一到 DefaultPolicy，无用户设置面）。
type Policy struct {
	// KeepSessions 保留会话总数；<=0 时取 KeepSessions。
	KeepSessions int
	// KeepPlaintext 保持明文的最近会话份数；<=0 时取 KeepPlaintext。
	KeepPlaintext int
	// MaxTotalBytes 总量硬顶（字节）；<=0 时取 MaxTotalBytes。
	MaxTotalBytes int64
}

// DefaultPolicy 返回编译期常量策略（ADR-0011 §1）。
func DefaultPolicy() Policy {
	return Policy{
		KeepSessions:  KeepSessions,
		KeepPlaintext: KeepPlaintext,
		MaxTotalBytes: MaxTotalBytes,
	}
}

// Options 是 Open 的可注入参数（零值 = 生产默认）。
type Options struct {
	// Now 返回启动时间（假时钟测试缝）；nil = time.Now。
	Now func() time.Time
	// Policy 保留策略；零值字段逐项归一到编译期常量。
	Policy Policy
}

// Session 是一次启动的会话日志：会话目录、session.log 与其 JSON logger。
type Session struct {
	// Logger 写本会话 session.log 的 slog 实例（JSON handler，Level=Info）。
	Logger *slog.Logger
	// Dir 是本次会话目录（<logsDir>/<启动时间戳>）。
	Dir string
	// Path 是 session.log 绝对路径。
	Path string

	file *os.File
}

// Open 启动本次会话：创建 <logsDir>/<启动时间戳>/session.log（同秒内重复
// 启动追加 -2/-3 序号保证目录唯一），随后执行启动时保留清理（本会话计入
// 份数、目录永不自删）。返回写 JSON 的 Session；清理个别条目失败不阻断
// 启动（记入本会话日志）。创建会话文件本身失败才返回错误，调用方退回
// 默认 slog 出口、不得因此中断应用启动。
func Open(logsDir string, opts Options) (*Session, error) {
	if logsDir == "" {
		return nil, errors.New("sessionlog: logsDir 不能为空")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	policy := normalizePolicy(opts.Policy)

	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return nil, fmt.Errorf("sessionlog: 创建日志根目录 %s: %w", logsDir, err)
	}
	dir, err := createSessionDir(logsDir, now())
	if err != nil {
		return nil, fmt.Errorf("sessionlog: 创建会话目录: %w", err)
	}
	s := &Session{Dir: dir, Path: filepath.Join(dir, sessionFile)}
	s.file, err = os.OpenFile(s.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("sessionlog: 打开会话日志 %s: %w", s.Path, err)
	}
	// 结构化 JSON 落盘（ADR-0011 §1）；直接写文件不缓冲，进程崩溃不丢已记条目。
	s.Logger = slog.New(slog.NewJSONHandler(s.file, nil))

	// 启动时清理（双轴保留，本会话计入）：失败聚合计入会话日志，不阻断启动。
	if err := sweep(logsDir, policy, dir); err != nil {
		s.Logger.Warn("sessionlog: 启动保留清理部分失败", "err", err)
	}
	return s, nil
}

// Close 关闭会话日志文件（main.go 在应用退出时调用；fatal 路径的 os.Exit
// 不经过 defer，但写入无用户态缓冲，已记条目不丢）。
func (s *Session) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// normalizePolicy 把零值字段逐项归一到编译期常量（允许测试按轴覆写单值）。
func normalizePolicy(p Policy) Policy {
	if p.KeepSessions <= 0 {
		p.KeepSessions = KeepSessions
	}
	if p.KeepPlaintext <= 0 {
		p.KeepPlaintext = KeepPlaintext
	}
	if p.MaxTotalBytes <= 0 {
		p.MaxTotalBytes = MaxTotalBytes
	}
	return p
}

// createSessionDir 以启动时间戳创建会话目录；同秒内目录已存在（真实场景
// 同秒二次启动、测试内连开多份）时追加 -2/-3 序号，保证目录唯一（封顶 99）。
func createSessionDir(logsDir string, now time.Time) (string, error) {
	base := filepath.Join(logsDir, now.Format(sessionDirLayout))
	dir := base
	for i := 2; ; i++ {
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !os.IsExist(err) || i > 99 {
			return "", err
		}
		dir = fmt.Sprintf("%s-%d", base, i)
	}
}
