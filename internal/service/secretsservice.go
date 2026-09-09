package service

import (
	"time"

	"packgradle/internal/secrets"
)

// SecretsService 是凭据槽位的 Wails 出口（ADR-0015；grilling Q3-a）。
// 明文永不出后端：方法面只有写入、删除与脱敏状态三件，没有任何返回
// 明文的通路。槽位为固定枚举（secrets.ParseSlot），不开放任意命名。
type SecretsService struct {
	store secrets.Store
}

// NewSecretsService 构造服务（生产接 KeyringStore，测试接 MemoryStore）。
func NewSecretsService(store secrets.Store) *SecretsService {
	return &SecretsService{store: store}
}

// SecretStatus 是 Status 的前端投影（时间戳为 RFC3339 文本）。
type SecretStatus struct {
	Set       bool   `json:"set"`
	Last4     string `json:"last4"`
	UpdatedAt string `json:"updatedAt"`
}

// Set 写入槽位值（trim 归一；空值是语义错误——清除走 Delete）。
func (s *SecretsService) Set(kind, value string) error {
	slot, err := secrets.ParseSlot(kind)
	if err != nil {
		return err
	}
	return s.store.Set(slot, value)
}

// Delete 清除槽位（幂等：未设置视作已删除）。
func (s *SecretsService) Delete(kind string) error {
	slot, err := secrets.ParseSlot(kind)
	if err != nil {
		return err
	}
	return s.store.Delete(slot)
}

// Status 返回槽位脱敏状态：已设置与否、末四位线索与设置时间。
func (s *SecretsService) Status(kind string) (SecretStatus, error) {
	slot, err := secrets.ParseSlot(kind)
	if err != nil {
		return SecretStatus{}, err
	}
	st, err := s.store.Status(slot)
	if err != nil {
		return SecretStatus{}, err
	}
	out := SecretStatus{Set: st.Set, Last4: st.Last4}
	if !st.UpdatedAt.IsZero() {
		out.UpdatedAt = st.UpdatedAt.Format(time.RFC3339)
	}
	return out, nil
}
