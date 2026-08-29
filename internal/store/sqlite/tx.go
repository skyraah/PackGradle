package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"packgradle/internal/application/ports"
)

// DBTX 是仓库单方法所需的查询面；*sql.DB 与 *sql.Tx 均满足。
// 仓库结构持有该接口而非具体类型，使同一实现既可独立运行（绑定 *sql.DB）
// 也可在事务域内运行（绑定 *sql.Tx）——ports 仓库方法签名一概不动（ADR-0003）。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// beginOrJoin 为仓库内部多语句序列提供事务边界：底层是 *sql.DB 时开启
// 独立事务（既有单方法「同事务原子」语义不变）；底层已是 *sql.Tx（处于
// RunInTx 事务域内）时直接加入外层事务——SQLite 单连接下嵌套事务无意义，
// 语句序列的原子性由外层事务承担。
func beginOrJoin(ctx context.Context, q DBTX, what string, fn func(DBTX) error) error {
	switch t := q.(type) {
	case *sql.Tx:
		return fn(t)
	case *sql.DB:
		tx, err := t.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: %s 开启事务: %w", what, err)
		}
		defer tx.Rollback()
		if err := fn(tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("sqlite: %s 提交: %w", what, err)
		}
		return nil
	default:
		return fmt.Errorf("sqlite: %s: 不支持的查询面 %T", what, q)
	}
}

// UnitOfWork 是 ports.UnitOfWork 的 SQLite 实现：事务域内构造绑定同一
// *sql.Tx 的一套仓库集合，闭包返回错误时整体回滚，成功时提交
// （ADR-0003 决议 1/2：多步元数据写入收进单个 SQLite 事务）。
type UnitOfWork struct {
	db *sql.DB
}

// NewUnitOfWork 创建事务边界入口（与各仓库共享同一 *sql.DB）。
func NewUnitOfWork(db *sql.DB) *UnitOfWork {
	return &UnitOfWork{db: db}
}

var _ ports.UnitOfWork = (*UnitOfWork)(nil)

// RunInTx 在单个 SQLite 事务内执行 fn。fn 只应使用闭包传入的事务域仓库；
// 事务打开期间共享 *sql.DB 上的其他查询会因单连接（MaxOpenConns=1）阻塞至提交。
func (u *UnitOfWork) RunInTx(ctx context.Context, fn func(repos ports.Repos) error) error {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: 开启事务: %w", err)
	}
	defer tx.Rollback()
	if err := fn(txRepos(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: 提交事务: %w", err)
	}
	return nil
}

// txRepos 构造绑定 tx 的事务域仓库集合。
func txRepos(tx *sql.Tx) ports.Repos {
	return ports.Repos{
		Endpoints:    &EndpointRepository{db: tx},
		Relations:    &RelationRepository{db: tx},
		Snapshots:    &SnapshotRepository{db: tx},
		Baselines:    &BaselineRepository{db: tx},
		Plans:        &PlanRepository{db: tx},
		Tasks:        &TaskRepository{db: tx},
		Mappings:     &MappingRepository{db: tx},
		Preparations: &PreparationRepository{db: tx},
		HashCache:    &HashCacheRepository{db: tx},
		Events:       &EventRepository{db: tx},
	}
}
