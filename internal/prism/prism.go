package prism

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/errs"
)

// 定位 Prism Launcher 的实例目录。
// Prism 的配置文件固定存放于 %APPDATA%\PrismLauncher（标准安装布局），
// 其中 prismlauncher.cfg 的 InstanceDir 字段记录实例根位置：
// 相对路径相对数据目录解析（默认 "instances"），也支持绝对路径。

// DataDir 返回 Prism 数据目录（配置文件所在位置）
func DataDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "PrismLauncher")
}

// InstancesDir 解析实例根目录：读数据目录下 prismlauncher.cfg 的 InstanceDir 字段。
// 返回的目录不一定存在（由调用方校验）。
func InstancesDir(dataDir string) (string, error) {
	cfgPath := filepath.Join(dataDir, "prismlauncher.cfg")
	f, err := os.Open(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 无配置文件时按默认布局处理
			return filepath.Join(dataDir, "instances"), nil
		}
		return "", errs.NewDetail("err.prism.cfg_read", err.Error())
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "\ufeff") // 容忍 BOM
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "InstanceDir" {
			continue
		}
		dir := strings.TrimSpace(value)
		if dir == "" {
			return filepath.Join(dataDir, "instances"), nil
		}
		if filepath.IsAbs(dir) {
			return filepath.Clean(dir), nil
		}
		return filepath.Join(dataDir, dir), nil
	}
	return filepath.Join(dataDir, "instances"), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
