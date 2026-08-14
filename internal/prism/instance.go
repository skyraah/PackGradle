package prism

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/errs"
	"packgradle/internal/fsutil"
)

// ScanInstances 扫描实例根目录下全部实例。
// 有效实例 = 含 instance.cfg 的一级子目录（目录名即实例 ID）；
// 单个实例解析失败时错误落入 Instance.Error，不中断整个列表（同 ListProjects 容错哲学）。
func ScanInstances(instancesDir string) []Instance {
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		return nil
	}
	groups := parseInstGroups(filepath.Join(instancesDir, "instgroups.json"))

	instances := make([]Instance, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(instancesDir, e.Name())
		if !fsutil.Exists(filepath.Join(dir, "instance.cfg")) {
			continue // 无 instance.cfg 不是实例（instgroups.json 等杂物目录）
		}
		instances = append(instances, parseInstance(dir, e.Name(), groups))
	}
	sortInstances(instances)
	return instances
}

// parseInstance 解析单个实例目录：instance.cfg（name）+ mmc-pack.json（版本信息）
func parseInstance(dir, id string, groups map[string]string) Instance {
	inst := Instance{
		ID:   id,
		Name: id,
		Path: dir,
		// Prism 的游戏目录为 <实例>/minecraft（MMC 格式，非 .minecraft）
		GameDir: filepath.Join(dir, "minecraft"),
		Group:   groups[id],
	}

	// instance.cfg：name= 缺失时回退目录名
	if name, ok, _ := readIniKey(filepath.Join(dir, "instance.cfg"), "name"); ok {
		inst.Name = name
	}

	// mmc-pack.json：解析失败时标记 Error 但不中断（版本信息留空）
	if err := parseMMCPack(filepath.Join(dir, "mmc-pack.json"), &inst); err != nil {
		inst.Error = err.Error()
	}
	return inst
}

// mmcPackRaw 对应 mmc-pack.json 的最小结构（字段与 Prism PackProfile.cpp 对齐）
type mmcPackRaw struct {
	FormatVersion int `json:"formatVersion"`
	Components    []struct {
		UID     string `json:"uid"`
		Version string `json:"version"`
	} `json:"components"`
}

// parseMMCPack 解析 mmc-pack.json 并填充实例的 MC/加载器版本信息
func parseMMCPack(path string, inst *Instance) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 无 mmc-pack.json（实例未配置）不算失败，版本信息留空
		}
		return errs.NewDetail("err.prism.mmcpack_read", err.Error())
	}
	var raw mmcPackRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return errs.NewDetail("err.prism.mmcpack_parse", err.Error())
	}
	for _, c := range raw.Components {
		switch c.UID {
		case "net.minecraft":
			inst.Minecraft = c.Version
		default:
			if loader, ok := loaderUIDs[c.UID]; ok {
				inst.Modloader = loader
				inst.ModloaderVersion = c.Version
			}
		}
	}
	return nil
}

// instGroupsRaw 对应 instgroups.json（实例分组映射）
type instGroupsRaw struct {
	Groups map[string]struct {
		Instances []string `json:"instances"`
	} `json:"groups"`
}

// parseInstGroups 读取 instgroups.json 构建 实例 ID → 分组名 映射；文件缺失时返回空映射
func parseInstGroups(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var raw instGroupsRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	for group, g := range raw.Groups {
		for _, id := range g.Instances {
			out[id] = group
		}
	}
	return out
}

// sortInstances 按名称不区分大小写排序（同 packwiz.sortMods 风格）
func sortInstances(instances []Instance) {
	sort.Slice(instances, func(i, j int) bool {
		return strings.ToLower(instances[i].Name) < strings.ToLower(instances[j].Name)
	})
}
