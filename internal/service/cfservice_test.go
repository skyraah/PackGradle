package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"packgradle/internal/appconfig"
	"packgradle/internal/curseforge"
	"packgradle/internal/errs"
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
	old := curseforge.BaseURL()
	curseforge.SetBaseURL(url)
	t.Cleanup(func() { curseforge.SetBaseURL(old) })
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

// 单个 mod 版本获取的完整流程：解析 → 请求 → 缓存 → 返回回填后的 ModInfo
func TestFetchModVersionEndToEnd(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	dir := cfTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(appconfig.ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
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

	// 缓存已写入项目目录的 .cache/modversion.cache，并可从磁盘读回
	key := curseforge.CacheKey(328085, 7178761)
	store := curseforge.NewCfCacheStore(filepath.Join(dir, ".cache"))
	cache, err := store.Load()
	if err != nil {
		t.Fatalf("读取缓存失败: %v", err)
	}
	if entry, ok := cache[key]; !ok || entry.DisplayName != "Mekanism-1.20.1-10.4.16.80.jar" {
		t.Errorf("缓存未写入: %+v", cache)
	}
	if _, err := os.Stat(filepath.Join(dir, ".cache", "modversion.cache")); err != nil {
		t.Errorf("缓存文件不存在: %v", err)
	}

	// 未配置 API Key
	m3 := newTestConfig(t)
	if err := m3.AddProject(appconfig.ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	svc3 := NewPackwizService(m3)
	if _, err := svc3.FetchModVersion("CF Test", "create"); errs.CodeOf(err) != "err.cf.api_key_missing" {
		t.Errorf("未配置 API Key 应返回 err.cf.api_key_missing，实际 %v", err)
	}

	// 非 CurseForge 源 mod
	if _, err := svc.FetchModVersion("CF Test", "mekanism"); errs.CodeOf(err) != "err.cf.not_cf_source" {
		t.Errorf("非 CurseForge 源应返回 err.cf.not_cf_source，实际 %v", err)
	}
}

// 批量获取：只处理 CurseForge 源，逐条返回结果并写入缓存
func TestFetchAllModVersions(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	dir := cfTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(appconfig.ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
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
	if err := m3.AddProject(appconfig.ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPackwizService(m3).FetchAllModVersions("CF Test"); errs.CodeOf(err) != "err.cf.api_key_missing" {
		t.Errorf("未配置 API Key 应返回 err.cf.api_key_missing，实际 %v", err)
	}
}

// applyCfCache：ListProjects 时缓存字段应回填到 mod 列表
func TestApplyCfCache(t *testing.T) {
	dir := cfTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(appconfig.ProjectEntry{Name: "CF Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := curseforge.NewCfCacheStore(filepath.Join(dir, ".cache")).Save(map[string]curseforge.CfFileCache{
		curseforge.CacheKey(328085, 7178761): {
			DisplayName: "create-1.20.1-6.0.8.jar",
			FileDate:    "2025-06-12T00:00:00Z",
			ReleaseType: 1,
		},
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

// 未导入的项目 / 不存在的 mod 应报错
func TestFetchModVersionNotFound(t *testing.T) {
	svc := NewPackwizService(newTestConfig(t))
	if _, err := svc.FetchModVersion("Missing", "create"); errs.CodeOf(err) != "err.proj.not_found" {
		t.Errorf("未导入的项目应返回 err.proj.not_found，实际 %v", err)
	}
}

// cfUpdateTestProject 构造一个含两个 CurseForge 源 mod 的项目：
// create 的 file-id 将被"更新"（7178761 -> 7178762），jei 保持不变
func cfUpdateTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "pack.toml"), `name = "CF Update Test"
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
file = "mods/jei.pw.toml"
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
	mustWriteFile(t, filepath.Join(dir, "mods", "jei.pw.toml"), `name = "JEI"
filename = "jei-1.20.1-15.0.0.6.jar"
side = "both"

[update]
[update.curseforge]
file-id = 678
project-id = 12345
`)
	return dir
}

// 更新成功后缓存重建：旧 file-id 条目与孤儿条目被清除，
// file-id 变化的 mod 自动获取当前版本写入新键，未变化的 mod 缓存保留
func TestRefreshCfCacheAfterUpdate(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	dir := cfUpdateTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(appconfig.ProjectEntry{Name: "CF Update Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetApiKey("test-key"); err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(m)

	oldProj, err := svc.findProject("CF Update Test")
	if err != nil {
		t.Fatalf("findProject: %v", err)
	}
	// 预写缓存：create 旧 file-id 条目、jei 条目、孤儿条目各一
	entry := curseforge.CfFileCache{DisplayName: "create-1.20.1-6.0.8.jar", ReleaseType: 1}
	if err := curseforge.NewCfCacheStore(filepath.Join(dir, ".cache")).Save(map[string]curseforge.CfFileCache{
		curseforge.CacheKey(328085, 7178761): entry, // 更新前 file-id，应被清除并重新获取
		curseforge.CacheKey(12345, 678):      entry, // 未更新，应保留
		"999:1":                              entry, // 孤儿条目，应被清除
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟 packwiz update 改写 .pw.toml：create 的 file-id 更新
	mustWriteFile(t, filepath.Join(dir, "mods", "create.pw.toml"), `name = "Create"
filename = "create-1.20.1-6.0.9.jar"
side = "both"

[update]
[update.curseforge]
file-id = 7178762
project-id = 328085
`)
	svc.refreshCfCacheAfterUpdate(oldProj)

	store := curseforge.NewCfCacheStore(filepath.Join(dir, ".cache"))
	cache, err := store.Load()
	if err != nil {
		t.Fatalf("读取缓存失败: %v", err)
	}
	// 新 file-id 自动获取并写入缓存
	if got := cache[curseforge.CacheKey(328085, 7178762)]; got.DisplayName != "Mekanism-1.20.1-10.4.16.80.jar" {
		t.Errorf("新 file-id 未自动获取写入: %+v", cache)
	}
	// 旧 file-id 条目被清除
	if _, ok := cache[curseforge.CacheKey(328085, 7178761)]; ok {
		t.Errorf("旧 file-id 条目未被清除: %+v", cache)
	}
	// 孤儿条目被清除
	if _, ok := cache["999:1"]; ok {
		t.Errorf("孤儿条目未被清除: %+v", cache)
	}
	// 未更新的 mod 缓存保留
	if _, ok := cache[curseforge.CacheKey(12345, 678)]; !ok {
		t.Errorf("未更新的 mod 缓存不应丢失: %+v", cache)
	}

	// 端到端：重新列出项目时新缓存应回填到版本列
	projects := svc.ListProjects()
	create := findMod(t, projects[0].Mods, "create")
	if create.CfVersion != "Mekanism-1.20.1-10.4.16.80.jar" {
		t.Errorf("更新后版本列未显示新版本: %+v", create)
	}
}

// 未配置 API Key 时更新后只清理旧缓存，不自动获取（不报错）
func TestRefreshCfCacheAfterUpdateNoApiKey(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	dir := cfUpdateTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(appconfig.ProjectEntry{Name: "CF Update Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(m)

	oldProj, err := svc.findProject("CF Update Test")
	if err != nil {
		t.Fatalf("findProject: %v", err)
	}
	entry := curseforge.CfFileCache{DisplayName: "create-1.20.1-6.0.8.jar", ReleaseType: 1}
	if err := curseforge.NewCfCacheStore(filepath.Join(dir, ".cache")).Save(map[string]curseforge.CfFileCache{
		curseforge.CacheKey(328085, 7178761): entry,
		curseforge.CacheKey(12345, 678):      entry,
	}); err != nil {
		t.Fatal(err)
	}

	mustWriteFile(t, filepath.Join(dir, "mods", "create.pw.toml"), `name = "Create"
filename = "create-1.20.1-6.0.9.jar"
side = "both"

[update]
[update.curseforge]
file-id = 7178762
project-id = 328085
`)
	svc.refreshCfCacheAfterUpdate(oldProj)

	cache, err := curseforge.NewCfCacheStore(filepath.Join(dir, ".cache")).Load()
	if err != nil {
		t.Fatalf("读取缓存失败: %v", err)
	}
	if _, ok := cache[curseforge.CacheKey(328085, 7178761)]; ok {
		t.Errorf("无 API Key 时旧 file-id 条目也应被清除: %+v", cache)
	}
	if _, ok := cache[curseforge.CacheKey(328085, 7178762)]; ok {
		t.Errorf("无 API Key 时不应自动获取: %+v", cache)
	}
	if _, ok := cache[curseforge.CacheKey(12345, 678)]; !ok {
		t.Errorf("未更新的 mod 缓存不应丢失: %+v", cache)
	}
}
