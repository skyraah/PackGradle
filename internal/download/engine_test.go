package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"packgradle/internal/errs"
)

// ---- 测试基建 ----

// newTestServer 起 httptest 假 CDN（与生产经同一 directURL 构造逻辑对接）
func newTestServer(t *testing.T, cdn *FakeCDN) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(cdn.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// cdnURL 取测试服务的 base URL（直链前缀注入用）
func cdnURL(srv *httptest.Server) string { return srv.URL }

// mustEngine 构造引擎，失败即中止测试
func mustEngine(t *testing.T, opts Options) *Engine {
	t.Helper()
	e, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// newRecordingEngine 构造注入了快退避与 sleep 记录器的测试引擎；
// 返回引擎与重试等待时长记录通道。
func newRecordingEngine(t *testing.T, baseURL string, mutate func(*Options)) (*Engine, *[]time.Duration) {
	t.Helper()
	sleeps := &[]time.Duration{}
	opts := Options{
		BaseURL: baseURL,
		Backoff: func(int) time.Duration { return time.Millisecond },
		Sleep: func(_ context.Context, d time.Duration) error {
			*sleeps = append(*sleeps, d)
			return nil
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	return mustEngine(t, opts), sleeps
}

// fixtureReq 构造以 sha256 声明 good 内容的取数请求
func fixtureReq(fileID int64, filename string, good []byte) Request {
	sum := sha256.Sum256(good)
	return Request{
		FileID:     fileID,
		Filename:   filename,
		HashFormat: "sha256",
		Hash:       hex.EncodeToString(sum[:]),
	}
}

func repeatStep(s FakeStep, n int) []FakeStep {
	out := make([]FakeStep, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// ---- 成功链 ----

// 成功取数：成品在场、`.part` 消失、内容逐字节一致、UA 可识别
func TestFetchSuccess(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	good := []byte("PackGradle fixture jar content")
	path := "/files/7270/446/jei.jar"
	cdn.SetFile(path, good)

	e := mustEngine(t, Options{BaseURL: cdnURL(srv) + "/files"})
	dir := t.TempDir()
	res, err := e.Fetch(t.Context(), dir, fixtureReq(7270446, "jei.jar", good))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if res.Path != filepath.Join(dir, "jei.jar") {
		t.Fatalf("成品路径不符: %s", res.Path)
	}
	if res.Size != int64(len(good)) {
		t.Fatalf("成品大小不符: %d", res.Size)
	}
	f, err := res.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, good) {
		t.Fatalf("成品内容不符: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "jei.jar.part")); !os.IsNotExist(err) {
		t.Fatalf(".part 应已消失，stat err=%v", err)
	}

	reqs := cdn.Requests()
	if len(reqs) != 1 {
		t.Fatalf("成功链应恰好 1 次请求，实际 %d", len(reqs))
	}
	if reqs[0].UserAgent != UserAgent {
		t.Fatalf("UA 应为 %q，实际 %q", UserAgent, reqs[0].UserAgent)
	}
	if reqs[0].Range != "" {
		t.Fatalf("首次请求不应带 Range 头，实际 %q", reqs[0].Range)
	}
}

// ---- 失败分桶矩阵（ADR-0008 §5，表驱动全绿=AC）----

func TestFailureBucketingMatrix(t *testing.T) {
	good := []byte("content")
	cases := []struct {
		name         string
		steps        []FakeStep
		wantCode     string
		wantRequests int // 假 CDN 收到的请求次数
		wantSleeps   int // 重试等待发生次数
	}{
		{"403恒不重试→unavailable", []FakeStep{{Status: 403}}, CodeUnavailable, 1, 0},
		{"404恒不重试→unavailable", []FakeStep{{Status: 404}}, CodeUnavailable, 1, 0},
		{"429重试4次耗尽→rate_limited", repeatStep(FakeStep{Status: 429}, 5), CodeRateLimited, 5, 4},
		{"503重试4次耗尽→rate_limited", repeatStep(FakeStep{Status: 503}, 5), CodeRateLimited, 5, 4},
		{"500重试4次耗尽→network", repeatStep(FakeStep{Status: 500}, 5), CodeNetwork, 5, 4},
		{"408重试4次耗尽→network", repeatStep(FakeStep{Status: 408}, 5), CodeNetwork, 5, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cdn := NewFakeCDN()
			srv := newTestServer(t, cdn)
			path := "/files/8778/11/m.jar"
			cdn.Script(path, tc.steps...)

			e, _ := newRecordingEngine(t, cdnURL(srv)+"/files", nil)
			res, err := e.Fetch(t.Context(), t.TempDir(), fixtureReq(8778011, "m.jar", good))
			if res != nil {
				t.Fatal("失败场景不得产出成品")
			}
			if errs.CodeOf(err) != tc.wantCode {
				t.Fatalf("分桶码 = %q, 期望 %q（err=%v）", errs.CodeOf(err), tc.wantCode, err)
			}
			if got := cdn.CountRequests(path); got != tc.wantRequests {
				t.Fatalf("请求数 = %d, 期望 %d", got, tc.wantRequests)
			}
		})
	}
}

// Retry-After 尊重：429 带 Retry-After: 7 时，等待时长取 max(退避, 7s)
func TestRetryAfterRespected(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	path := "/files/8778/11/ra.jar"
	cdn.Script(path, repeatStep(FakeStep{Status: 429, RetryAfterSeconds: 7}, 5)...)

	e, sleeps := newRecordingEngine(t, cdnURL(srv)+"/files", nil)
	_, err := e.Fetch(t.Context(), t.TempDir(), fixtureReq(8778011, "ra.jar", []byte("x")))
	if errs.CodeOf(err) != CodeRateLimited {
		t.Fatalf("应 rate_limited，实际 %v", err)
	}
	if len(*sleeps) != 4 {
		t.Fatalf("应发生 4 次重试等待，实际 %d 次: %v", len(*sleeps), *sleeps)
	}
	for i, d := range *sleeps {
		if d < 7*time.Second {
			t.Fatalf("第 %d 次等待 %v 应 ≥ Retry-After(7s)", i, d)
		}
	}
}

// 连接拒绝 → 重试耗尽 → network 桶
func TestConnectionRefused(t *testing.T) {
	addr, err := RefusingAddr()
	if err != nil {
		t.Skipf("无法起本地监听: %v", err)
	}
	e, sleeps := newRecordingEngine(t, "http://"+addr+"/files", nil)
	_, ferr := e.Fetch(t.Context(), t.TempDir(), fixtureReq(8778011, "x.jar", []byte("x")))
	if errs.CodeOf(ferr) != CodeNetwork {
		t.Fatalf("连接拒绝应归 network，实际 %v", ferr)
	}
	if len(*sleeps) != maxRetries {
		t.Fatalf("连接拒绝应重试 %d 次，实际等待 %d 次", maxRetries, len(*sleeps))
	}
}

// ---- hash 校验与重取 ----

// 错误字节：hash 不符 → 清 `.part` 全量重取一次，仍败 → hash_mismatch；
// 两轮均为全量（Range 头为空），成品绝不落盘（两层校验第一层）
func TestHashMismatchFullRefetchOnce(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	good := []byte("real content")
	bad := []byte("wrong content")
	path := "/files/8778/11/bad.jar"
	cdn.Script(path, FakeStep{Status: 200, Body: bad}) // 越界重复：永远错误字节

	e, _ := newRecordingEngine(t, cdnURL(srv)+"/files", nil)
	dir := t.TempDir()
	res, err := e.Fetch(t.Context(), dir, fixtureReq(8778011, "bad.jar", good))
	if errs.CodeOf(err) != CodeHashMismatch {
		t.Fatalf("应 hash_mismatch，实际 %v", err)
	}
	if res != nil {
		t.Fatal("hash 不符不得产出成品")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "bad.jar")); !os.IsNotExist(statErr) {
		t.Fatal("hash 不符的成品不得落盘")
	}
	if got := cdn.CountRequests(path); got != 2 {
		t.Fatalf("应恰好重取一次（共 2 次请求），实际 %d", got)
	}
	for i, r := range cdn.Requests() {
		if r.Range != "" {
			t.Fatalf("第 %d 次请求应为全量重取（无 Range 头），实际 %q", i, r.Range)
		}
	}
}

// 错误字节一次后转好：全量重取成功
func TestHashMismatchRefetchSucceeds(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	good := []byte("real content")
	bad := []byte("corrupted")
	path := "/files/8778/11/flip.jar"
	cdn.Script(path,
		FakeStep{Status: 200, Body: bad},
		FakeStep{Status: 200, Body: good},
	)

	e, _ := newRecordingEngine(t, cdnURL(srv)+"/files", nil)
	res, err := e.Fetch(t.Context(), t.TempDir(), fixtureReq(8778011, "flip.jar", good))
	if err != nil {
		t.Fatalf("重取应成功: %v", err)
	}
	if res.Size != int64(len(good)) {
		t.Fatalf("成品大小不符: %d", res.Size)
	}
	if got := cdn.CountRequests(path); got != 2 {
		t.Fatalf("应恰好 2 次请求，实际 %d", got)
	}
}

// ---- 半截断流 → Range 续传（AC：假 CDN 记录 Range 头为证）----

func TestTruncatedStreamResumesWithRange(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	good := []byte("PackGradle truncated stream resume fixture")
	path := "/files/8778/11/trunc.jar"
	cdn.Script(path,
		FakeStep{Status: 200, Body: good, TruncateAt: 5}, // 全长 Content-Length 只发 5 字节即断流
		Step206(good, 5),                                 // 续传第二程：206 补余下部分
	)

	e, _ := newRecordingEngine(t, cdnURL(srv)+"/files", nil)
	res, err := e.Fetch(t.Context(), t.TempDir(), fixtureReq(8778011, "trunc.jar", good))
	if err != nil {
		t.Fatalf("半截断流应经 Range 续传完成: %v", err)
	}
	if res.Size != int64(len(good)) {
		t.Fatalf("续传后成品大小不符: %d != %d", res.Size, len(good))
	}

	reqs := cdn.Requests()
	if len(reqs) != 2 {
		t.Fatalf("应恰好 2 次请求，实际 %d", len(reqs))
	}
	if reqs[0].Range != "" {
		t.Fatalf("首次请求不应带 Range，实际 %q", reqs[0].Range)
	}
	if reqs[1].Range != "bytes=5-" {
		t.Fatalf("续传请求应带 Range 头为证，实际 %q", reqs[1].Range)
	}
}

// ---- 读停顿看门狗 ----

// 挂住的响应体在 StallTimeout 内无字节推进即断，重试耗尽 → network
func TestStallWatchdog(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	path := "/files/8778/11/hang.jar"
	cdn.Script(path, repeatStep(FakeStep{Hang: 5 * time.Second}, 5)...)

	e, _ := newRecordingEngine(t, cdnURL(srv)+"/files", func(o *Options) {
		o.StallTimeout = 100 * time.Millisecond
	})
	start := time.Now()
	_, err := e.Fetch(t.Context(), t.TempDir(), fixtureReq(8778011, "hang.jar", []byte("x")))
	if errs.CodeOf(err) != CodeNetwork {
		t.Fatalf("读停顿重试耗尽应归 network，实际 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("看门狗应按 100ms 断流，总耗时 %v 过长", elapsed)
	}
	if got := cdn.CountRequests(path); got != 5 {
		t.Fatalf("应重试共 5 次请求，实际 %d", got)
	}
}

// ---- 并发批量 ----

// FetchAll 并发完成全部任务且不超并发上限；单文件失败不中断批次
func TestFetchAllBatch(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	good := []byte("batch content")
	bad := []byte("broken")
	badIndex := 3

	reqs := make([]Request, 0, 8)
	for i := 0; i < 8; i++ {
		filename := "mod" + string(rune('a'+i)) + ".jar"
		// 与引擎同口径构造路径（directURL 注入版），保证假 CDN 命中
		path := directURL("/files", 7270446+int64(i), filename)
		content := good
		if i == badIndex {
			// 唯一坏文件：返回错误字节 → hash_mismatch（批次不中断）
			content = bad
		}
		cdn.SetFile(path, content)
		reqs = append(reqs, fixtureReq(7270446+int64(i), filename, good))
	}

	e := mustEngine(t, Options{BaseURL: cdnURL(srv) + "/files", Concurrency: 3})
	dir := t.TempDir()
	var mu sync.Mutex
	done, failed := 0, 0
	seen := make(map[int]bool, len(reqs)) // 回调对位下标全覆盖（重复请求不塌缩的契约面）
	err := e.FetchAll(t.Context(), dir, reqs, func(k int, req Request, res *Result, ferr error) {
		mu.Lock()
		defer mu.Unlock()
		seen[k] = true
		if ferr != nil {
			failed++
			if errs.CodeOf(ferr) != CodeHashMismatch {
				t.Errorf("唯一失败应 hash_mismatch，实际 %v", ferr)
			}
			return
		}
		done++
	})
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if done != 7 || failed != 1 {
		t.Fatalf("应 7 成功 1 失败，实际 %d/%d", done, failed)
	}
	for k := range reqs {
		if !seen[k] {
			t.Fatalf("reqs[%d] 未回调（对位下标缺失）", k)
		}
	}
	if peak := cdn.MaxInFlight(); peak > 3 {
		t.Fatalf("并发峰值 %d 超过上限 3", peak)
	}
}

// 并发度构造校验：0 取默认（合法）、1/16 边界合法、17/-3 越界报错
func TestNewConcurrencyBounds(t *testing.T) {
	if _, err := New(Options{Concurrency: 17}); errs.CodeOf(err) != "err.config.download_concurrency_invalid" {
		t.Fatalf("17 应越界报错，实际 %v", err)
	}
	if _, err := New(Options{Concurrency: -3}); errs.CodeOf(err) != "err.config.download_concurrency_invalid" {
		t.Fatalf("-3 应越界报错，实际 %v", err)
	}
	for _, n := range []int{0, 1, 6, 16} {
		if _, err := New(Options{Concurrency: n}); err != nil {
			t.Fatalf("并发 %d 应合法（0=默认 6），实际 %v", n, err)
		}
	}
}

// 调用方 ctx 取消：透传取消而非分桶（不误报 network）
func TestFetchContextCanceled(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	good := []byte("x")
	path := "/files/8778/11/cancel.jar"
	cdn.SetFile(path, good)

	e, _ := newRecordingEngine(t, cdnURL(srv)+"/files", nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := e.Fetch(ctx, t.TempDir(), fixtureReq(8778011, "cancel.jar", good))
	if err == nil || errs.CodeOf(err) == CodeNetwork {
		t.Fatalf("ctx 取消应透传取消错误，不应分桶为 network，实际 %v", err)
	}
}
