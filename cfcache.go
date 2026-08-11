package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// CfCacheStore 管理每个项目的 CurseForge 文件信息缓存。
// 缓存文件存放在项目目录内的 .cache/<项目名>.cache，避免写入 config.toml
type CfCacheStore struct {
	mu   sync.Mutex
	root string // .cache 目录（项目目录内）
}

// NewCfCacheStore 创建缓存存储，root 为 .cache 目录
func NewCfCacheStore(root string) *CfCacheStore {
	return &CfCacheStore{root: root}
}

// path 返回指定项目的缓存文件路径
func (c *CfCacheStore) path(projectName string) string {
	return filepath.Join(c.root, projectName+".cache")
}

// Load 读取指定项目的缓存；文件不存在或为空时返回空 map
func (c *CfCacheStore) Load(projectName string) (map[string]CfFileCache, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked(projectName)
}

func (c *CfCacheStore) loadLocked(projectName string) (map[string]CfFileCache, error) {
	var cache map[string]CfFileCache
	if _, err := toml.DecodeFile(c.path(projectName), &cache); err != nil {
		if os.IsNotExist(err) {
			return map[string]CfFileCache{}, nil
		}
		return nil, fmt.Errorf("读取缓存 %s 失败: %w", c.path(projectName), err)
	}
	if cache == nil {
		cache = map[string]CfFileCache{}
	}
	return cache, nil
}

// Save 覆盖写入指定项目的缓存
func (c *CfCacheStore) Save(projectName string, cache map[string]CfFileCache) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked(projectName, cache)
}

// Upsert 读取项目缓存、写入一条后保存（线程安全，供并发批量写入）
func (c *CfCacheStore) Upsert(projectName, key string, entry CfFileCache) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cache, err := c.loadLocked(projectName)
	if err != nil {
		return err
	}
	cache[key] = entry
	return c.saveLocked(projectName, cache)
}

// saveLocked 原子写入：先写临时文件再重命名，避免中断导致缓存损坏
func (c *CfCacheStore) saveLocked(projectName string, cache map[string]CfFileCache) error {
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return fmt.Errorf("创建缓存目录 %s 失败: %w", c.root, err)
	}
	path := c.path(projectName)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("写入缓存 %s 失败: %w", path, err)
	}
	if err := toml.NewEncoder(f).Encode(cache); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("序列化缓存失败: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("写入缓存 %s 失败: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("保存缓存 %s 失败: %w", path, err)
	}
	return nil
}
