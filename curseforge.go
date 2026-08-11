package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// cfBaseURL 是 CurseForge 官方 API 基地址（测试中可替换为 httptest 服务器）
var cfBaseURL = "https://api.curseforge.com"

// ModVersionResult 是一次批量获取中单个 mod 的结果
type ModVersionResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"` // displayName
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
}

// cfGet 向 CurseForge API 发起 GET 请求并返回响应体。
// 非 200 时按状态码给出明确的中文错误
func cfGet(apiKey, path string) ([]byte, error) {
	url := cfBaseURL + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PackGradle/1.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 CurseForge 失败: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("API Key 无效或未授权（HTTP %d）", resp.StatusCode)
	case http.StatusNotFound:
		return nil, fmt.Errorf("文件不存在（HTTP 404）")
	default:
		return nil, fmt.Errorf("CurseForge 返回错误（HTTP %d）", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// cfFile 是官方 Get Mod File 接口响应中的文件记录
// 文档: https://docs.curseforge.com/rest-api/#get-mod-file
type cfFile struct {
	DisplayName string `json:"displayName"`
	FileDate    string `json:"fileDate"`
	ReleaseType int    `json:"releaseType"`
}

// fetchCfFile 调用官方 Get Mod File 接口获取文件信息。
// 文件记录没有独立版本号字段，displayName（通常为文件名，含版本号）即版本标识
func fetchCfFile(apiKey string, projectID, fileID int64) (CfFileCache, error) {
	body, err := cfGet(apiKey, fmt.Sprintf("/v1/mods/%d/files/%d", projectID, fileID))
	if err != nil {
		return CfFileCache{}, err
	}
	var out struct {
		Data cfFile `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return CfFileCache{}, fmt.Errorf("解析响应失败: %w", err)
	}
	return CfFileCache{
		DisplayName: out.Data.DisplayName,
		FileDate:    out.Data.FileDate,
		ReleaseType: out.Data.ReleaseType,
		FetchedAt:   time.Now().Format(time.RFC3339),
	}, nil
}

// cfModLatestFile 是 Get Mod 响应 latestFiles 数组中的条目（最近发布的文件）
type cfModLatestFile struct {
	ID           uint32   `json:"id"`
	FileName     string   `json:"fileName"`
	ReleaseType  int      `json:"releaseType"`
	FileDate     string   `json:"fileDate"`
	GameVersions []string `json:"gameVersions"`
}

// cfModLatestIndex 是 Get Mod 响应 latestFilesIndexes 数组中的条目（每个 MC 版本一个最新文件）
type cfModLatestIndex struct {
	GameVersion string `json:"gameVersion"`
	FileID      uint32 `json:"fileId"`
	Name        string `json:"filename"`
	ReleaseType int    `json:"releaseType"`
	Modloader   int    `json:"modLoader"`
}

// cfMod 是官方 Get Mod 接口响应中的 mod 信息（检查更新用）。
// latestFilesIndexes 每个 MC 版本一个最新文件条目，latestFiles 为最近发布的文件列表
// 文档: https://docs.curseforge.com/rest-api/#get-mod
type cfMod struct {
	ID                 uint32             `json:"id"`
	Name               string             `json:"name"`
	LatestFiles        []cfModLatestFile  `json:"latestFiles"`
	LatestFilesIndexes []cfModLatestIndex `json:"latestFilesIndexes"`
}

// fetchCfMod 调用官方 Get Mod 接口获取 mod 信息
func fetchCfMod(apiKey string, projectID int64) (cfMod, error) {
	body, err := cfGet(apiKey, fmt.Sprintf("/v1/mods/%d", projectID))
	if err != nil {
		return cfMod{}, err
	}
	var out struct {
		Data cfMod `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return cfMod{}, fmt.Errorf("解析响应失败: %w", err)
	}
	return out.Data, nil
}

// cfLoaderName 返回加载器在文件 gameVersions 数组中的标记名
func cfLoaderName(loader string) string {
	switch strings.ToLower(loader) {
	case "forge":
		return "Forge"
	case "fabric":
		return "Fabric"
	case "quilt":
		return "Quilt"
	case "neoforge":
		return "NeoForge"
	}
	return ""
}

// cfLoaderType 返回加载器对应的 CurseForge modloaderType 数值（1=Forge 4=Fabric 5=Quilt 6=NeoForge）
func cfLoaderType(loader string) int {
	switch strings.ToLower(loader) {
	case "forge":
		return 1
	case "fabric":
		return 4
	case "quilt":
		return 5
	case "neoforge":
		return 6
	}
	return 0 // 未知加载器按「任意」处理
}

// cfLatestCandidate 是一个可匹配的最新文件候选
type cfLatestCandidate struct {
	fileID      int64
	fileName    string
	releaseType int
	fileDate    string
	mcMatch     bool // gameVersion 与 pack 的 MC 版本精确匹配
	loaderMatch bool // 加载器匹配
}

// betterCfCandidate 判断 a 是否优于 b：MC 版本匹配优先，其次加载器匹配，
// 最后 file-id 更大（相等时取后遍历到的条目，可用 latestFiles 的完整信息补全）
func betterCfCandidate(a, b cfLatestCandidate) bool {
	if a.mcMatch != b.mcMatch {
		return a.mcMatch
	}
	if a.loaderMatch != b.loaderMatch {
		return a.loaderMatch
	}
	return a.fileID >= b.fileID
}

// findLatestCfFile 从 Get Mod 响应中按 pack 的 MC 版本与加载器匹配最新文件
// （与 packwiz 一致：优先 latestFilesIndexes，回退 latestFiles）。
// 快照版本名未做映射，需精确匹配（正式版无影响）
func findLatestCfFile(mod cfMod, mcVersion, loader string) cfLatestCandidate {
	wantLoader := cfLoaderType(loader)
	loaderName := cfLoaderName(loader)
	var best cfLatestCandidate

	for _, f := range mod.LatestFilesIndexes {
		c := cfLatestCandidate{
			fileID:      int64(f.FileID),
			fileName:    f.Name,
			releaseType: f.ReleaseType,
			mcMatch:     f.GameVersion == mcVersion,
			loaderMatch: wantLoader == 0 || f.Modloader == wantLoader,
		}
		if betterCfCandidate(c, best) {
			best = c
		}
	}
	for _, f := range mod.LatestFiles {
		mcMatch, loaderMatch := false, loaderName == ""
		for _, gv := range f.GameVersions {
			if gv == mcVersion {
				mcMatch = true
			}
			if gv == loaderName {
				loaderMatch = true
			}
		}
		c := cfLatestCandidate{
			fileID:      int64(f.ID),
			fileName:    f.FileName,
			releaseType: f.ReleaseType,
			fileDate:    f.FileDate,
			mcMatch:     mcMatch,
			loaderMatch: loaderMatch,
		}
		if betterCfCandidate(c, best) {
			best = c
		}
	}
	return best
}

// ModUpdateInfo 是单个 mod 的更新检查结果
type ModUpdateInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	HasUpdate     bool   `json:"has_update"`
	CurrentFile   string `json:"current_file"` // 当前已安装文件名
	LatestFile    string `json:"latest_file"`  // 最新文件名
	LatestFileID  int64  `json:"latest_file_id"`
	LatestRelease int    `json:"latest_release"` // 1=正式版 2=测试版 3=Alpha
	LatestDate    string `json:"latest_date"`
	Error         string `json:"error"`
}

// checkCfModUpdate 检查单个 mod 是否有更新：查 Get Mod → 按 MC 版本+加载器匹配最新文件
// → 与已安装 file-id 对比 → 最新文件信息写入本地缓存。
// 与 packwiz 一致：对比的是 file-id（CurseForge 的 file-id 单调递增，越大越新）
func (s *PackwizService) checkCfModUpdate(apiKey string, proj PackProject, m ModInfo) ModUpdateInfo {
	info := ModUpdateInfo{ID: m.ID, Name: m.Name, CurrentFile: m.File}
	if m.CfProjectID == 0 || m.CfFileID == 0 {
		info.Error = "不是 CurseForge 源（元数据中无 project-id/file-id）"
		return info
	}
	mod, err := fetchCfMod(apiKey, m.CfProjectID)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	latest := findLatestCfFile(mod, proj.Minecraft, proj.Modloader)
	if latest.fileID == 0 {
		// 未找到匹配文件（API 无数据等）：视为无更新（与 packwiz 一致）
		return info
	}
	info.LatestFileID = latest.fileID
	info.LatestFile = latest.fileName
	info.LatestRelease = latest.releaseType
	info.LatestDate = latest.fileDate
	info.HasUpdate = latest.fileID != m.CfFileID

	key := cfCacheKey(m.CfProjectID, m.CfFileID)
	entry := s.config.Get().CfCache[key]
	entry.LatestFileID = latest.fileID
	entry.LatestFileName = latest.fileName
	entry.LatestRelease = latest.releaseType
	entry.CheckedAt = time.Now().Format(time.RFC3339)
	if err := s.config.SetCurseforgeCache(key, entry); err != nil {
		info.Error = "写入缓存失败: " + err.Error()
	}
	return info
}

// CheckModUpdate 检查单个 mod 是否有更新（写入本地缓存并返回结果）
func (s *PackwizService) CheckModUpdate(projectName, modID string) (ModUpdateInfo, error) {
	proj, mod, err := s.findProjectMod(projectName, modID)
	if err != nil {
		return ModUpdateInfo{}, err
	}
	apiKey, err := s.apiKeyOrError()
	if err != nil {
		return ModUpdateInfo{}, err
	}
	return s.checkCfModUpdate(apiKey, proj, mod), nil
}

// CheckAllModUpdates 检查项目中所有 CurseForge 源 mod 的更新（并发上限 8），
// 结果写入本地缓存并逐条返回
func (s *PackwizService) CheckAllModUpdates(projectName string) ([]ModUpdateInfo, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return nil, err
	}
	var targets []ModInfo
	for _, m := range proj.Mods {
		if m.CfProjectID > 0 && m.CfFileID > 0 {
			targets = append(targets, m)
		}
	}
	if len(targets) == 0 {
		return []ModUpdateInfo{}, fmt.Errorf("项目中没有 CurseForge 源的 mod")
	}
	apiKey, err := s.apiKeyOrError()
	if err != nil {
		return nil, err
	}

	results := make([]ModUpdateInfo, len(targets))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, m := range targets {
		wg.Add(1)
		go func(i int, m ModInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = s.checkCfModUpdate(apiKey, proj, m)
		}(i, m)
	}
	wg.Wait()
	return results, nil
}

