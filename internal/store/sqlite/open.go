package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	// 注册 modernc.org/sqlite 驱动（driver 名 "sqlite"，纯 Go 实现）。
	_ "modernc.org/sqlite"
)

// DSN 构造 packgradle.db 的连接串：
//   - busy_timeout(5000)：写冲突时最多等待 5s 再报 SQLITE_BUSY；
//   - journal_mode(WAL)：架构文档 §8.3 要求的持久化日志模式；
//   - synchronous(FULL)：事务提交即落盘；
//   - foreign_keys(1)：启用外键约束。
//
// 路径统一转换为正斜杠，SQLite 的 file: URI 在 Windows 上同样接受。
func dsn(dbPath string) string {
	return fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)",
		filepath.ToSlash(dbPath),
	)
}

// Open 打开（必要时创建）SQLite 数据库并完成连接级配置。
// 单连接（MaxOpenConns=1）避免了 WAL 下多连接写冲突的复杂度，
// 也使 event stream 的 sequence 分配可以依赖串行化。
func Open(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("sqlite: 打开 %s 失败: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)

	// 校验 journal_mode 确为 wal（DSN 中设置失败时连接即报错，这里二次确认）。
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: 读取 journal_mode 失败: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		db.Close()
		return nil, fmt.Errorf("sqlite: journal_mode = %q, 期望 wal", mode)
	}
	return db, nil
}
