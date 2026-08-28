//go:build !windows

package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Fingerprinter 实现 ports.BindingFingerprinter（非 Windows 回退）：
// 卷身份信息不可移植获取，P1 使用规范化小写绝对路径。
type Fingerprinter struct{}

// NewFingerprinter 构造回退指纹器。
func NewFingerprinter() *Fingerprinter { return &Fingerprinter{} }

// Fingerprint 返回 "sha256:<hex>"。
func (f *Fingerprinter) Fingerprint(rootPath string) (string, error) {
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return "", err
	}
	material := "unix|" + strings.ToLower(filepath.Clean(abs))
	sum := sha256.Sum256([]byte(material))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
