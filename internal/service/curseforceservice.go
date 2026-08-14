package service

import (
	"strings"
	"sync"

	"packgradle/internal/curseforge"
	"packgradle/internal/errs"
	"packgradle/internal/packwiz"
)

// ModVersionResult 是一次批量获取中单个 mod 的结果
type ModVersionResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"` // displayName
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
}

// apiKeyOrError 返回已配置的 CurseForge API Key
func (s *PackwizService) apiKeyOrError() (string, error) {
	key := strings.TrimSpace(s.config.Get().CurseforgeApiKey)
	if key == "" {
		return "", errs.New("err.cf.api_key_missing")
	}
	return key, nil
}

// FetchModVersion 获取单个 mod 的 CurseForge 版本信息，写入本地缓存，
// 返回带缓存字段的 ModInfo
func (s *PackwizService) FetchModVersion(projectName, modID string) (packwiz.ModInfo, error) {
	proj, mod, err := s.findProjectMod(projectName, modID)
	if err != nil {
		return packwiz.ModInfo{}, err
	}
	if mod.CfProjectID == 0 || mod.CfFileID == 0 {
		return packwiz.ModInfo{}, errs.New("err.cf.not_cf_source", modID)
	}
	apiKey, err := s.apiKeyOrError()
	if err != nil {
		return packwiz.ModInfo{}, err
	}

	entry, err := curseforge.FetchFile(apiKey, mod.CfProjectID, mod.CfFileID)
	if err != nil {
		return packwiz.ModInfo{}, err
	}
	if err := s.cfCacheStore(proj).Upsert(curseforge.CacheKey(mod.CfProjectID, mod.CfFileID), entry); err != nil {
		return packwiz.ModInfo{}, err
	}
	s.applyCfCache(&proj)
	for i := range proj.Mods {
		if proj.Mods[i].ID == modID {
			return proj.Mods[i], nil
		}
	}
	return mod, nil
}

// FetchAllModVersions 批量获取项目中所有 CurseForge 源 mod 的版本（并发上限 8），
// 结果一次性合并写入本地缓存并逐条返回（避免逐条 Upsert 全量重写缓存文件）
func (s *PackwizService) FetchAllModVersions(projectName string) ([]ModVersionResult, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return nil, err
	}
	var targets []packwiz.ModInfo
	for _, m := range proj.Mods {
		if m.CfProjectID > 0 && m.CfFileID > 0 {
			targets = append(targets, m)
		}
	}
	if len(targets) == 0 {
		return []ModVersionResult{}, errs.New("err.cf.no_cf_mods")
	}
	apiKey, err := s.apiKeyOrError()
	if err != nil {
		return nil, err
	}

	results := make([]ModVersionResult, len(targets))
	type fetchedEntry struct {
		key   string
		entry curseforge.CfFileCache
	}
	fetched := make([]fetchedEntry, len(targets))
	fetchedOK := make([]bool, len(targets))
	store := s.cfCacheStore(proj)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, m := range targets {
		wg.Add(1)
		go func(i int, m packwiz.ModInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entry, err := curseforge.FetchFile(apiKey, m.CfProjectID, m.CfFileID)
			if err != nil {
				results[i] = ModVersionResult{ID: m.ID, Name: m.Name, OK: false, Error: err.Error()}
				return
			}
			fetched[i] = fetchedEntry{key: curseforge.CacheKey(m.CfProjectID, m.CfFileID), entry: entry}
			fetchedOK[i] = true
		}(i, m)
	}
	wg.Wait()

	entries := make(map[string]curseforge.CfFileCache, len(targets))
	for i, ok := range fetchedOK {
		if !ok {
			continue
		}
		entries[fetched[i].key] = fetched[i].entry
	}
	if saveErr := store.UpsertMany(entries); saveErr != nil {
		for i, ok := range fetchedOK {
			if ok {
				m := targets[i]
				results[i] = ModVersionResult{ID: m.ID, Name: m.Name, OK: false, Error: saveErr.Error()}
			}
		}
		return results, nil
	}
	for i, ok := range fetchedOK {
		if ok {
			m := targets[i]
			results[i] = ModVersionResult{ID: m.ID, Name: m.Name, Version: fetched[i].entry.DisplayName, OK: true}
		}
	}
	return results, nil
}

