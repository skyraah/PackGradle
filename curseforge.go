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
	url := fmt.Sprintf("%s/v1/mods/%d/files/%d", cfBaseURL, projectID, fileID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return CfFileCache{}, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PackGradle/1.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return CfFileCache{}, fmt.Errorf("请求 CurseForge 失败: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return CfFileCache{}, fmt.Errorf("API Key 无效或未授权（HTTP %d）", resp.StatusCode)
	case http.StatusNotFound:
		return CfFileCache{}, fmt.Errorf("文件不存在（HTTP 404）")
	default:
		return CfFileCache{}, fmt.Errorf("CurseForge 返回错误（HTTP %d）", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CfFileCache{}, fmt.Errorf("读取响应失败: %w", err)
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