// findProject 按名称查找项目并解析
func (s *PackwizService) findProject(projectName string) (PackProject, error) {
	projectDir := ""
	for _, p := range s.config.Get().Projects {
		if p.Name == projectName {
			projectDir = p.Path
			break
		}
	}
	if projectDir == "" {
		return PackProject{}, fmt.Errorf("未找到项目: %s", projectName)
	}
	return parsePackToml(filepath.Join(projectDir, "pack.toml"))
}

// findProjectMod 按名称查找项目并定位其中指定 ID 的 mod
func (s *PackwizService) findProjectMod(projectName, modID string) (PackProject, ModInfo, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return PackProject{}, ModInfo{}, err
	}
	for _, m := range proj.Mods {
		if m.ID == modID {
			return proj, m, nil
		}
	}
	return PackProject{}, ModInfo{}, fmt.Errorf("未找到 mod: %s", modID)
}

// apiKeyOrError 返回已配置的 CurseForge API Key
func (s *PackwizService) apiKeyOrError() (string, error) {
	key := strings.TrimSpace(s.config.Get().CurseforgeApiKey)
	if key == "" {
		return "", fmt.Errorf("未配置 CurseForge API Key，请先在「环境配置」中填写")
	}
	return key, nil
}

// FetchModVersion 获取单个 mod 的 CurseForge 版本信息，写入本地缓存，
// 返回带缓存字段的 ModInfo
func (s *PackwizService) FetchModVersion(projectName, modID string) (ModInfo, error) {
	proj, mod, err := s.findProjectMod(projectName, modID)
	if err != nil {
		return ModInfo{}, err
	}
	if mod.CfProjectID == 0 || mod.CfFileID == 0 {
		return ModInfo{}, fmt.Errorf("「%s」不是 CurseForge 源（元数据中无 project-id/file-id）", modID)
	}
	apiKey, err := s.apiKeyOrError()
	if err != nil {
		return ModInfo{}, err
	}

	entry, err := fetchCfFile(apiKey, mod.CfProjectID, mod.CfFileID)
	if err != nil {
		return ModInfo{}, err
	}
	if err := s.config.SetCurseforgeCache(cfCacheKey(mod.CfProjectID, mod.CfFileID), entry); err != nil {
		return ModInfo{}, err
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
// 结果写入本地缓存并逐条返回
func (s *PackwizService) FetchAllModVersions(projectName string) ([]ModVersionResult, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return nil, err
	}
	var targets []ModInfo
	for _, m := range proj.Mods {
		if m.CfProjectID > 0 && m.CfFileID > 0 {
			targets = append(targets, m)
		}
	}
	if len(targets) == 0 {
		return []ModVersionResult{}, fmt.Errorf("项目中没有 CurseForge 源的 mod")
	}
	apiKey, err := s.apiKeyOrError()
	if err != nil {
		return nil, err
	}

	results := make([]ModVersionResult, len(targets))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, m := range targets {
		wg.Add(1)
		go func(i int, m ModInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entry, err := fetchCfFile(apiKey, m.CfProjectID, m.CfFileID)
			if err != nil {
				results[i] = ModVersionResult{ID: m.ID, Name: m.Name, OK: false, Error: err.Error()}
				return
			}
			if err := s.config.SetCurseforgeCache(cfCacheKey(m.CfProjectID, m.CfFileID), entry); err != nil {
				results[i] = ModVersionResult{ID: m.ID, Name: m.Name, OK: false, Error: err.Error()}
				return
			}
			results[i] = ModVersionResult{ID: m.ID, Name: m.Name, Version: entry.DisplayName, OK: true}
		}(i, m)
	}
	wg.Wait()
	return results, nil
}
