// storagerepo.go 实现存储占用概览采集（ports.StorageStatsSource，ADR-0011 §8
// 勘误兑现，票 #90）。口径（docs/research/crosscutting-retention.md §4 承载）：
//   - cas_object_count / cas_total_bytes：objects 表 ready 行计数与 size 总和
//     （GC 账面口径，含未被存活提交引用的对象——引用侧归 gc.Audit）；
//   - cas_tmp_leftovers：objectsRoot 根下 `.tmp-*` 写中断残留文件数（objectstore
//     cas.go Put 的临时前缀；残留清扫归 GC 孤儿清扫阶段）；
//   - task_events_count：task_events 行数；
//   - db_size_bytes：packgradle.db 文件字节数（含 -wal）；
//   - free_disk_bytes：用户数据根所在卷剩余字节数（fsutil.FreeDiskBytes）。
//
// staging 侧指标不占位（ADR-0011 §5 雾区，待 #69 决议后补）；全量惰性查询，
// 无后台定时器、无缓存。
package sqlite

import (
	"context"
	"database/sql"
	"os"
	"strings"

	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/fsutil"
	"packgradle/internal/store"
)

// tmpPrefix 是 CAS 对象写入期临时文件前缀（objectstore.cas.go 同款字面；
// 前缀本体为 objectstore 包私有，此处按观测口径引用字面值）。
const storageTmpPrefix = ".tmp-"

// StorageStatsRepository 实现存储占用概览的只读采集。
type StorageStatsRepository struct {
	db     *sql.DB
	layout store.Layout
}

// NewStorageStatsRepository 构造仓库（layout 为同一用户数据根的装配产物）。
func NewStorageStatsRepository(db *sql.DB, layout store.Layout) *StorageStatsRepository {
	return &StorageStatsRepository{db: db, layout: layout}
}

// StorageStats 惰性采集当前存储占用概览。
func (r *StorageStatsRepository) StorageStats(ctx context.Context) (view.StorageStatsView, error) {
	out := view.StorageStatsView{SchemaVersion: model.CurrentSchemaVersion}

	// CAS 账面：ready 对象计数与字节总量
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(size), 0) FROM objects WHERE state = 'ready'",
	).Scan(&out.CasObjectCount, &out.CasTotalBytes); err != nil {
		return out, err
	}

	// task_events 行数
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM task_events",
	).Scan(&out.TaskEventsCount); err != nil {
		return out, err
	}

	// DB 文件体积（含 -wal；-shm 为共享内存索引不计账）
	for _, p := range []string{r.layout.DBPath, r.layout.DBPath + "-wal"} {
		if st, err := os.Stat(p); err == nil {
			out.DBSizeBytes += st.Size()
		} else if !os.IsNotExist(err) {
			return out, err
		}
	}

	// .tmp-* 写中断残留（objectsRoot 根下，不递归分片目录）
	entries, err := os.ReadDir(r.layout.ObjectsDir)
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), storageTmpPrefix) {
			out.CasTmpLeftovers++
		}
	}

	// 数据根所在卷剩余空间（容量红线双指标之二）
	free, err := fsutil.FreeDiskBytes(r.layout.Root)
	if err != nil {
		return out, err
	}
	out.FreeDiskBytes = free
	return out, nil
}
