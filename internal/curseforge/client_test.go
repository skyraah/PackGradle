package curseforge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	old := cfBaseURL
	SetBaseURL(url)
	t.Cleanup(func() { SetBaseURL(old) })
}

// 单文件获取：成功、缺 key（401）、文件不存在（404）
func TestFetchFile(t *testing.T) {
	srv := fakeCfServer(t)
	withCfBaseURL(t, srv.URL)

	entry, err := FetchFile("test-key", 268560, 6552911)
	if err != nil {
		t.Fatalf("FetchFile: %v", err)
	}
	if entry.DisplayName != "Mekanism-1.20.1-10.4.16.80.jar" || entry.ReleaseType != 1 ||
		entry.FileDate == "" || entry.FetchedAt == "" {
		t.Errorf("文件信息解析不正确: %+v", entry)
	}

	// 缺少 API Key → 401 错误码
	if _, err := FetchFile("", 268560, 6552911); errs.CodeOf(err) != "err.cf.unauthorized" {
		t.Errorf("缺少 API Key 应返回 err.cf.unauthorized，实际 %v", err)
	}
	// 文件不存在 → 404 错误码
	if _, err := FetchFile("test-key", 404, 404); errs.CodeOf(err) != "err.cf.not_found" {
		t.Errorf("文件不存在应返回 err.cf.not_found，实际 %v", err)
	}
}

// CfCacheStore：Load/Save/Upsert 与不同存储（目录）间的隔离
func TestCfCacheStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".cache")
	store := NewCfCacheStore(root)

	// 不存在的缓存文件返回空 map
	if cache, err := store.Load(); err != nil || len(cache) != 0 {
		t.Fatalf("空缓存应返回空 map: %v %v", cache, err)
	}

	// 写入并读回
	entry := CfFileCache{DisplayName: "create-1.20.1-6.0.8.jar", ReleaseType: 1}
	if err := store.Save(map[string]CfFileCache{"1:2": entry}); err != nil {
		t.Fatal(err)
	}
	cache1, _ := store.Load()
	if got := cache1["1:2"]; got.DisplayName != "create-1.20.1-6.0.8.jar" {
		t.Errorf("读回不正确: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "modversion.cache")); err != nil {
		t.Errorf("缓存文件不存在: %v", err)
	}

	// Upsert 追加条目，不影响已有条目
	if err := store.Upsert("3:4", CfFileCache{DisplayName: "other.jar"}); err != nil {
		t.Fatal(err)
	}
	cache, _ := store.Load()
	if len(cache) != 2 || cache["1:2"].DisplayName == "" || cache["3:4"].DisplayName != "other.jar" {
		t.Errorf("Upsert 后缓存不正确: %+v", cache)
	}

	// 不同存储（目录）之间互不影响
	store2 := NewCfCacheStore(filepath.Join(t.TempDir(), ".cache"))
	if err := store2.Save(map[string]CfFileCache{"9:9": entry}); err != nil {
		t.Fatal(err)
	}
	cacheA, _ := store.Load()
	if len(cacheA) != 2 {
		t.Errorf("store 缓存应不受 store2 影响: %+v", cacheA)
	}

	// 不残留临时文件
	if _, err := os.Stat(filepath.Join(root, "modversion.cache.tmp")); !os.IsNotExist(err) {
		t.Errorf("不应残留临时文件: %v", err)
	}
}

// Prune：删除不满足 keep 条件的条目，保留满足条件的条目
func TestCfCacheStorePrune(t *testing.T) {
	store := NewCfCacheStore(filepath.Join(t.TempDir(), ".cache"))
	entry := CfFileCache{DisplayName: "create-1.20.1-6.0.8.jar", ReleaseType: 1}
	if err := store.Save(map[string]CfFileCache{
		"328085:7178761": entry, // 保留
		"328085:9999999": entry, // 旧 file-id，删除
		"123:456":        entry, // 孤儿条目，删除
	}); err != nil {
		t.Fatal(err)
	}

	keep := map[string]bool{"328085:7178761": true}
	if err := store.Prune(func(key string) bool { return keep[key] }); err != nil {
		t.Fatal(err)
	}
	cache, _ := store.Load()
	if len(cache) != 1 || cache["328085:7178761"].DisplayName == "" {
		t.Errorf("Prune 后缓存不正确: %+v", cache)
	}
	if _, ok := cache["328085:9999999"]; ok {
		t.Errorf("旧 file-id 条目未被删除: %+v", cache)
	}
	if _, ok := cache["123:456"]; ok {
		t.Errorf("孤儿条目未被删除: %+v", cache)
	}

	// keep 全部命中时不写盘（不报错，缓存不变）
	if err := store.Prune(func(key string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	cache, _ = store.Load()
	if len(cache) != 1 {
		t.Errorf("全部保留时缓存不应变化: %+v", cache)
	}
}

// 缓存键格式
func TestCacheKey(t *testing.T) {
	if got := CacheKey(328085, 7178761); got != "328085:7178761" {
		t.Errorf("CacheKey = %q, 期望 328085:7178761", got)
	}
}
