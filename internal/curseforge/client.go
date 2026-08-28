package curseforge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"packgradle/internal/errs"
)

// cfBaseURL 是 CurseForge 官方 API 基地址（测试中可替换）
var cfBaseURL = "https://api.curseforge.com"

// httpClient 全局共享：连接复用 + 统一 15s 超时
var httpClient = &http.Client{Timeout: 15 * time.Second}

// BaseURL 返回当前 API 基地址（供测试保存/恢复）
func BaseURL() string {
	return cfBaseURL
}

// SetBaseURL 替换 API 基地址，仅用于测试（如 httptest 服务器）
func SetBaseURL(url string) {
	cfBaseURL = url
}

// CfFileCache 是 CurseForge 文件信息的本地缓存条目（键为 "projectID:fileID"）
type CfFileCache struct {
	DisplayName string `toml:"display_name"` // 版本显示名（文件名，通常含版本号）
	FileDate    string `toml:"file_date"`    // 发布日期（RFC3339）
	ReleaseType int    `toml:"release_type"` // 1=正式版 2=测试版 3=Alpha
	FetchedAt   string `toml:"fetched_at"`   // 获取时间（RFC3339）
}

// CacheKey 生成 CurseForge 文件缓存键
func CacheKey(projectID, fileID int64) string {
	return fmt.Sprintf("%d:%d", projectID, fileID)
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

	client := httpClient
	resp, err := client.Do(req)
	if err != nil {
		return nil, errs.NewDetail("err.cf.request", err.Error())
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errs.New("err.cf.unauthorized", resp.StatusCode)
	case http.StatusNotFound:
		return nil, errs.New("err.cf.not_found", resp.StatusCode)
	default:
		return nil, errs.New("err.cf.http", resp.StatusCode)
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

// FetchFile 调用官方 Get Mod File 接口获取文件信息。
// 文件记录没有独立版本号字段，displayName（通常为文件名，含版本号）即版本标识
func FetchFile(apiKey string, projectID, fileID int64) (CfFileCache, error) {
	body, err := cfGet(apiKey, fmt.Sprintf("/v1/mods/%d/files/%d", projectID, fileID))
	if err != nil {
		return CfFileCache{}, err
	}
	var out struct {
		Data cfFile `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return CfFileCache{}, errs.NewDetail("err.cf.parse_response", err.Error())
	}
	return CfFileCache{
		DisplayName: out.Data.DisplayName,
		FileDate:    out.Data.FileDate,
		ReleaseType: out.Data.ReleaseType,
		FetchedAt:   time.Now().Format(time.RFC3339),
	}, nil
}
