package secrets

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KEKFilename 是信封钥匙文件名（应用数据根下，与 config.toml 同目录）。
const KEKFilename = "credentials.kek"

// loadOrCreateKEK 返回信封钥匙（32 字节随机）：文件存在则解包复用，不存在
// 则生成新钥、包裹落盘（0600）。新钥只在首条凭据写入前诞生，不存在「旧密文
// 对不上新钥」的窗口；文件损坏时不静默重建——那会让既有条目变成永远解不开
// 的乱码，宁可报错让用户重填（grilling Q5-a：如实报错，不降级）。
func loadOrCreateKEK(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, uerr := unwrapKEK(raw)
		if uerr != nil {
			return nil, fmt.Errorf("secrets: KEK 文件损坏（%s）: %w", path, uerr)
		}
		return key, nil
	case errors.Is(err, fs.ErrNotExist):
		// 落到下方生成路径
	default:
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	wrapped, err := wrapKEK(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, wrapped, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
