//go:build windows

package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// Fingerprinter 实现 ports.BindingFingerprinter（Windows）：
// 根目录 file identity（卷序列号 + file index）+ realpath 材质 → sha256。
// 同一路径被替换为另一个目录时 file identity 变化，指纹失配 → rebind_required。
type Fingerprinter struct{}

// NewFingerprinter 构造 Windows 指纹器。
func NewFingerprinter() *Fingerprinter { return &Fingerprinter{} }

// byHandleFileInformation 对应 Windows BY_HANDLE_FILE_INFORMATION 布局。
type byHandleFileInformation struct {
	FileAttributes     uint32
	CreationTime       syscall.Filetime
	LastAccessTime     syscall.Filetime
	LastWriteTime      syscall.Filetime
	VolumeSerialNumber uint32
	FileSizeHigh       uint32
	FileSizeLow        uint32
	NumberOfLinks      uint32
	FileIndexHigh      uint32
	FileIndexLow       uint32
}

var procGetFileInformationByHandle = kernel32.NewProc("GetFileInformationByHandle")

// dirIdentity 返回句柄所指目录的绑定身份（卷序列号 + file index）。
func dirIdentity(h syscall.Handle) (uint32, uint64, bool) {
	var info byHandleFileInformation
	ret, _, _ := procGetFileInformationByHandle.Call(uintptr(h), uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return 0, 0, false
	}
	return info.VolumeSerialNumber,
		uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		true
}

// Fingerprint 返回 "sha256:<hex>"。root 不可达即报错；file identity 取不到
// （网络盘/权限/FAT）时降级为纯路径材质并加 "w-noid" 标记，保证同机稳定。
func (f *Fingerprinter) Fingerprint(rootPath string) (string, error) {
	real, err := NormalizeEndpointPath(rootPath)
	if err != nil {
		return "", err
	}
	realLower := strings.ToLower(real)
	material := "w-noid|" + realLower
	if h, herr := openDirHandle(real); herr == nil {
		if serial, fileID, ok := dirIdentity(h); ok {
			material = fmt.Sprintf("w|%d|%d|%s", serial, fileID, realLower)
		}
		procCloseHandle.Call(uintptr(h))
	}
	sum := sha256.Sum256([]byte(material))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
