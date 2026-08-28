//go:build !windows

package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// Fingerprinter 实现 ports.BindingFingerprinter（非 Windows）：
// dev/ino file identity（可取得时）+ realpath 材质 → sha256。
type Fingerprinter struct{}

// NewFingerprinter 构造回退指纹器。
func NewFingerprinter() *Fingerprinter { return &Fingerprinter{} }

// Fingerprint 返回 "sha256:<hex>"。root 不可达即报错；file identity 取不到
// （FS 不支持）时降级为纯路径材质并加 "unix-noid" 标记。
func (f *Fingerprinter) Fingerprint(rootPath string) (string, error) {
	real, err := NormalizeEndpointPath(rootPath)
	if err != nil {
		return "", err
	}
	realLower := strings.ToLower(real)
	material := "unix-noid|" + realLower
	if st, serr := os.Stat(real); serr == nil {
		if statT, ok := st.Sys().(*syscall.Stat_t); ok {
			material = fmt.Sprintf("unix|%d|%d|%s", statT.Dev, statT.Ino, realLower)
		}
	}
	sum := sha256.Sum256([]byte(material))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
