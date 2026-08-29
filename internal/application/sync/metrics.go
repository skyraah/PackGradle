package sync

import (
	"context"

	"packgradle/internal/application/view"
)

// GetHashCacheStats 返回 hash cache 命中统计（进程生命周期累计）。
// 热扫描命中证明与 T14 性能基线的供数口；计数在 cachedHash 的每次
// Lookup 归入 hit 或 miss，缓存删除不重置计数。
func (a *App) GetHashCacheStats(ctx context.Context) (view.HashCacheStatsView, error) {
	hits := a.cacheHits.Load()
	misses := a.cacheMisses.Load()
	ratio := 0.0
	if total := hits + misses; total > 0 {
		ratio = float64(hits) / float64(total)
	}
	return view.HashCacheStatsView{Hits: hits, Misses: misses, HitRatio: ratio}, nil
}
