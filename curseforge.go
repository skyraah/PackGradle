package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
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
	if err := s.cfCacheStore(proj).Upsert(proj.Name, cfCacheKey(mod.CfProjectID, mod.CfFileID), entry); err != nil {
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
	store := s.cfCacheStore(proj)
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
			if err := store.Upsert(proj.Name, cfCacheKey(m.CfProjectID, m.CfFileID), entry); err != nil {
				results[i] = ModVersionResult{ID: m.ID, Name: m.Name, OK: false, Error: err.Error()}
				return
			}
			results[i] = ModVersionResult{ID: m.ID, Name: m.Name, Version: entry.DisplayName, OK: true}
		}(i, m)
	}
	wg.Wait()
	return results, nil
}

// ModUpdateInfo 是 packwiz update 检查结果中单个 mod 的信息
type ModUpdateInfo struct {
	Name        string `json:"name"`
	HasUpdate   bool   `json:"has_update"`
	CurrentFile string `json:"current_file"`
	LatestFile  string `json:"latest_file"`
	Error       string `json:"error"`
}

// UpdateCheckResult 是 packwiz update 检查结果
type UpdateCheckResult struct {
	OK      bool            `json:"ok"`
	Output  string          `json:"output"`
	Updates []ModUpdateInfo `json:"updates"` // 有更新的 mod
	Errors  []ModUpdateInfo `json:"errors"`  // 检查失败 / 跳过 / 无更新源的 mod
}

// 解析 `packwiz update --all` 输出（对应 packwiz 源码 cmd/update.go 的打印格式）。
// 注意：检查顺序为 固定跳过 → 无更新源 → 检查失败 → 有更新，避免错误信息误匹配更新行
var (
	pinnedSkipRe  = regexp.MustCompile(`^Update skipped for pinned mod (.+)$`)
	noUpdaterRe   = regexp.MustCompile(`^A supported update system for "(.+)" cannot be found\.$`)
	failedCheckRe = regexp.MustCompile(`^Failed to check updates for (.+?): (.+)$`)
	updateLineRe  = regexp.MustCompile(`^(.+): (.+) -> (.+)$`) // <Name>: <旧文件> -> <新文件>
)

// parseUpdateOutput 解析 `packwiz update --all` 的文本输出，提取有更新的 mod 与失败/跳过的 mod
func parseUpdateOutput(output string) (updates, errors []ModUpdateInfo) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := pinnedSkipRe.FindStringSubmatch(line); m != nil {
			errors = append(errors, ModUpdateInfo{Name: m[1], Error: "版本已固定（pinned），跳过更新"})
			continue
		}
		if m := noUpdaterRe.FindStringSubmatch(line); m != nil {
			errors = append(errors, ModUpdateInfo{Name: m[1], Error: "无支持的更新源"})
			continue
		}
		if m := failedCheckRe.FindStringSubmatch(line); m != nil {
			errors = append(errors, ModUpdateInfo{Name: m[1], Error: m[2]})
			continue
		}
		if m := updateLineRe.FindStringSubmatch(line); m != nil {
			updates = append(updates, ModUpdateInfo{
				Name:        m[1],
				HasUpdate:   true,
				CurrentFile: m[2],
				LatestFile:  m[3],
			})
		}
	}
	return
}

// CheckUpdates 通过 packwiz 官方 update 命令检查项目更新（不实际应用）：
// 运行 `packwiz update --all` 并向确认提示喂入 "n"，使其打印更新列表后取消
func (s *PackwizService) CheckUpdates(projectName string) (UpdateCheckResult, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return UpdateCheckResult{}, err
	}
	packwiz, err := s.findPackwiz()
	if err != nil {
		return UpdateCheckResult{OK: false, Output: err.Error()}, nil
	}
	cmd := exec.Command(packwiz, "update", "--all")
	cmd.Dir = proj.Path
	cmd.Stdin = strings.NewReader("n\n") // 确认输入为 n：只打印更新列表，不应用
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	updates, errors := parseUpdateOutput(output)
	return UpdateCheckResult{OK: err == nil, Output: output, Updates: updates, Errors: errors}, nil
}

// UpdateMods 应用更新：modName 非空时更新单个（packwiz update <name>，无确认直接应用），
// 为空时更新全部（packwiz update --all -y）。
// name 为 .pw.toml 文件名（即 mod id）
func (s *PackwizService) UpdateMods(projectName, modName string) (RefreshResult, error) {
	proj, err := s.findProject(projectName)
	if err != nil {
		return RefreshResult{}, err
	}
	packwiz, err := s.findPackwiz()
	if err != nil {
		return RefreshResult{OK: false, Output: err.Error()}, nil
	}
	args := []string{"update"}
	if modName != "" {
		args = append(args, modName)
	} else {
		args = append(args, "--all", "-y")
	}
	cmd := exec.Command(packwiz, args...)
	cmd.Dir = proj.Path
	out, err := cmd.CombinedOutput()
	return RefreshResult{OK: err == nil, Output: strings.TrimSpace(string(out))}, nil
}
