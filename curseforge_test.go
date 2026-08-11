package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// fakeCfServer 模拟 CurseForge 官方 Get Mod File 接口
func fakeCfServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/mods/404/files/404":
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/v1/mods/") && strings.Contains(r.URL.Path, "/files/"): // Get Mod File
			_, _ = w.Write([]byte(`{"data":{"id":6552911,"displayName":"Mekanism-1.20.1-10.4.16.80.jar","fileName":"Mekanism-1.20.1-10.4.16.80.jar","releaseType":1,"fileDate":"2025-06-12T00:00:00Z","gameVersions":["1.20.1"]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withCfBaseURL 临时替换 CurseForge API 基地址，测试结束后恢复
func withCfBaseURL(t *testing.T, url string) {
	t.Helper()
	old := cfBaseURL
	cfBaseURL = url
	t.Cleanup(func() { cfBaseURL = old })
}

// cfTestProject 构造一个含 CurseForge 源 mod（create）与非 CurseForge 源 mod（mekanism）的项目
func cfTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "pack.toml"), `name = "CF Test"
version = "1.0.0"
pack-format = "packwiz:1.1.0"

[index]
file = "index.toml"

[versions]
minecraft = "1.20.1"
forge = "47.4.10"
`)
	mustWriteFile(t, filepath.Join(dir, "index.toml"), `hash-format = "sha256"

[[files]]
file = "mods/create.pw.toml"
hash = "h1"
metafile = true

[[files]]
file = "mods/mekanism.pw.toml"
hash = "h2"
metafile = true
`)
	mustWriteFile(t, filepath.Join(dir, "mods", "create.pw.toml"), `name = "Create"
filename = "create-1.20.1-6.0.8.jar"
side = "both"

[update]
[update.curseforge]
file-id = 7178761
project-id = 328085
`)
	mustWriteFile(t, filepath.Join(dir, "mods", "mekanism.pw.toml"), `name = "Mekanism"
filename = "Mekanism-1.20.1-10.4.16.80.jar"
side = "both"
`)
	return dir
}

// 单文件获取：成功、缺 key（401）、文件不存在（404）
func TestFetchCfFile(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	entry, err := fetchCfFile("test-key", 268560, 6552911)
	if err != nil {
		t.Fatalf("fetchCfFile: %v", err)
	}
	if entry.DisplayName != "Mekanism-1.20.1-10.4.16.80.jar" || entry.ReleaseType != 1 ||
		entry.FileDate == "" || entry.FetchedAt == "" {
		t.Errorf("文件信息解析不正确: %+v", entry)
	}

	if _, err := fetchCfFile("", 268560, 6552911); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("缺少 API Key 应返回 401 错误，实际 %v", err)
	}
	if _, err := fetchCfFile("test-key", 404, 404); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("文件不存在应返回 404 错误，实际 %v", err)
	}
}

// 单个 mod 版本获取的完整流程：解析 → 请求 → 缓存 → 返回回填后的 ModInfo
func TestFetchModVersionEndToEnd(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	dir := cfTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetApiKey("test-key"); err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(m)

	updated, err := svc.FetchModVersion("CF Test", "create")
	if err != nil {
		t.Fatalf("FetchModVersion: %v", err)
	}
	if updated.CfVersion != "Mekanism-1.20.1-10.4.16.80.jar" || updated.CfReleaseType != 1 || updated.CfFileDate == "" {
		t.Errorf("返回的 ModInfo 未回填缓存字段: %+v", updated)
	}

	// 缓存已写入内存并持久化到磁盘
	key := cfCacheKey(328085, 7178761)
	if entry, ok := m.Get().CfCache[key]; !ok || entry.DisplayName != "Mekanism-1.20.1-10.4.16.80.jar" {
		t.Errorf("缓存未写入: %+v", m.Get().CfCache)
	}
	m2 := &ConfigManager{path: m.path}
	if _, err := toml.DecodeFile(m2.path, &m2.cfg); err != nil {
		t.Fatal(err)
	}
	if entry, ok := m2.cfg.CfCache[key]; !ok || entry.DisplayName == "" {
		t.Errorf("缓存未持久化到磁盘: %+v", m2.cfg.CfCache)
	}

	// 未配置 API Key
	m3 := newTestConfig(t)
	if err := m3.AddProject(ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	svc3 := NewPackwizService(m3)
	if _, err := svc3.FetchModVersion("CF Test", "create"); err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Errorf("未配置 API Key 应报错，实际 %v", err)
	}

	// 非 CurseForge 源 mod
	if _, err := svc.FetchModVersion("CF Test", "mekanism"); err == nil || !strings.Contains(err.Error(), "不是 CurseForge 源") {
		t.Errorf("非 CurseForge 源应报错，实际 %v", err)
	}
}

// 批量获取：只处理 CurseForge 源，逐条返回结果并写入缓存
func TestFetchAllModVersions(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	dir := cfTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetApiKey("test-key"); err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(m)

	results, err := svc.FetchAllModVersions("CF Test")
	if err != nil {
		t.Fatalf("FetchAllModVersions: %v", err)
	}
	if len(results) != 1 { // 只有 create 是 CurseForge 源
		t.Fatalf("应只处理 1 个 CurseForge 源 mod，实际 %d: %+v", len(results), results)
	}
	if !results[0].OK || results[0].Version != "Mekanism-1.20.1-10.4.16.80.jar" || results[0].Error != "" {
		t.Errorf("批量结果不正确: %+v", results[0])
	}

	// 未配置 API Key
	m3 := newTestConfig(t)
	if err := m3.AddProject(ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPackwizService(m3).FetchAllModVersions("CF Test"); err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Errorf("未配置 API Key 应报错，实际 %v", err)
	}
}

// applyCfCache：ListProjects 时缓存字段应回填到 mod 列表
func TestApplyCfCache(t *testing.T) {
	dir := cfTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetCurseforgeCache(cfCacheKey(328085, 7178761), CfFileCache{
		DisplayName: "create-1.20.1-6.0.8.jar",
		FileDate:    "2025-06-12T00:00:00Z",
		ReleaseType: 1,
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(m)

	projects := svc.ListProjects()
	if len(projects) != 1 {
		t.Fatalf("应返回 1 个项目，实际 %d", len(projects))
	}
	mods := projects[0].Mods
	create := findMod(t, mods, "create")
	if create.CfVersion != "create-1.20.1-6.0.8.jar" || create.CfReleaseType != 1 || create.CfFileDate == "" {
		t.Errorf("缓存未回填: %+v", create)
	}
	// 非 CurseForge 源 mod 不受影响
	mek := findMod(t, mods, "mekanism")
	if mek.CfVersion != "" || mek.CfProjectID != 0 {
		t.Errorf("非 CurseForge 源不应有缓存字段: %+v", mek)
	}
}

func TestParseUpdateOutput(t *testing.T) {
	output := `Loading modpack...
Reading metadata files...
A supported update system for "mcrd-cn.ksmcbrigade-1.20.1-4" cannot be found.
Checking for updates...
Updates found:
Create: create-1.20.1-6.0.8.jar -> create-1.20.1-6.0.9.jar
Mekanism: Mekanism-1.20.1-10.4.16.80.jar -> Mekanism-1.20.1-10.4.17.00.jar
Failed to check updates for Jade: unexpected API response
Update skipped for pinned mod Sodium
Do you want to update? [Y/n]: Cancelled!
`
	updates, errors := parseUpdateOutput(output)
	if len(updates) != 2 {
		t.Fatalf("应解析到 2 个有更新的 mod，实际 %d: %+v", len(updates), updates)
	}
	create := updates[0]
	if create.Name != "Create" || !create.HasUpdate ||
		create.CurrentFile != "create-1.20.1-6.0.8.jar" || create.LatestFile != "create-1.20.1-6.0.9.jar" {
		t.Errorf("有更新条目解析不正确: %+v", create)
	}
	if updates[1].Name != "Mekanism" || updates[1].LatestFile != "Mekanism-1.20.1-10.4.17.00.jar" {
		t.Errorf("有更新条目解析不正确: %+v", updates[1])
	}

	if len(errors) != 3 {
		t.Fatalf("应解析到 3 个失败/跳过条目，实际 %d: %+v", len(errors), errors)
	}
	byName := map[string]string{}
	for _, e := range errors {
		byName[e.Name] = e.Error
	}
	if byName["mcrd-cn.ksmcbrigade-1.20.1-4"] != "无支持的更新源" {
		t.Errorf("无更新源条目解析不正确: %+v", byName)
	}
	if byName["Jade"] != "unexpected API response" {
		t.Errorf("检查失败条目解析不正确: %+v", byName)
	}
	if byName["Sodium"] == "" {
		t.Errorf("固定跳过条目解析不正确: %+v", byName)
	}
}

// 空输出 / 无更新输出
func TestParseUpdateOutputNoUpdates(t *testing.T) {
	updates, errors := parseUpdateOutput("Loading modpack...\nAll files are up to date!\n")
	if len(updates) != 0 || len(errors) != 0 {
		t.Errorf("无更新时不应解析出条目: updates=%+v errors=%+v", updates, errors)
	}
	updates, errors = parseUpdateOutput("")
	if len(updates) != 0 || len(errors) != 0 {
		t.Errorf("空输出不应解析出条目: updates=%+v errors=%+v", updates, errors)
	}
}
