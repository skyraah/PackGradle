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
	dir, ok, err := readIniKey(cfgPath, "InstanceDir")
	if err != nil {
		return "", errs.NewDetail("err.prism.cfg_read", err.Error())
	}
	if !ok {
		// 无配置文件或未指定 InstanceDir：按默认布局处理
		return filepath.Join(dataDir, "instances"), nil
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	return filepath.Join(dataDir, dir), nil
}

// readIniKey 从 Prism 风格的最小 INI 文件（instance.cfg / prismlauncher.cfg）中
// 读取指定键的值：容忍 BOM/CRLF，跳过空行与 #/; 注释行。
// 文件不存在或键未找到/为空时返回 ("", false, nil)；读取失败返回 ("", false, err)。
// instance.cfg 与 prismlauncher.cfg 共用，避免各自维护一套解析逻辑。
func readIniKey(path, key string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "\ufeff") // 容忍 BOM
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		if v := strings.TrimSpace(value); v != "" {
			return v, true, nil
		}
	}
	return "", false, nil
}
