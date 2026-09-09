package secrets

import (
	"sync"
	"time"
)

// MemoryStore 是进程内 Store 替身（单测与语义基准）。不加密、不落盘——
// 只保证与 KeyringStore 相同的行为契约（校验、幂等删除、ErrNotFound、
// Status 投影），密文断言由 KeyringStore 自己的测试承担。
type MemoryStore struct {
	mu   sync.Mutex
	vals map[Slot]payload
}

// NewMemoryStore 构造空存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{vals: map[Slot]payload{}}
}

func (m *MemoryStore) Set(slot Slot, value string) error {
	v, err := validate(value)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vals[slot] = payload{Secret: v, UpdatedAt: time.Now()}
	return nil
}

func (m *MemoryStore) Get(slot Slot) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.vals[slot]
	if !ok {
		return "", ErrNotFound
	}
	return p.Secret, nil
}

func (m *MemoryStore) Delete(slot Slot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.vals, slot)
	return nil
}

func (m *MemoryStore) Status(slot Slot) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.vals[slot]
	if !ok {
		return Status{}, nil
	}
	return Status{Set: true, Last4: last4(p.Secret), UpdatedAt: p.UpdatedAt}, nil
}
