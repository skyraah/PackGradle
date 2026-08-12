package curseforge

import (
	"path/filepath"
	"sync"

	"packgradle/internal/appconfig"
)

// CfCacheStore 管理每个项目的 CurseForge 文件信息缓存。
// 缓存文件存放在项目目录内的 .cache/modversion.cache，避免写入 config.toml；
// 固定文件名便于与 .cache 目录中其他缓存（如更新检查）区分
type CfCacheStore struct {
	mu   sync.Mutex
	root string // .cache 目录（项目目录内）
}

// NewCfCacheStore 创建缓存存储，root 为 .cache 目录
func NewCfCacheStore(root string) *CfCacheStore {
	return &CfCacheStore{root: root}
}

// path 返回缓存文件路径（固定文件名 modversion.cache）
func (c *CfCacheStore) path() string {
	return filepath.Join(c.root, "modversion.cache")
}

// Load 读取项目缓存；文件不存在或为空时返回空 map
func (c *CfCacheStore) Load() (map[string]CfFileCache, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked()
}

func (c *CfCacheStore) loadLocked() (map[string]CfFileCache, error) {
	var cache map[string]CfFileCache
	if err := appconfig.ReadToml(c.path(), &cache); err != nil {
		return nil, err
	}
	if cache == nil {
		cache = map[string]CfFileCache{}
	}
	return cache, nil
}

// Save 覆盖写入项目缓存
func (c *CfCacheStore) Save(cache map[string]CfFileCache) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked(cache)
}

// Upsert 读取缓存、写入一条后保存（线程安全，供并发批量写入）
func (c *CfCacheStore) Upsert(key string, entry CfFileCache) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cache, err := c.loadLocked()
	if err != nil {
		return err
	}
	cache[key] = entry
	return c.saveLocked(cache)
}

// Prune 删除不满足 keep 条件的缓存条目（如更新后失效的旧 file-id 条目、
// 已移除 mod 的孤儿条目），避免缓存堆积；仅在有删除时写盘
func (c *CfCacheStore) Prune(keep func(key string) bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cache, err := c.loadLocked()
	if err != nil {
		return err
	}
	changed := false
	for key := range cache {
		if !keep(key) {
			delete(cache, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return c.saveLocked(cache)
}

// saveLocked 原子写入（临时文件+重命名），避免中断导致缓存损坏
func (c *CfCacheStore) saveLocked(cache map[string]CfFileCache) error {
	return appconfig.WriteTomlAtomic(c.path(), cache)
}