// CheckUpdates 通过 packwiz 官方 update 命令检查项目更新（不实际应用）：
// 运行 `packwiz update --all` 并向确认提示喂入 "n"，使其打印更新列表后取消
func (s *PackwizService) CheckUpdates(projectName string) (packwiz.UpdateCheckResult, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return packwiz.UpdateCheckResult{}, err
	}
	packwizPath, err := s.findPackwiz()
	if err != nil {
		return packwiz.UpdateCheckResult{OK: false, Output: err.Error()}, nil
	}
	return packwiz.RunCheckUpdates(packwizPath, proj.Path)
}

// UpdateMods 应用更新：modName 非空时更新单个（packwiz update <name>，无确认直接应用），
// 为空时更新全部（packwiz update --all -y）。name 为 .pw.toml 文件名（即 mod id）。
// 更新成功后重建版本缓存：删除旧 file-id 的缓存条目并自动获取当前版本，避免缓存堆积
func (s *PackwizService) UpdateMods(projectName, modName string) (packwiz.RefreshResult, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return packwiz.RefreshResult{}, err
	}
	packwizPath, err := s.findPackwiz()
	if err != nil {
		return packwiz.RefreshResult{OK: false, Output: err.Error()}, nil
	}
	result := packwiz.RunUpdateMods(packwizPath, proj.Path, modName)
	if result.OK {
		s.refreshCfCacheAfterUpdate(proj)
	}
	return result, nil
}

// refreshCfCacheAfterUpdate 在 mod 更新成功后重建 CurseForge 版本缓存：
//  1. 删除旧 file-id 的缓存条目（含已移除 mod 的孤儿条目），避免缓存堆积；
//  2. 对 file-id 发生变化的 CurseForge 源 mod 自动获取当前版本并写入缓存。
//
// 更新后解析失败、新列表不含 CurseForge 源 mod（如旧式目录结构）时跳过，
// 避免误删整份缓存；API Key 缺失或获取失败时静默跳过，仅影响版本列显示
func (s *PackwizService) refreshCfCacheAfterUpdate(oldProj packwiz.PackProject) {
	newProj, err := packwiz.ParseProject(oldProj.PackToml)
	if err != nil {
		return
	}
	store := s.cfCacheStore(newProj)

	// 当前所有 CurseForge 源 mod 的缓存键（更新后的 file-id），作为保留集合
	current := map[string]struct{}{}
	for _, m := range newProj.Mods {
		if m.CfProjectID > 0 && m.CfFileID > 0 {
			current[curseforge.CacheKey(m.CfProjectID, m.CfFileID)] = struct{}{}
		}
	}
	if len(current) == 0 {
		return
	}
	// 更新前快照的缓存键（旧 file-id），用于识别 file-id 变化的 mod
	oldKey := map[string]string{}
	for _, m := range oldProj.Mods {
		if m.CfProjectID > 0 && m.CfFileID > 0 {
			oldKey[m.ID] = curseforge.CacheKey(m.CfProjectID, m.CfFileID)
		}
	}

	if err := store.Prune(func(key string) bool {
		_, ok := current[key]
		return ok
	}); err != nil {
		return
	}

	apiKey, err := s.apiKeyOrError()
	if err != nil {
		return
	}
	for _, m := range newProj.Mods {
		if m.CfProjectID == 0 || m.CfFileID == 0 {
			continue
		}
		if oldKey[m.ID] == curseforge.CacheKey(m.CfProjectID, m.CfFileID) {
			continue // file-id 未变化，缓存仍有效
		}
		entry, err := curseforge.FetchFile(apiKey, m.CfProjectID, m.CfFileID)
		if err != nil {
			continue // 获取失败不阻塞更新，版本列显示为空待手动获取
		}
		_ = store.Upsert(curseforge.CacheKey(m.CfProjectID, m.CfFileID), entry)
	}
}
