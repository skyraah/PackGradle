package secrets

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"

	"packgradle/internal/errs"
)

// keyringService 是系统凭据管理器里的服务名。条目定位为 service:username
//（Windows 凭据管理器的 TargetName 形态），username 即槽位名——设置页展示
// 串 `credential: keyring://packgradle/<槽位>` 与此对齐。
const keyringService = "PackGradle"

// KeyringStore 是生产 Store：系统凭据管理器承载 AES-GCM 密文信封，KEK 经
// DPAPI 包裹落 <root>/credentials.kek（ADR-0015）。
type KeyringStore struct {
	kek []byte
}

// NewKeyringStore 以应用数据根（store.DefaultRoot 的值）定位 KEK 并构造
// 存储。失败时调用方应退 BrokenStore 继续启动——钥匙只影响 CF API 面，
// 不值得拖垮整个应用。
func NewKeyringStore(root string) (*KeyringStore, error) {
	kek, err := loadOrCreateKEK(filepath.Join(root, KEKFilename))
	if err != nil {
		return nil, errs.NewDetail(CodeStoreUnavailable, err.Error())
	}
	return &KeyringStore{kek: kek}, nil
}

func (s *KeyringStore) Set(slot Slot, value string) error {
	v, err := validate(value)
	if err != nil {
		return err
	}
	stored, err := sealEnvelope(s.kek, payload{Secret: v, UpdatedAt: time.Now()})
	if err != nil {
		return errs.NewDetail(CodeStoreUnavailable, err.Error())
	}
	if err := keyring.Set(keyringService, string(slot), stored); err != nil {
		return mapKeyringErr(err)
	}
	return nil
}

func (s *KeyringStore) Get(slot Slot) (string, error) {
	stored, err := keyring.Get(keyringService, string(slot))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", mapKeyringErr(err)
	}
	p, err := openEnvelope(s.kek, stored)
	if err != nil {
		return "", err
	}
	return p.Secret, nil
}

func (s *KeyringStore) Delete(slot Slot) error {
	err := keyring.Delete(keyringService, string(slot))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil // 幂等：未设置视作已删除
	}
	if err != nil {
		return mapKeyringErr(err)
	}
	return nil
}

func (s *KeyringStore) Status(slot Slot) (Status, error) {
	stored, err := keyring.Get(keyringService, string(slot))
	if errors.Is(err, keyring.ErrNotFound) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, mapKeyringErr(err)
	}
	p, err := openEnvelope(s.kek, stored)
	if err != nil {
		return Status{}, err
	}
	return Status{Set: true, Last4: last4(p.Secret), UpdatedAt: p.UpdatedAt}, nil
}

// mapKeyringErr 把底层凭据管理器错误归一到结构化错误码。条目超限（Windows
// 凭据管理器单条 2560 字节硬上限）归 value_too_large——归一与校验同因：
// 值本身过长；其余归 store_unavailable。
func mapKeyringErr(err error) error {
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		return errs.NewDetail(CodeValueTooLarge, err.Error(), MaxValueBytes)
	}
	return errs.NewDetail(CodeStoreUnavailable, err.Error())
}

// Broken 返回所有操作恒报 err.secrets.store_unavailable 的 Store：初始化
// 失败时的退路——应用照常启动（同步/工具面不需要钥匙），凭据操作如实报错、
// 不降级明文（grilling Q5-a）。
func Broken(cause error) Store {
	return brokenStore{cause: cause}
}

type brokenStore struct{ cause error }

func (b brokenStore) Set(Slot, string) error {
	return errs.NewDetail(CodeStoreUnavailable, b.cause.Error())
}

func (b brokenStore) Get(Slot) (string, error) {
	return "", errs.NewDetail(CodeStoreUnavailable, b.cause.Error())
}

func (b brokenStore) Delete(Slot) error {
	return errs.NewDetail(CodeStoreUnavailable, b.cause.Error())
}

func (b brokenStore) Status(Slot) (Status, error) {
	return Status{}, errs.NewDetail(CodeStoreUnavailable, b.cause.Error())
}
