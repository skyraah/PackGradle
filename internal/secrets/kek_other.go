//go:build !windows

package secrets

import (
	"bytes"
	"fmt"
)

// 非 Windows 无 DPAPI：KEK 以 0600 明文形态落盘（loadOrCreateKEK 的写入
// 权限）。密文本体仍在系统凭据服务（Secret Service / Keychain）内，此处
// 让步影响的只是「密文条目被整份导出后的第二道锁」；Windows 主平台不受
// 影响（ADR-0015 §5 权衡）。
func wrapKEK(key []byte) ([]byte, error) { return bytes.Clone(key), nil }

func unwrapKEK(blob []byte) ([]byte, error) {
	if len(blob) != keySize {
		return nil, fmt.Errorf("secrets: KEK 长度 %d 非 %d", len(blob), keySize)
	}
	return bytes.Clone(blob), nil
}
