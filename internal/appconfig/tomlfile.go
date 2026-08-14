package appconfig

import (
	"os"
	"path/filepath"

	"packgradle/internal/errs"

	"github.com/BurntSushi/toml"
)

// ReadToml 读取 TOML 文件到 v；文件不存在时返回 nil 并保持 v 不变
// （配置/缓存首启场景，避免调用方各自处理 os.IsNotExist）
func ReadToml(path string, v any) error {
	if _, err := toml.DecodeFile(path, v); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

// WriteTomlAtomic 原子写入 TOML：先写唯一临时文件再重命名，
// 避免中断导致配置/缓存文件损坏。父目录不存在时自动创建。
// 临时文件使用 CreateTemp 保证并发写入同一目标时不会互相覆盖对方的临时文件。
func WriteTomlAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.NewDetail("err.file.mkdir", err.Error(), filepath.Dir(path))
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return errs.NewDetail("err.file.write", err.Error(), path)
	}
	tmp := f.Name()
	if err := toml.NewEncoder(f).Encode(v); err != nil {
		f.Close()
		os.Remove(tmp)
		return errs.NewDetail("err.file.serialize", err.Error(), path)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return errs.NewDetail("err.file.write", err.Error(), path)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return errs.NewDetail("err.file.save", err.Error(), path)
	}
	return nil
}
