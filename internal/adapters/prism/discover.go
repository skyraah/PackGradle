package prism

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Instance 描述发现的一个 Prism 实例。
type Instance struct {
	Dir     string // 实例目录（含 instance.cfg）
	Name    string // instance.cfg 的 name，缺失回退目录名
	GameDir string // <实例>/minecraft（Prism/MMC 事实标准游戏目录）
}

// DiscoverInstances 扫描 instancesDir 的一级子目录，含 instance.cfg 的目录才视为实例。
// 单个实例读取失败时跳过（不影响其余实例）；实例根目录自身读取失败
// （权限/IO 错误）返回错误，避免把故障误报成空列表。
func DiscoverInstances(instancesDir string) ([]Instance, error) {
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		return nil, fmt.Errorf("prism: 读取实例根目录 %s: %w", instancesDir, err)
	}
	instances := make([]Instance, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue // 顶层文件（instgroups.json 等）不是实例
		}
		dir := filepath.Join(instancesDir, e.Name())
		cfg := filepath.Join(dir, "instance.cfg")
		if _, err := os.Stat(cfg); err != nil {
			continue // 无 instance.cfg（或不可访问）：不是实例，跳过
		}
		// name= 缺失或读取失败时回退目录名（目录名是实例的事实 ID）
		name := e.Name()
		if v, ok, err := readINIKey(cfg, "name"); err == nil && ok {
			name = v
		}
		instances = append(instances, Instance{
			Dir:     dir,
			Name:    name,
			GameDir: filepath.Join(dir, "minecraft"),
		})
	}
	sort.Slice(instances, func(i, j int) bool {
		return strings.ToLower(instances[i].Name) < strings.ToLower(instances[j].Name)
	})
	return instances, nil
}

// readINIKey 从 Prism 风格的最小 INI 文件（instance.cfg / prismlauncher.cfg）
// 读取指定键的值：容忍 BOM/CRLF，跳过空行与 #/; 注释行。
// 文件不存在或键未找到/值为空时返回 ("", false, nil)；读取失败返回 ("", false, err)。
func readINIKey(path, key string) (string, bool, error) {
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
		line = strings.TrimPrefix(line, "\ufeff") // 容忍行首 BOM
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
	if err := sc.Err(); err != nil {
		return "", false, err
	}
	return "", false, nil
}
