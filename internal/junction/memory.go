package junction

import (
	"os"
	"path/filepath"
	"sync"
)

// memoryManager 是 Manager 的内存假实现，供 service 层测试注入：
// 记录 link → target 映射并模拟状态，不产生真实文件系统副作用。
type memoryManager struct {
	mu      sync.Mutex
	links   map[string]string // link → target
	deleted map[string]bool   // 已 Remove 的位置
}

// NewMemoryManager 返回内存假实现
func NewMemoryManager() Manager {
	return &memoryManager{links: map[string]string{}, deleted: map[string]bool{}}
}

func (m *memoryManager) Create(link, target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	linkAbs, err := filepath.Abs(link)
	if err != nil {
		return err
	}
	// 模拟：目标必须存在
	if _, err := os.Stat(targetAbs); err != nil {
		return err
	}
	if _, exists := m.links[linkAbs]; exists || m.deleted[linkAbs] {
		return os.ErrExist
	}
	m.links[linkAbs] = targetAbs
	return nil
}

func (m *memoryManager) Remove(link string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	linkAbs, err := filepath.Abs(link)
	if err != nil {
		return err
	}
	if _, ok := m.links[linkAbs]; !ok {
		return os.ErrInvalid
	}
	delete(m.links, linkAbs)
	m.deleted[linkAbs] = true
	return nil
}

func (m *memoryManager) IsJunction(link string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	linkAbs, err := filepath.Abs(link)
	if err != nil {
		return false, err
	}
	_, ok := m.links[linkAbs]
	return ok, nil
}

func (m *memoryManager) TargetOf(link string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	linkAbs, err := filepath.Abs(link)
	if err != nil {
		return "", err
	}
	target, ok := m.links[linkAbs]
	if !ok {
		return "", os.ErrInvalid
	}
	return target, nil
}
