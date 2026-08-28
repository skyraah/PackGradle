//go:build windows

package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Fingerprinter 实现 ports.BindingFingerprinter（Windows）：
// 卷序列号 + 规范化小写绝对路径 → sha256。
type Fingerprinter struct{}

// NewFingerprinter 构造 Windows 指纹器。
func NewFingerprinter() *Fingerprinter { return &Fingerprinter{} }

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetVolumeInformationW = kernel32.NewProc("GetVolumeInformationW")
)

// Fingerprint 返回 "sha256:<hex>"。卷序列号取不到（如网络盘/权限）时
// 回退纯路径材料并加 "novolume" 标记，保证同机稳定。
func (f *Fingerprinter) Fingerprint(rootPath string) (string, error) {
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return "", err
	}
	material := "novolume|" + strings.ToLower(filepath.Clean(abs))
	if vol := filepath.VolumeName(abs); vol != "" {
		volRoot := vol + `\`
		var serial uint32
		ret, _, _ := procGetVolumeInformationW.Call(
			uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(volRoot))),
			0, 0,
			uintptr(unsafe.Pointer(&serial)),
			0, 0, 0, 0,
		)
		if ret != 0 && serial != 0 {
			material = fmt.Sprintf("%d|%s", serial, strings.ToLower(filepath.Clean(abs)))
		}
	}
	sum := sha256.Sum256([]byte(material))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
