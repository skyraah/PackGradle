package prism

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/errs"
)

// CreateMinimalInstance 在 instancesDir 下程序创建一个最小 Prism 实例：
// instance.cfg（InstanceType=OneSix + name）+ mmc-pack.json（formatVersion 1，
// components 取自请求的 MC/加载器版本）+ 空游戏目录骨架（minecraft/）。
// Prism 启动时会自动加载并补齐组件；实例 ID 为项目名的合法化形式，目录已存在时拒绝创建。
// 任一步失败会回滚已创建的目录。
func CreateMinimalInstance(instancesDir string, req CreateRequest) (Instance, error) {
	id := sanitizeInstanceID(req.Name)
	if id == "" {
		return Instance{}, errs.New("err.prism.create_invalid_name", req.Name)
	}
	dir := filepath.Join(instancesDir, id)
	if fileExists(dir) {
		return Instance{}, errs.New("err.prism.instance_exists", id)
	}

	// 回滚：创建过程中失败时删除本次创建的目录
	created := false
	defer func() {
		if !created {
			_ = os.RemoveAll(dir)
		}
	}()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Instance{}, errs.NewDetail("err.prism.create_failed", err.Error())
	}
	if err := os.MkdirAll(filepath.Join(dir, "minecraft"), 0o755); err != nil {
		return Instance{}, errs.NewDetail("err.prism.create_failed", err.Error())
	}

	// instance.cfg：InstanceType=OneSix 为 MMC 格式实例
	cfg := "InstanceType=OneSix\nname=" + req.Name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "instance.cfg"), []byte(cfg), 0o644); err != nil {
		return Instance{}, errs.NewDetail("err.prism.create_failed", err.Error())
	}

	// mmc-pack.json：net.minecraft 组件 + 加载器组件（与 Prism PackProfile 结构一致）
	pack := mmcPackOut{FormatVersion: 1}
	pack.Components = append(pack.Components, mmcComponentOut{UID: "net.minecraft", Version: req.Minecraft, Important: true})
	if req.Modloader != "" {
		uid := loaderUID(req.Modloader)
		if uid == "" {
			return Instance{}, errs.New("err.prism.loader_unsupported", req.Modloader)
		}
		pack.Components = append(pack.Components, mmcComponentOut{UID: uid, Version: req.ModloaderVersion})
	}
	data, err := json.MarshalIndent(pack, "", "    ")
	if err != nil {
		return Instance{}, errs.NewDetail("err.prism.create_failed", err.Error())
	}
	if err := os.WriteFile(filepath.Join(dir, "mmc-pack.json"), data, 0o644); err != nil {
		return Instance{}, errs.NewDetail("err.prism.create_failed", err.Error())
	}

	created = true
	return Instance{
		ID:               id,
		Name:             req.Name,
		Path:             dir,
		GameDir:          filepath.Join(dir, "minecraft"),
		Minecraft:        req.Minecraft,
		Modloader:        req.Modloader,
		ModloaderVersion: req.ModloaderVersion,
	}, nil
}

// mmcPackOut / mmcComponentOut 是 mmc-pack.json 的写入结构（camelCase，与 Prism 一致）
type mmcPackOut struct {
	FormatVersion int              `json:"formatVersion"`
	Components    []mmcComponentOut `json:"components"`
}

type mmcComponentOut struct {
	UID       string `json:"uid"`
	Version   string `json:"version"`
	Important bool   `json:"important,omitempty"`
}

// sanitizeInstanceID 将项目名合法化为实例目录名（Windows 非法字符替换为 _）
func sanitizeInstanceID(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// loaderUID 反查加载器名对应的组件 uid
func loaderUID(loader string) string {
	for uid, name := range loaderUIDs {
		if name == loader {
			return uid
		}
	}
	return ""
}
