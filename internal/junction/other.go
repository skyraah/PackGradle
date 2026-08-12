//go:build !windows

package junction

import "errors"

// 非 Windows 平台占位实现：Junction 是 NTFS 专属机制。
// 项目主目标为 Windows，此处仅保证多平台可编译。

type otherManager struct{}

// NewWindowsManager 在非 Windows 平台返回占位实现
func NewWindowsManager() Manager {
	return &otherManager{}
}

func (m *otherManager) Create(link, target string) error {
	return errors.New("junction 仅在 Windows NTFS 卷上受支持")
}

func (m *otherManager) Remove(link string) error {
	return errors.New("junction 仅在 Windows NTFS 卷上受支持")
}

func (m *otherManager) IsJunction(link string) (bool, error) {
	return false, nil
}

func (m *otherManager) TargetOf(link string) (string, error) {
	return "", errors.New("junction 仅在 Windows NTFS 卷上受支持")
}
