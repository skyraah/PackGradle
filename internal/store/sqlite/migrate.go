package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// migration 是单个前向迁移步骤。Stmt 为函数以支持按版本惰性拼装 DDL。
type migration struct {
	Version int
	Name    string
	Stmt    func() string
	// DisableFK 在迁移事务期间关闭外键（foreign_keys 是事务外 PRAGMA，
	// 由 applyMigration 在 BEGIN 前 OFF、COMMIT 后 ON）。关闭后由 Verify
	// 的 foreign_key_check 兜底；默认 false 保持外键开启。
	DisableFK bool
	// Verify 在 COMMIT 前执行的自检；返回错误则整个迁移回滚。
	Verify func(ctx context.Context, conn *sql.Conn) error
}

// migrations 是按版本升序排列的前向迁移列表，只能追加、不能修改历史条目。
var migrations = []migration{
	{Version: 1, Name: "initial schema", Stmt: func() string { return schemaV1 }},
	{Version: 2, Name: "tasks plan/commit 引用约束", Stmt: func() string { return schemaV2 },
		DisableFK: true,
		Verify: func(ctx context.Context, conn *sql.Conn) error {
			rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check(tasks)")
			if err != nil {
				return fmt.Errorf("sqlite: foreign_key_check(tasks) 失败: %w", err)
			}
			defer rows.Close()
			bad := 0
			for rows.Next() {
				var table string
				var rowid, parent, fkid sql.NullInt64
				if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
					return fmt.Errorf("sqlite: 读取 foreign_key_check 结果: %w", err)
				}
				bad++
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("sqlite: 遍历 foreign_key_check 结果: %w", err)
			}
			if bad > 0 {
				return fmt.Errorf("sqlite: tasks 表存在 %d 行悬挂引用（plan/commit/relation），迁移中止", bad)
			}
			return nil
		}},
	{Version: 3, Name: "sync_plans requested_exactness", Stmt: func() string { return schemaV3 }},
}

// SchemaVersion 返回当前代码支持的目标 schema 版本（= len(migrations)）。
func SchemaVersion() int { return len(migrations) }

// Migrate 将数据库迁移到目标版本（架构文档 §8.3）：
//   - 读 PRAGMA user_version；已是目标版本则直接返回（幂等）；
//   - 升级已有库（0 < user_version < 目标）前，先在事务外执行 VACUUM INTO
//     生成一致备份；备份失败则中止迁移并返回错误——迁移失败不得启动写操作；
//   - 每个版本一个事务：BEGIN IMMEDIATE → DDL → PRAGMA user_version=N →
//     INSERT schema_migrations → COMMIT，失败回滚。
func Migrate(ctx context.Context, db *sql.DB, backupDir string) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("sqlite: 读取 user_version 失败: %w", err)
	}
	target := len(migrations)
	if current == target {
		return nil
	}
	if current > target {
		return fmt.Errorf("sqlite: user_version=%d 高于程序支持的版本 %d，请升级 PackGradle", current, target)
	}
	if current < 0 {
		return fmt.Errorf("sqlite: user_version=%d 非法", current)
	}
	if current > 0 {
		if err := backupBeforeMigrate(ctx, db, backupDir); err != nil {
			return err
		}
	}
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

// backupBeforeMigrate 在事务外用 VACUUM INTO 生成一致性备份。
// 备份文件名形如 packgradle.db.bak-<UTC 时间戳>（Windows 文件名安全字符）。
func backupBeforeMigrate(ctx context.Context, db *sql.DB, backupDir string) error {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("sqlite: 创建备份目录 %s 失败: %w", backupDir, err)
	}
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	path := filepath.Join(backupDir, "packgradle.db.bak-"+ts)
	// SQL 字符串字面量：单引号包裹，内部单引号加倍转义。
	literal := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := db.ExecContext(ctx, "VACUUM INTO "+literal); err != nil {
		return fmt.Errorf("sqlite: 迁移前备份失败（已中止迁移，禁止启动写操作）: %w", err)
	}
	return nil
}

// applyMigration 以 BEGIN IMMEDIATE 事务执行单个迁移版本。
// 通过 db.Conn 独占一条连接，以便手动控制事务边界（database/sql 的 Tx 只发普通 BEGIN）。
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlite: 迁移 v%d (%s) 获取连接失败: %w", m.Version, m.Name, err)
	}
	defer conn.Close()

	if m.DisableFK {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
			return fmt.Errorf("sqlite: 迁移 v%d (%s) 关闭外键失败: %w", m.Version, m.Name, err)
		}
		defer func() {
			_, _ = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		}()
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("sqlite: 迁移 v%d (%s) BEGIN IMMEDIATE 失败: %w", m.Version, m.Name, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, m.Stmt()); err != nil {
		return fmt.Errorf("sqlite: 迁移 v%d (%s) DDL 失败: %w", m.Version, m.Name, err)
	}
	if m.Verify != nil {
		if err := m.Verify(ctx, conn); err != nil {
			return fmt.Errorf("sqlite: 迁移 v%d (%s) 自检失败: %w", m.Version, m.Name, err)
		}
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", m.Version)); err != nil {
		return fmt.Errorf("sqlite: 迁移 v%d (%s) 写 user_version 失败: %w", m.Version, m.Name, err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, applied_at) VALUES(?,?,?)",
		m.Version, m.Name, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("sqlite: 迁移 v%d (%s) 写 schema_migrations 失败: %w", m.Version, m.Name, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("sqlite: 迁移 v%d (%s) COMMIT 失败: %w", m.Version, m.Name, err)
	}
	committed = true
	return nil
}
