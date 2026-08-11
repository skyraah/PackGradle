package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// fakeCfServer 模拟 CurseForge 官方 API：
//   - Get Mod（/v1/mods/{id}）：create 有更新（最新 7178800 > 已装 7178761，含 fabric/其他 MC 版本干扰项），
//     mekanism 无更新（最新 == 已装）
//   - Get Mod File（/v1/mods/{id}/files/{fid}）：返回固定文件详情
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
		case r.URL.Path == "/v1/mods/328085": // create
			_, _ = w.Write([]byte(`{"data":{"id":328085,"name":"Create","latestFiles":[{"id":7178800,"fileName":"create-1.20.1-6.0.9.jar","displayName":"create-1.20.1-6.0.9.jar","releaseType":1,"fileDate":"2025-07-01T00:00:00Z","gameVersions":["1.20.1","Forge"]}],"latestFilesIndexes":[{"gameVersion":"1.20.1","fileId":7178800,"filename":"create-1.20.1-6.0.9.jar","releaseType":1,"modLoader":1},{"gameVersion":"1.20.1","fileId":7178900,"filename":"create-fabric-1.20.1-6.0.8.jar","releaseType":1,"modLoader":4},{"gameVersion":"1.21.1","fileId":9999000,"filename":"create-1.21.1-7.0.0.jar","releaseType":1,"modLoader":1}]}}`))
		case r.URL.Path == "/v1/mods/268560": // mekanism：无更新
			_, _ = w.Write([]byte(`{"data":{"id":268560,"name":"Mekanism","latestFilesIndexes":[{"gameVersion":"1.20.1","fileId":6552911,"filename":"Mekanism-1.20.1-10.4.16.80.jar","releaseType":1,"modLoader":1}]}}`))
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

// cfUpdateTestProject 构造一个含两个 CurseForge 源 mod 的项目：
// create 有更新（最新 7178800 > 已装 7178761），mekanism 无更新
func cfUpdateTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "pack.toml"), `name = "Update Test"
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

[update]
[update.curseforge]
file-id = 6552911
project-id = 268560
`)
	return dir
}

// findLatestCfFile 的匹配逻辑：MC 版本优先、加载器其次、file-id 最大（与 packwiz 一致）
func TestFindLatestCfFile(t *testing.T) {
	mod := cfMod{
		ID: 328085,
		LatestFilesIndexes: []cfModLatestIndex{
			{GameVersion: "1.20.1", FileID: 7178800, Name: "create-1.20.1-6.0.9.jar", ReleaseType: 1, Modloader: 1},        // forge
			{GameVersion: "1.20.1", FileID: 7178900, Name: "create-fabric-1.20.1-6.0.8.jar", ReleaseType: 1, Modloader: 4}, // fabric（ID 更大，应被加载器过滤）
			{GameVersion: "1.21.1", FileID: 9999000, Name: "create-1.21.1-7.0.0.jar", ReleaseType: 1, Modloader: 1},        // 其他 MC（ID 更大，应被版本过滤）
		},
		LatestFiles: []cfModLatestFile{
			{ID: 7178600, FileName: "create-1.20.1-6.0.8.jar", ReleaseType: 1, GameVersions: []string{"1.20.1", "Forge"}},
		},
	}

	if got := findLatestCfFile(mod, "1.20.1", "forge"); got.fileID != 7178800 {
		t.Errorf("forge/1.20.1 应匹配 7178800，实际 %d（%s）", got.fileID, got.fileName)
	}
	if got := findLatestCfFile(mod, "1.21.1", "forge"); got.fileID != 9999000 {
		t.Errorf("1.21.1 应匹配 9999000，实际 %d", got.fileID)
	}
	// 未知加载器：1.20.1 中按 file-id 取最大（fabric 7178900）
	if got := findLatestCfFile(mod, "1.20.1", "unknown"); got.fileID != 7178900 {
		t.Errorf("未知加载器应取 1.20.1 最大 file-id 7178900，实际 %d", got.fileID)
	}
	// latestFiles 中较旧的文件不应胜出
	if got := findLatestCfFile(mod, "1.20.1", "forge"); got.fileID == 7178600 {
		t.Error("不应匹配 latestFiles 中较旧的文件")
	}
}

// 单个 mod 更新检查：有更新 / 无更新 / 缓存持久化 / 列表回填
func TestCheckModUpdate(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	dir := cfUpdateTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(ProjectEntry{Name: "Update Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetApiKey("test-key"); err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(m)

	// create：有更新（file-id 7178800 > 已装 7178761）
	info, err := svc.CheckModUpdate("Update Test", "create")
	if err != nil {
		t.Fatalf("CheckModUpdate: %v", err)
	}
	if !info.HasUpdate || info.LatestFileID != 7178800 || info.LatestFile != "create-1.20.1-6.0.9.jar" ||
		info.CurrentFile != "create-1.20.1-6.0.8.jar" || info.LatestRelease != 1 || info.LatestDate == "" {
		t.Errorf("create 更新检查结果不正确: %+v", info)
	}

	// mekanism：无更新（最新 file-id == 已装）
	info2, err := svc.CheckModUpdate("Update Test", "mekanism")
	if err != nil {
		t.Fatalf("CheckModUpdate: %v", err)
	}
	if info2.HasUpdate {
		t.Errorf("mekanism 不应有更新: %+v", info2)
	}

	// 最新文件信息已写入缓存
	entry, ok := m.Get().CfCache[cfCacheKey(328085, 7178761)]
	if !ok || entry.LatestFileID != 7178800 || entry.LatestFileName != "create-1.20.1-6.0.9.jar" || entry.CheckedAt == "" {
		t.Errorf("更新检查结果未写入缓存: %+v", entry)
	}

	// ListProjects 回填最新文件信息
	projects := svc.ListProjects()
	if len(projects) != 1 {
		t.Fatalf("应返回 1 个项目，实际 %d", len(projects))
	}
	create := findMod(t, projects[0].Mods, "create")
	if create.CfLatestFileID != 7178800 || create.CfLatestFileName != "create-1.20.1-6.0.9.jar" {
		t.Errorf("缓存未回填到 mod 列表: %+v", create)
	}
}

// 批量更新检查：逐条返回结果
func TestCheckAllModUpdates(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	dir := cfUpdateTestProject(t)
	m := newTestConfig(t)
	if err := m.AddProject(ProjectEntry{Name: "Update Test", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetApiKey("test-key"); err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(m)

	results, err := svc.CheckAllModUpdates("Update Test")
	if err != nil {
		t.Fatalf("CheckAllModUpdates: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("应返回 2 个结果，实际 %d: %+v", len(results), results)
	}
	byID := map[string]ModUpdateInfo{}
	for _, r := range results {
		byID[r.ID] = r
	}
	if !byID["create"].HasUpdate || byID["create"].LatestFileID != 7178800 {
		t.Errorf("create 应有更新: %+v", byID["create"])
	}
	if byID["mekanism"].HasUpdate || byID["mekanism"].Error != "" {
		t.Errorf("mekanism 应无更新: %+v", byID["mekanism"])
	}
}
