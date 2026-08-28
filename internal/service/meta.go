package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"packgradle/internal/appconfig"
	"packgradle/internal/curseforge"
	"packgradle/internal/errs"
	"packgradle/internal/fsutil"
	"packgradle/internal/packwiz"
	"packgradle/internal/prism"

	"github.com/BurntSushi/toml"
)

// metaPushContext 组装单个 mod 推送所需的项目级信息
type metaPushContext struct {
	loaders    string
	mcVersions string
	cache      map[string]curseforge.CfFileCache
}

// PushMeta 将项目 mod 元数据推送到实例 mods/.index（Prism 兼容格式）：
// 每个 mod 的 pw.toml 在 side 条目后插入 x-prismlauncher-* 四个扩展字段
// （loaders/mc-versions/release-type/version-number），供 Prism 识别。
// modID 非空时仅推送该 mod（空串推送全部）。mods 目录本身不建 junction（meta 推送机制）。
func (s *PrismService) PushMeta(projectName, modID string) (int, error) {
	lp, err := s.loadLinkedProjectPack(projectName)
	if err != nil {
		return 0, err
	}
	indexDir := filepath.Join(lp.Inst.GameDir, "mods", ".index")
	if err := fsutil.MkdirAll(indexDir); err != nil {
		return 0, err
	}

	// 项目级推送上下文（loader/mc 版本 + 版本缓存用于 release-type）
	ctx := metaPushContext{
		loaders:    loaderMeta(lp.Pack),
		mcVersions: lp.Pack.Minecraft,
	}
	if cache, err := curseforge.NewCfCacheStore(filepath.Join(lp.Pack.Path, ".cache")).Load(); err == nil {
		ctx.cache = cache
	}

	count := 0
	for _, mod := range lp.Pack.Mods {
		if modID != "" && mod.ID != modID {
			continue // 仅推送指定 mod
		}
		if mod.Path == "" {
			continue // 元数据文件缺失的条目（索引保留展示），跳过
		}
		content, err := os.ReadFile(mod.Path)
		if err != nil {
			continue
		}
		out, err := prism.ToPrismFormat(content, prism.PrismMeta{
			Loaders:     ctx.loaders,
			MCVersions:  ctx.mcVersions,
			ReleaseType: releaseTypeMeta(ctx, mod),
			Version:     mod.Version,
		})
		if err != nil {
			continue
		}
		target := filepath.Join(indexDir, mod.ID+".pw.toml")
		if err := fsutil.WriteFile(target, out); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// PullMeta 将实例 mods/.index 的元数据拉回项目 mods 目录（packwiz 格式）：
// 删除 x-prismlauncher-* 扩展字段与 [download] 表中的 url 条目。
// modID 非空时仅拉取该 mod（空串拉取全部）。拉回后需运行 packwiz refresh 收录。
func (s *PrismService) PullMeta(projectName, modID string) (int, error) {
	lp, err := s.loadLinkedProjectPack(projectName)
	if err != nil {
		return 0, err
	}
	indexDir := filepath.Join(lp.Inst.GameDir, "mods", ".index")
	entries, err := os.ReadDir(indexDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // 实例侧尚无 .index
		}
		return 0, errs.NewDetail("err.toml.read", err.Error(), indexDir)
	}
	modsDir := filepath.Join(lp.Pack.Path, "mods")
	if err := fsutil.MkdirAll(modsDir); err != nil {
		return 0, err
	}

	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".pw.toml") {
			continue
		}
		if modID != "" && strings.TrimSuffix(e.Name(), ".pw.toml") != modID {
			continue // 仅拉取指定 mod
		}
		content, err := os.ReadFile(filepath.Join(indexDir, e.Name()))
		if err != nil {
			continue
		}
		out, err := prism.FromPrismFormat(content)
		if err != nil {
			continue
		}
		target := filepath.Join(modsDir, e.Name())
		if err := fsutil.WriteFile(target, out); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// MetaDiff 计算项目 ↔ 实例 mods 的元数据差异并刷新缓存：
// 项目侧以 index.toml 权威列表为准，实例侧扫描 mods/.index 的 pw.toml。
// 每次「查看差异」时调用（重新计算 + 写入 .cache/metadiff.cache），
// 不做实时监听，避免目录变化带来的性能开销。
func (s *PrismService) MetaDiff(projectName string) (prism.MetaDiff, error) {
	lp, err := s.loadLinkedProjectPack(projectName)
	if err != nil {
		return prism.MetaDiff{}, err
	}
	indexDir := filepath.Join(lp.Inst.GameDir, "mods", ".index")

	// 实例侧：mod id → 版本（.index/*.pw.toml）
	instanceMods := map[string]string{}
	if entries, err := os.ReadDir(indexDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".pw.toml") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".pw.toml")
			instanceMods[id] = parseMetaVersion(filepath.Join(indexDir, e.Name()))
		}
	}

	// 项目侧：index.toml 权威列表
	projectMods := map[string]string{}
	// CurseForge 源 mod 的版本不在 pw.toml 顶层，而在 .cache/modversion.cache 的 displayName；
	// 差异计算必须回退到缓存，否则 CF mod 的版本差异永远不可见。
	cfCache, _ := curseforge.NewCfCacheStore(filepath.Join(lp.Pack.Path, ".cache")).Load()
	for _, m := range lp.Pack.Mods {
		projectMods[m.ID] = projectMetaVersion(m, cfCache)
	}

	diff := prism.MetaDiff{FetchedAt: time.Now().Format(time.RFC3339)}
	for id := range instanceMods {
		if _, ok := projectMods[id]; !ok {
			diff.InstanceOnly = append(diff.InstanceOnly, id)
		}
	}
	for id := range projectMods {
		if _, ok := instanceMods[id]; !ok {
			diff.ProjectOnly = append(diff.ProjectOnly, id)
		}
	}
	for id, pv := range projectMods {
		if iv, ok := instanceMods[id]; ok && pv != "" && pv != iv {
			diff.VersionDiff = append(diff.VersionDiff, prism.VersionDiffItem{
				ID: id, ProjectVersion: pv, InstanceVersion: iv,
			})
		}
	}
	sort.Strings(diff.InstanceOnly)
	sort.Strings(diff.ProjectOnly)
	sort.Slice(diff.VersionDiff, func(i, j int) bool { return diff.VersionDiff[i].ID < diff.VersionDiff[j].ID })

	// 刷新缓存（写失败不阻断展示——缓存仅用于持久化与离线读取）
	_ = appconfig.WriteTomlAtomic(filepath.Join(lp.Pack.Path, ".cache", "metadiff.cache"), diff)
	return diff, nil
}

