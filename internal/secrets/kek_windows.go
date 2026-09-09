//go:build windows

package secrets

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wrapKEK 用 DPAPI（用户域，flags=0）包裹信封钥匙：解包需要同一 Windows
// 登录账户，程序自身不持口令、盘上无明文钥（ADR-0015）。经 golang.org/x/sys
// 纯 syscall 调 crypt32.dll，无 cgo。
func wrapKEK(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("secrets: 空 KEK")
	}
	var out windows.DataBlob
	in := &windows.DataBlob{Size: uint32(len(key)), Data: &key[0]}
	if err := windows.CryptProtectData(in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	buf := make([]byte, out.Size)
	copy(buf, unsafe.Slice(out.Data, out.Size))
	return buf, nil
}

// unwrapKEK 解开 DPAPI 包裹的信封钥匙，并校验长度仍是 32 字节。
func unwrapKEK(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, fmt.Errorf("secrets: 空 KEK 包")
	}
	var out windows.DataBlob
	in := &windows.DataBlob{Size: uint32(len(blob)), Data: &blob[0]}
	if err := windows.CryptUnprotectData(in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	buf := make([]byte, out.Size)
	copy(buf, unsafe.Slice(out.Data, out.Size))
	if len(buf) != keySize {
		return nil, fmt.Errorf("secrets: KEK 解包后长度 %d 非 %d", len(buf), keySize)
	}
	return buf, nil
}
