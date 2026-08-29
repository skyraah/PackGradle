package prism

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/application/ports"
)

// Discoverer 实现 ports.RuntimeDiscovery（/runtimes 页发现入口）。
type Discoverer struct {
	// InstancesDir 覆盖实例根目录定位（测试注入）；nil 用默认定位
	//（%APPDATA%\PrismLauncher → prismlauncher.cfg 的 InstanceDir → 默认布局）。
	InstancesDir func() (string, error)
}

// NewDiscoverer 构造运行实例发现器（默认实例根目录定位）。
func NewDiscoverer() *Discoverer { return &Discoverer{} }

// NewDiscovererWith 构造指定实例根目录定位函数的发现器（测试与自定义安装位置注入）。
func NewDiscovererWith(instancesDir func() (string, error)) *Discoverer {
	return &Discoverer{InstancesDir: instancesDir}
}

// loaderUIDs 是加载器组件 uid → 短名（mmc-pack.json components）。
var loaderUIDs = map[string]string{
	"net.fabricmc.fabric-loader": "fabric",
	"net.minecraftforge":         "forge",
	"org.quiltmc.quilt-loader":   "quilt",
	"net.neoforged":              "neoforge",
}

// mmcPack 是 mmc-pack.json 中发现所需的子集。
type mmcPack struct {
	Components []struct {
		UID     string `json:"uid"`
		Version string `json:"version"`
	} `json:"components"`
}

// DiscoverRuntimes 定位 Prism 实例根目录并枚举实例候选。
// 定位或读取失败统一返回 *ports.InstancesDirError（携带尝试目录与底层原因）；
// 单个实例元数据读取失败按空元数据保留候选（路径事实成立），不影响其余实例。
func (d *Discoverer) DiscoverRuntimes(ctx context.Context) ([]ports.RuntimeCandidate, error) {
	dir, err := d.instancesDir()
	if err != nil {
		return nil, err
	}
	instances, err := DiscoverInstances(dir)
	if err != nil {
		return nil, &ports.InstancesDirError{DataDir: dir, Err: err}
	}
	candidates := make([]ports.RuntimeCandidate, 0, len(instances))
	for _, inst := range instances {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		mc, loader := instanceMeta(inst.Dir)
		candidates = append(candidates, ports.RuntimeCandidate{
			InstanceID:  filepath.Base(inst.Dir),
			InstanceDir: inst.Dir,
			DisplayName: inst.Name,
			GameDir:     inst.GameDir,
			Minecraft:   mc,
			Modloader:   loader,
		})
	}
	return candidates, nil
}

// instancesDir 解析实例根目录；定位失败返回 *ports.InstancesDirError。
func (d *Discoverer) instancesDir() (string, error) {
	if d.InstancesDir != nil {
		return d.InstancesDir()
	}
	dataDir := filepath.Join(os.Getenv("APPDATA"), "PrismLauncher")
	if _, err := os.Stat(dataDir); err != nil {
		return "", &ports.InstancesDirError{DataDir: dataDir, Err: err}
	}
	dir, ok, err := readINIKey(filepath.Join(dataDir, "prismlauncher.cfg"), "InstanceDir")
	if err != nil {
		return "", &ports.InstancesDirError{DataDir: dataDir, Err: fmt.Errorf("读取 prismlauncher.cfg: %w", err)}
	}
	if !ok {
		return filepath.Join(dataDir, "instances"), nil
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	return filepath.Join(dataDir, dir), nil
}

// instanceMeta 提取实例的 Minecraft 版本与加载器短名（缺失为空，不报错）。
func instanceMeta(instDir string) (minecraft, modloader string) {
	if data, err := os.ReadFile(filepath.Join(instDir, "mmc-pack.json")); err == nil {
		var pack mmcPack
		if json.Unmarshal(data, &pack) == nil {
			for _, c := range pack.Components {
				if c.UID == "net.minecraft" {
					minecraft = c.Version
				}
				if name, ok := loaderUIDs[c.UID]; ok && modloader == "" {
					modloader = name
				}
			}
			if minecraft != "" {
				return minecraft, modloader
			}
		}
	}
	// MultiMC 时代的 vanilla 实例：版本写在 instance.cfg 的 IntendedVersion
	if v, ok, _ := readINIKey(filepath.Join(instDir, "instance.cfg"), "IntendedVersion"); ok {
		minecraft = strings.TrimSpace(v)
	}
	return minecraft, modloader
}