// projectMetaVersion 返回项目侧用于差异比较的版本：
// 顶层/update 表版本优先，CurseForge 源回退到版本缓存 displayName。
func projectMetaVersion(mod packwiz.ModInfo, cache map[string]curseforge.CfFileCache) string {
	if mod.Version != "" {
		return mod.Version
	}
	if cache != nil && mod.CfProjectID > 0 && mod.CfFileID > 0 {
		if entry, ok := cache[curseforge.CacheKey(mod.CfProjectID, mod.CfFileID)]; ok {
			return entry.DisplayName
		}
	}
	return ""
}

// parseMetaVersion 轻量解析 pw.toml 的版本号：
// x-prismlauncher-version-number（Prism 侧）> 顶层 version > [update.*] 版本
func parseMetaVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var raw struct {
		Version      string                    `toml:"version"`
		PrismVersion string                    `toml:"x-prismlauncher-version-number"`
		Update       map[string]map[string]any `toml:"update"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return ""
	}
	if raw.PrismVersion != "" {
		return raw.PrismVersion
	}
	if raw.Version != "" {
		return raw.Version
	}
	return packwiz.UpdateVersion(raw.Update)
}

// loaderMeta 组装 Prism 的加载器字段（"forge:47.4.10"；无加载器为空串）
func loaderMeta(proj packwiz.PackProject) string {
	if proj.Modloader == "" {
		return ""
	}
	if proj.ModloaderVersion != "" {
		return proj.Modloader + ":" + proj.ModloaderVersion
	}
	return proj.Modloader
}

// releaseTypeMeta 从版本缓存取发布类型（1=正式 2=测试 3=Alpha），无缓存默认 release
func releaseTypeMeta(ctx metaPushContext, mod packwiz.ModInfo) string {
	if ctx.cache != nil && mod.CfProjectID > 0 && mod.CfFileID > 0 {
		if entry, ok := ctx.cache[curseforge.CacheKey(mod.CfProjectID, mod.CfFileID)]; ok {
			switch entry.ReleaseType {
			case 2:
				return "beta"
			case 3:
				return "alpha"
			}
		}
	}
	return "release"
}
