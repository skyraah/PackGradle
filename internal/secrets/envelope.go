package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"packgradle/internal/errs"
)

// envelopeV1 是信封格式版本 1（ADR-0015）：AES-256-GCM、12 字节随机 nonce、
// 密文自带 16 字节认证标签。外层 JSON 自描述（v/n/c 三键）；内层明文 JSON
// 携带值与 UpdatedAt——元数据与值同密，Status 解密后可得，盘上与凭据管理器
// 条目里没有任何明文或可读元数据。
const envelopeV1 = 1

// keySize 是 KEK 长度（AES-256）。
const keySize = 32

// envelope 是存入系统凭据管理器的外层密文信封。
type envelope struct {
	V int    `json:"v"`
	N string `json:"n"` // base64(nonce)
	C string `json:"c"` // base64(密文 ‖ GCM tag)
}

// payload 是信封的内层明文。
type payload struct {
	Secret    string    `json:"secret"`
	UpdatedAt time.Time `json:"updated_at"`
}

// sealEnvelope 用 KEK 加密 payload，返回外层信封 JSON 文本。
func sealEnvelope(key []byte, p payload) (string, error) {
	if len(key) != keySize {
		return "", fmt.Errorf("secrets: KEK 长度 %d 非 %d", len(key), keySize)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	out, err := json.Marshal(envelope{
		V: envelopeV1,
		N: base64.StdEncoding.EncodeToString(nonce),
		C: base64.StdEncoding.EncodeToString(ct),
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// openEnvelope 校验信封版本并用 KEK 解密。版本不符、解码失败或认证失败
// 一律报 err.secrets.store_corrupt（不区分原因——条目已不可信，值不可用）。
func openEnvelope(key []byte, stored string) (payload, error) {
	if len(key) != keySize {
		return payload{}, fmt.Errorf("secrets: KEK 长度 %d 非 %d", len(key), keySize)
	}
	var e envelope
	if err := json.Unmarshal([]byte(stored), &e); err != nil {
		return payload{}, errs.NewDetail(CodeStoreCorrupt, err.Error())
	}
	if e.V != envelopeV1 {
		return payload{}, errs.NewDetail(CodeStoreCorrupt, fmt.Sprintf("envelope v%d", e.V))
	}
	nonce, err := base64.StdEncoding.DecodeString(e.N)
	if err != nil {
		return payload{}, errs.NewDetail(CodeStoreCorrupt, err.Error())
	}
	ct, err := base64.StdEncoding.DecodeString(e.C)
	if err != nil {
		return payload{}, errs.NewDetail(CodeStoreCorrupt, err.Error())
	}
	gcm, err := newGCM(key)
	if err != nil {
		return payload{}, err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return payload{}, errs.NewDetail(CodeStoreCorrupt, err.Error())
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		return payload{}, errs.NewDetail(CodeStoreCorrupt, err.Error())
	}
	return p, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
